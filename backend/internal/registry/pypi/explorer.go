package pypi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/amkargar/mirror-registry/backend/internal/auth"
)

// explorerProject is the JSON view of a project used by the PyPI explorer UI.
type explorerProject struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	DisplayName    string            `json:"display_name,omitempty"`
	Origin         string            `json:"origin"`
	Summary        string            `json:"summary,omitempty"`
	Description    string            `json:"description,omitempty"`
	Homepage       string            `json:"homepage,omitempty"`
	Author         string            `json:"author,omitempty"`
	AuthorEmail    string            `json:"author_email,omitempty"`
	License        string            `json:"license,omitempty"`
	RequiresPython string            `json:"requires_python,omitempty"`
	Classifiers    []string          `json:"classifiers,omitempty"`
	ProjectURLs    map[string]string `json:"project_urls,omitempty"`
	LatestVersion  string            `json:"latest_version,omitempty"`
	ReleaseCount   int               `json:"release_count"`
	FileCount      int               `json:"file_count"`
	TotalSize      int64             `json:"total_size_bytes"`
	Downloads      int64             `json:"downloads"`
	UpdatedAt      string            `json:"updated_at,omitempty"`
	CreatedAt      string            `json:"created_at,omitempty"`
}

// explorerFile is the JSON view of a distribution file.
type explorerFile struct {
	Filename       string `json:"filename"`
	Version        string `json:"version"`
	Packagetype    string `json:"packagetype"`
	PythonVersion  string `json:"python_version,omitempty"`
	RequiresPython string `json:"requires_python,omitempty"`
	Digest         string `json:"digest,omitempty"`
	SizeBytes      int64  `json:"size_bytes"`
	Yanked         bool   `json:"yanked"`
	YankedReason   string `json:"yanked_reason,omitempty"`
	Origin         string `json:"origin"`
	UploadTime     string `json:"upload_time,omitempty"`
	HasMetadata    bool   `json:"has_metadata"`
	URL            string `json:"url"`
}

// explorerProjectDetail is the JSON view of a single project.
type explorerProjectDetail struct {
	Project     explorerProject   `json:"project"`
	Releases    []explorerFile    `json:"releases"`
	Versions    []string          `json:"versions"`
	InstallURL  string            `json:"install_url"`
	SimpleURL   string            `json:"simple_url"`
	RequiresPy  string            `json:"requires_python,omitempty"`
	SummaryText string            `json:"summary,omitempty"`
	ProjectURLs map[string]string `json:"project_urls,omitempty"`
}

// registerExplorerRoutes mounts the explorer API used by the PyPI explorer UI.
func (r *PyPIRegistry) registerExplorerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/projects", r.handleListProjects)
	mux.HandleFunc("GET /api/projects/{project}", r.handleGetProjectDetail)
}

// handleListProjects serves the project listing for the explorer UI.
func (r *PyPIRegistry) handleListProjects(w http.ResponseWriter, req *http.Request) {
	if _, ok := r.authorizeRequest(w, req, auth.PermissionRead, ""); !ok {
		return
	}
	page, _ := strconv.Atoi(req.URL.Query().Get("page"))
	limit, _ := strconv.Atoi(req.URL.Query().Get("limit"))
	if page <= 0 {
		page = 1
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	projects, total, err := r.store.ListProjects(req.Context(), ProjectListQuery{
		RegistryID: r.inst.ID,
		Search:     strings.TrimSpace(req.URL.Query().Get("q")),
		Origin:     strings.TrimSpace(req.URL.Query().Get("origin")),
		Page:       page,
		Limit:      limit,
	})
	if err != nil {
		writeExplorerError(w, http.StatusInternalServerError, err.Error())
		return
	}

	items := make([]explorerProject, 0, len(projects))
	for _, p := range projects {
		items = append(items, toExplorerProject(p))
	}
	writeExplorerJSON(w, http.StatusOK, map[string]any{
		"projects": items,
		"total":    total,
		"page":     page,
		"limit":    limit,
	})
}

// handleGetProjectDetail serves the detail view of a single project.
func (r *PyPIRegistry) handleGetProjectDetail(w http.ResponseWriter, req *http.Request) {
	project := strings.TrimSpace(req.PathValue("project"))
	if project == "" {
		writeExplorerError(w, http.StatusBadRequest, "project name is required")
		return
	}
	if _, ok := r.authorizeRequest(w, req, auth.PermissionRead, NormalizeProjectName(project)); !ok {
		return
	}

	normalized := NormalizeProjectName(project)
	proj, err := r.store.GetProject(req.Context(), r.inst.ID, normalized)
	if err != nil {
		writeExplorerError(w, http.StatusNotFound, "project not found")
		return
	}

	files, versions, err := r.projectFiles(req.Context(), normalized)
	if err != nil {
		writeExplorerError(w, http.StatusInternalServerError, err.Error())
		return
	}

	baseURL := r.baseURL(req)
	releases := make([]explorerFile, 0, len(files))
	for _, f := range files {
		releases = append(releases, explorerFile{
			Filename:       f.Filename,
			Version:        f.Version,
			Packagetype:    f.Packagetype,
			PythonVersion:  f.PythonVersion,
			RequiresPython: f.RequiresPython,
			Digest:         f.Digest,
			SizeBytes:      f.SizeBytes,
			Yanked:         f.Yanked,
			YankedReason:   f.YankedReason,
			Origin:         f.Origin,
			UploadTime:     formatTime(f.UploadTime),
			HasMetadata:    f.HasMetadata,
			URL:            fileDownloadURL(baseURL, f),
		})
	}

	writeExplorerJSON(w, http.StatusOK, explorerProjectDetail{
		Project:     toExplorerProject(proj),
		Releases:    releases,
		Versions:    versions,
		InstallURL:  baseURL + "/simple/",
		SimpleURL:   baseURL + "/simple/" + normalized + "/",
		RequiresPy:  proj.RequiresPython,
		SummaryText: proj.Summary,
		ProjectURLs: proj.ProjectURLs,
	})
}

// toExplorerProject converts a stored project into its explorer view.
func toExplorerProject(p *PyProject) explorerProject {
	return explorerProject{
		ID:             p.ID,
		Name:           p.Name,
		DisplayName:    p.DisplayName,
		Origin:         p.Origin,
		Summary:        p.Summary,
		Description:    p.Description,
		Homepage:       p.Homepage,
		Author:         p.Author,
		AuthorEmail:    p.AuthorEmail,
		License:        p.License,
		RequiresPython: p.RequiresPython,
		Classifiers:    p.Classifiers,
		ProjectURLs:    p.ProjectURLs,
		LatestVersion:  p.LatestVersion,
		ReleaseCount:   p.ReleaseCount,
		FileCount:      p.FileCount,
		TotalSize:      p.TotalSize,
		Downloads:      p.Downloads,
		UpdatedAt:      formatTime(p.UpdatedAt),
		CreatedAt:      formatTime(p.CreatedAt),
	}
}

// formatTime renders a timestamp as RFC 3339, or an empty string when unset.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// writeExplorerJSON writes a JSON response for the explorer API.
func writeExplorerJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// writeExplorerError writes a JSON error response for the explorer API.
func writeExplorerError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": message})
}
