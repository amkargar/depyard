package admin

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/amkargar/mirror-registry/backend/internal/registry"
	"github.com/amkargar/mirror-registry/backend/internal/upstream"
)

// PlatformSettings holds global registry-agnostic platform & upstream settings stored in the `settings` collection.
// Every field is editable live from the Admin Console without restarting the server.
type PlatformSettings struct {
	ID                         string    `json:"id" bson:"_id"`
	SiteTitle                  string    `json:"site_title" bson:"site_title"`
	PublicBaseURL              string    `json:"public_base_url" bson:"public_base_url"`
	EnableRootCompatRoute      bool      `json:"enable_root_compat_route" bson:"enable_root_compat_route"`
	AuditRetentionDays         int       `json:"audit_retention_days" bson:"audit_retention_days"`
	StatsRetentionDays         int       `json:"stats_retention_days" bson:"stats_retention_days"`
	UpstreamProxyURL           string    `json:"upstream_proxy_url" bson:"upstream_proxy_url"`
	UpstreamTimeoutSeconds     int       `json:"upstream_timeout_seconds" bson:"upstream_timeout_seconds"`
	UpstreamUserAgent          string    `json:"upstream_user_agent" bson:"upstream_user_agent"`
	UpstreamInsecureSkipVerify bool      `json:"upstream_insecure_skip_verify" bson:"upstream_insecure_skip_verify"`
	DefaultPubUpstreamURL      string    `json:"default_pub_upstream_url" bson:"default_pub_upstream_url"`
	DefaultFlutterStorageURL   string    `json:"default_flutter_storage_url" bson:"default_flutter_storage_url"`
	UpdatedAt                  time.Time `json:"updated_at" bson:"updated_at"`

	// DefaultFlutterStorageConfig is the platform-wide base FLUTTER_STORAGE_BASE_URL configuration.
	// It seeds new Flutter registries, can be pushed to existing ones through the apply endpoint, and
	// (in "inherit" mode) supplies values for fields a registry does not set.
	DefaultFlutterStorageConfig map[string]any `json:"default_flutter_storage_config,omitempty" bson:"default_flutter_storage_config,omitempty"`

	// FlutterBaseConfigMode selects how the base config is consumed:
	//   "defaults" -> seed new registries + manual apply only (default)
	//   "inherit"  -> unset per-registry fields also follow the base live
	FlutterBaseConfigMode string `json:"flutter_base_config_mode,omitempty" bson:"flutter_base_config_mode,omitempty"`
}

// FlutterBaseKey is the registry type keyed by BaseConfigSpec registration in the admin panel.
const flutterBaseRegistryType = "flutter"

// Flutter base-config modes.
const (
	FlutterBaseModeDefaults = "defaults"
	FlutterBaseModeInherit  = "inherit"
)

func DefaultSettings(publicBaseURL string, rootCompat bool) *PlatformSettings {
	return &PlatformSettings{
		ID:                         "global",
		SiteTitle:                  "Mirror Registry",
		PublicBaseURL:              strings.TrimRight(strings.TrimSpace(publicBaseURL), "/"),
		EnableRootCompatRoute:      rootCompat,
		AuditRetentionDays:         90,
		StatsRetentionDays:         90,
		UpstreamProxyURL:           "",
		UpstreamTimeoutSeconds:     60,
		UpstreamUserAgent:          "mirror-registry/1.0",
		UpstreamInsecureSkipVerify: false,
		DefaultPubUpstreamURL:      "https://pub.dev",
		DefaultFlutterStorageURL:   "https://storage.googleapis.com",
		FlutterBaseConfigMode:      FlutterBaseModeDefaults,
		UpdatedAt:                  time.Now().UTC(),
	}
}

// Normalize fills in sensible defaults for unset fields (e.g. migrating older persisted settings docs).
func (s *PlatformSettings) Normalize() {
	if s.ID == "" {
		s.ID = "global"
	}
	s.SiteTitle = strings.TrimSpace(s.SiteTitle)
	if s.SiteTitle == "" {
		s.SiteTitle = "Mirror Registry"
	}
	s.PublicBaseURL = strings.TrimRight(strings.TrimSpace(s.PublicBaseURL), "/")
	if s.AuditRetentionDays <= 0 {
		s.AuditRetentionDays = 90
	}
	if s.StatsRetentionDays <= 0 {
		s.StatsRetentionDays = 90
	}
	s.UpstreamProxyURL = strings.TrimSpace(s.UpstreamProxyURL)
	if s.UpstreamTimeoutSeconds <= 0 {
		s.UpstreamTimeoutSeconds = 60
	}
	if s.UpstreamTimeoutSeconds > 3600 {
		s.UpstreamTimeoutSeconds = 3600
	}
	s.UpstreamUserAgent = strings.TrimSpace(s.UpstreamUserAgent)
	if s.UpstreamUserAgent == "" {
		s.UpstreamUserAgent = "mirror-registry/1.0"
	}
	s.DefaultPubUpstreamURL = strings.TrimRight(strings.TrimSpace(s.DefaultPubUpstreamURL), "/")
	if s.DefaultPubUpstreamURL == "" {
		s.DefaultPubUpstreamURL = "https://pub.dev"
	}
	s.DefaultFlutterStorageURL = strings.TrimRight(strings.TrimSpace(s.DefaultFlutterStorageURL), "/")
	if s.DefaultFlutterStorageURL == "" {
		s.DefaultFlutterStorageURL = "https://storage.googleapis.com"
	}

	s.FlutterBaseConfigMode = strings.ToLower(strings.TrimSpace(s.FlutterBaseConfigMode))
	if s.FlutterBaseConfigMode != FlutterBaseModeInherit {
		s.FlutterBaseConfigMode = FlutterBaseModeDefaults
	}

	if s.DefaultFlutterStorageConfig == nil {
		s.DefaultFlutterStorageConfig = map[string]any{}
	}
	// Keep the legacy single-URL field consistent with the base config's upstream URL.
	if up, ok := s.DefaultFlutterStorageConfig["upstream_url"].(string); ok && strings.TrimSpace(up) != "" {
		s.DefaultFlutterStorageURL = strings.TrimRight(strings.TrimSpace(up), "/")
	}
}

