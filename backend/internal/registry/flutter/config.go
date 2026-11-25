package flutter

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/amkargar/mirror-registry/backend/internal/registry"
)

const (
	RegistryType               = "flutter"
	DefaultUpstreamURL         = "https://storage.googleapis.com"
	DefaultReleasesBaseURL     = "https://storage.googleapis.com/flutter_infra_release/releases"
	defaultMaxArtifactBytes    = int64(2 * 1024 * 1024 * 1024) // 2 GiB (Flutter SDK tarballs can exceed 1 GiB)
	defaultUpstreamTimeoutSecs = 300
	defaultManifestTTLSecs     = 300
	defaultRegistryTTLSecs     = 900
)

// DefaultAllowedPrefixes lists the official upstream path prefixes requested by Flutter SDK when FLUTTER_STORAGE_BASE_URL is configured.
var DefaultAllowedPrefixes = []string{
	"flutter_infra_release",
	"flutter_infra",
	"download.flutter.io",
	"dart-archive",
}

// DownloadStorageSetting describes one of the FLUTTER_STORAGE_BASE_URL categories the Flutter CLI
// and Gradle plugin consult when resolving SDK releases, engine artifacts, and Dart archives.
// Admins can point each category at a different upstream repository (for example a regional or
// internal mirror) while all requests still flow through this single registry.
type DownloadStorageSetting struct {
	// Name is the canonical FLUTTER_STORAGE_BASE_URL category, e.g. "flutter_infra_release".
	Name string `json:"name"`

	// UpstreamURL replaces the registry-level upstream URL for this category only.
	// Empty means "inherit the registry Upstream URL".
	UpstreamURL string `json:"upstream_url,omitempty"`

	// AllowedPrefixes restricts the mirrored paths served from this category.
	// Empty means "use the category name as the sole prefix".
	AllowedPrefixes []string `json:"allowed_prefixes,omitempty"`

	// ManifestTTLSeconds overrides the mutable manifest cache TTL for this category.
	// Zero means "inherit the registry manifest_ttl_seconds".
	ManifestTTLSeconds int `json:"manifest_ttl_seconds,omitempty"`

	// RewriteManifestURLs overrides base_url rewriting for this category.
	RewriteManifestURLs *bool `json:"rewrite_manifest_urls,omitempty"`

	// Enabled toggles serving this category without deleting its configuration.
	Enabled *bool `json:"enabled,omitempty"`
}

func (d DownloadStorageSetting) isEnabled() bool {
	return d.Enabled == nil || *d.Enabled
}

func (d DownloadStorageSetting) rewritesManifests() bool {
	return d.RewriteManifestURLs == nil || *d.RewriteManifestURLs
}

// DefaultDownloadStorage returns the built-in FLUTTER_STORAGE_BASE_URL category registry used by the
// Flutter SDK, ordered by how the Flutter tool resolves SDK downloads.
func DefaultDownloadStorage() []DownloadStorageSetting {
	out := make([]DownloadStorageSetting, 0, len(DefaultAllowedPrefixes))
	for _, name := range DefaultAllowedPrefixes {
		out = append(out, DownloadStorageSetting{Name: name})
	}
	return out
}

