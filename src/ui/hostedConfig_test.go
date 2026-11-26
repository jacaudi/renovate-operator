package ui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"renovate-operator/config"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"github.com/gorilla/mux"
)

// setHostedConfig points HOSTED_CONFIG_DIR (and BASE_PATH) at the given values
// for one test and restores the config module afterwards.
func setHostedConfig(t *testing.T, dir, basePath string) {
	t.Helper()
	defs := []config.ConfigItemDescription{
		{Key: "BASE_PATH", Optional: true, Default: ""},
		{Key: "WEBHOOK_SERVER_UNIFIED_HOST", Optional: true, Default: "false"},
		{Key: "HOSTED_CONFIG_DIR", Optional: true, Default: ""},
	}
	// Registered before t.Setenv so it runs after the environment is restored.
	t.Cleanup(func() {
		if err := config.InitializeConfigModule(defs); err != nil {
			t.Fatalf("failed to restore config module: %v", err)
		}
	})
	t.Setenv("HOSTED_CONFIG_DIR", dir)
	t.Setenv("BASE_PATH", basePath)
	if err := config.InitializeConfigModule(defs); err != nil {
		t.Fatalf("failed to initialize config module: %v", err)
	}
}

// hostedConfigRouter mounts the server's real route set, so the tests also pin
// that hosted config is registered ahead of the UI catch-all.
func hostedConfigRouter() *mux.Router {
	s := &Server{logger: logr.Discard()}
	root := mux.NewRouter()
	base := root
	if bp := BasePath(); bp != "" {
		base = root.PathPrefix(bp).Subrouter()
	}
	s.registerRoutes(base)
	return root
}

func writeHostedFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func get(router http.Handler, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

const sampleYAML = `# org default
extends:
  - config:recommended
timezone: Europe/Berlin
`

const sampleJSON = `{"extends":["config:recommended"],"timezone":"Europe/Berlin"}`

func TestHostedConfigConvertsYAMLForEachExtension(t *testing.T) {
	dir := t.TempDir()
	setHostedConfig(t, dir, "")
	writeHostedFile(t, dir, "default.yaml", sampleYAML)
	router := hostedConfigRouter()

	for _, ext := range []string{"json", "jsonc", "json5"} {
		rec := get(router, "/config/default."+ext)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", ext, rec.Code)
		}
		if got := rec.Body.String(); got != sampleJSON {
			t.Errorf("%s: body = %q, want %q", ext, got, sampleJSON)
		}
		if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
			t.Errorf("%s: Content-Type = %q", ext, got)
		}
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: X-Content-Type-Options = %q, want nosniff", ext, got)
		}
	}
}

func TestHostedConfigAcceptsJSONWrittenInYAMLFile(t *testing.T) {
	dir := t.TempDir()
	setHostedConfig(t, dir, "")
	writeHostedFile(t, dir, "default.yaml", "{\n  \"packageRules\": [{\"matchUpdateTypes\": [\"patch\"], \"automerge\": true}]\n}\n")
	want := `{"packageRules":[{"automerge":true,"matchUpdateTypes":["patch"]}]}`
	if got := get(hostedConfigRouter(), "/config/default.json").Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestHostedConfigInvalidYAMLIs500WithoutContent(t *testing.T) {
	dir := t.TempDir()
	setHostedConfig(t, dir, "")
	// The parser error for an unknown alias names it.
	writeHostedFile(t, dir, "broken.yaml", "a: *secretish\n")
	rec := get(hostedConfigRouter(), "/config/broken.json")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "secretish") {
		t.Errorf("error body echoes the file: %q", rec.Body.String())
	}
}

func TestHostedConfigServesEditsWithoutRestart(t *testing.T) {
	dir := t.TempDir()
	setHostedConfig(t, dir, "")
	writeHostedFile(t, dir, "default.yaml", "a: 1\n")
	router := hostedConfigRouter()
	if got := get(router, "/config/default.json").Body.String(); got != `{"a":1}` {
		t.Fatalf("first read = %q", got)
	}

	writeHostedFile(t, dir, "default.yaml", "a: 2\n")

	if got := get(router, "/config/default.json").Body.String(); got != `{"a":2}` {
		t.Errorf("after edit = %q, want the new content", got)
	}
}

