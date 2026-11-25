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

	"github.com/amkargar/mirror-registry/backend/internal/auth"
	"github.com/amkargar/mirror-registry/backend/internal/registry"
	"github.com/amkargar/mirror-registry/backend/internal/storage"
)

func (r *FlutterRegistry) registerRoutes() {
	// Explorer JSON listing API for the isolated Flutter Storage UI
	r.mux.HandleFunc("GET /api/artifacts", r.handleListArtifactsAPI)

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

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"items":                 items,
		"total":                 total,
		"page":                  page,
		"limit":                 limit,
		"allowed_prefixes":      r.cfg.AllowedPrefixes,
		"upstream_url":          r.inst.UpstreamURL,
		"manifest_ttl_seconds":  r.cfg.ManifestTTLSeconds,
		"rewrite_manifest_urls": r.cfg.RewriteManifestURLs,
		"logical_bytes":         stats.LogicalBytes,
		"downloads":             downloads,
	})
}

func (r *FlutterRegistry) handleObject(w http.ResponseWriter, req *http.Request) {
	rawPath := req.PathValue("path")
	cleanPath, err := sanitizePath(rawPath)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !r.isPathAllowed(cleanPath) {
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
		if r.cfg.ServeStaleOnError && haveMeta {
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

	val, err, shared := r.deps.Cache.Do(r.inst.ID, "flutter:"+cleanPath, func() (any, error) {
		upstreamURL := strings.TrimRight(r.inst.UpstreamURL, "/") + "/" + cleanPath
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
			MaxArtifactBytes: r.cfg.MaxArtifactSizeBytes,
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
		if manifest && r.cfg.ManifestTTLSeconds > 0 {
			exp := now.Add(time.Duration(r.cfg.ManifestTTLSeconds) * time.Second)
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
	if manifest && r.cfg.RewriteManifestURLs && isFlutterReleaseJSON(rec.Path) {
		rawBytes, readErr := io.ReadAll(io.LimitReader(f, 32*1024*1024))
		if readErr == nil {
			rewritten := rewriteReleaseManifestBaseURL(rawBytes, r.inst.UpstreamURL, registry.BaseURLFromContext(req.Context()))
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("X-Registry-Cache", cacheState)
			w.Header().Set("X-Checksum-SHA256", ref.Digest)
			w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", r.cfg.ManifestTTLSeconds))
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
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", r.cfg.ManifestTTLSeconds))
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

func rewriteReleaseManifestBaseURL(raw []byte, upstreamURL, mirrorBaseURL string) []byte {
	mirrorBaseURL = strings.TrimRight(strings.TrimSpace(mirrorBaseURL), "/")
	upstreamOrigin := strings.TrimRight(strings.TrimSpace(upstreamURL), "/")
	if mirrorBaseURL == "" || upstreamOrigin == "" {
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
	if strings.HasPrefix(baseVal, upstreamOrigin) {
		payload["base_url"] = mirrorBaseURL + strings.TrimPrefix(baseVal, upstreamOrigin)
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(payload); err == nil {
			return buf.Bytes()
		}
	}
	return raw
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