// Config holds validated Flutter Storage mirror settings stored inside Instance.Config.
// All fields are editable live in the Admin Dashboard without restarting the server.
type Config struct {
	// StorageName is a human-friendly label for this Flutter Storage registry shown in the Admin UI.
	StorageName string `json:"storage_name,omitempty"`

	// AllowedPrefixes is the merged allowlist of mirrored top-level storage paths.
	AllowedPrefixes []string `json:"allowed_prefixes,omitempty"`

	// DownloadStorage holds the per-category FLUTTER_STORAGE_BASE_URL settings.
	// When omitted (legacy instances), it is derived from AllowedPrefixes.
	DownloadStorage []DownloadStorageSetting `json:"download_storage,omitempty"`

	// ReleasesBaseURL optionally points the Flutter release manifest lookup at a different
	// upstream repository (e.g. https://storage.flutter-io.cn/flutter_infra_release/releases).
	ReleasesBaseURL string `json:"releases_base_url,omitempty"`

	// RegistryTTLSeconds is the cache duration for the derived per-version SDK registry index.
	RegistryTTLSeconds int `json:"registry_ttl_seconds,omitempty"`

	ManifestTTLSeconds     int   `json:"manifest_ttl_seconds,omitempty"`
	RewriteManifestURLs    bool  `json:"rewrite_manifest_urls"`
	ServeStaleOnError      bool  `json:"serve_stale_on_error"`
	MaxArtifactSizeBytes   int64 `json:"max_artifact_size_bytes,omitempty"`
	UpstreamTimeoutSeconds int   `json:"upstream_timeout_seconds,omitempty"`
}

func DefaultConfig() Config {
	return Config{
		StorageName:            "Flutter Storage",
		AllowedPrefixes:        append([]string(nil), DefaultAllowedPrefixes...),
		DownloadStorage:        DefaultDownloadStorage(),
		RegistryTTLSeconds:     defaultRegistryTTLSecs,
		ManifestTTLSeconds:     defaultManifestTTLSecs,
		RewriteManifestURLs:    true,
		ServeStaleOnError:      true,
		MaxArtifactSizeBytes:   defaultMaxArtifactBytes,
		UpstreamTimeoutSeconds: defaultUpstreamTimeoutSecs,
	}
}