func TestHostedConfigRefusesEverythingElse(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "served")
	setHostedConfig(t, dir, "")
	writeHostedFile(t, dir, "default.yaml", "a: 1\n")
	writeHostedFile(t, dir, "other.yml", "a: 1\n")
	writeHostedFile(t, dir, "raw.json", "{}")
	writeHostedFile(t, dir, "raw.json5", "{}")
	writeHostedFile(t, dir, "notes.txt", "x")
	writeHostedFile(t, dir, ".hidden.yaml", "a: 1\n")
	writeHostedFile(t, dir, "..data/default.yaml", "a: 1\n")
	writeHostedFile(t, dir, "sub/default.yaml", "a: 1\n")
	writeHostedFile(t, dir, "dir.yaml/x", "x")
	writeHostedFile(t, root, "outside.yaml", "secret: true\n")
	router := hostedConfigRouter()

	for _, target := range []string{
		"/config/missing.json",
		"/config/default.yaml",
		"/config/default.yml",
		"/config/other.json",
		"/config/default",
		"/config/raw.json",
		"/config/raw.json5",
		"/config/notes.txt",
		"/config/notes.json",
		"/config/.hidden.json",
		"/config/..data/default.json",
		"/config/sub/default.json",
		"/config/dir.json",
		"/config/",
	} {
		if rec := get(router, target); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d with body %q, want 404", target, rec.Code, rec.Body.String())
		}
	}
	// The router redirects these to a cleaned path before any handler runs.
	for _, target := range []string{"/config/..%2Foutside.json", "/config/%2e%2e%2foutside.json", "/config/..%2Foutside.yaml"} {
		if rec := get(router, target); rec.Code == http.StatusOK {
			t.Errorf("GET %s = 200 with body %q, want it refused", target, rec.Body.String())
		}
	}
}

