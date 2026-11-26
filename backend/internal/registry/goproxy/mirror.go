package goproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/amkargar/depyard/backend/internal/registry"
	"github.com/amkargar/depyard/backend/internal/storage"
)

// isModuleAllowed applies the allowed/blocked module path policies.
func (r *GoRegistry) isModuleAllowed(modulePath string) bool {
	for _, blocked := range r.cfg.BlockedModules {
		if matchModulePattern(blocked, modulePath) {
			return false
		}
	}
	if len(r.cfg.AllowedModules) > 0 {
		for _, allowed := range r.cfg.AllowedModules {
			if matchModulePattern(allowed, modulePath) {
				return true
			}
		}
		return false
	}
	return true
}

// isProtectedModule reports whether a module path is pinned against cache eviction.
func (r *GoRegistry) isProtectedModule(modulePath string) bool {
	for _, p := range r.cfg.ProtectedModules {
		if matchModulePattern(p, modulePath) {
			return true
		}
	}
	return false
}

// matchModulePattern matches a module path against a policy pattern.
//
// Patterns support an exact match, the "*" wildcard, a trailing "/*" prefix match,
// and the "..." suffix that the go command uses for sub-module trees.
func matchModulePattern(pattern, modulePath string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	if pattern == "*" || pattern == modulePath {
		return true
	}
	if strings.HasSuffix(pattern, "/...") {
		prefix := strings.TrimSuffix(pattern, "...")
		return modulePath == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(modulePath, prefix)
	}
	if strings.HasSuffix(pattern, "/*") {
		prefix := strings.TrimSuffix(pattern, "*")
		return strings.HasPrefix(modulePath, prefix)
	}
	if strings.Contains(pattern, "*") {
		// Simple glob support for patterns like "github.com/*/internal/*".
		return matchGlob(pattern, modulePath)
	}
	return false
}

// matchGlob implements a minimal "*" glob matcher that does not treat "/" specially.
func matchGlob(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	rest := s[len(parts[0]):]
	for i := 1; i < len(parts); i++ {
		seg := parts[i]
		if seg == "" {
			if i == len(parts)-1 {
				return true
			}
			continue
		}
		idx := strings.Index(rest, seg)
		if idx < 0 {
			return false
		}
		rest = rest[idx+len(seg):]
	}
	return true
}

// upstreamURL builds the upstream proxy URL for a module path and file.
//
// Module paths must use the escaped form (uppercase letters encoded as "!<lower>").
func (r *GoRegistry) upstreamURL(modulePath, file string) string {
	escaped := escapeModulePath(modulePath)
	return fmt.Sprintf("%s/%s/@v/%s", r.upstreamBase(), escaped, file)
}

// getOrFetchModule resolves module metadata, fetching it from upstream when needed.
func (r *GoRegistry) getOrFetchModule(ctx context.Context, modulePath string) (*GoModule, []*GoVersion, error) {
	if !r.isModuleAllowed(modulePath) {
		return nil, nil, ErrModuleNotFound
	}

	localMod, err := r.store.GetModule(ctx, r.inst.ID, modulePath)
	if err != nil && !errors.Is(err, ErrModuleNotFound) {
		return nil, nil, err
	}

	// Hosted-only mode never proxies upstream.
	if r.inst.Mode == registry.ModeHosted {
		if localMod == nil {
			return nil, nil, ErrModuleNotFound
		}
		versions, verr := r.store.ListVersions(ctx, r.inst.ID, modulePath)
		return localMod, versions, verr
	}

	// Cache freshness check.
	ttl := time.Duration(r.inst.CachePolicy.MetadataTTLSeconds) * time.Second
	if localMod != nil && localMod.UpstreamSyncedAt != nil && ttl > 0 && time.Since(*localMod.UpstreamSyncedAt) < ttl {
		versions, verr := r.store.ListVersions(ctx, r.inst.ID, modulePath)
		if verr == nil && len(versions) > 0 {
			r.deps.Metrics.RecordCacheLookup(r.inst.ID, true)
			return localMod, versions, nil
		}
	}

	type fetchResult struct {
		mod      *GoModule
		versions []*GoVersion
	}
	val, fetchErr, _ := r.deps.Cache.Do(r.inst.ID, "gometa:"+modulePath, func() (any, error) {
		mod, vers, serr := r.syncModuleFromUpstream(ctx, modulePath, localMod)
		if serr != nil {
			return nil, serr
		}
		return &fetchResult{mod: mod, versions: vers}, nil
	})
	if fetchErr != nil {
		// Stale-while-error fallback.
		if localMod != nil && !errors.Is(fetchErr, ErrModuleNotFound) {
			if versions, lerr := r.store.ListVersions(ctx, r.inst.ID, modulePath); lerr == nil && len(versions) > 0 {
				return localMod, versions, nil
			}
		}
		return nil, nil, fetchErr
	}

	res := val.(*fetchResult)
	return res.mod, res.versions, nil
}

