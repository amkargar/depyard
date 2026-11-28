package config

import (
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/amkargar/depyard/backend/internal/auth"
)

// Config holds application-level configuration.
// It remains strictly ecosystem-agnostic; registry-specific settings live on each registry instance.
type Config struct {
	ListenAddr                string
	TLSCertFile               string
	TLSKeyFile                string
	PublicBaseURL             string
	DataDir                   string
	StorageBackend            string // "mongo" (default) or "memory" (dev/testing)
	MongoURI                  string
	MongoDatabase             string
	EnableRootCompatRoute     bool
	DefaultRegistrySlug       string
	DefaultRegistryName       string
	DefaultRegistryType       string
	DefaultRegistryMode       string
	DefaultUpstreamURL        string
	SeedDefaultRegistry       bool
	SeedFlutterStorage        bool
	DefaultFlutterSlug        string
	DefaultFlutterName        string
	DefaultFlutterUpstreamURL string
	SeedGoProxy               bool
	DefaultGoSlug             string
	DefaultGoName             string
	DefaultGoUpstreamURL      string
	SeedPyPI                  bool
	DefaultPyPISlug           string
	DefaultPyPIName           string
	DefaultPyPIUpstreamURL    string
	DefaultPyPIMode           string
	SeedNPM                   bool
	DefaultNPMSlug            string
	DefaultNPMName            string
	DefaultNPMUpstreamURL     string
	DefaultNPMMode            string
	UpstreamProxyURL          string
	AdminUsername             string
	AdminPassword             string
	AdminEmail                string
	MinPasswordLength         int
	SessionTTL                time.Duration
	CacheEvictionInterval     time.Duration
	HealthCheckInterval       time.Duration
	StatsFlushInterval        time.Duration
	UpstreamDefaultTimeout    time.Duration
	AllowedOrigins            []string
	SeedDemoPackages          bool
}

// Load reads configuration from environment variables with sensible defaults.
func Load() Config {
	return Config{
		ListenAddr:  getEnv("LISTEN_ADDR", net.JoinHostPort(getEnv("BIND_HOST", "0.0.0.0"), getEnv("BIND_PORT", "8080"))),
		TLSCertFile: getEnv("TLS_CERT_FILE", ""), TLSKeyFile: getEnv("TLS_KEY_FILE", ""),
		PublicBaseURL:             strings.TrimRight(getEnv("PUBLIC_BASE_URL", ""), "/"),
		DataDir:                   getEnv("DATA_DIR", "./data"),
		StorageBackend:            strings.ToLower(getEnv("STORAGE_BACKEND", "mongo")),
		MongoURI:                  getEnv("MONGODB_URI", "mongodb://localhost:27017"),
		MongoDatabase:             getEnv("MONGODB_DATABASE", "mirror_registry"),
		EnableRootCompatRoute:     getEnvBool("ENABLE_ROOT_COMPAT_ROUTE", true),
		DefaultRegistrySlug:       getEnv("DEFAULT_REGISTRY_SLUG", "pub-public"),
		DefaultRegistryName:       getEnv("DEFAULT_REGISTRY_NAME", "Pub Public Mirror"),
		DefaultRegistryType:       getEnv("DEFAULT_REGISTRY_TYPE", "pub"),
		DefaultRegistryMode:       getEnv("DEFAULT_REGISTRY_MODE", "hybrid"),
		DefaultUpstreamURL:        getEnv("DEFAULT_UPSTREAM_URL", "https://pub.dev"),
		SeedDefaultRegistry:       getEnvBool("SEED_DEFAULT_REGISTRY", true),
		SeedFlutterStorage:        getEnvBool("SEED_FLUTTER_STORAGE", true),
		DefaultFlutterSlug:        getEnv("DEFAULT_FLUTTER_SLUG", "flutter-storage"),
		DefaultFlutterName:        getEnv("DEFAULT_FLUTTER_NAME", "Flutter Storage Mirror"),
		DefaultFlutterUpstreamURL: getEnv("DEFAULT_FLUTTER_UPSTREAM_URL", "https://storage.googleapis.com"),
		SeedGoProxy:               getEnvBool("SEED_GO_PROXY", true),
		DefaultGoSlug:             getEnv("DEFAULT_GO_SLUG", "go-proxy"),
		DefaultGoName:             getEnv("DEFAULT_GO_NAME", "Go Module Proxy"),
		DefaultGoUpstreamURL:      getEnv("DEFAULT_GO_UPSTREAM_URL", "https://proxy.golang.org"),
		SeedPyPI:                  getEnvBool("SEED_PYPI", true),
		DefaultPyPISlug:           getEnv("DEFAULT_PYPI_SLUG", "python-packages"),
		DefaultPyPIName:           getEnv("DEFAULT_PYPI_NAME", "Python Package Index"),
		DefaultPyPIUpstreamURL:    getEnv("DEFAULT_PYPI_UPSTREAM_URL", "https://pypi.org"),
		DefaultPyPIMode:           getEnv("DEFAULT_PYPI_MODE", "hybrid"),
		SeedNPM:                   getEnvBool("SEED_NPM", true),
		DefaultNPMSlug:            getEnv("DEFAULT_NPM_SLUG", "npm-packages"),
		DefaultNPMName:            getEnv("DEFAULT_NPM_NAME", "npm Registry"),
		DefaultNPMUpstreamURL:     getEnv("DEFAULT_NPM_UPSTREAM_URL", "https://registry.npmjs.org"),
		DefaultNPMMode:            getEnv("DEFAULT_NPM_MODE", "hybrid"),
		UpstreamProxyURL:          getEnv("UPSTREAM_PROXY_URL", ""),
		AdminUsername:             getEnv("ADMIN_USERNAME", "admin"),
		AdminPassword:             strings.TrimSpace(os.Getenv("ADMIN_PASSWORD")),
		AdminEmail:                getEnv("ADMIN_EMAIL", "admin@localhost"),
		MinPasswordLength:         getEnvInt("MIN_PASSWORD_LENGTH", auth.DefaultMinPasswordLength),
		SessionTTL:                getEnvDuration("SESSION_TTL", 24*time.Hour),
		CacheEvictionInterval:     getEnvDuration("CACHE_EVICTION_INTERVAL", 5*time.Minute),
		HealthCheckInterval:       getEnvDuration("HEALTH_CHECK_INTERVAL", 2*time.Minute),
		StatsFlushInterval:        getEnvDuration("STATS_FLUSH_INTERVAL", 30*time.Second),
		UpstreamDefaultTimeout:    getEnvDuration("UPSTREAM_TIMEOUT", 30*time.Second),
		AllowedOrigins:            getEnvSlice("ALLOWED_ORIGINS", []string{"*"}),
		SeedDemoPackages:          getEnvBool("SEED_DEMO_PACKAGES", false),
	}
}

func getEnv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return fallback
	}
	return n
}

func getEnvBool(key string, fallback bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}

func getEnvSlice(key string, fallback []string) []string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return fallback
	}
	return out
}
