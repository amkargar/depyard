package pub

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/amkargar/depyard/backend/internal/auth"
	"github.com/amkargar/depyard/backend/internal/registry"
	"github.com/amkargar/depyard/backend/internal/storage"
)

func (r *PubRegistry) buildHandler() http.Handler {
	mux := http.NewServeMux()

	// Official Dart Pub Hosted Repository Specification v2 routes
	mux.HandleFunc("GET /api/packages", r.handleListPackages)
	mux.HandleFunc("GET /api/search", r.handleSearchPackages)

	// Publishing workflow (must be registered cleanly alongside {package} route)
	mux.HandleFunc("GET /api/packages/versions/new", r.handleNewVersion)
	mux.HandleFunc("POST /api/packages/versions/newUpload/{id}", r.handleNewUpload)
	mux.HandleFunc("GET /api/packages/versions/newUploadFinish/{id}", r.handleUploadFinish)

	// Package metadata, Pub explorer detail, retraction, discontinuation, uploaders
	mux.HandleFunc("GET /api/packages/{package}", r.handleGetPackage)
	mux.HandleFunc("GET /api/packages/{package}/detail", r.handleGetPackageDetail)
	mux.HandleFunc("POST /api/packages/{package}/versions/{version}/retract", func(w http.ResponseWriter, req *http.Request) {
		r.handleRetractVersion(w, req, true)
	})
	mux.HandleFunc("POST /api/packages/{package}/versions/{version}/unretract", func(w http.ResponseWriter, req *http.Request) {
		r.handleRetractVersion(w, req, false)
	})
	mux.HandleFunc("POST /api/packages/{package}/discontinue", r.handleDiscontinuePackage)
	mux.HandleFunc("GET /api/packages/{package}/uploaders", r.handleListUploaders)
	mux.HandleFunc("POST /api/packages/{package}/uploaders", r.handleAddUploader)
	mux.HandleFunc("DELETE /api/packages/{package}/uploaders/{email}", r.handleRemoveUploader)

	// Package archive download: /packages/{package}/versions/{version}.tar.gz
	mux.HandleFunc("GET /packages/{package}/versions/{archive}", r.handleDownloadArchive)

	return mux
}

func (r *PubRegistry) authorizeRequest(w http.ResponseWriter, req *http.Request, perm auth.Permission, namespace string) (*auth.Principal, bool) {
	principal, err := r.deps.Auth.AuthenticateRequest(req.Context(), req)
	if err != nil {
		writePubError(w, http.StatusUnauthorized, "Unauthorized", err.Error())
		return nil, false
	}
	if err := r.deps.Auth.Authorize(principal, auth.AuthRequest{
		Policy:     r.inst.AccessPolicy(),
		Permission: perm,
		Namespace:  namespace,
	}); err != nil {
		if errors.Is(err, auth.ErrUnauthenticated) {
			writePubError(w, http.StatusUnauthorized, "Unauthorized", "authentication required for this pub registry")
		} else {
			writePubError(w, http.StatusForbidden, "Forbidden", "insufficient permissions for this pub registry")
		}
		return nil, false
	}
	return principal, true
}

func (r *PubRegistry) handleGetPackage(w http.ResponseWriter, req *http.Request) {
	pkgName := strings.TrimSpace(req.PathValue("package"))
	if _, ok := r.authorizeRequest(w, req, auth.PermissionRead, pkgName); !ok {
		return
	}
	if !IsValidPackageName(pkgName) {
		writePubError(w, http.StatusNotFound, "NotFound", fmt.Sprintf("invalid package name %q", pkgName))
		return
	}

	pkg, versions, err := r.getOrFetchPackage(req.Context(), pkgName)
	if err != nil {
		if errors.Is(err, ErrPackageNotFound) {
			writePubError(w, http.StatusNotFound, "NotFound", fmt.Sprintf("package %q not found", pkgName))
			return
		}
		writePubError(w, http.StatusBadGateway, "UpstreamError", err.Error())
		return
	}

	baseURL := registry.BaseURLFromContext(req.Context())
	resp := buildPackageResponse(baseURL, pkg, versions)
	writePubJSON(w, http.StatusOK, resp)
}