// listVersions returns the sorted list of known versions for a module.
func (r *GoRegistry) listVersions(ctx context.Context, modulePath string) ([]string, error) {
	mod, versions, err := r.getOrFetchModule(ctx, modulePath)
	if err != nil {
		return nil, err
	}
	_ = mod

	out := make([]string, 0, len(versions))
	for _, v := range versions {
		if !r.cfg.IncludePrerelease && isPseudoVersion(v.Version) {
			continue
		}
		out = append(out, v.Version)
	}
	sortVersions(out)
	return out, nil
}

// syncModuleFromUpstream fetches the version list from the upstream proxy and stores it.
func (r *GoRegistry) syncModuleFromUpstream(ctx context.Context, modulePath string, existing *GoModule) (*GoModule, []*GoVersion, error) {
	r.deps.Metrics.RecordCacheLookup(r.inst.ID, false)

	listURL := r.upstreamURL(modulePath, "list")
	resp, err := r.deps.Upstream.GetWithOptions(ctx, listURL, r.inst.UpstreamRequestOptions(nil, r.upstreamTimeout()))
	if err != nil {
		r.deps.Metrics.RecordUpstreamError(r.inst.ID)
		return nil, nil, fmt.Errorf("upstream request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return nil, nil, ErrModuleNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		r.deps.Metrics.RecordUpstreamError(r.inst.ID)
		return nil, nil, fmt.Errorf("upstream returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, nil, fmt.Errorf("read upstream version list: %w", err)
	}

	versions := parseVersionList(string(body))
	if len(versions) == 0 {
		return nil, nil, ErrModuleNotFound
	}

	existingVersions := make(map[string]*GoVersion)
	if existing != nil {
		if vers, lerr := r.store.ListVersions(ctx, r.inst.ID, modulePath); lerr == nil {
			for _, v := range vers {
				existingVersions[v.Version] = v
			}
		}
	}

	now := time.Now().UTC()
	stored := make([]*GoVersion, 0, len(versions))
	var totalSize int64
	for _, version := range versions {
		if !isValidVersion(version) {
			continue
		}
		v := &GoVersion{
			ID:          versionDocID(r.inst.ID, modulePath, version),
			RegistryID:  r.inst.ID,
			ModulePath:  modulePath,
			Version:     version,
			Origin:      storage.OriginCached,
			PublishedAt: now,
			CreatedAt:   now,
			UpstreamZip: r.upstreamURL(modulePath, url.PathEscape(version)+".zip"),
		}
		if prev, ok := existingVersions[version]; ok {
			v.CreatedAt = prev.CreatedAt
			v.ZipDigest = prev.ZipDigest
			v.ZipSize = prev.ZipSize
			v.ZipCached = prev.ZipCached
			v.ModCached = prev.ModCached
			v.ModSize = prev.ModSize
			v.GoModDigest = prev.GoModDigest
			v.CachedAt = prev.CachedAt
			if !prev.InfoTime.IsZero() {
				v.InfoTime = prev.InfoTime
			}
			totalSize += v.ZipSize
		}
		if err := r.store.UpsertVersion(ctx, v); err != nil {
			return nil, nil, err
		}
		stored = append(stored, v)
	}
	SortVersionsAscending(stored)

	// Resolve the latest version's info timestamp so /@v/<version>.info is accurate.
	if len(stored) > 0 {
		latest := stored[len(stored)-1]
		if latest.InfoTime.IsZero() {
			if infoTime, ierr := r.fetchVersionInfoTime(ctx, modulePath, latest.Version); ierr == nil {
				latest.InfoTime = infoTime
				_ = r.store.UpsertVersion(ctx, latest)
			}
		}
	}

	mod := &GoModule{
		ID:               moduleDocID(r.inst.ID, modulePath),
		RegistryID:       r.inst.ID,
		Path:             modulePath,
		Origin:           storage.OriginCached,
		VersionCount:     len(stored),
		TotalSizeBytes:   totalSize,
		UpstreamSyncedAt: &now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if existing != nil {
		mod.CreatedAt = existing.CreatedAt
		mod.Downloads = existing.Downloads
	}
	if len(stored) > 0 {
		mod.LatestVersion = stored[len(stored)-1].Version
	}

	if err := r.store.UpsertModule(ctx, mod); err != nil {
		return nil, nil, err
	}
	return mod, stored, nil
}

// parseVersionList parses the plain-text upstream version list.
func parseVersionList(body string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if _, ok := seen[line]; ok {
			continue
		}
		seen[line] = struct{}{}
		out = append(out, line)
	}
	sortVersions(out)
	return out
}

// fetchVersionInfoTime fetches the .info document and returns its timestamp.
func (r *GoRegistry) fetchVersionInfoTime(ctx context.Context, modulePath, version string) (time.Time, error) {
	infoURL := r.upstreamURL(modulePath, url.PathEscape(version)+".info")
	resp, err := r.deps.Upstream.GetWithOptions(ctx, infoURL, r.inst.UpstreamRequestOptions(nil, r.upstreamTimeout()))
	if err != nil {
		return time.Time{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return time.Time{}, fmt.Errorf("upstream returned HTTP %d", resp.StatusCode)
	}
	var info VersionInfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&info); err != nil {
		return time.Time{}, err
	}
	return info.Time, nil
}

// fetchGoMod downloads the go.mod file for a module version.
func (r *GoRegistry) fetchGoMod(ctx context.Context, modulePath, version string) ([]byte, error) {
	modURL := r.upstreamURL(modulePath, url.PathEscape(version)+".mod")
	resp, err := r.deps.Upstream.GetWithOptions(ctx, modURL, r.inst.UpstreamRequestOptions(nil, r.upstreamTimeout()))
	if err != nil {
		r.deps.Metrics.RecordUpstreamError(r.inst.ID)
		return nil, fmt.Errorf("upstream request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return nil, ErrVersionNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		r.deps.Metrics.RecordUpstreamError(r.inst.ID)
		return nil, fmt.Errorf("upstream returned HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read upstream go.mod: %w", err)
	}

	// Cache the go.mod file in shared storage so repeated requests avoid upstream traffic.
	putRes, perr := r.deps.Storage.PutArtifact(ctx, storage.PutArtifactInput{
		RegistryID:       r.inst.ID,
		ArtifactKey:      modArtifactKey(modulePath, version),
		Origin:           storage.OriginCached,
		MaxArtifactBytes: 16 * 1024 * 1024,
		RegistryQuota:    r.inst.StorageQuotaBytes,
		Reader:           strings.NewReader(string(data)),
	})
	if perr == nil {
		if ver, verr := r.store.GetVersion(ctx, r.inst.ID, modulePath, version); verr == nil {
			ver.ModCached = true
			ver.ModSize = putRes.Ref.SizeBytes
			ver.GoModDigest = putRes.Ref.Digest
			now := time.Now().UTC()
			ver.CachedAt = &now
			_ = r.store.UpsertVersion(ctx, ver)
		}
	}
	return data, nil
}

// ensureZipCached guarantees the module zip exists in local storage. Returns (ref, cacheHit, error).
func (r *GoRegistry) ensureZipCached(ctx context.Context, modulePath, version string) (*storage.ArtifactRef, bool, error) {
	if !r.isModuleAllowed(modulePath) {
		return nil, false, ErrModuleNotFound
	}
	key := zipArtifactKey(modulePath, version)

	// Fast path: already cached.
	if ref, err := r.deps.Storage.GetArtifactRef(ctx, r.inst.ID, key); err == nil && ref != nil {
		return ref, true, nil
	}

	// Ensure the version metadata is known.
	ver, err := r.store.GetVersion(ctx, r.inst.ID, modulePath, version)
	if errors.Is(err, ErrVersionNotFound) && r.inst.Mode != registry.ModeHosted {
		if _, _, fetchErr := r.getOrFetchModule(ctx, modulePath); fetchErr != nil {
			return nil, false, fetchErr
		}
		ver, err = r.store.GetVersion(ctx, r.inst.ID, modulePath, version)
	}
	if err != nil {
		return nil, false, err
	}

	// Deduplicated concurrent download via singleflight.
	val, dlErr, _ := r.deps.Cache.Do(r.inst.ID, "gozip:"+key, func() (any, error) {
		if ref, err := r.deps.Storage.GetArtifactRef(ctx, r.inst.ID, key); err == nil && ref != nil {
			return ref, nil
		}

		dlURL := ver.UpstreamZip
		if dlURL == "" {
			dlURL = r.upstreamURL(modulePath, url.PathEscape(version)+".zip")
		}

		resp, err := r.deps.Upstream.GetWithOptions(ctx, dlURL, r.inst.UpstreamRequestOptions(nil, r.upstreamTimeout()*3))
		if err != nil {
			r.deps.Metrics.RecordUpstreamError(r.inst.ID)
			return nil, fmt.Errorf("download upstream module: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
			return nil, ErrVersionNotFound
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			r.deps.Metrics.RecordUpstreamError(r.inst.ID)
			return nil, fmt.Errorf("upstream module returned HTTP %d", resp.StatusCode)
		}

		putRes, err := r.deps.Storage.PutArtifact(ctx, storage.PutArtifactInput{
			RegistryID:       r.inst.ID,
			ArtifactKey:      key,
			Origin:           storage.OriginCached,
			MaxArtifactBytes: r.maxModuleBytes(),
			RegistryQuota:    r.inst.StorageQuotaBytes,
			Reader:           resp.Body,
		})
		if err != nil {
			return nil, err
		}

		now := time.Now().UTC()
		ver.ZipDigest = putRes.Ref.Digest
		ver.ZipSize = putRes.Ref.SizeBytes
		ver.ZipCached = true
		ver.CachedAt = &now
		_ = r.store.UpsertVersion(ctx, ver)

		if mod, merr := r.store.GetModule(ctx, r.inst.ID, modulePath); merr == nil {
			mod.TotalSizeBytes = r.recalcModuleSize(ctx, modulePath)
			mod.UpdatedAt = now
			_ = r.store.UpsertModule(ctx, mod)
		}

		return putRes.Ref, nil
	})
	if dlErr != nil {
		return nil, false, dlErr
	}
	return val.(*storage.ArtifactRef), false, nil
}

func (r *GoRegistry) recalcModuleSize(ctx context.Context, modulePath string) int64 {
	versions, err := r.store.ListVersions(ctx, r.inst.ID, modulePath)
	if err != nil {
		return 0
	}
	var sum int64
	for _, v := range versions {
		sum += v.ZipSize
	}
	return sum
}

// MatchModulePattern matches a module path against a policy pattern.
func MatchModulePattern(pattern, modulePath string) bool {
	return matchModulePattern(pattern, modulePath)
}
