package flutter

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/amkargar/mirror-registry/backend/internal/registry"
	"github.com/amkargar/mirror-registry/backend/internal/storage"
	"github.com/amkargar/mirror-registry/backend/internal/upstream"
)

// Capabilities declares the capabilities genuinely supported by Flutter Storage mirrors.
func Capabilities() registry.Capabilities {
	return registry.Capabilities{
		Proxy:                 true,
		Hosted:                false,
		Publish:               false,
		Search:                true,
		ListVersions:          false,
		CacheWarm:             true,
		MetadataSync:          true,
		ArtifactDeletion:      true,
		VersionRetraction:     false,
		PackageOwnership:      false,
		SignatureVerification: false,
	}
}

// Registration returns the compiled registry type registration used by the composition root.
func Registration(store MetadataStore) registry.Registration {
	return registry.Registration{
		Type:               RegistryType,
		DisplayName:        "Flutter Storage (FLUTTER_STORAGE_BASE_URL)",
		Description:        "Mirror for Flutter SDK archives, engine artifacts, and Gradle Maven downloads via FLUTTER_STORAGE_BASE_URL.",
		DefaultUpstreamURL: DefaultUpstreamURL,
		Capabilities:       Capabilities(),
		ConfigHints:        ConfigHints(),
		ValidateConfig: func(cfg map[string]any) error {
			_, err := ParseConfig(cfg)
			return err
		},
		Create: func(_ context.Context, inst *registry.Instance, deps registry.ServiceDeps) (registry.Registry, error) {
			return New(inst, deps, store)
		},
	}
}

// FlutterRegistry implements a self-hosted FLUTTER_STORAGE_BASE_URL mirror using shared core services.
type FlutterRegistry struct {
	inst  *registry.Instance
	cfg   Config
	deps  registry.ServiceDeps
	store MetadataStore
	mux   *http.ServeMux
}

func New(inst *registry.Instance, deps registry.ServiceDeps, store MetadataStore) (*FlutterRegistry, error) {
	cfg, err := ParseConfig(inst.Config)
	if err != nil {
		return nil, err
	}
	if store == nil {
		store = NewMemoryStore()
	}
	r := &FlutterRegistry{
		inst:  inst.Clone(),
		cfg:   cfg,
		deps:  deps,
		store: store,
		mux:   http.NewServeMux(),
	}
	r.registerRoutes()
	return r, nil
}

func (r *FlutterRegistry) Type() string {
	return RegistryType
}

func (r *FlutterRegistry) Capabilities() registry.Capabilities {
	return Capabilities()
}

func (r *FlutterRegistry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rawPath := req.URL.Path
		if strings.Contains(rawPath, "..") || strings.Contains(rawPath, "\\") || strings.ContainsRune(rawPath, 0) {
			writeJSONError(w, http.StatusBadRequest, "invalid artifact path")
			return
		}
		r.mux.ServeHTTP(w, req)
	})
}

func (r *FlutterRegistry) upstreamTimeout() time.Duration {
	if r.cfg.UpstreamTimeoutSeconds > 0 {
		return time.Duration(r.cfg.UpstreamTimeoutSeconds) * time.Second
	}
	return defaultUpstreamTimeoutSecs * time.Second
}

// upstreamForPath resolves the upstream base URL for a clean artifact path, honoring per-category
// download_storage overrides while falling back to the registry-level upstream URL.
func (r *FlutterRegistry) upstreamForPath(cleanPath string) string {
	channel, ok := r.cfg.resolveCategory(cleanPath)
	if !ok {
		return strings.TrimRight(r.inst.UpstreamURL, "/")
	}
	return r.cfg.categoryUpstreamURL(channel, r.inst.UpstreamURL)
}

// manifestTTLForPath resolves the mutable-manifest TTL for a path, honoring per-category overrides.
func (r *FlutterRegistry) manifestTTLForPath(cleanPath string) int {
	channel, ok := r.cfg.resolveCategory(cleanPath)
	if !ok {
		return r.cfg.ManifestTTLSeconds
	}
	return r.cfg.categoryManifestTTL(channel)
}

// pathRewritesManifests reports whether Flutter release manifests fetched through this path
// should have their base_url rewritten to point at this mirror.
func (r *FlutterRegistry) pathRewritesManifests(cleanPath string) bool {
	channel, ok := r.cfg.resolveCategory(cleanPath)
	if !ok {
		return r.cfg.RewriteManifestURLs
	}
	return r.cfg.categoryRewriteManifests(channel)
}

func (r *FlutterRegistry) isPathAllowed(cleanPath string) bool {
	for _, prefix := range r.cfg.AllowedPrefixes {
		if prefix == "*" || cleanPath == prefix || strings.HasPrefix(cleanPath, prefix+"/") {
			return true
		}
	}
	return false
}

