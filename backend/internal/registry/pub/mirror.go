package pub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/amkargar/mirror-registry/backend/internal/registry"
	"github.com/amkargar/mirror-registry/backend/internal/storage"
)

type upstreamPackagePayload struct {
	Name           string                   `json:"name"`
	IsDiscontinued bool                     `json:"isDiscontinued"`
	ReplacedBy     string                   `json:"replacedBy"`
	Versions       []upstreamVersionPayload `json:"versions"`
}

type upstreamVersionPayload struct {
	Version       string         `json:"version"`
	Retracted     bool           `json:"retracted"`
	Pubspec       map[string]any `json:"pubspec"`
	ArchiveURL    string         `json:"archive_url"`
	ArchiveSHA256 string         `json:"archive_sha256"`
	Published     string         `json:"published"`
}

func (r *PubRegistry) isPackageAllowed(name string) bool {
	for _, blocked := range r.cfg.BlockedPackages {
		if matchPattern(blocked, name) {
			return false
		}
	}
	if len(r.cfg.AllowedPackages) > 0 {
		for _, allowed := range r.cfg.AllowedPackages {
			if matchPattern(allowed, name) {
				return true
			}
		}
		return false
	}
	return true
}

func (r *PubRegistry) isProtectedNamespace(name string) bool {
	for _, ns := range r.cfg.ProtectedNamespaces {
		if matchPattern(ns, name) {
			return true
		}
	}
	return false
}

func matchPattern(pattern, name string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	if pattern == "*" || pattern == name {
		return true
	}
	if ok, err := path.Match(pattern, name); err == nil && ok {
		return true
	}
	if strings.HasSuffix(pattern, "*") && strings.HasPrefix(name, strings.TrimSuffix(pattern, "*")) {
		return true
	}
	return false
}

// getOrFetchPackage resolves package metadata with deterministic hybrid resolution & namespace protection.
func (r *PubRegistry) getOrFetchPackage(ctx context.Context, name string) (*PubPackage, []*PubVersion, error) {
	if !r.isPackageAllowed(name) {
		return nil, nil, ErrPackageNotFound
	}

	localPkg, err := r.store.GetPackage(ctx, r.inst.ID, name)
	if err != nil && !errors.Is(err, ErrPackageNotFound) {
		return nil, nil, err
	}

	// Hosted package ALWAYS takes precedence in hosted & hybrid modes (namespace protection!).
	if localPkg != nil && localPkg.Origin == storage.OriginHosted {
		versions, err := r.store.ListVersions(ctx, r.inst.ID, name)
		if err != nil {
			return nil, nil, err
		}
		return localPkg, versions, nil
	}

	// Hosted-only mode or protected local namespace: never proxy upstream.
	if r.inst.Mode == registry.ModeHosted || r.isProtectedNamespace(name) {
		if localPkg == nil {
			return nil, nil, ErrPackageNotFound
		}
		versions, err := r.store.ListVersions(ctx, r.inst.ID, name)
		return localPkg, versions, err
	}

	// Proxy / Hybrid cache freshness check
	ttl := time.Duration(r.inst.CachePolicy.MetadataTTLSeconds) * time.Second
	if localPkg != nil && localPkg.UpstreamSyncedAt != nil && ttl > 0 && time.Since(*localPkg.UpstreamSyncedAt) < ttl {
		versions, err := r.store.ListVersions(ctx, r.inst.ID, name)
		if err == nil && len(versions) > 0 {
			r.deps.Metrics.RecordCacheLookup(r.inst.ID, true)
			return localPkg, versions, nil
		}
	}

	// Deduplicated upstream metadata fetch
	type fetchResult struct {
		pkg      *PubPackage
		versions []*PubVersion
	}
	val, fetchErr, _ := r.deps.Cache.Do(r.inst.ID, "meta:"+name, func() (any, error) {
		pkg, vers, err := r.syncPackageFromUpstream(ctx, name, localPkg)
		if err != nil {
			return nil, err
		}
		return &fetchResult{pkg: pkg, versions: vers}, nil
	})
	if fetchErr != nil {
		// Stale-while-error fallback when upstream is temporarily unreachable
		if localPkg != nil && !errors.Is(fetchErr, ErrPackageNotFound) {
			if versions, lerr := r.store.ListVersions(ctx, r.inst.ID, name); lerr == nil && len(versions) > 0 {
				return localPkg, versions, nil
			}
		}
		return nil, nil, fetchErr
	}

	res := val.(*fetchResult)
	return res.pkg, res.versions, nil
}