// handleGetPackageDetail returns rich Pub-specific package metadata for the Pub Explorer UI.
func (r *PubRegistry) handleGetPackageDetail(w http.ResponseWriter, req *http.Request) {
	pkgName := strings.TrimSpace(req.PathValue("package"))
	if _, ok := r.authorizeRequest(w, req, auth.PermissionRead, pkgName); !ok {
		return
	}
	if !IsValidPackageName(pkgName) {
		writePubError(w, http.StatusNotFound, "NotFound", fmt.Sprintf("invalid package name %q", pkgName))
		return
	}

	pkg, versions, err := r.getOrFetchPackage(req.Context(), pkgName)
	if err != nil {
		if errors.Is(err, ErrPackageNotFound) {
			writePubError(w, http.StatusNotFound, "NotFound", fmt.Sprintf("package %q not found", pkgName))
			return
		}
		writePubError(w, http.StatusBadGateway, "UpstreamError", err.Error())
		return
	}

	baseURL := registry.BaseURLFromContext(req.Context())
	SortVersionsAscending(versions)

	type detailVersion struct {
		Version       string         `json:"version"`
		Origin        string         `json:"origin"`
		Pubspec       map[string]any `json:"pubspec"`
		ArchiveURL    string         `json:"archive_url"`
		ArchiveSHA256 string         `json:"archive_sha256,omitempty"`
		ArchiveSize   int64          `json:"archive_size"`
		ArchiveCached bool           `json:"archive_cached"`
		Retracted     bool           `json:"retracted"`
		PublishedAt   string         `json:"published_at"`
	}

	// Return newest-first for the Pub UI Versions tab
	verList := make([]detailVersion, 0, len(versions))
	for i := len(versions) - 1; i >= 0; i-- {
		v := versions[i]
		verList = append(verList, detailVersion{
			Version:       v.Version,
			Origin:        v.Origin,
			Pubspec:       v.Pubspec,
			ArchiveURL:    buildArchiveURL(baseURL, v.PackageName, v.Version),
			ArchiveSHA256: v.ArchiveDigest,
			ArchiveSize:   v.ArchiveSize,
			ArchiveCached: v.ArchiveCached,
			Retracted:     v.Retracted,
			PublishedAt:   v.PublishedAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}

	latest := SelectLatestVersion(versions)
	var latestPubspec map[string]any
	if latest != nil {
		latestPubspec = latest.Pubspec
	}

	writePubJSON(w, http.StatusOK, map[string]any{
		"package":        pkg,
		"latest_pubspec": latestPubspec,
		"versions":       verList,
		"base_url":       baseURL,
		"registry_slug":  r.inst.Slug,
		"registry_mode":  r.inst.Mode,
	})
}

func (r *PubRegistry) handleDownloadArchive(w http.ResponseWriter, req *http.Request) {
	pkgName := strings.TrimSpace(req.PathValue("package"))
	archivePart := strings.TrimSpace(req.PathValue("archive"))
	if !strings.HasSuffix(archivePart, ".tar.gz") {
		writePubError(w, http.StatusNotFound, "NotFound", "expected .tar.gz archive path")
		return
	}
	version := strings.TrimSuffix(archivePart, ".tar.gz")

	// Authorize BEFORE touching storage — knowledge of blob digest or URL never bypasses auth!
	if _, ok := r.authorizeRequest(w, req, auth.PermissionRead, pkgName); !ok {
		return
	}

	endTransfer := r.deps.Metrics.BeginTransfer(r.inst.ID)
	defer endTransfer()

	ref, cacheHit, err := r.ensureArchiveCached(req.Context(), pkgName, version)
	if err != nil {
		switch {
		case errors.Is(err, ErrPackageNotFound), errors.Is(err, ErrVersionNotFound):
			writePubError(w, http.StatusNotFound, "NotFound", "package archive not found")
		case errors.Is(err, storage.ErrQuotaExceeded):
			writePubError(w, http.StatusInsufficientStorage, "QuotaExceeded", "registry storage quota exceeded")
		case errors.Is(err, storage.ErrDigestMismatch):
			writePubError(w, http.StatusBadGateway, "IntegrityError", "upstream archive checksum verification failed")
		default:
			writePubError(w, http.StatusBadGateway, "UpstreamError", err.Error())
		}
		return
	}

	file, openedRef, err := r.deps.Storage.OpenArtifact(req.Context(), r.inst.ID, artifactKey(pkgName, version))
	if err != nil {
		writePubError(w, http.StatusNotFound, "NotFound", "archive blob unavailable")
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.tar.gz"`, pkgName, version))
	w.Header().Set("Content-Length", strconv.FormatInt(openedRef.SizeBytes, 10))
	w.Header().Set("X-Checksum-Sha256", ref.Digest)
	if cacheHit {
		w.Header().Set("X-Registry-Cache", "HIT")
	} else {
		w.Header().Set("X-Registry-Cache", "MISS")
	}
	w.WriteHeader(http.StatusOK)

	written, _ := io.Copy(w, file)
	r.deps.Metrics.RecordDownload(r.inst.ID, written, cacheHit)
	_ = r.store.IncrementPackageDownloads(req.Context(), r.inst.ID, pkgName)
}

func (r *PubRegistry) handleListPackages(w http.ResponseWriter, req *http.Request) {
	if _, ok := r.authorizeRequest(w, req, auth.PermissionRead, ""); !ok {
		return
	}
	page, _ := strconv.Atoi(req.URL.Query().Get("page"))
	limit, _ := strconv.Atoi(req.URL.Query().Get("limit"))
	if page <= 0 {
		page = 1
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	search := strings.TrimSpace(req.URL.Query().Get("q"))
	origin := strings.TrimSpace(req.URL.Query().Get("origin"))

	pkgs, total, err := r.store.ListPackages(req.Context(), PackageListQuery{
		RegistryID: r.inst.ID,
		Search:     search,
		Origin:     origin,
		Page:       page,
		Limit:      limit,
	})
	if err != nil {
		writePubError(w, http.StatusInternalServerError, "ServerError", err.Error())
		return
	}
	if pkgs == nil {
		pkgs = []*PubPackage{}
	}

	writePubJSON(w, http.StatusOK, map[string]any{
		"packages": pkgs,
		"total":    total,
		"page":     page,
		"limit":    limit,
	})
}

// handleSearchPackages supports Dart Pub's GET /api/search?q=...&page=... endpoint.
func (r *PubRegistry) handleSearchPackages(w http.ResponseWriter, req *http.Request) {
	if _, ok := r.authorizeRequest(w, req, auth.PermissionRead, ""); !ok {
		return
	}
	q := strings.TrimSpace(req.URL.Query().Get("q"))
	page, _ := strconv.Atoi(req.URL.Query().Get("page"))
	if page <= 0 {
		page = 1
	}
	pkgs, total, err := r.store.ListPackages(req.Context(), PackageListQuery{
		RegistryID: r.inst.ID,
		Search:     q,
		Page:       page,
		Limit:      20,
	})
	if err != nil {
		writePubError(w, http.StatusInternalServerError, "ServerError", err.Error())
		return
	}

	items := make([]map[string]any, 0, len(pkgs))
	for _, p := range pkgs {
		items = append(items, map[string]any{
			"package":        p.Name,
			"latest_version": p.LatestVersion,
			"description":    p.Description,
			"origin":         p.Origin,
			"downloads":      p.Downloads,
			"updated_at":     p.UpdatedAt,
		})
	}
	writePubJSON(w, http.StatusOK, map[string]any{
		"packages": items,
		"total":    total,
		"page":     page,
	})
}
