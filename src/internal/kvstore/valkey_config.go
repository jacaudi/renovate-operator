package kvstore

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// ValkeyConfig holds the configuration for connecting to Valkey.
// Either URL or Host must be set; URL takes precedence over
// Host/Port/Username/Password/TLS.
type ValkeyConfig struct {
	URL      string
	Host     string
	Port     string
	Username string
	Password string
	TLS      bool
	Cluster  bool
	// MultiDB selects per-usage databases: nil = auto (split unless cluster),
	// true = split in both modes, false = one database in both modes.
	MultiDB *bool
}

func (cfg *ValkeyConfig) IsConfigured() bool {
	return cfg.URL != "" || cfg.Host != ""
}

// ConfigFromEnv builds a ValkeyConfig by looking up the VALKEY_* configuration
// values through the provided getter (typically config.GetValue). Keeping the
// lookup injected avoids coupling this package to the config singleton.
func ConfigFromEnv(get func(key string) string) ValkeyConfig {
	var multiDB *bool
	if v := get("VALKEY_MULTI_DB"); v != "" {
		multiDB = new(v == "true")
	}
	return ValkeyConfig{
		URL:      get("VALKEY_URL"),
		Host:     get("VALKEY_HOST"),
		Port:     get("VALKEY_PORT"),
		Username: get("VALKEY_USERNAME"),
		Password: get("VALKEY_PASSWORD"),
		TLS:      get("VALKEY_TLS") == "true",
		Cluster:  get("VALKEY_CLUSTER") == "true",
		MultiDB:  multiDB,
	}
}

// Usage identifies a logical Valkey database role.
// The value doubles as an offset from the base database when a predefined URL is used,
// or as an absolute database index when connecting via host/port.
type Usage int

// The Renovate cache takes the base slot: Renovate's cluster client ignores the
// URL database on its data connections, so its cache data always lands in
// database 0. With base 0 that is the cache's own database, which no operator
// store uses.
const (
	UsageRenovateCache Usage = 0 // Renovate job cache forwarded to executor jobs
	UsageSessionStore  Usage = 1 // Session encryption store
	UsageRenovateLogs  Usage = 2 // Log storage for completed Renovate runs
)

// URLForUsage returns the Valkey connection URL for the given usage.
//
// ValkeyConfig.MultiDB decides whether each usage gets its own database:
//
//   - nil (auto): split for standalone, one database for cluster (a cluster URL
//     uses redis+cluster:// / rediss+cluster://, a Host config sets Cluster).
//   - true: split in both modes. Cluster mode needs Valkey 9.0+ with
//     cluster-databases set to 3 or more, or SELECT fails.
//   - false: one database in both modes.
//
// When split:
//
//   - URL-based (ValkeyConfig.URL set): the URL's database index is the base, and the
//     usage value is added as an offset. A predefined URL of redis://host/5 yields:
//     UsageRenovateCache→5, UsageSessionStore→6, UsageRenovateLogs→7.
//     If the URL carries no explicit database (e.g. redis://host), base is 0.
//
//   - Host-based (ValkeyConfig.Host set): usage value is the absolute database index.
//     UsageRenovateCache→0, UsageSessionStore→1, UsageRenovateLogs→2.
//
// When not split, a URL is returned unchanged and a Host config uses database 0;
// the usages' key prefixes don't collide.
//
// Returns "" if neither URL nor Host is configured.
func (cfg ValkeyConfig) URLForUsage(usage Usage) string {
	cluster := cfg.Cluster
	if cfg.URL != "" {
		cluster = isClusterURL(cfg.URL)
	}
	multiDB := !cluster
	if cfg.MultiDB != nil {
		multiDB = *cfg.MultiDB
	}
	db := 0
	if multiDB {
		db = int(usage)
	}
	if cfg.URL == "" {
		return BuildValkeyURL(cfg.Host, cfg.Port, cfg.Username, cfg.Password, cfg.TLS, cfg.Cluster, db)
	}
	if !multiDB {
		return cfg.URL
	}
	return offsetURLDB(cfg.URL, db)
}

// isClusterURL reports whether rawURL uses a cluster scheme (redis+cluster://,
// rediss+cluster://), the convention Renovate uses for cluster connections.
func isClusterURL(rawURL string) bool {
	scheme, _, ok := strings.Cut(rawURL, "://")
	return ok && strings.HasSuffix(scheme, "+cluster")
}

// BuildValkeyURL constructs a Valkey URL from host, port, credentials, and
// database index. Returns "" if host is empty. Uses the redis:// scheme
// (wire-compatible with Valkey), or rediss:// when useTLS is set.
// When cluster is true, "+cluster" is appended to the scheme; db is kept
// (Valkey 9+ clusters support multiple databases via cluster-databases).
// A username without a password is emitted as user@host (valid for ACL users
// created with nopass).
func BuildValkeyURL(host, port, username, password string, useTLS, cluster bool, db int) string {
	if host == "" {
		return ""
	}
	if port == "" {
		port = "6379"
	}
	scheme := "redis"
	if useTLS {
		scheme = "rediss"
	}
	if cluster {
		scheme += "+cluster"
	}
	u := url.URL{
		Scheme: scheme,
		Host:   host + ":" + port,
		Path:   fmt.Sprintf("/%d", db),
	}
	switch {
	case password != "":
		u.User = url.UserPassword(username, password)
	case username != "":
		u.User = url.User(username)
	}
	return u.String()
}

// offsetURLDB parses rawURL, reads its database index from the path as a base,
// adds offset, and returns the modified URL.
func offsetURLDB(rawURL string, offset int) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	base := 0
	if p := strings.TrimPrefix(u.Path, "/"); p != "" {
		if n, parseErr := strconv.Atoi(p); parseErr == nil {
			base = n
		}
	}
	u.Path = fmt.Sprintf("/%d", base+offset)
	return u.String()
}