func (r *PubRegistry) syncPackageFromUpstream(ctx context.Context, name string, existing *PubPackage) (*PubPackage, []*PubVersion, error) {
	r.deps.Metrics.RecordCacheLookup(r.inst.ID, false)

	upstreamURL := fmt.Sprintf("%s/api/packages/%s", strings.TrimRight(r.inst.UpstreamURL, "/"), url.PathEscape(name))
	headers := map[string]string{
		"Accept": PubV2MediaType,
	}
	resp, err := r.deps.Upstream.Get(ctx, upstreamURL, headers, r.upstreamTimeout())
	if err != nil {
		r.deps.Metrics.RecordUpstreamError(r.inst.ID)
		return nil, nil, fmt.Errorf("upstream request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil, ErrPackageNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		r.deps.Metrics.RecordUpstreamError(r.inst.ID)
		return nil, nil, fmt.Errorf("upstream returned HTTP %d", resp.StatusCode)
	}

	var payload upstreamPackagePayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		r.deps.Metrics.RecordUpstreamError(r.inst.ID)
		return nil, nil, fmt.Errorf("decode upstream package metadata: %w", err)
	}
	if payload.Name == "" {
		payload.Name = name
	}

	existingVersions := make(map[string]*PubVersion)
	if existing != nil {
		if vers, err := r.store.ListVersions(ctx, r.inst.ID, name); err == nil {
			for _, v := range vers {
				existingVersions[v.Version] = v
			}
		}
	}

	now := time.Now().UTC()
	versions := make([]*PubVersion, 0, len(payload.Versions))
	var totalSize int64
	for _, uv := range payload.Versions {
		publishedAt := now
		if uv.Published != "" {
			if t, err := time.Parse(time.RFC3339Nano, uv.Published); err == nil {
				publishedAt = t
			} else if t, err := time.Parse(time.RFC3339, uv.Published); err == nil {
				publishedAt = t
			}
		}

		archiveURL := uv.ArchiveURL
		if archiveURL == "" {
			archiveURL = fmt.Sprintf("%s/packages/%s/versions/%s.tar.gz",
				strings.TrimRight(r.inst.UpstreamURL, "/"),
				url.PathEscape(payload.Name),
				url.PathEscape(uv.Version),
			)
		}

		v := &PubVersion{
			ID:                 verDocID(r.inst.ID, payload.Name, uv.Version),
			RegistryID:         r.inst.ID,
			PackageName:        payload.Name,
			Version:            uv.Version,
			Origin:             storage.OriginCached,
			Pubspec:            uv.Pubspec,
			ArchiveDigest:      strings.ToLower(strings.TrimSpace(uv.ArchiveSHA256)),
			UpstreamArchiveURL: archiveURL,
			Retracted:          uv.Retracted,
			PublishedAt:        publishedAt,
			CreatedAt:          now,
		}
		if prev, ok := existingVersions[uv.Version]; ok {
			v.CreatedAt = prev.CreatedAt
			v.ArchiveCached = prev.ArchiveCached
			v.ArchiveSize = prev.ArchiveSize
			v.CachedAt = prev.CachedAt
			if v.ArchiveDigest == "" {
				v.ArchiveDigest = prev.ArchiveDigest
			}
			totalSize += v.ArchiveSize
		}
		if err := r.store.UpsertVersion(ctx, v); err != nil {
			return nil, nil, err
		}
		versions = append(versions, v)
	}

	latest := SelectLatestVersion(versions)
	pkg := &PubPackage{
		ID:               pkgDocID(r.inst.ID, payload.Name),
		RegistryID:       r.inst.ID,
		Name:             payload.Name,
		Origin:           storage.OriginCached,
		VersionCount:     len(versions),
		TotalSizeBytes:   totalSize,
		IsDiscontinued:   payload.IsDiscontinued,
		ReplacedBy:       payload.ReplacedBy,
		UpstreamSyncedAt: &now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if existing != nil {
		pkg.CreatedAt = existing.CreatedAt
		pkg.Downloads = existing.Downloads
		pkg.Readme = existing.Readme
		pkg.Changelog = existing.Changelog
	}
	if latest != nil {
		pkg.LatestVersion = latest.Version
		pkg.Description = strField(latest.Pubspec, "description")
		pkg.Homepage = strField(latest.Pubspec, "homepage")
		pkg.Repository = strField(latest.Pubspec, "repository")
		pkg.Documentation = strField(latest.Pubspec, "documentation")
	}

	if err := r.store.UpsertPackage(ctx, pkg); err != nil {
		return nil, nil, err
	}
	SortVersionsAscending(versions)
	return pkg, versions, nil
}

// ensureArchiveCached guarantees the requested version tarball exists in shared content-addressed storage.
// Returns (ref, cacheHit, error).
func (r *PubRegistry) ensureArchiveCached(ctx context.Context, pkgName, version string) (*storage.ArtifactRef, bool, error) {
	if !r.isPackageAllowed(pkgName) {
		return nil, false, ErrPackageNotFound
	}
	key := artifactKey(pkgName, version)

	// Fast path: already in local content-addressed storage for this registry.
	if ref, err := r.deps.Storage.GetArtifactRef(ctx, r.inst.ID, key); err == nil && ref != nil {
		return ref, true, nil
	}

	// Ensure version metadata is known
	ver, err := r.store.GetVersion(ctx, r.inst.ID, pkgName, version)
	if errors.Is(err, ErrVersionNotFound) && r.inst.Mode != registry.ModeHosted {
		if _, _, fetchErr := r.getOrFetchPackage(ctx, pkgName); fetchErr != nil {
			return nil, false, fetchErr
		}
		ver, err = r.store.GetVersion(ctx, r.inst.ID, pkgName, version)
	}
	if err != nil {
		return nil, false, err
	}

	// Never fetch a hosted package version from upstream!
	if ver.Origin == storage.OriginHosted || r.inst.Mode == registry.ModeHosted {
		return nil, false, ErrVersionNotFound
	}

	// Deduplicated concurrent archive download via singleflight
	val, dlErr, _ := r.deps.Cache.Do(r.inst.ID, "archive:"+key, func() (any, error) {
		// Re-check storage inside singleflight in case another goroutine just completed.
		if ref, err := r.deps.Storage.GetArtifactRef(ctx, r.inst.ID, key); err == nil && ref != nil {
			return ref, nil
		}

		dlURL := ver.UpstreamArchiveURL
		if dlURL == "" {
			dlURL = fmt.Sprintf("%s/packages/%s/versions/%s.tar.gz",
				strings.TrimRight(r.inst.UpstreamURL, "/"),
				url.PathEscape(pkgName),
				url.PathEscape(version),
			)
		}

		resp, err := r.deps.Upstream.Get(ctx, dlURL, nil, r.upstreamTimeout()*2)
		if err != nil {
			r.deps.Metrics.RecordUpstreamError(r.inst.ID)
			return nil, fmt.Errorf("download upstream archive: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound {
			return nil, ErrVersionNotFound
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			r.deps.Metrics.RecordUpstreamError(r.inst.ID)
			return nil, fmt.Errorf("upstream archive returned HTTP %d", resp.StatusCode)
		}

		putRes, err := r.deps.Storage.PutArtifact(ctx, storage.PutArtifactInput{
			RegistryID:       r.inst.ID,
			ArtifactKey:      key,
			Origin:           storage.OriginCached,
			ExpectedDigest:   ver.ArchiveDigest,
			MaxArtifactBytes: r.cfg.MaxArchiveSizeBytes,
			RegistryQuota:    r.inst.StorageQuotaBytes,
			Reader:           resp.Body,
		})
		if err != nil {
			return nil, err
		}

		now := time.Now().UTC()
		ver.ArchiveDigest = putRes.Ref.Digest
		ver.ArchiveSize = putRes.Ref.SizeBytes
		ver.ArchiveCached = true
		ver.CachedAt = &now
		_ = r.store.UpsertVersion(ctx, ver)

		// Extract README/CHANGELOG opportunistically for the Pub explorer
		if f, _, openErr := r.deps.Storage.OpenArtifact(ctx, r.inst.ID, key); openErr == nil {
			if extracted, exErr := ExtractPubArchive(f); exErr == nil {
				if pkg, perr := r.store.GetPackage(ctx, r.inst.ID, pkgName); perr == nil {
					if pkg.LatestVersion == version || pkg.Readme == "" {
						if extracted.Readme != "" {
							pkg.Readme = extracted.Readme
						}
						if extracted.Changelog != "" {
							pkg.Changelog = extracted.Changelog
						}
					}
					pkg.TotalSizeBytes = r.recalcPackageSize(ctx, pkgName)
					pkg.UpdatedAt = now
					_ = r.store.UpsertPackage(ctx, pkg)
				}
			}
			_ = f.Close()
		}

		return putRes.Ref, nil
	})
	if dlErr != nil {
		return nil, false, dlErr
	}
	return val.(*storage.ArtifactRef), false, nil
}

func (r *PubRegistry) recalcPackageSize(ctx context.Context, pkgName string) int64 {
	versions, err := r.store.ListVersions(ctx, r.inst.ID, pkgName)
	if err != nil {
		return 0
	}
	var sum int64
	for _, v := range versions {
		sum += v.ArchiveSize
	}
	return sum
}

// WarmArtifact pre-fetches a package's metadata and latest archive into cache.
func (r *PubRegistry) WarmArtifact(ctx context.Context, name string) error {
	if r.inst.Mode == registry.ModeHosted {
		return registry.ErrUnsupportedOp
	}
	pkg, versions, err := r.getOrFetchPackage(ctx, name)
	if err != nil {
		return err
	}
	latest := SelectLatestVersion(versions)
	if latest == nil {
		return nil
	}
	if pkg.Origin == storage.OriginHosted {
		return nil
	}
	_, _, err = r.ensureArchiveCached(ctx, pkg.Name, latest.Version)
	return err
}
