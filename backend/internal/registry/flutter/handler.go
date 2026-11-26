package flutter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/amkargar/depyard/backend/internal/auth"
	"github.com/amkargar/depyard/backend/internal/registry"
	"github.com/amkargar/depyard/backend/internal/storage"
)

func (r *FlutterRegistry) registerRoutes() {
	// Explorer JSON listing API for the isolated Flutter Storage UI
	r.mux.HandleFunc("GET /api/artifacts", r.handleListArtifactsAPI)
	// Per-version Flutter SDK registry (versions, channels, storage settings) for SDK tooling & UI
	r.mux.HandleFunc("GET /api/registry", r.handleVersionRegistryAPI)
	r.mux.HandleFunc("GET /api/registry/versions/{version}", r.handleVersionRegistryEntryAPI)

	// Flutter Storage Base URL protocol routes (Go's GET pattern matches both GET and HEAD)
	r.mux.HandleFunc("GET /{path...}", r.handleObject)
}

func (r *FlutterRegistry) authorizeRead(w http.ResponseWriter, req *http.Request, namespace string) bool {
	principal, err := r.deps.Auth.AuthenticateRequest(req.Context(), req)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, err.Error())
		return false
	}
	if err := r.deps.Auth.Authorize(principal, auth.AuthRequest{
		Policy:     r.inst.AccessPolicy(),
		Permission: auth.PermissionRead,
		Namespace:  namespace,
	}); err != nil {
		if errors.Is(err, auth.ErrUnauthenticated) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="flutter-storage"`)
			writeJSONError(w, http.StatusUnauthorized, "authentication required")
		} else {
			writeJSONError(w, http.StatusForbidden, "permission denied")
		}
		return false
	}
	return true
}

func (r *FlutterRegistry) handleListArtifactsAPI(w http.ResponseWriter, req *http.Request) {
	if !r.authorizeRead(w, req, "") {
		return
	}
	q := req.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page <= 0 {
		page = 1
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	items, total, err := r.store.ListArtifacts(req.Context(), r.inst.ID, ListQuery{
		Search:   q.Get("q"),
		Category: q.Get("category"),
		Page:     page,
		Limit:    limit,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if items == nil {
		items = []*ArtifactRecord{}
	}

	var stats storage.RegistryStorageStats
	if r.deps.Storage != nil {
		stats, _ = r.deps.Storage.GetRegistryStats(req.Context(), r.inst.ID)
	}
	var downloads int64
	if r.deps.Metrics != nil {
		if snap, err := r.deps.Metrics.GetRegistrySnapshot(req.Context(), r.inst.ID); err == nil {
			downloads = snap.Downloads
		}
	}

	baseURL := registry.BaseURLFromContext(req.Context())

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"items":                 items,
		"total":                 total,
		"page":                  page,
		"limit":                 limit,
		"allowed_prefixes":      r.config().AllowedPrefixes,
		"upstream_url":          r.inst.UpstreamURL,
		"storage_name":          r.config().StorageName,
		"releases_base_url":     r.releasesBaseURL(),
		"registry_ttl_seconds":  r.config().RegistryTTLSeconds,
		"manifest_ttl_seconds":  r.config().ManifestTTLSeconds,
		"rewrite_manifest_urls": r.config().RewriteManifestURLs,
		"download_storage":      r.storageChannels(baseURL),
		"logical_bytes":         stats.LogicalBytes,
		"downloads":             downloads,
	})
}

// handleVersionRegistryAPI serves the live per-version Flutter SDK registry for this mirror.
// GET /r/<slug>/api/registry
func (r *FlutterRegistry) handleVersionRegistryAPI(w http.ResponseWriter, req *http.Request) {
	if !r.authorizeRead(w, req, "") {
		return
	}
	baseURL := strings.TrimRight(registry.BaseURLFromContext(req.Context()), "/")
	reg, err := r.buildVersionRegistry(req.Context(), baseURL)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", minInt(r.config().RegistryTTLSeconds, 60)))
	if req.Method != http.MethodHead {
		_ = json.NewEncoder(w).Encode(reg)
	}
}

// handleVersionRegistryEntryAPI serves one Flutter SDK version entry from the registry.
// GET /r/<slug>/api/registry/versions/{version}
func (r *FlutterRegistry) handleVersionRegistryEntryAPI(w http.ResponseWriter, req *http.Request) {
	if !r.authorizeRead(w, req, "") {
		return
	}
	version := strings.TrimSpace(req.PathValue("version"))
	if version == "" {
		writeJSONError(w, http.StatusBadRequest, "version is required")
		return
	}
	baseURL := strings.TrimRight(registry.BaseURLFromContext(req.Context()), "/")
	reg, err := r.buildVersionRegistry(req.Context(), baseURL)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	for _, v := range reg.Versions {
		if v.Version == version {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "public, max-age=60")
			if req.Method != http.MethodHead {
				_ = json.NewEncoder(w).Encode(v)
			}
			return
		}
	}
	writeJSONError(w, http.StatusNotFound, "flutter version "+version+" is not available from this mirror")
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (r *FlutterRegistry) handleObject(w http.ResponseWriter, req *http.Request) {
	rawPath := req.PathValue("path")
	cleanPath, err := sanitizePath(rawPath)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !r.isPathAllowed(cleanPath) {
		if cat, disabled := r.config().disabledCategoryForPath(cleanPath); disabled {
			writeJSONError(w, http.StatusForbidden, fmt.Sprintf("storage category %q is disabled on this registry", cat.Name))
			return
		}
		writeJSONError(w, http.StatusForbidden, "requested path is outside configured allowed_prefixes")
		return
	}
	if !r.authorizeRead(w, req, cleanPath) {
		return
	}

	manifest := isManifestPath(cleanPath)
	rec, err := r.store.GetArtifact(req.Context(), r.inst.ID, cleanPath)
	haveMeta := err == nil && rec != nil

	// Check cache freshness
	useCached := false
	if haveMeta {
		if !manifest {
			useCached = true
		} else if rec.ExpiresAt != nil && time.Now().UTC().Before(*rec.ExpiresAt) {
			useCached = true
		}
	}

	if useCached {
		if served := r.serveFromStorage(w, req, rec, "HIT", manifest); served {
			return
		}
		// Blob missing on disk despite metadata — fall through and re-fetch cleanly.
	}

	// Fetch from upstream with singleflight deduplication
	fetchedRec, _, fetchErr := r.fetchAndCacheFromUpstream(req.Context(), cleanPath, manifest)
	if fetchErr != nil {
		if errors.Is(fetchErr, errUpstreamNotFound) {
			writeJSONError(w, http.StatusNotFound, "artifact not found on upstream flutter storage")
			return
		}
		if errors.Is(fetchErr, storage.ErrQuotaExceeded) {
			writeJSONError(w, http.StatusInsufficientStorage, "registry storage quota exceeded")
			return
		}
		if errors.Is(fetchErr, storage.ErrSizeExceeded) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "artifact exceeds max_artifact_size_bytes")
			return
		}
		// Stale-while-error fallback for cached manifests/artifacts when upstream is unreachable
		if r.config().ServeStaleOnError && haveMeta {
			if served := r.serveFromStorage(w, req, rec, "STALE", manifest); served {
				return
			}
		}
		writeJSONError(w, http.StatusBadGateway, fmt.Sprintf("upstream flutter storage error: %v", fetchErr))
		return
	}

	if !r.serveFromStorage(w, req, fetchedRec, "MISS", manifest) {
		writeJSONError(w, http.StatusInternalServerError, "failed to read cached artifact")
	}
}

var errUpstreamNotFound = errors.New("upstream returned 404")

func (r *FlutterRegistry) fetchAndCacheFromUpstream(ctx context.Context, cleanPath string, manifest bool) (*ArtifactRecord, bool, error) {
	type sfResult struct {
		rec *ArtifactRecord
	}

	upstreamBase := r.upstreamForPath(cleanPath)
	val, err, shared := r.deps.Cache.Do(r.inst.ID, "flutter:"+cleanPath, func() (any, error) {
		upstreamURL := upstreamBase + "/" + cleanPath
		opts := r.inst.UpstreamRequestOptions(nil, r.upstreamTimeout())

		resp, err := r.deps.Upstream.GetWithOptions(ctx, upstreamURL, opts)
		if err != nil {
			r.deps.Metrics.RecordUpstreamError(r.inst.ID)
			return nil, err
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound {
			return nil, errUpstreamNotFound
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			r.deps.Metrics.RecordUpstreamError(r.inst.ID)
			return nil, fmt.Errorf("upstream HTTP %d", resp.StatusCode)
		}

		putRes, err := r.deps.Storage.PutArtifact(ctx, storage.PutArtifactInput{
			RegistryID:       r.inst.ID,
			ArtifactKey:      cleanPath,
			Origin:           storage.OriginCached,
			MaxArtifactBytes: r.config().MaxArtifactSizeBytes,
			RegistryQuota:    r.inst.StorageQuotaBytes,
			Reader:           resp.Body,
		})
		if err != nil {
			return nil, err
		}

		now := time.Now().UTC()
		ct := strings.TrimSpace(resp.Header.Get("Content-Type"))
		if ct == "" {
			ct = inferContentType(cleanPath)
		}

		rec := &ArtifactRecord{
			ID:           docID(r.inst.ID, cleanPath),
			RegistryID:   r.inst.ID,
			Path:         cleanPath,
			Category:     categoryForPath(cleanPath),
			ContentType:  ct,
			ETag:         strings.TrimSpace(resp.Header.Get("ETag")),
			LastModified: strings.TrimSpace(resp.Header.Get("Last-Modified")),
			Digest:       putRes.Ref.Digest,
			SizeBytes:    putRes.Ref.SizeBytes,
			IsManifest:   manifest,
			UpstreamURL:  upstreamURL,
			CachedAt:     now,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if ttl := r.manifestTTLForPath(cleanPath); manifest && ttl > 0 {
			exp := now.Add(time.Duration(ttl) * time.Second)
			rec.ExpiresAt = &exp
		}
		if prev, err := r.store.GetArtifact(ctx, r.inst.ID, cleanPath); err == nil && prev != nil {
			rec.CreatedAt = prev.CreatedAt
			rec.Downloads = prev.Downloads
		}
		if err := r.store.UpsertArtifact(ctx, rec); err != nil {
			return nil, err
		}
		return &sfResult{rec: rec}, nil
	})
	if err != nil {
		return nil, shared, err
	}
	return val.(*sfResult).rec, shared, nil
}

func (r *FlutterRegistry) serveFromStorage(w http.ResponseWriter, req *http.Request, rec *ArtifactRecord, cacheState string, manifest bool) bool {
	f, ref, err := r.deps.Storage.OpenArtifact(req.Context(), r.inst.ID, rec.Path)
	if err != nil {
		return false
	}
	defer f.Close()

	cacheHit := cacheState == "HIT" || cacheState == "STALE"
	ct := rec.ContentType
	if ct == "" {
		ct = inferContentType(rec.Path)
	}

	// Dynamic base_url rewriting for Flutter release JSON manifests (releases_linux.json, etc.)
	if manifest && r.pathRewritesManifests(rec.Path) && isFlutterReleaseJSON(rec.Path) {
		rawBytes, readErr := io.ReadAll(io.LimitReader(f, 32*1024*1024))
		if readErr == nil {
			rewritten := rewriteReleaseManifestBaseURL(rawBytes, r.upstreamForPath(rec.Path), registry.BaseURLFromContext(req.Context()))
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("X-Registry-Cache", cacheState)
			w.Header().Set("X-Checksum-SHA256", ref.Digest)
			w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", r.manifestTTLForPath(rec.Path)))
			w.Header().Set("Content-Length", strconv.Itoa(len(rewritten)))
			w.WriteHeader(http.StatusOK)
			if req.Method != http.MethodHead {
				n, _ := w.Write(rewritten)
				r.deps.Metrics.RecordDownload(r.inst.ID, int64(n), cacheHit)
				_ = r.store.IncrementDownloads(req.Context(), r.inst.ID, rec.Path)
			}
			return true
		}
		_, _ = f.Seek(0, io.SeekStart)
	}

	stopTransfer := r.deps.Metrics.BeginTransfer(r.inst.ID)
	defer stopTransfer()

	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Registry-Cache", cacheState)
	w.Header().Set("X-Checksum-SHA256", ref.Digest)
	if rec.ETag != "" {
		w.Header().Set("ETag", rec.ETag)
	} else {
		w.Header().Set("ETag", `"`+ref.Digest+`"`)
	}
	if manifest {
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", r.manifestTTLForPath(rec.Path)))
	} else {
		w.Header().Set("Cache-Control", "public, max-age=86400")
	}

	cw := &countingResponseWriter{ResponseWriter: w}
	modTime := rec.CachedAt
	if modTime.IsZero() {
		modTime = time.Now().UTC()
	}
	http.ServeContent(cw, req, path.Base(rec.Path), modTime, f)

	if req.Method != http.MethodHead {
		r.deps.Metrics.RecordDownload(r.inst.ID, cw.written, cacheHit)
		_ = r.store.IncrementDownloads(req.Context(), r.inst.ID, rec.Path)
	}
	return true
}

func sanitizePath(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("artifact path is required")
	}
	if strings.ContainsRune(raw, '\\') || strings.ContainsRune(raw, 0) {
		return "", fmt.Errorf("invalid artifact path")
	}
	for _, seg := range strings.Split(raw, "/") {
		if seg == ".." {
			return "", fmt.Errorf("path traversal is not allowed")
		}
	}
	cleaned := strings.TrimPrefix(path.Clean("/"+raw), "/")
	if cleaned == "" || cleaned == "." {
		return "", fmt.Errorf("artifact path is required")
	}
	return cleaned, nil
}

func isManifestPath(cleanPath string) bool {
	base := strings.ToLower(path.Base(cleanPath))
	if isFlutterReleaseJSON(cleanPath) {
		return true
	}
	if strings.HasPrefix(base, "maven-metadata.xml") {
		return true
	}
	if base == "version" || base == "latest" || strings.Contains(cleanPath, "/latest/") {
		return true
	}
	return false
}

func isFlutterReleaseJSON(cleanPath string) bool {
	base := strings.ToLower(path.Base(cleanPath))
	return strings.HasPrefix(base, "releases_") && strings.HasSuffix(base, ".json")
}

// manifestBaseSuffix is the stable path segment every Flutter release manifest uses for base_url.
// Upstream manifests may point base_url at a CDN that is not the host used to fetch the manifest
// (e.g. storage.googleapis.com serves base_url = https://storage.googleapis.com/flutter_infra_release/releases
// while regional mirrors or private caches use their own host), so rewriting aligns on this suffix.
const manifestBaseSuffix = "flutter_infra_release/releases"

// rewriteReleaseManifestBaseURL rewrites base_url inside releases_*.json manifests so Flutter CLI
// downloads SDK archives from this mirror instead of the upstream storage host.
func rewriteReleaseManifestBaseURL(raw []byte, upstreamURL, mirrorBaseURL string) []byte {
	mirrorBaseURL = strings.TrimRight(strings.TrimSpace(mirrorBaseURL), "/")
	upstreamOrigin := strings.TrimRight(strings.TrimSpace(upstreamURL), "/")
	if mirrorBaseURL == "" {
		return raw
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return raw
	}
	baseVal, ok := payload["base_url"].(string)
	if !ok || baseVal == "" {
		return raw
	}
	rewritten, changed := rewriteManifestBase(baseVal, upstreamOrigin, mirrorBaseURL)
	if !changed {
		return raw
	}
	payload["base_url"] = rewritten
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err == nil {
		return buf.Bytes()
	}
	return raw
}

// rewriteManifestBase maps an upstream manifest base_url onto this mirror.
func rewriteManifestBase(baseVal, upstreamOrigin, mirrorBaseURL string) (string, bool) {
	baseVal = strings.TrimRight(strings.TrimSpace(baseVal), "/")
	if baseVal == "" {
		return baseVal, false
	}
	if upstreamOrigin != "" && strings.HasPrefix(baseVal, upstreamOrigin) {
		return mirrorBaseURL + strings.TrimPrefix(baseVal, upstreamOrigin), true
	}
	// Align on the canonical flutter_infra_release/releases suffix so regional/CDN base URLs
	// (which differ from the configured upstream host) are still rewritten correctly.
	if idx := strings.Index(baseVal, manifestBaseSuffix); idx >= 0 {
		if tail := strings.TrimPrefix(baseVal[idx:], manifestBaseSuffix); tail == "" || strings.HasPrefix(tail, "/") {
			return mirrorBaseURL + "/" + manifestBaseSuffix + tail, true
		}
	}
	if idx := strings.Index(baseVal, "releases"); idx > 0 && strings.HasSuffix(baseVal[:idx], "/") {
		return mirrorBaseURL + "/" + manifestBaseSuffix + baseVal[idx+len("releases"):], true
	}
	return baseVal, false
}

func inferContentType(cleanPath string) string {
	ext := strings.ToLower(path.Ext(cleanPath))
	switch ext {
	case ".json":
		return "application/json; charset=utf-8"
	case ".xml", ".pom":
		return "application/xml; charset=utf-8"
	case ".zip", ".jar", ".aar":
		return "application/zip"
	case ".xz":
		return "application/x-xz"
	case ".gz":
		return "application/gzip"
	case ".sha1", ".sha256", ".md5", ".txt":
		return "text/plain; charset=utf-8"
	}
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

type countingResponseWriter struct {
	http.ResponseWriter
	written int64
}

func (c *countingResponseWriter) Write(p []byte) (int, error) {
	n, err := c.ResponseWriter.Write(p)
	c.written += int64(n)
	return n, err
}
