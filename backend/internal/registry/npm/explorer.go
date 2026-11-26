package npm

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/amkargar/depyard/backend/internal/auth"
)

// explorerPagination reads and bounds the common page/limit query parameters.
func explorerPagination(req *http.Request) (page, limit int) {
	page, _ = strconv.Atoi(req.URL.Query().Get("page"))
	limit, _ = strconv.Atoi(req.URL.Query().Get("limit"))
	if page < 1 {
		page = 1
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	return page, limit
}

// explorerPackage is the JSON view of a package used by the public npm explorer.
type explorerPackage struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description,omitempty"`
	Origin        string `json:"origin"`
	LatestVersion string `json:"latest_version,omitempty"`
	VersionCount  int    `json:"version_count"`
	TotalSize     int64  `json:"total_size_bytes"`
	Downloads     int64  `json:"downloads"`
	UpdatedAt     string `json:"updated_at,omitempty"`
	CreatedAt     string `json:"created_at,omitempty"`
}

type explorerVersion struct {
	Version     string `json:"version"`
	Filename    string `json:"filename"`
	Digest      string `json:"digest,omitempty"`
	Shasum      string `json:"shasum,omitempty"`
	Integrity   string `json:"integrity,omitempty"`
	SizeBytes   int64  `json:"size_bytes"`
	Origin      string `json:"origin"`
	PublishedAt string `json:"published_at,omitempty"`
	URL         string `json:"url"`
}

type explorerPackageDetail struct {
	Package     explorerPackage   `json:"package"`
	Versions    []explorerVersion `json:"versions"`
	RegistryURL string            `json:"registry_url"`
	InstallURL  string            `json:"install_url"`
	PublishURL  string            `json:"publish_url"`
}

// registerExplorerRoutes mounts the read-only explorer API.
func (r *NpmRegistry) registerExplorerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/packages", r.handleExplorerList)
	// Catch-all so scoped names reach this handler whether the client escapes
	// their slash (%2F) or sends it literally.
	mux.HandleFunc("GET /api/packages/{package...}", r.handleExplorerDetail)
}

func (r *NpmRegistry) handleExplorerList(w http.ResponseWriter, req *http.Request) {
	if _, ok := r.authorize(w, req, auth.PermissionRead, ""); !ok {
		return
	}
	page, limit := explorerPagination(req)
	packages, total, err := r.store.ListPackages(req.Context(), PackageListQuery{
		RegistryID: r.inst.ID,
		Search:     strings.TrimSpace(req.URL.Query().Get("q")),
		Origin:     strings.TrimSpace(req.URL.Query().Get("origin")),
		Page:       page,
		Limit:      limit,
	})
	if err != nil {
		writeNpmError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]explorerPackage, 0, len(packages))
	for _, p := range packages {
		items = append(items, toExplorerPackage(p))
	}
	writeNpmJSON(w, http.StatusOK, map[string]any{
		"packages": items,
		"total":    total,
		"page":     page,
		"limit":    limit,
	}, ContentTypeJSON)
}

func (r *NpmRegistry) handleExplorerDetail(w http.ResponseWriter, req *http.Request) {
	raw := strings.TrimPrefix(req.PathValue("package"), "/")
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		writeNpmError(w, http.StatusBadRequest, "invalid package name encoding")
		return
	}
	name := NormalizePackageName(decoded)
	if name == "" {
		writeNpmError(w, http.StatusBadRequest, "package name is required")
		return
	}
	if _, ok := r.authorize(w, req, auth.PermissionRead, name); !ok {
		return
	}
	pkg, err := r.store.GetPackage(req.Context(), r.inst.ID, name)
	if err != nil {
		writeNpmError(w, http.StatusNotFound, "package not found")
		return
	}
	baseURL := r.baseURL(req)
	versions := make([]explorerVersion, 0, len(pkg.Versions))
	for _, v := range ensureSorted(pkg.Versions) {
		versions = append(versions, explorerVersion{
			Version:     v.Version,
			Filename:    v.Filename,
			Digest:      v.Digest,
			Shasum:      v.Shasum,
			Integrity:   v.Integrity,
			SizeBytes:   v.SizeBytes,
			Origin:      v.Origin,
			PublishedAt: timestamp(v.PublishedAt),
			URL:         tarballURL(baseURL, pkg.Name, v.Version, v.Filename),
		})
	}
	writeNpmJSON(w, http.StatusOK, explorerPackageDetail{
		Package:     toExplorerPackage(pkg),
		Versions:    versions,
		RegistryURL: baseURL,
		InstallURL:  baseURL + "/" + escapePackagePath(pkg.Name),
		PublishURL:  baseURL + "/" + escapePackagePath(pkg.Name),
	}, ContentTypeJSON)
}

func toExplorerPackage(p *NpmPackage) explorerPackage {
	return explorerPackage{
		ID:            p.ID,
		Name:          p.Name,
		Description:   p.Description,
		Origin:        p.Origin,
		LatestVersion: p.LatestVersion,
		VersionCount:  p.VersionCount,
		TotalSize:     p.TotalSizeBytes,
		Downloads:     p.Downloads,
		UpdatedAt:     timestamp(p.UpdatedAt),
		CreatedAt:     timestamp(p.CreatedAt),
	}
}
