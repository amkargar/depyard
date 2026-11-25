package goproxy

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/amkargar/mirror-registry/backend/internal/auth"
)

// The explorer endpoints below serve the public Go module browser UI. They are
// mounted alongside the GOPROXY protocol routes, so a registry instance exposes
// both the machine-facing protocol and the human-facing directory.

func (r *GoRegistry) buildExplorerHandler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/modules", r.handleListModules)
	mux.HandleFunc("GET /api/modules/{path...}", r.handleGetModuleDetail)

	return mux
}

// handleListModules serves the module directory for the explorer UI.
func (r *GoRegistry) handleListModules(w http.ResponseWriter, req *http.Request) {
	if !r.authorizeRequest(w, req) {
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

	mods, total, err := r.store.ListModules(req.Context(), ModuleListQuery{
		RegistryID: r.inst.ID,
		Search:     search,
		Page:       page,
		Limit:      limit,
	})
	if err != nil {
		writeGoError(w, http.StatusInternalServerError, "ServerError", err.Error())
		return
	}
	if mods == nil {
		mods = []*GoModule{}
	}

	writeGoJSON(w, http.StatusOK, map[string]any{
		"modules": mods,
		"total":   total,
		"page":    page,
		"limit":   limit,
	})
}

// handleGetModuleDetail serves a single module's metadata plus its version list.
func (r *GoRegistry) handleGetModuleDetail(w http.ResponseWriter, req *http.Request) {
	if !r.authorizeRequest(w, req) {
		return
	}
	modulePath := strings.Trim(req.PathValue("path"), "/")
	if modulePath == "" || !isValidModulePath(modulePath) {
		writeGoError(w, http.StatusNotFound, "NotFound", "invalid module path")
		return
	}
	if !r.isModuleAllowed(modulePath) {
		writeGoError(w, http.StatusNotFound, "NotFound", "module not found")
		return
	}

	mod, versions, err := r.getOrFetchModule(req.Context(), modulePath)
	if err != nil {
		if errors.Is(err, ErrModuleNotFound) {
			writeGoError(w, http.StatusNotFound, "NotFound", "module not found")
			return
		}
		writeGoError(w, http.StatusBadGateway, "UpstreamError", err.Error())
		return
	}

	type versionView struct {
		Version     string `json:"version"`
		InfoTime    string `json:"info_time,omitempty"`
		PublishedAt string `json:"published_at,omitempty"`
		ZipSize     int64  `json:"zip_size"`
		ZipCached   bool   `json:"zip_cached"`
	}

	items := make([]versionView, 0, len(versions))
	for _, v := range versions {
		item := versionView{
			Version:   v.Version,
			ZipSize:   v.ZipSize,
			ZipCached: v.ZipCached,
		}
		if !v.InfoTime.IsZero() {
			item.InfoTime = v.InfoTime.UTC().Format("2006-01-02T15:04:05Z07:00")
		}
		if !v.PublishedAt.IsZero() {
			item.PublishedAt = v.PublishedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
		}
		items = append(items, item)
	}

	writeGoJSON(w, http.StatusOK, map[string]any{
		"id":               mod.ID,
		"path":             mod.Path,
		"origin":           mod.Origin,
		"description":      mod.Description,
		"latest_version":   mod.LatestVersion,
		"version_count":    len(versions),
		"total_size_bytes": mod.TotalSizeBytes,
		"downloads":        mod.Downloads,
		"updated_at":       mod.UpdatedAt,
		"versions":         items,
	})
}

// ensure the auth package stays referenced when the explorer handlers are compiled out.
var _ = auth.PermissionRead