// NormalizeFlutterBase validates the base Flutter Storage config through the registry type's own
// BaseConfigSpec (the same strict parser used at runtime) and keeps the legacy single-URL field in
// sync with it.
func (s *PlatformSettings) NormalizeFlutterBase(spec *registry.BaseConfigSpec) error {
	if s.DefaultFlutterStorageConfig == nil {
		s.DefaultFlutterStorageConfig = map[string]any{}
	}
	raw := s.DefaultFlutterStorageConfig
	// The legacy field doubles as the registry-level upstream for the base config.
	if _, ok := raw["upstream_url"]; !ok && s.DefaultFlutterStorageURL != "" {
		raw["upstream_url"] = strings.TrimRight(strings.TrimSpace(s.DefaultFlutterStorageURL), "/")
	}
	if spec != nil && spec.Normalize != nil {
		normalized, err := spec.Normalize(raw)
		if err != nil {
			return err
		}
		s.DefaultFlutterStorageConfig = normalized
	}
	if up, ok := s.DefaultFlutterStorageConfig["upstream_url"].(string); ok && strings.TrimSpace(up) != "" {
		s.DefaultFlutterStorageURL = strings.TrimRight(strings.TrimSpace(up), "/")
	}
	s.FlutterBaseConfigMode = strings.ToLower(strings.TrimSpace(s.FlutterBaseConfigMode))
	if s.FlutterBaseConfigMode != FlutterBaseModeInherit {
		s.FlutterBaseConfigMode = FlutterBaseModeDefaults
	}
	return nil
}

// BaseConfig implements registry.DefaultsProvider. It only serves the base when the mode is
// "inherit"; in "defaults" mode registries must be seeded/applied explicitly.
func (s *PlatformSettings) BaseConfig(_ context.Context, registryType string) (map[string]any, error) {
	if !strings.EqualFold(strings.TrimSpace(registryType), flutterBaseRegistryType) {
		return nil, nil
	}
	if s.FlutterBaseConfigMode != FlutterBaseModeInherit {
		return nil, nil
	}
	if len(s.DefaultFlutterStorageConfig) == 0 {
		return nil, nil
	}
	return cloneStringMap(s.DefaultFlutterStorageConfig), nil
}

// BaseConfigMap returns the raw base config for an arbitrary registry type (currently only the
// Flutter base is persisted as a first-class platform field).
func (s *PlatformSettings) BaseConfigMap(registryType string) map[string]any {
	if strings.EqualFold(strings.TrimSpace(registryType), flutterBaseRegistryType) {
		return s.SeedConfig()
	}
	return nil
}

// SeedConfig returns the base Flutter config used to pre-fill new registries regardless of mode.
func (s *PlatformSettings) SeedConfig() map[string]any {
	if len(s.DefaultFlutterStorageConfig) == 0 {
		return nil
	}
	return cloneStringMap(s.DefaultFlutterStorageConfig)
}

// ToUpstreamConfig converts global platform settings into an upstream.Config.
func (s *PlatformSettings) ToUpstreamConfig() upstream.Config {
	return upstream.Config{
		ProxyURL:           s.UpstreamProxyURL,
		DefaultTimeout:     time.Duration(s.UpstreamTimeoutSeconds) * time.Second,
		UserAgent:          s.UpstreamUserAgent,
		InsecureSkipVerify: s.UpstreamInsecureSkipVerify,
	}
}

// cloneStringMap deep-copies a config map through JSON so callers cannot mutate stored settings.
func cloneStringMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	b, err := json.Marshal(in)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

// SettingsStore persists global platform settings.
type SettingsStore interface {
	GetSettings(ctx context.Context) (*PlatformSettings, error)
	SaveSettings(ctx context.Context, s *PlatformSettings) error
}

// LiveDefaults is a late-bound registry.DefaultsProvider. The registry manager needs a provider at
// construction time while the provider needs the admin handler, which in turn needs the manager;
// LiveDefaults breaks that cycle and picks up new settings without a restart.
type LiveDefaults struct {
	mu sync.RWMutex
	fn func(ctx context.Context, registryType string) (map[string]any, error)
}

// Set installs the backing function.
func (l *LiveDefaults) Set(fn func(ctx context.Context, registryType string) (map[string]any, error)) {
	l.mu.Lock()
	l.fn = fn
	l.mu.Unlock()
}

// BaseConfig implements registry.DefaultsProvider.
func (l *LiveDefaults) BaseConfig(ctx context.Context, registryType string) (map[string]any, error) {
	l.mu.RLock()
	fn := l.fn
	l.mu.RUnlock()
	if fn == nil {
		return nil, nil
	}
	return fn(ctx, registryType)
}
