package admin

import (
	"context"
	"time"
)

// PlatformSettings holds global registry-agnostic platform settings stored in the `settings` collection.
type PlatformSettings struct {
	ID                    string    `json:"id" bson:"_id"`
	SiteTitle             string    `json:"site_title" bson:"site_title"`
	PublicBaseURL         string    `json:"public_base_url" bson:"public_base_url"`
	EnableRootCompatRoute bool      `json:"enable_root_compat_route" bson:"enable_root_compat_route"`
	AuditRetentionDays    int       `json:"audit_retention_days" bson:"audit_retention_days"`
	StatsRetentionDays    int       `json:"stats_retention_days" bson:"stats_retention_days"`
	UpdatedAt             time.Time `json:"updated_at" bson:"updated_at"`
}

func DefaultSettings(publicBaseURL string, rootCompat bool) *PlatformSettings {
	return &PlatformSettings{
		ID:                    "global",
		SiteTitle:             "Mirror Registry",
		PublicBaseURL:         publicBaseURL,
		EnableRootCompatRoute: rootCompat,
		AuditRetentionDays:    90,
		StatsRetentionDays:    90,
		UpdatedAt:             time.Now().UTC(),
	}
}

// SettingsStore persists global platform settings.
type SettingsStore interface {
	GetSettings(ctx context.Context) (*PlatformSettings, error)
	SaveSettings(ctx context.Context, s *PlatformSettings) error
}
