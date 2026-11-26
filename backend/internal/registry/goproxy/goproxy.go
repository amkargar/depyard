// Package goproxy implements a Go module proxy (the GOPROXY protocol) as a registry type.
//
// It mirrors the ecosystem-neutral registry contract: a registry instance of type "goproxy"
// serves module metadata, version lists, and module zip archives from a configurable upstream
// (proxy.golang.org by default), caching everything in the shared content-addressed store.
//
// The protocol implemented here is the one documented at https://go.dev/ref/mod#goproxy-protocol:
//
//	GET $base/<module>/@v/list              -> list of known versions
//	GET $base/<module>/@v/<version>.info    -> JSON {"Version": "...", "Time": "..."}
//	GET $base/<module>/@v/<version>.mod      -> the module's go.mod file
//	GET $base/<module>/@v/<version>.zip      -> the module source zip
//
// A request for a path that is not one of the above must return 404 or 410; anything else
// (in particular a directory listing) is a protocol violation.
package goproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/amkargar/depyard/backend/internal/registry"
)

const RegistryType = "goproxy"

// Default upstream used by the Go module proxy protocol.
const DefaultUpstreamURL = "https://proxy.golang.org"

const defaultMaxModuleSizeBytes int64 = 512 * 1024 * 1024 // 512 MiB per module zip

// Config holds validated Go-proxy-specific registry settings stored inside Instance.Config.
type Config struct {
	UpstreamTimeoutSeconds int      `json:"upstream_timeout_seconds,omitempty"`
	MaxModuleSizeBytes     int64    `json:"max_module_size_bytes,omitempty"`
	AllowedModules         []string `json:"allowed_modules,omitempty"`
	BlockedModules         []string `json:"blocked_modules,omitempty"`
	ProtectedModules       []string `json:"protected_modules,omitempty"`
	// IncludePrerelease keeps pseudo-versions and pre-release tags in @v/list responses.
	IncludePrerelease bool `json:"include_prerelease"`
}

func DefaultConfig() Config {
	return Config{
		UpstreamTimeoutSeconds: 60,
		MaxModuleSizeBytes:     defaultMaxModuleSizeBytes,
		IncludePrerelease:      true,
	}
}

