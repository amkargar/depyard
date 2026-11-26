package pub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/amkargar/depyard/backend/internal/registry"
	"github.com/amkargar/depyard/backend/internal/storage"
	"github.com/amkargar/depyard/backend/internal/upstream"
)

const RegistryType = "pub"

const defaultMaxArchiveSizeBytes int64 = 100 * 1024 * 1024 // 100 MiB (Dart pub.dev limit)

// Config holds validated Pub-specific registry settings stored inside Instance.Config.
type Config struct {
	UpstreamTimeoutSeconds int      `json:"upstream_timeout_seconds,omitempty"`
	MaxArchiveSizeBytes    int64    `json:"max_archive_size_bytes,omitempty"`
	AllowRetract           bool     `json:"allow_retract"`
	AllowedPackages        []string `json:"allowed_packages,omitempty"`
	BlockedPackages        []string `json:"blocked_packages,omitempty"`
	ProtectedNamespaces    []string `json:"protected_namespaces,omitempty"`
}

func DefaultConfig() Config {
	return Config{
		UpstreamTimeoutSeconds: 30,
		MaxArchiveSizeBytes:    defaultMaxArchiveSizeBytes,
		AllowRetract:           true,
	}
}

func ParseConfig(raw map[string]any) (Config, error) {
	cfg := DefaultConfig()
	if len(raw) == 0 {
		return cfg, nil
	}
	// Reject unknown top-level config keys to catch misconfiguration early.
	allowedKeys := map[string]bool{
		"upstream_timeout_seconds": true,
		"max_archive_size_bytes":   true,
		"allow_retract":            true,
		"allowed_packages":         true,
		"blocked_packages":         true,
		"protected_namespaces":     true,
	}
	for k := range raw {
		if !allowedKeys[k] {
			return cfg, fmt.Errorf("unknown pub config field %q", k)
		}
	}

	b, err := json.Marshal(raw)
	if err != nil {
		return cfg, fmt.Errorf("encode pub config: %w", err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("invalid pub config: %w", err)
	}
	if cfg.UpstreamTimeoutSeconds < 0 || cfg.UpstreamTimeoutSeconds > 600 {
		return cfg, fmt.Errorf("upstream_timeout_seconds must be between 0 and 600")
	}
	if cfg.UpstreamTimeoutSeconds == 0 {
		cfg.UpstreamTimeoutSeconds = 30
	}
	if cfg.MaxArchiveSizeBytes < 0 || cfg.MaxArchiveSizeBytes > 1024*1024*1024 {
		return cfg, fmt.Errorf("max_archive_size_bytes must be between 0 and 1 GiB")
	}
	if cfg.MaxArchiveSizeBytes == 0 {
		cfg.MaxArchiveSizeBytes = defaultMaxArchiveSizeBytes
	}
	return cfg, nil
}

// PubCapabilities returns the declared capabilities of the Pub registry type.
func PubCapabilities() registry.Capabilities {
	return registry.Capabilities{
		Proxy:                 true,
		Hosted:                true,
		Publish:               true,
		Search:                true,
		ListVersions:          true,
		CacheWarm:             true,
		MetadataSync:          true,
		ArtifactDeletion:      true,
		VersionRetraction:     true,
		PackageOwnership:      true,
		SignatureVerification: false,
	}
}

// Registration returns the in-process registration descriptor used by the composition root.
func Registration(store MetadataStore) registry.Registration {
	return registry.Registration{
		Type:               RegistryType,
		DisplayName:        "Dart / Flutter Pub",
		Description:        "Official Dart & Flutter Pub hosted repository v2 (proxy, hosted, and hybrid with namespace protection).",
		DefaultUpstreamURL: "https://pub.dev",
		Capabilities:       PubCapabilities(),
		ConfigHints: []registry.ConfigFieldHint{
			{
				Key:          "upstream_timeout_seconds",
				Label:        "Upstream Timeout (seconds)",
				Type:         "number",
				Description:  "Timeout for upstream pub metadata requests (1-600s).",
				DefaultValue: 30,
			},
			{
				Key:          "max_archive_size_bytes",
				Label:        "Max Archive Size (bytes)",
				Type:         "number",
				Description:  "Maximum allowed .tar.gz package archive size (default 100 MiB).",
				DefaultValue: defaultMaxArchiveSizeBytes,
			},
			{
				Key:          "allow_retract",
				Label:        "Allow Version Retraction",
				Type:         "boolean",
				Description:  "Allow publishers to retract and unretract hosted package versions.",
				DefaultValue: true,
			},
			{
				Key:         "protected_namespaces",
				Label:       "Protected Local Namespaces",
				Type:        "string_list",
				Description: "Package name patterns (e.g. acme_*) that are never proxied from upstream in hybrid mode.",
			},
			{
				Key:         "blocked_packages",
				Label:       "Blocked Package Patterns",
				Type:        "string_list",
				Description: "Package patterns denied from being mirrored or published.",
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

// PubRegistry implements the Dart Pub v2 registry protocol for a single configured instance.
type PubRegistry struct {
	inst    *registry.Instance
	cfg     Config
	deps    registry.ServiceDeps
	store   MetadataStore
	handler http.Handler

	uploadMu sync.Mutex
	uploads  map[string]*uploadSession
}

func New(inst *registry.Instance, deps registry.ServiceDeps, store MetadataStore) (*PubRegistry, error) {
	cfg, err := ParseConfig(inst.Config)
	if err != nil {
		return nil, err
	}
	if store == nil {
		store = NewMemoryStore()
	}
	r := &PubRegistry{
		inst:    inst.Clone(),
		cfg:     cfg,
		deps:    deps,
		store:   store,
		uploads: make(map[string]*uploadSession),
	}
	r.handler = r.buildHandler()
	return r, nil
}

func (r *PubRegistry) Type() string {
	return RegistryType
}

func (r *PubRegistry) Capabilities() registry.Capabilities {
	return PubCapabilities()
}

func (r *PubRegistry) Handler() http.Handler {
	return r.handler
}

func (r *PubRegistry) upstreamTimeout() time.Duration {
	if r.cfg.UpstreamTimeoutSeconds > 0 {
		return time.Duration(r.cfg.UpstreamTimeoutSeconds) * time.Second
	}
	return 30 * time.Second
}

func (r *PubRegistry) maxArchiveBytes() int64 {
	if r.cfg.MaxArchiveSizeBytes > 0 {
		return r.cfg.MaxArchiveSizeBytes
	}
	return defaultMaxArchiveSizeBytes
}

// --- Optional Capability Interface Implementations ---

// ListArtifacts implements registry.ArtifactBrowser for the shared Admin UI.
func (r *PubRegistry) ListArtifacts(ctx context.Context, q registry.ArtifactQuery) (registry.ArtifactPage, error) {
	pkgs, total, err := r.store.ListPackages(ctx, PackageListQuery{
		RegistryID: r.inst.ID,
		Search:     q.Search,
		Origin:     q.Origin,
		Page:       q.Page,
		Limit:      q.Limit,
	})
	if err != nil {
		return registry.ArtifactPage{}, err
	}
	items := make([]registry.ArtifactDescriptor, 0, len(pkgs))
	for _, p := range pkgs {
		var digest string
		if p.LatestVersion != "" {
			if v, err := r.store.GetVersion(ctx, r.inst.ID, p.Name, p.LatestVersion); err == nil {
				digest = v.ArchiveDigest
			}
		}
		items = append(items, registry.ArtifactDescriptor{
			ID:           p.ID,
			RegistryID:   p.RegistryID,
			Name:         p.Name,
			Version:      p.LatestVersion,
			VersionCount: p.VersionCount,
			SizeBytes:    p.TotalSizeBytes,
			Digest:       digest,
			Origin:       p.Origin,
			Description:  p.Description,
			Downloads:    p.Downloads,
			CreatedAt:    p.CreatedAt,
			UpdatedAt:    p.UpdatedAt,
		})
	}
	return registry.ArtifactPage{
		Items: items,
		Total: total,
		Page:  q.Page,
		Limit: q.Limit,
	}, nil
}

// DeleteArtifact implements registry.ArtifactDeleter.
// Deletes either a single version (if version != "") or an entire package and its blob references.
func (r *PubRegistry) DeleteArtifact(ctx context.Context, name, version string) error {
	pkg, err := r.store.GetPackage(ctx, r.inst.ID, name)
	if err != nil {
		return err
	}

	if version != "" {
		if _, err := r.store.GetVersion(ctx, r.inst.ID, name, version); err != nil {
			return err
		}
		_ = r.deps.Storage.DeleteArtifactRef(ctx, r.inst.ID, artifactKey(name, version))
		if err := r.store.DeleteVersion(ctx, r.inst.ID, name, version); err != nil {
			return err
		}
		remaining, _ := r.store.ListVersions(ctx, r.inst.ID, name)
		if len(remaining) == 0 {
			return r.store.DeletePackage(ctx, r.inst.ID, name)
		}
		if latest := SelectLatestVersion(remaining); latest != nil {
			pkg.LatestVersion = latest.Version
		}
		pkg.VersionCount = len(remaining)
		pkg.TotalSizeBytes = r.recalcPackageSize(ctx, name)
		pkg.UpdatedAt = time.Now().UTC()
		return r.store.UpsertPackage(ctx, pkg)
	}

	versions, _ := r.store.ListVersions(ctx, r.inst.ID, name)
	for _, v := range versions {
		_ = r.deps.Storage.DeleteArtifactRef(ctx, r.inst.ID, artifactKey(name, v.Version))
	}
	return r.store.DeletePackage(ctx, r.inst.ID, name)
}

// PurgeCache implements registry.CachePurger.
// Evicts ONLY non-protected cached blobs and cached metadata; hosted packages remain untouched!
func (r *PubRegistry) PurgeCache(ctx context.Context) (storage.EvictResult, error) {
	res, err := r.deps.Cache.PurgeRegistry(ctx, r.inst.ID)
	if err != nil {
		return res, err
	}
	if err := r.store.DeleteCachedPackages(ctx, r.inst.ID); err != nil {
		return res, err
	}
	return res, nil
}

// CheckHealth implements registry.HealthChecker.
func (r *PubRegistry) CheckHealth(ctx context.Context) upstream.HealthCheckResult {
	if r.inst.Mode == registry.ModeHosted || r.inst.UpstreamURL == "" {
		return upstream.HealthCheckResult{
			Available: true,
			CheckedAt: time.Now().UTC(),
		}
	}
	return r.deps.Upstream.CheckHealthWithOptions(ctx, r.inst.UpstreamURL, r.inst.UpstreamRequestOptions(nil, 8*time.Second))
}

// MatchesCompatPath implements registry.CompatPathMatcher for backward-compatible root Pub routing.
func (r *PubRegistry) MatchesCompatPath(p string) bool {
	return p == "/api/packages" ||
		strings.HasPrefix(p, "/api/packages/") ||
		strings.HasPrefix(p, "/packages/") ||
		p == "/api/search"
}

// CleanupRegistry implements registry.RegistryCleaner when a registry instance is deleted.
func (r *PubRegistry) CleanupRegistry(ctx context.Context) error {
	return r.store.DeleteRegistryMetadata(ctx, r.inst.ID)
}