func ParseConfig(raw map[string]any) (Config, error) {
	cfg := DefaultConfig()
	if len(raw) == 0 {
		return cfg, nil
	}

	allowedKeys := map[string]bool{
		"storage_name":             true,
		"allowed_prefixes":         true,
		"download_storage":         true,
		"releases_base_url":        true,
		"registry_ttl_seconds":     true,
		"manifest_ttl_seconds":     true,
		"rewrite_manifest_urls":    true,
		"serve_stale_on_error":     true,
		"max_artifact_size_bytes":  true,
		"upstream_timeout_seconds": true,
	}
	for k := range raw {
		if !allowedKeys[k] {
			return cfg, fmt.Errorf("unknown flutter storage config field %q", k)
		}
	}

	b, err := json.Marshal(raw)
	if err != nil {
		return cfg, fmt.Errorf("encode flutter config: %w", err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("invalid flutter config: %w", err)
	}

	cfg.StorageName = strings.TrimSpace(cfg.StorageName)
	if cfg.StorageName == "" {
		cfg.StorageName = "Flutter Storage"
	}

	if cfg.ManifestTTLSeconds < 0 || cfg.ManifestTTLSeconds > 86400 {
		return cfg, fmt.Errorf("manifest_ttl_seconds must be between 0 and 86400")
	}
	if _, specified := raw["manifest_ttl_seconds"]; !specified {
		cfg.ManifestTTLSeconds = defaultManifestTTLSecs
	}

	if cfg.RegistryTTLSeconds < 0 || cfg.RegistryTTLSeconds > 86400 {
		return cfg, fmt.Errorf("registry_ttl_seconds must be between 0 and 86400")
	}
	if cfg.RegistryTTLSeconds == 0 {
		cfg.RegistryTTLSeconds = defaultRegistryTTLSecs
	}

	if cfg.UpstreamTimeoutSeconds < 0 || cfg.UpstreamTimeoutSeconds > 3600 {
		return cfg, fmt.Errorf("upstream_timeout_seconds must be between 0 and 3600")
	}
	if cfg.UpstreamTimeoutSeconds == 0 {
		cfg.UpstreamTimeoutSeconds = defaultUpstreamTimeoutSecs
	}

	if cfg.MaxArtifactSizeBytes < 0 || cfg.MaxArtifactSizeBytes > 10*1024*1024*1024 {
		return cfg, fmt.Errorf("max_artifact_size_bytes must be between 0 and 10 GiB")
	}
	if cfg.MaxArtifactSizeBytes == 0 {
		cfg.MaxArtifactSizeBytes = defaultMaxArtifactBytes
	}

	cfg.ReleasesBaseURL = strings.TrimRight(strings.TrimSpace(cfg.ReleasesBaseURL), "/")
	if cfg.ReleasesBaseURL != "" && !strings.HasPrefix(cfg.ReleasesBaseURL, "http://") && !strings.HasPrefix(cfg.ReleasesBaseURL, "https://") {
		return cfg, fmt.Errorf("releases_base_url must be an http(s) URL")
	}

	_, prefixesSpecified := raw["allowed_prefixes"]
	if len(cfg.AllowedPrefixes) == 0 {
		cfg.AllowedPrefixes = nil
	} else {
		cleaned, err := sanitizePrefixList(cfg.AllowedPrefixes)
		if err != nil {
			return cfg, err
		}
		cfg.AllowedPrefixes = cleaned
	}

	if _, specified := raw["download_storage"]; !specified {
		// Derive the per-category registry from the legacy prefix allowlist so existing
		// instances keep working without losing their configuration.
		derived := make([]DownloadStorageSetting, 0, len(cfg.AllowedPrefixes))
		for _, p := range cfg.AllowedPrefixes {
			derived = append(derived, DownloadStorageSetting{Name: p})
		}
		if len(derived) == 0 {
			derived = DefaultDownloadStorage()
		}
		cfg.DownloadStorage = derived
	} else {
		seen := make(map[string]bool, len(cfg.DownloadStorage))
		for i := range cfg.DownloadStorage {
			entry := &cfg.DownloadStorage[i]
			entry.Name = strings.Trim(strings.TrimSpace(entry.Name), "/")
			if entry.Name == "" {
				return cfg, fmt.Errorf("download_storage entries require a name")
			}
			if strings.Contains(entry.Name, "..") || strings.ContainsRune(entry.Name, '\\') || strings.Contains(entry.Name, " ") {
				return cfg, fmt.Errorf("invalid download_storage name %q", entry.Name)
			}
			if seen[entry.Name] {
				return cfg, fmt.Errorf("duplicate download_storage entry %q", entry.Name)
			}
			seen[entry.Name] = true

			entry.UpstreamURL = strings.TrimRight(strings.TrimSpace(entry.UpstreamURL), "/")
			if entry.UpstreamURL != "" && !strings.HasPrefix(entry.UpstreamURL, "http://") && !strings.HasPrefix(entry.UpstreamURL, "https://") {
				return cfg, fmt.Errorf("download_storage[%s].upstream_url must be an http(s) URL", entry.Name)
			}
			if entry.ManifestTTLSeconds < 0 || entry.ManifestTTLSeconds > 86400 {
				return cfg, fmt.Errorf("download_storage[%s].manifest_ttl_seconds must be between 0 and 86400", entry.Name)
			}
			if entry.AllowedPrefixes != nil {
				cleaned, err := sanitizePrefixList(entry.AllowedPrefixes)
				if err != nil {
					return cfg, fmt.Errorf("download_storage[%s]: %w", entry.Name, err)
				}
				entry.AllowedPrefixes = cleaned
			}
		}
		// Always keep the canonical categories available so upstream Flutter clients never
		// fall into a 403 because an admin removed a built-in category by accident.
		for _, name := range DefaultAllowedPrefixes {
			if !seen[name] {
				cfg.DownloadStorage = append(cfg.DownloadStorage, DownloadStorageSetting{Name: name})
			}
		}
	}

	// Resolve the effective allowlist:
	//   - explicit allowed_prefixes            -> union with enabled categories
	//   - explicit download_storage (no prefixes) -> enabled categories only, so disabling a
	//     category genuinely stops serving it
	//   - nothing specified (legacy instance)  -> the built-in canonical categories
	switch {
	case prefixesSpecified:
		cfg.AllowedPrefixes = cfg.mergedAllowedPrefixes()
	case len(cfg.DownloadStorage) > 0:
		cfg.AllowedPrefixes = cfg.enabledCategoryPrefixes()
	default:
		cfg.AllowedPrefixes = append([]string(nil), DefaultAllowedPrefixes...)
	}
	return cfg, nil
}

// enabledCategoryPrefixes returns the union of prefixes served by enabled storage categories.
func (c Config) enabledCategoryPrefixes() []string {
	seen := make(map[string]bool, len(c.DownloadStorage))
	out := make([]string, 0, len(c.DownloadStorage))
	for _, d := range c.DownloadStorage {
		if !d.isEnabled() {
			continue
		}
		for _, p := range d.prefixes() {
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		out = append(out, DefaultAllowedPrefixes...)
	}
	return out
}

// disabledCategoryForPath reports the disabled storage category claiming cleanPath, if any.
func (c Config) disabledCategoryForPath(cleanPath string) (DownloadStorageSetting, bool) {
	for _, d := range c.DownloadStorage {
		if d.isEnabled() {
			continue
		}
		for _, prefix := range d.prefixes() {
			if cleanPath == prefix || strings.HasPrefix(cleanPath, prefix+"/") {
				return d, true
			}
		}
	}
	return DownloadStorageSetting{}, false
}

func sanitizePrefixList(in []string) ([]string, error) {
	cleaned := make([]string, 0, len(in))
	for _, p := range in {
		p = strings.Trim(strings.TrimSpace(p), "/")
		if p == "" {
			continue
		}
		if p != "*" && (strings.Contains(p, "..") || strings.ContainsRune(p, '\\')) {
			return nil, fmt.Errorf("invalid allowed_prefixes entry %q", p)
		}
		cleaned = append(cleaned, p)
	}
	if len(cleaned) == 0 {
		return append([]string(nil), DefaultAllowedPrefixes...), nil
	}
	return cleaned, nil
}

// mergedAllowedPrefixes unions the explicit allowlist with every enabled download_storage category.
func (c Config) mergedAllowedPrefixes() []string {
	seen := make(map[string]bool, len(c.AllowedPrefixes)+len(c.DownloadStorage))
	out := make([]string, 0, len(c.AllowedPrefixes)+len(c.DownloadStorage))
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, p := range c.AllowedPrefixes {
		add(p)
	}
	for _, d := range c.DownloadStorage {
		if !d.isEnabled() {
			continue
		}
		for _, p := range d.prefixes() {
			add(p)
		}
	}
	if len(out) == 0 {
		out = append(out, DefaultAllowedPrefixes...)
	}
	return out
}

// prefixes returns the effective path prefixes served by this download_storage category.
func (d DownloadStorageSetting) prefixes() []string {
	if len(d.AllowedPrefixes) == 0 {
		return []string{d.Name}
	}
	return d.AllowedPrefixes
}

// resolveCategory finds the enabled download_storage category that owns cleanPath.
func (c Config) resolveCategory(cleanPath string) (DownloadStorageSetting, bool) {
	var best DownloadStorageSetting
	bestLen := -1
	for _, d := range c.DownloadStorage {
		if !d.isEnabled() {
			continue
		}
		for _, prefix := range d.prefixes() {
			if prefix == "*" {
				if bestLen < 0 {
					best = d
					bestLen = 0
				}
				continue
			}
			if cleanPath == prefix || strings.HasPrefix(cleanPath, prefix+"/") {
				if len(prefix) > bestLen {
					best = d
					bestLen = len(prefix)
				}
			}
		}
	}
	return best, bestLen >= 0
}

// categoryUpstreamURL returns the upstream base URL used for this category.
func (c Config) categoryUpstreamURL(d DownloadStorageSetting, registryUpstream string) string {
	if d.UpstreamURL != "" {
		return d.UpstreamURL
	}
	return strings.TrimRight(registryUpstream, "/")
}

// categoryManifestTTL returns the effective manifest TTL for this category.
func (c Config) categoryManifestTTL(d DownloadStorageSetting) int {
	if d.ManifestTTLSeconds > 0 {
		return d.ManifestTTLSeconds
	}
	return c.ManifestTTLSeconds
}

// categoryRewriteManifests reports whether base_url rewriting applies to this category.
func (c Config) categoryRewriteManifests(d DownloadStorageSetting) bool {
	if d.RewriteManifestURLs != nil {
		return *d.RewriteManifestURLs
	}
	return c.RewriteManifestURLs
}

// ConfigHints describes Flutter Storage settings for registry-agnostic Admin UI forms.
func ConfigHints() []registry.ConfigFieldHint {
	return []registry.ConfigFieldHint{
		{
			Key:          "storage_name",
			Label:        "Flutter Storage Name",
			Type:         "string",
			Description:  "Human-friendly label for this Flutter Storage registry (shown in the Admin Console and Explorer).",
			DefaultValue: "Flutter Storage",
		},
		{
			Key:          "available_storage_categories",
			Label:        "Available Storage Categories",
			Type:         "string_list",
			Description:  "Read-only catalogue of the FLUTTER_STORAGE_BASE_URL categories this registry understands.",
			DefaultValue: DefaultAllowedPrefixes,
		},
		{
			Key:          "releases_base_url",
			Label:        "Release Manifest Base URL (optional)",
			Type:         "string",
			Description:  "Override where releases_*.json manifests are fetched from (e.g. a regional Flutter mirror). Empty uses <upstream>/flutter_infra_release/releases.",
			DefaultValue: DefaultReleasesBaseURL,
		},
		{
			Key:          "registry_ttl_seconds",
			Label:        "Version Registry TTL (seconds)",
			Type:         "number",
			Description:  "Cache duration for the derived per-version SDK registry index served at /api/registry.",
			DefaultValue: defaultRegistryTTLSecs,
		},
		{
			Key:          "allowed_prefixes",
			Label:        "Allowed Storage Prefixes",
			Type:         "string_list",
			Description:  "Upstream path prefixes mirrored for FLUTTER_STORAGE_BASE_URL (use * to allow any path).",
			DefaultValue: DefaultAllowedPrefixes,
		},
		{
			Key:          "manifest_ttl_seconds",
			Label:        "Release Manifest TTL (seconds)",
			Type:         "number",
			Description:  "Cache duration for mutable index files such as releases_linux.json and maven-metadata.xml.",
			DefaultValue: defaultManifestTTLSecs,
		},
		{
			Key:          "rewrite_manifest_urls",
			Label:        "Rewrite Manifest Base URLs",
			Type:         "boolean",
			Description:  "Rewrite base_url inside Flutter releases_*.json manifests to point at this mirror.",
			DefaultValue: true,
		},
		{
			Key:          "serve_stale_on_error",
			Label:        "Serve Stale Cache on Upstream Error",
			Type:         "boolean",
			Description:  "Return cached release manifests if the upstream storage host is temporarily unreachable.",
			DefaultValue: true,
		},
		{
			Key:          "upstream_timeout_seconds",
			Label:        "Upstream Download Timeout (seconds)",
			Type:         "number",
			Description:  "Timeout for upstream artifact downloads (default 300s for large SDK archives).",
			DefaultValue: defaultUpstreamTimeoutSecs,
		},
		{
			Key:          "max_artifact_size_bytes",
			Label:        "Max Artifact Size (bytes)",
			Type:         "number",
			Description:  "Maximum allowed size per mirrored Flutter artifact (default 2 GiB).",
			DefaultValue: defaultMaxArtifactBytes,
		},
	}
}