// ListArtifacts implements registry.ArtifactBrowser for the shared Admin artifact table.
func (r *FlutterRegistry) ListArtifacts(ctx context.Context, q registry.ArtifactQuery) (registry.ArtifactPage, error) {
	if q.Page <= 0 {
		q.Page = 1
	}
	if q.Limit <= 0 || q.Limit > 100 {
		q.Limit = 25
	}
	recs, total, err := r.store.ListArtifacts(ctx, r.inst.ID, ListQuery{
		Search: q.Search,
		Page:   q.Page,
		Limit:  q.Limit,
	})
	if err != nil {
		return registry.ArtifactPage{}, err
	}
	items := make([]registry.ArtifactDescriptor, 0, len(recs))
	for _, rec := range recs {
		desc := rec.ContentType
		if rec.IsManifest {
			desc = "Release manifest (" + rec.Category + ")"
		} else if rec.Category != "" {
			desc = rec.Category + " · " + rec.ContentType
		}
		items = append(items, registry.ArtifactDescriptor{
			ID:          rec.ID,
			RegistryID:  r.inst.ID,
			Name:        rec.Path,
			SizeBytes:   rec.SizeBytes,
			Digest:      rec.Digest,
			Origin:      string(storage.OriginCached),
			Description: desc,
			Downloads:   rec.Downloads,
			CreatedAt:   rec.CreatedAt,
			UpdatedAt:   rec.UpdatedAt,
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
func (r *FlutterRegistry) DeleteArtifact(ctx context.Context, name, _ string) error {
	cleanPath, err := sanitizePath(name)
	if err != nil {
		return err
	}
	_ = r.deps.Storage.DeleteArtifactRef(ctx, r.inst.ID, cleanPath)
	return r.store.DeleteArtifact(ctx, r.inst.ID, cleanPath)
}

// PurgeCache implements registry.CachePurger.
func (r *FlutterRegistry) PurgeCache(ctx context.Context) (storage.EvictResult, error) {
	res, err := r.deps.Cache.PurgeRegistry(ctx, r.inst.ID)
	if err != nil {
		return res, err
	}
	if err := r.store.DeleteRegistryMetadata(ctx, r.inst.ID); err != nil {
		return res, err
	}
	return res, nil
}

// WarmArtifact implements registry.CacheWarmer so admins can pre-warm e.g. flutter_infra_release/releases/releases_linux.json.
func (r *FlutterRegistry) WarmArtifact(ctx context.Context, rawPath string) error {
	cleanPath, err := sanitizePath(rawPath)
	if err != nil {
		return err
	}
	if !r.isPathAllowed(cleanPath) {
		return fmt.Errorf("path %q is outside allowed_prefixes", cleanPath)
	}
	_, _, err = r.fetchAndCacheFromUpstream(ctx, cleanPath, isManifestPath(cleanPath))
	return err
}

// CheckHealth implements registry.HealthChecker.
func (r *FlutterRegistry) CheckHealth(ctx context.Context) upstream.HealthCheckResult {
	if r.inst.UpstreamURL == "" {
		return upstream.HealthCheckResult{
			Available: false,
			Error:     "upstream_url is not configured",
			CheckedAt: time.Now().UTC(),
		}
	}
	probeURL := strings.TrimRight(r.inst.UpstreamURL, "/") + "/flutter_infra_release/releases/releases_linux.json"
	res := r.deps.Upstream.CheckHealthWithOptions(ctx, probeURL, r.inst.UpstreamRequestOptions(nil, 8*time.Second))
	if res.StatusCode == http.StatusNotFound {
		// Fall back to base upstream URL if a custom mirror uses a non-standard prefix.
		res = r.deps.Upstream.CheckHealthWithOptions(ctx, r.inst.UpstreamURL, r.inst.UpstreamRequestOptions(nil, 8*time.Second))
	}
	return res
}

// MatchesCompatPath implements registry.CompatPathMatcher for root-level FLUTTER_STORAGE_BASE_URL routing.
func (r *FlutterRegistry) MatchesCompatPath(p string) bool {
	trimmed := strings.TrimPrefix(p, "/")
	if trimmed == "" {
		return false
	}
	if _, disabled := r.cfg.disabledCategoryForPath(trimmed); disabled {
		return false
	}
	for _, prefix := range r.cfg.AllowedPrefixes {
		if prefix == "*" {
			// Never match wildcard on root compat route to avoid shadowing UI/API routes.
			continue
		}
		if trimmed == prefix || strings.HasPrefix(trimmed, prefix+"/") {
			return true
		}
	}
	return false
}

// VersionRegistry implements registry.VersionRegistryProvider so the Admin Console and Explorer can
// list every Flutter SDK version known to this mirror together with its storage settings.
func (r *FlutterRegistry) VersionRegistry(ctx context.Context) (any, error) {
	baseURL := registry.BaseURLFromContext(ctx)
	if baseURL == "" {
		baseURL = "/r/" + r.inst.Slug
	}
	return r.buildVersionRegistry(ctx, strings.TrimRight(baseURL, "/"))
}

// CleanupRegistry implements registry.RegistryCleaner when the registry instance is deleted.
func (r *FlutterRegistry) CleanupRegistry(ctx context.Context) error {
	return r.store.DeleteRegistryMetadata(ctx, r.inst.ID)
}
