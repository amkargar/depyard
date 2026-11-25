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
	defaultMaxArtifactBytes    = int64(2 * 1024 * 1024 * 1024) // 2 GiB (Flutter SDK tarballs can exceed 1 GiB)
	defaultUpstreamTimeoutSecs = 300
	defaultManifestTTLSecs     = 300
)

// DefaultAllowedPrefixes lists the official upstream path prefixes requested by Flutter SDK when FLUTTER_STORAGE_BASE_URL is configured.
var DefaultAllowedPrefixes = []string{
	"flutter_infra_release",
	"flutter_infra",
	"download.flutter.io",
	"dart-archive",
}

// Config holds validated Flutter Storage mirror settings stored inside Instance.Config.
// All fields are editable live in the Admin Dashboard without restarting the server.
type Config struct {
	AllowedPrefixes        []string `json:"allowed_prefixes,omitempty"`
	ManifestTTLSeconds     int      `json:"manifest_ttl_seconds,omitempty"`
	RewriteManifestURLs    bool     `json:"rewrite_manifest_urls"`
	ServeStaleOnError      bool     `json:"serve_stale_on_error"`
	MaxArtifactSizeBytes   int64    `json:"max_artifact_size_bytes,omitempty"`
	UpstreamTimeoutSeconds int      `json:"upstream_timeout_seconds,omitempty"`
}

func DefaultConfig() Config {
	return Config{
		AllowedPrefixes:        append([]string(nil), DefaultAllowedPrefixes...),
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
		"allowed_prefixes":         true,
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

	if cfg.ManifestTTLSeconds < 0 || cfg.ManifestTTLSeconds > 86400 {
		return cfg, fmt.Errorf("manifest_ttl_seconds must be between 0 and 86400")
	}
	if _, specified := raw["manifest_ttl_seconds"]; !specified {
		cfg.ManifestTTLSeconds = defaultManifestTTLSecs
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

	if len(cfg.AllowedPrefixes) == 0 {
		cfg.AllowedPrefixes = append([]string(nil), DefaultAllowedPrefixes...)
	} else {
		cleaned := make([]string, 0, len(cfg.AllowedPrefixes))
		for _, p := range cfg.AllowedPrefixes {
			p = strings.Trim(strings.TrimSpace(p), "/")
			if p == "" {
				continue
			}
			if strings.Contains(p, "..") || strings.ContainsRune(p, '\\') {
				return cfg, fmt.Errorf("invalid allowed_prefixes entry %q", p)
			}
			cleaned = append(cleaned, p)
		}
		if len(cleaned) == 0 {
			cfg.AllowedPrefixes = append([]string(nil), DefaultAllowedPrefixes...)
		} else {
			cfg.AllowedPrefixes = cleaned
		}
	}

	return cfg, nil
}

// ConfigHints describes Flutter Storage settings for registry-agnostic Admin UI forms.
func ConfigHints() []registry.ConfigFieldHint {
	return []registry.ConfigFieldHint{
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
