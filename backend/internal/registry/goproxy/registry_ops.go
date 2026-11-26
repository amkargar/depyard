package goproxy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/amkargar/depyard/backend/internal/registry"
	"github.com/amkargar/depyard/backend/internal/storage"
	"github.com/amkargar/depyard/backend/internal/upstream"
)

// ListArtifacts implements registry.ArtifactBrowser for the shared Admin UI.
func (r *GoRegistry) ListArtifacts(ctx context.Context, q registry.ArtifactQuery) (registry.ArtifactPage, error) {
	mods, total, err := r.store.ListModules(ctx, ModuleListQuery{
		RegistryID: r.inst.ID,
		Search:     q.Search,
		Origin:     q.Origin,
		Page:       q.Page,
		Limit:      q.Limit,
	})
	if err != nil {
		return registry.ArtifactPage{}, err
	}
	items := make([]registry.ArtifactDescriptor, 0, len(mods))
	for _, m := range mods {
		var digest string
		if m.LatestVersion != "" {
			if v, err := r.store.GetVersion(ctx, r.inst.ID, m.Path, m.LatestVersion); err == nil {
				digest = v.ZipDigest
			}
		}
		items = append(items, registry.ArtifactDescriptor{
			ID:           m.ID,
			RegistryID:   m.RegistryID,
			Name:         m.Path,
			Version:      m.LatestVersion,
			VersionCount: m.VersionCount,
			SizeBytes:    m.TotalSizeBytes,
			Digest:       digest,
			Origin:       m.Origin,
			Description:  m.Description,
			Downloads:    m.Downloads,
			CreatedAt:    m.CreatedAt,
			UpdatedAt:    m.UpdatedAt,
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
//
// Deleting a specific version removes its cached blobs and metadata. Deleting the
// module itself removes every version. Cached artifacts are always removable; hosted
// artifacts (if any) are protected by the shared storage layer.
func (r *GoRegistry) DeleteArtifact(ctx context.Context, modulePath, version string) error {
	mod, err := r.store.GetModule(ctx, r.inst.ID, modulePath)
	if err != nil {
		return err
	}

	if version != "" {
		if _, err := r.store.GetVersion(ctx, r.inst.ID, modulePath, version); err != nil {
			return err
		}
		_ = r.deps.Storage.DeleteArtifactRef(ctx, r.inst.ID, zipArtifactKey(modulePath, version))
		_ = r.deps.Storage.DeleteArtifactRef(ctx, r.inst.ID, modArtifactKey(modulePath, version))
		if err := r.store.DeleteVersion(ctx, r.inst.ID, modulePath, version); err != nil {
			return err
		}
		remaining, _ := r.store.ListVersions(ctx, r.inst.ID, modulePath)
		if len(remaining) == 0 {
			return r.store.DeleteModule(ctx, r.inst.ID, modulePath)
		}
		mod.VersionCount = len(remaining)
		mod.TotalSizeBytes = r.recalcModuleSize(ctx, modulePath)
		if len(remaining) > 0 {
			mod.LatestVersion = remaining[len(remaining)-1].Version
		}
		mod.UpdatedAt = time.Now().UTC()
		return r.store.UpsertModule(ctx, mod)
	}

	versions, _ := r.store.ListVersions(ctx, r.inst.ID, modulePath)
	for _, v := range versions {
		_ = r.deps.Storage.DeleteArtifactRef(ctx, r.inst.ID, zipArtifactKey(modulePath, v.Version))
		_ = r.deps.Storage.DeleteArtifactRef(ctx, r.inst.ID, modArtifactKey(modulePath, v.Version))
	}
	return r.store.DeleteModule(ctx, r.inst.ID, modulePath)
}

// PurgeCache implements registry.CachePurger.
// It evicts ONLY non-protected cached blobs and cached metadata.
func (r *GoRegistry) PurgeCache(ctx context.Context) (storage.EvictResult, error) {
	res, err := r.deps.Cache.PurgeRegistry(ctx, r.inst.ID)
	if err != nil {
		return res, err
	}
	if err := r.store.DeleteCachedModules(ctx, r.inst.ID); err != nil {
		return res, err
	}
	return res, nil
}

// CheckHealth implements registry.HealthChecker.
func (r *GoRegistry) CheckHealth(ctx context.Context) upstream.HealthCheckResult {
	if r.inst.Mode == registry.ModeHosted || r.inst.UpstreamURL == "" {
		return upstream.HealthCheckResult{
			Available: true,
			CheckedAt: time.Now().UTC(),
		}
	}
	return r.deps.Upstream.CheckHealthWithOptions(ctx, r.inst.UpstreamURL, r.inst.UpstreamRequestOptions(nil, 8*time.Second))
}

// CleanupRegistry implements registry.RegistryCleaner when a registry instance is deleted.
func (r *GoRegistry) CleanupRegistry(ctx context.Context) error {
	return r.store.DeleteRegistryMetadata(ctx, r.inst.ID)
}

// WarmArtifact implements registry.CacheWarmer by pre-fetching a module's metadata.
func (r *GoRegistry) WarmArtifact(ctx context.Context, modulePath string) error {
	if r.inst.Mode == registry.ModeHosted {
		return registry.ErrUnsupportedOp
	}
	if !r.isModuleAllowed(modulePath) {
		return fmt.Errorf("%w: module %q is not allowed", registry.ErrInvalidConfig, modulePath)
	}
	_, versions, err := r.getOrFetchModule(ctx, modulePath)
	if err != nil {
		return err
	}
	if len(versions) == 0 {
		return ErrModuleNotFound
	}
	latest := versions[len(versions)-1]
	if _, _, err := r.ensureZipCached(ctx, modulePath, latest.Version); err != nil {
		if errors.Is(err, ErrVersionNotFound) || errors.Is(err, ErrModuleNotFound) {
			return nil
		}
		return err
	}
	return nil
}
