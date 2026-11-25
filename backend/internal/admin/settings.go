package admin

import (
	"context"
	"strings"
	"time"

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
}

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

// SettingsStore persists global platform settings.
type SettingsStore interface {
	GetSettings(ctx context.Context) (*PlatformSettings, error)
	SaveSettings(ctx context.Context, s *PlatformSettings) error
}