func TestIsHostedConfigName(t *testing.T) {
	cases := map[string]bool{
		"default.json":     true,
		"default.jsonc":    true,
		"team-a_1.json5":   true,
		"a.b.json":         true,
		"../x.json":        false,
		"a/b.json":         false,
		`a\b.json`:         false,
		"..data":           false,
		".x.json":          false,
		"_base.json":       false,
		"-x.json":          false,
		"x.json\n":         false,
		"x.json.bak":       false,
		"x.js":             false,
		"x.JSON":           false,
		".json":            false,
		"":                 false,
		"a b.json":         false,
		"..2024_01/x.json": false,
	}
	for name, want := range cases {
		if got := isHostedConfigName(name); got != want {
			t.Errorf("isHostedConfigName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestHostedConfigAnswersHead(t *testing.T) {
	dir := t.TempDir()
	setHostedConfig(t, dir, "")
	writeHostedFile(t, dir, "default.yaml", "{}")
	rec := httptest.NewRecorder()
	hostedConfigRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/config/default.json", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("HEAD status = %d, want 200", rec.Code)
	}
}

func TestHostedConfigIsNotCached(t *testing.T) {
	dir := t.TempDir()
	setHostedConfig(t, dir, "")
	writeHostedFile(t, dir, "default.yaml", "{}")
	if got := get(hostedConfigRouter(), "/config/default.json").Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}
}

func TestHostedConfigRejectsWrites(t *testing.T) {
	dir := t.TempDir()
	setHostedConfig(t, dir, "")
	writeHostedFile(t, dir, "default.yaml", "{}")
	rec := httptest.NewRecorder()
	hostedConfigRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/config/default.json", nil))
	if rec.Code == http.StatusOK {
		t.Errorf("POST status = 200, want it refused")
	}
}

func TestHostedConfigOffWhenDirUnset(t *testing.T) {
	dir := t.TempDir()
	writeHostedFile(t, dir, "default.yaml", "{}")
	// An empty directory setting must not fall back to the working directory.
	t.Chdir(dir)
	setHostedConfig(t, "", "")
	if rec := get(hostedConfigRouter(), "/config/default.json"); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHostedConfigHonoursBasePath(t *testing.T) {
	dir := t.TempDir()
	setHostedConfig(t, dir, "/renovate")
	writeHostedFile(t, dir, "default.yaml", "a: 1\n")
	router := hostedConfigRouter()

	if rec := get(router, "/renovate/config/default.json5"); rec.Code != http.StatusOK || rec.Body.String() != `{"a":1}` {
		t.Errorf("under base path: status = %d body = %q", rec.Code, rec.Body.String())
	}
	if rec := get(router, "/config/default.json5"); rec.Code == http.StatusOK {
		t.Errorf("outside base path: status = 200, want it not served")
	}
}

func TestHostedConfigIsPublicOnlyWhenEnabled(t *testing.T) {
	setHostedConfig(t, t.TempDir(), "")
	if !isPublicPath("/config/default.json") {
		t.Errorf("enabled: /config/default.json should not require login")
	}
	for _, path := range []string{"/config/", "/config/sub/default.json", "/config/notes.txt", "/config/..data"} {
		if isPublicPath(path) {
			t.Errorf("enabled: %s is not a hosted file and must stay behind login", path)
		}
	}

	setHostedConfig(t, "", "")
	if isPublicPath("/config/default.json") {
		t.Errorf("disabled: /config/default.json must stay behind login")
	}
}

func TestHostedConfigNonMappingYAMLIs500(t *testing.T) {
	dir := t.TempDir()
	setHostedConfig(t, dir, "")
	writeHostedFile(t, dir, "empty.yaml", "")
	writeHostedFile(t, dir, "list.yaml", "- a\n- b\n")
	writeHostedFile(t, dir, "scalar.yaml", "just text\n")
	router := hostedConfigRouter()
	for _, name := range []string{"empty", "list", "scalar"} {
		if rec := get(router, "/config/"+name+".json"); rec.Code != http.StatusInternalServerError {
			t.Errorf("%s: status = %d body = %q, want 500", name, rec.Code, rec.Body.String())
		}
	}
}

func TestHostedConfigDottedNameUsesWholeBase(t *testing.T) {
	dir := t.TempDir()
	setHostedConfig(t, dir, "")
	writeHostedFile(t, dir, "team.a.yaml", "from: team.a\n")
	writeHostedFile(t, dir, "team.yaml", "from: team\n")
	if got := get(hostedConfigRouter(), "/config/team.a.json").Body.String(); got != `{"from":"team.a"}` {
		t.Errorf("body = %q, want team.a.yaml", got)
	}
}

func TestHostedConfigRefusesOversizedSource(t *testing.T) {
	dir := t.TempDir()
	setHostedConfig(t, dir, "")
	writeHostedFile(t, dir, "ok.yaml", "description: "+strings.Repeat("a", maxHostedSourceBytes-len("description: \n"))+"\n")
	writeHostedFile(t, dir, "big.yaml", "description: "+strings.Repeat("a", maxHostedSourceBytes)+"\n")
	router := hostedConfigRouter()
	if rec := get(router, "/config/ok.json"); rec.Code != http.StatusOK {
		t.Errorf("at the limit: status = %d, want 200", rec.Code)
	}
	rec := get(router, "/config/big.json")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("over the limit: status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), dir) {
		t.Errorf("error body reveals the directory: %q", rec.Body.String())
	}
}

func TestHostedConfigConvertsOnlyWhenSourceChanges(t *testing.T) {
	var c hostedConfigCache
	first, _, err := c.convert("default", []byte("a: 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	again, _, _ := c.convert("default", []byte("a: 1\n"))
	if &first[0] != &again[0] {
		t.Errorf("unchanged source was converted again")
	}

	changed, _, _ := c.convert("default", []byte("a: 2\n"))
	if string(changed) != `{"a":2}` {
		t.Errorf("changed source = %q, want the new JSON", changed)
	}
	other, _, _ := c.convert("other", []byte("a: 1\n"))
	if string(other) != `{"a":1}` || &other[0] == &first[0] {
		t.Errorf("a second preset must have its own entry, got %q", other)
	}
}

func TestHostedConfigRemembersABrokenSourceUntilItChanges(t *testing.T) {
	var c hostedConfigCache
	_, _, firstErr := c.convert("broken", []byte("a: *nope\n"))
	if firstErr == nil {
		t.Fatal("want an error for an unknown alias")
	}
	if _, fresh, err := c.convert("broken", []byte("a: *nope\n")); err != firstErr || fresh {
		t.Errorf("unchanged broken source was parsed again")
	}
	if body, _, err := c.convert("broken", []byte("a: 1\n")); err != nil || string(body) != `{"a":1}` {
		t.Errorf("fixed source: body = %q err = %v", body, err)
	}
}

func TestHostedConfigLogsABrokenSourceOncePerChange(t *testing.T) {
	dir := t.TempDir()
	setHostedConfig(t, dir, "")
	writeHostedFile(t, dir, "broken.yaml", "a: *nope\n")
	errorLogs := 0
	s := &Server{logger: funcr.New(func(prefix, args string) {
		if strings.Contains(args, `"error"`) {
			errorLogs++
		}
	}, funcr.Options{})}
	router := mux.NewRouter()
	s.registerRoutes(router)

	for range 3 {
		if rec := get(router, "/config/broken.json"); rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
	}
	if errorLogs != 1 {
		t.Errorf("error logs = %d, want 1 for an unchanged broken file", errorLogs)
	}

	writeHostedFile(t, dir, "broken.yaml", "a: *still\n")
	get(router, "/config/broken.json")
	if errorLogs != 2 {
		t.Errorf("error logs = %d, want 2 after the file changed", errorLogs)
	}
}