// ParseConfig validates the raw per-instance configuration, rejecting unknown keys early.
func ParseConfig(raw map[string]any) (Config, error) {
	cfg := DefaultConfig()
	if len(raw) == 0 {
		return cfg, nil
	}
	allowedKeys := map[string]bool{
		"upstream_timeout_seconds": true,
		"max_module_size_bytes":    true,
		"allowed_modules":          true,
		"blocked_modules":          true,
		"protected_modules":        true,
		"include_prerelease":       true,
	}
	for k := range raw {
		if !allowedKeys[k] {
			return cfg, fmt.Errorf("unknown goproxy config field %q", k)
		}
	}

	b, err := json.Marshal(raw)
	if err != nil {
		return cfg, fmt.Errorf("encode goproxy config: %w", err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("invalid goproxy config: %w", err)
	}
	if cfg.UpstreamTimeoutSeconds < 0 || cfg.UpstreamTimeoutSeconds > 600 {
		return cfg, fmt.Errorf("upstream_timeout_seconds must be between 0 and 600")
	}
	if cfg.UpstreamTimeoutSeconds == 0 {
		cfg.UpstreamTimeoutSeconds = 60
	}
	if cfg.MaxModuleSizeBytes < 0 || cfg.MaxModuleSizeBytes > 8*1024*1024*1024 {
		return cfg, fmt.Errorf("max_module_size_bytes must be between 0 and 8 GiB")
	}
	if cfg.MaxModuleSizeBytes == 0 {
		cfg.MaxModuleSizeBytes = defaultMaxModuleSizeBytes
	}
	return cfg, nil
}

// GoCapabilities returns the declared capabilities of the Go module proxy registry type.
//
// A Go proxy is a read-only mirror of the module ecosystem: there is no "publish a module"
// operation in the GOPROXY protocol, so Hosted and Publish are false.
func GoCapabilities() registry.Capabilities {
	return registry.Capabilities{
		Proxy:                 true,
		Hosted:                false,
		Publish:               false,
		Search:                false,
		ListVersions:          true,
		CacheWarm:             true,
		MetadataSync:          true,
		ArtifactDeletion:      true,
		VersionRetraction:     false,
		PackageOwnership:      false,
		SignatureVerification: true,
	}
}

// Registration returns the in-process registration descriptor used by the composition root.
func Registration(store MetadataStore) registry.Registration {
	return registry.Registration{
		Type:               RegistryType,
		DisplayName:        "Go Module Proxy",
		Description:        "Go module proxy (GOPROXY protocol): proxies module metadata, go.mod files, and source zips from proxy.golang.org or any compatible upstream.",
		DefaultUpstreamURL: DefaultUpstreamURL,
		Capabilities:       GoCapabilities(),
		ConfigHints: []registry.ConfigFieldHint{
			{
				Key:          "upstream_timeout_seconds",
				Label:        "Upstream Timeout (seconds)",
				Type:         "number",
				Description:  "Timeout for upstream Go proxy requests (1-600s).",
				DefaultValue: 60,
			},
			{
				Key:          "max_module_size_bytes",
				Label:        "Max Module Size (bytes)",
				Type:         "number",
				Description:  "Maximum allowed module zip size (default 512 MiB).",
				DefaultValue: defaultMaxModuleSizeBytes,
			},
			{
				Key:          "include_prerelease",
				Label:        "Include Pre-releases",
				Type:         "boolean",
				Description:  "Include pseudo-versions and pre-release tags in version lists.",
				DefaultValue: true,
			},
			{
				Key:         "blocked_modules",
				Label:       "Blocked Module Patterns",
				Type:        "string_list",
				Description: "Module path patterns denied from being mirrored.",
			},
			{
				Key:         "allowed_modules",
				Label:       "Allowed Module Patterns",
				Type:        "string_list",
				Description: "When set, only module paths matching these patterns are mirrored.",
			},
			{
				Key:         "protected_modules",
				Label:       "Protected Module Patterns",
				Type:        "string_list",
				Description: "Module path patterns that are never evicted from the local cache.",
			},
		},
		ValidateConfig: func(cfg map[string]any) error {
			_, err := ParseConfig(cfg)
			return err
		},
		Create: func(_ context.Context, inst *registry.Instance, deps registry.ServiceDeps) (registry.Registry, error) {
			return New(inst, deps, store)
		},
	}
}

// GoRegistry implements the GOPROXY protocol for a single configured registry instance.
type GoRegistry struct {
	inst    *registry.Instance
	cfg     Config
	deps    registry.ServiceDeps
	store   MetadataStore
	handler http.Handler
}

func New(inst *registry.Instance, deps registry.ServiceDeps, store MetadataStore) (*GoRegistry, error) {
	cfg, err := ParseConfig(inst.Config)
	if err != nil {
		return nil, err
	}
	if store == nil {
		store = NewMemoryStore()
	}
	r := &GoRegistry{
		inst:  inst.Clone(),
		cfg:   cfg,
		deps:  deps,
		store: store,
	}
	r.handler = r.buildHandler()
	return r, nil
}

func (r *GoRegistry) Type() string {
	return RegistryType
}

func (r *GoRegistry) Capabilities() registry.Capabilities {
	return GoCapabilities()
}

func (r *GoRegistry) Handler() http.Handler {
	return r.handler
}

func (r *GoRegistry) upstreamTimeout() time.Duration {
	if r.cfg.UpstreamTimeoutSeconds > 0 {
		return time.Duration(r.cfg.UpstreamTimeoutSeconds) * time.Second
	}
	return 60 * time.Second
}

func (r *GoRegistry) maxModuleBytes() int64 {
	if r.cfg.MaxModuleSizeBytes > 0 {
		return r.cfg.MaxModuleSizeBytes
	}
	return defaultMaxModuleSizeBytes
}

// upstreamBase returns the normalized upstream base URL used to build proxy request paths.
func (r *GoRegistry) upstreamBase() string {
	base := strings.TrimSpace(r.inst.UpstreamURL)
	if base == "" {
		base = DefaultUpstreamURL
	}
	return strings.TrimRight(base, "/")
}

// MatchesCompatPath implements registry.CompatPathMatcher so a default Go proxy instance can
// also serve the well-known root GOPROXY layout (GOPROXY=https://mirror.example.com).
func (r *GoRegistry) MatchesCompatPath(p string) bool {
	if p == "/cache" || strings.HasPrefix(p, "/cache/") {
		return false
	}
	// A root-level GOPROXY request always looks like /<module>/@v/<file>.
	return strings.Contains(p, "/@v/")
}

// sortVersions orders versions using Go's semantic version ordering rules.
// It falls back to a lexical comparison for versions that are not valid semver.
func sortVersions(versions []string) {
	sort.Slice(versions, func(i, j int) bool {
		return compareVersions(versions[i], versions[j]) < 0
	})
}
