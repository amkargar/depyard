package registry

import "fmt"

// RepositoryMode defines how a registry instance sources and stores artifacts.
type RepositoryMode string

const (
	ModeProxy  RepositoryMode = "proxy"
	ModeHosted RepositoryMode = "hosted"
	ModeHybrid RepositoryMode = "hybrid"
)

// Visibility controls whether a registry instance is publicly discoverable/readable.
type Visibility string

const (
	VisibilityPublic  Visibility = "public"
	VisibilityPrivate Visibility = "private"
)

// Capabilities declares which features a registry type genuinely supports.
// The administration panel and API gate operations strictly on these flags.
type Capabilities struct {
	Proxy                 bool `json:"proxy"`
	Hosted                bool `json:"hosted"`
	Publish               bool `json:"publish"`
	Search                bool `json:"search"`
	ListVersions          bool `json:"list_versions"`
	CacheWarm             bool `json:"cache_warm"`
	MetadataSync          bool `json:"metadata_sync"`
	ArtifactDeletion      bool `json:"artifact_deletion"`
	VersionRetraction     bool `json:"version_retraction"`
	PackageOwnership      bool `json:"package_ownership"`
	SignatureVerification bool `json:"signature_verification"`
}

// SupportedModes derives the valid repository modes from declared capabilities.
func (c Capabilities) SupportedModes() []RepositoryMode {
	modes := make([]RepositoryMode, 0, 3)
	if c.Proxy {
		modes = append(modes, ModeProxy)
	}
	if c.Hosted {
		modes = append(modes, ModeHosted)
	}
	if c.Proxy && c.Hosted {
		modes = append(modes, ModeHybrid)
	}
	return modes
}

// SupportsMode validates whether a repository mode is permitted by these capabilities.
func (c Capabilities) SupportsMode(mode RepositoryMode) error {
	switch mode {
	case ModeProxy:
		if !c.Proxy {
			return fmt.Errorf("registry type does not support proxy mode")
		}
		return nil
	case ModeHosted:
		if !c.Hosted {
			return fmt.Errorf("registry type does not support hosted mode")
		}
		return nil
	case ModeHybrid:
		if !c.Proxy || !c.Hosted {
			return fmt.Errorf("registry type does not support hybrid mode")
		}
		return nil
	default:
		return fmt.Errorf("unknown repository mode %q", mode)
	}
}

// EffectiveCapabilities narrows a registry type's capabilities based on an instance's repository mode.
// For example, a proxy-only instance cannot accept publishing even if the registry type supports hosted mode.
func (c Capabilities) EffectiveForMode(mode RepositoryMode) Capabilities {
	eff := c
	if mode == ModeProxy {
		eff.Hosted = false
		eff.Publish = false
		eff.VersionRetraction = false
		eff.PackageOwnership = false
	}
	if mode == ModeHosted {
		eff.Proxy = false
		eff.CacheWarm = false
		eff.MetadataSync = false
	}
	return eff
}

// AuthPolicy holds genuinely shared access control settings for a registry instance.
type AuthPolicy struct {
	AllowAnonymousRead bool `json:"allow_anonymous_read" bson:"allow_anonymous_read"`
	RequireAuthPublish bool `json:"require_auth_publish" bson:"require_auth_publish"`
}

// CachePolicy holds genuinely shared proxy cache settings for a registry instance.
type CachePolicy struct {
	MetadataTTLSeconds      int64  `json:"metadata_ttl_seconds" bson:"metadata_ttl_seconds"`
	ArchiveTTLSeconds       int64  `json:"archive_ttl_seconds" bson:"archive_ttl_seconds"`
	MaxSizeBytes            int64  `json:"max_size_bytes" bson:"max_size_bytes"`
	EvictionPolicy          string `json:"eviction_policy" bson:"eviction_policy"` // "lru"
	NegativeCacheTTLSeconds int64  `json:"negative_cache_ttl_seconds" bson:"negative_cache_ttl_seconds"`
}

// DefaultCachePolicy returns conservative shared defaults for proxy/hybrid caches.
func DefaultCachePolicy() CachePolicy {
	return CachePolicy{
		MetadataTTLSeconds:      300,
		ArchiveTTLSeconds:       0, // 0 = retain until evicted by size or manual purge
		MaxSizeBytes:            0, // 0 = bounded only by registry quota
		EvictionPolicy:          "lru",
		NegativeCacheTTLSeconds: 60,
	}
}
