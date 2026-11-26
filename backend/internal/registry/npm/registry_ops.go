package npm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/amkargar/depyard/backend/internal/registry"
	"github.com/amkargar/depyard/backend/internal/storage"
	"github.com/amkargar/depyard/backend/internal/upstream"
)

// ListArtifacts implements registry.ArtifactBrowser for the shared admin console.
func (r *NpmRegistry) ListArtifacts(ctx context.Context, q registry.ArtifactQuery) (registry.ArtifactPage, error) {
	packages, total, err := r.store.ListPackages(ctx, PackageListQuery{
		RegistryID: r.inst.ID,
		Search:     q.Search,
		Origin:     q.Origin,
		Page:       q.Page,
		Limit:      q.Limit,
	})
	if err != nil {
		return registry.ArtifactPage{}, err
	}
	items := make([]registry.ArtifactDescriptor, 0, len(packages))
	for _, p := range packages {
		digest := ""
		if v := findVersion(p, p.LatestVersion); v != nil {
			digest = v.Digest
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
	return registry.ArtifactPage{Items: items, Total: total, Page: q.Page, Limit: q.Limit}, nil
}

// DeleteArtifact implements registry.ArtifactDeleter. A version deletes only
// that tarball; an empty version deletes the whole package.
func (r *NpmRegistry) DeleteArtifact(ctx context.Context, name, version string) error {
	name = NormalizePackageName(name)
	if name == "" {
		return fmt.Errorf("npm package name is required")
	}
	r.publishMu.Lock()
	defer r.publishMu.Unlock()

	pkg, err := r.store.GetPackage(ctx, r.inst.ID, name)
	if err != nil {
		return err
	}
	if version != "" {
		v := findVersion(pkg, version)
		if v == nil {
			return ErrVersionNotFound
		}
		if err := r.deps.Storage.DeleteArtifactRef(ctx, r.inst.ID, artifactKey(name, version, v.Filename)); err != nil {
			return err
		}
		removeVersion(pkg, version, r.cfg.IncludePrerelease)
		if len(pkg.Versions) == 0 {
			return r.store.DeletePackage(ctx, r.inst.ID, name)
		}
		pkg.UpdatedAt = time.Now().UTC()
		return r.store.UpsertPackage(ctx, pkg)
	}
	for _, v := range pkg.Versions {
		_ = r.deps.Storage.DeleteArtifactRef(ctx, r.inst.ID, artifactKey(name, v.Version, v.Filename))
	}
	return r.store.DeletePackage(ctx, r.inst.ID, name)
}

// PurgeCache implements registry.CachePurger: shared cached blobs are evicted
// and only mirrored metadata is removed. Locally published versions survive.
func (r *NpmRegistry) PurgeCache(ctx context.Context) (storage.EvictResult, error) {
	res, err := r.deps.Cache.PurgeRegistry(ctx, r.inst.ID)
	if err != nil {
		return storage.EvictResult{}, err
	}
	if err := r.store.DeleteCachedPackages(ctx, r.inst.ID); err != nil {
		return res, err
	}
	return res, nil
}

// CheckHealth implements registry.HealthChecker.
func (r *NpmRegistry) CheckHealth(ctx context.Context) upstream.HealthCheckResult {
	if r.inst.Mode == registry.ModeHosted || r.inst.UpstreamURL == "" {
		return upstream.HealthCheckResult{Available: true, CheckedAt: time.Now().UTC()}
	}
	base := strings.TrimRight(r.inst.UpstreamURL, "/")
	opts := r.inst.UpstreamRequestOptions(map[string]string{"Accept": ContentTypeJSON}, 8*time.Second)
	return r.deps.Upstream.CheckHealthWithOptions(ctx, base+"/-/ping", opts)
}

// WarmArtifact implements registry.CacheWarmer by mirroring metadata plus the
// latest tarball of a package.
func (r *NpmRegistry) WarmArtifact(ctx context.Context, name string) error {
	name = NormalizePackageName(name)
	if !r.canProxy(name) {
		return fmt.Errorf("%w: package %q cannot be mirrored", registry.ErrInvalidConfig, name)
	}
	if _, err := r.syncPackageFromUpstream(ctx, name); err != nil {
		return err
	}
	pkg, err := r.store.GetPackage(ctx, r.inst.ID, name)
	if err != nil {
		return err
	}
	version := pkg.LatestVersion
	if version == "" {
		return ErrPackageNotFound
	}
	if _, err := r.cacheUpstreamTarball(ctx, name, version); err != nil {
		return err
	}
	return nil
}

// CleanupRegistry implements registry.RegistryCleaner when an instance is removed.
func (r *NpmRegistry) CleanupRegistry(ctx context.Context) error {
	return r.store.DeleteRegistryMetadata(ctx, r.inst.ID)
}

// compile-time checks that optional capability interfaces are honoured.
var (
	_ registry.ArtifactBrowser = (*NpmRegistry)(nil)
	_ registry.ArtifactDeleter = (*NpmRegistry)(nil)
	_ registry.CachePurger     = (*NpmRegistry)(nil)
	_ registry.CacheWarmer     = (*NpmRegistry)(nil)
	_ registry.HealthChecker   = (*NpmRegistry)(nil)
	_ registry.RegistryCleaner = (*NpmRegistry)(nil)
	_ registry.Registry        = (*NpmRegistry)(nil)
)
