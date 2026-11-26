package ui

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"

	"renovate-operator/config"

	"github.com/gorilla/mux"
	"sigs.k8s.io/yaml"
)

// hostedConfigNamePattern is the only shape of name served under /config/: a
// single path element with a preset extension Renovate can parse. The name
// minus that extension selects the YAML source. The leading character class
// keeps dotfiles (and a ConfigMap mount's "..data" entries) out.
var hostedConfigNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.(json|jsonc|json5)$`)

// maxHostedSourceBytes caps a preset's YAML source. The route is public and
// converts on every request, so the cost of one request has to stay bounded.
const maxHostedSourceBytes = 256 << 10

// hostedConfigDir returns the directory hosted Renovate presets are served
// from. Empty means the feature is off.
func hostedConfigDir() string {
	return strings.TrimSpace(config.GetValue("HOSTED_CONFIG_DIR"))
}

func isHostedConfigName(name string) bool {
	return hostedConfigNamePattern.MatchString(name)
}

// registerHostedConfigRoutes serves the YAML files in HOSTED_CONFIG_DIR as
// Renovate presets, converted to JSON, at /config/<base>.json, .jsonc and
// .json5. It must be registered before the UI catch-all.
func (s *Server) registerHostedConfigRoutes(router *mux.Router) {
	dir := hostedConfigDir()
	if dir == "" {
		return
	}
	router.HandleFunc("/config/{name}", func(w http.ResponseWriter, r *http.Request) {
		s.serveHostedConfig(w, r, dir)
	}).Methods(http.MethodGet, http.MethodHead)
}

func (s *Server) serveHostedConfig(w http.ResponseWriter, r *http.Request, dir string) {
	name := mux.Vars(r)["name"]
	if !isHostedConfigName(name) {
		http.NotFound(w, r)
		return
	}
	base := strings.TrimSuffix(name, filepath.Ext(name))
	// Read per request: a mounted ConfigMap is swapped in place on edit.
	source, err := readCapped(filepath.Join(dir, base+".yaml"))
	if err != nil {
		// EISDIR: a directory that happens to carry a source-like name.
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.EISDIR) {
			http.NotFound(w, r)
			return
		}
		s.logger.Error(err, "failed to read hosted config", "name", name)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	// JSON is valid JSONC and JSON5, so one body answers all three extensions.
	body, fresh, err := s.hostedConfig.convert(base, source)
	if err != nil {
		if fresh {
			s.logger.Error(err, "hosted config is not a valid YAML preset", "name", name)
		}
		http.Error(w, "hosted config source is not a valid YAML preset", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Edits must reach Renovate even through a caching proxy.
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(body)
}

// hostedConfigCache keeps the JSON converted from each preset's YAML source,
// so a source is parsed once and again only when its content changes.
type hostedConfigCache struct {
	mu      sync.Mutex
	entries map[string]hostedConfigEntry
}

type hostedConfigEntry struct {
	source []byte
	body   []byte
	err    error
}

// convert returns the JSON for a preset's YAML source, or the error that
// source produces. fresh reports that the source differed from the one last
// seen under this name and was converted now.
func (c *hostedConfigCache) convert(base string, source []byte) (body []byte, fresh bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.entries[base]; ok && bytes.Equal(entry.source, source) {
		return entry.body, false, entry.err
	}
	body, err = yaml.YAMLToJSON(source)
	if err == nil && !bytes.HasPrefix(body, []byte("{")) {
		err = errors.New("top level is not a mapping")
	}
	if err != nil {
		body = nil
	}
	if c.entries == nil {
		c.entries = map[string]hostedConfigEntry{}
	}
	c.entries[base] = hostedConfigEntry{source: source, body: body, err: err}
	return body, true, err
}

// readCapped reads a file, refusing one larger than maxHostedSourceBytes.
func readCapped(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxHostedSourceBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxHostedSourceBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, maxHostedSourceBytes)
	}
	return data, nil
}
