package pypi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// buildHandler wires the PyPI HTTP routes.
//
// The route layout mirrors the PyPI protocol:
//
//	GET  /simple/                       the project index
//	GET  /simple/{project}/             one project's files
//	GET  /packages/{project}/{version}/{filename}          a distribution file
//	GET  /packages/{project}/{version}/{filename}.metadata the PEP 658 core metadata
//	POST /legacy/                       the Twine-compatible upload endpoint
//	GET  /pypi/{project}/json           the Warehouse-style JSON API
func (r *PyPIRegistry) buildHandler() http.Handler {
	mux := http.NewServeMux()

	// The PEP 503 index lives at "/simple/" and the project pages at
	// "/simple/{project}/". Both live under the "/simple/" subtree, so a single
	// handler dispatches on the remaining path.
	mux.HandleFunc("GET /simple/", r.handleSimpleRoot)
	mux.HandleFunc("GET /simple", r.handleSimpleIndex)
	mux.HandleFunc("GET /packages/", r.handlePackageFile)
	mux.HandleFunc("GET /pypi/", r.handleWarehouseJSON)
	mux.HandleFunc("POST /legacy/", r.handleLegacyUpload)
	mux.HandleFunc("POST /legacy", r.handleLegacyUpload)

	// Explorer API used by the PyPI explorer UI.
	r.registerExplorerRoutes(mux)

	return mux
}

// handleSimpleRoot dispatches between the project index and a project page.
func (r *PyPIRegistry) handleSimpleRoot(w http.ResponseWriter, req *http.Request) {
	rest := strings.TrimPrefix(req.URL.Path, "/simple/")
	if rest == "" {
		r.handleSimpleIndex(w, req)
		return
	}
	r.handleSimpleProject(w, req)
}

// handleSimpleIndex serves the PEP 503/691 project index.
func (r *PyPIRegistry) handleSimpleIndex(w http.ResponseWriter, req *http.Request) {
	format, contentType, ok := negotiate(req.Header.Get("Accept"))
	if !ok {
		writeProblem(w, http.StatusNotAcceptable,
			"Not Acceptable",
			"No supported representation is available for the requested Accept header.")
		return
	}

	projects, err := r.projectNames(req.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Internal Server Error", err.Error())
		return
	}

	if format == formatJSON {
		entries := make([]simpleProject, 0, len(projects))
		for _, p := range projects {
			entries = append(entries, simpleProject{Name: p})
		}
		renderJSON(w, http.StatusOK, simpleIndex{
			Meta:     simpleMeta{APIVersion: APIVersion},
			Projects: entries,
		})
		return
	}
	renderHTML(w, http.StatusOK, contentType, htmlIndexPage(projects))
}

// handleSimpleProject serves the PEP 503/691 project detail page.
func (r *PyPIRegistry) handleSimpleProject(w http.ResponseWriter, req *http.Request) {
	format, contentType, ok := negotiate(req.Header.Get("Accept"))
	if !ok {
		writeProblem(w, http.StatusNotAcceptable,
			"Not Acceptable",
			"No supported representation is available for the requested Accept header.")
		return
	}

	// "/simple/{project}/" — the trailing slash is mandatory in PEP 503.
	raw := strings.TrimPrefix(req.URL.Path, "/simple/")
	raw = strings.TrimSuffix(raw, "/")
	if raw == "" || strings.Contains(raw, "/") {
		writeProblem(w, http.StatusNotFound, "Not Found", "Unknown project.")
		return
	}
	project := NormalizeProjectName(raw)
	if !r.allowsProject(project) {
		writeProblem(w, http.StatusNotFound, "Not Found", "Unknown project.")
		return
	}

	files, versions, err := r.projectFiles(req.Context(), project)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Internal Server Error", err.Error())
		return
	}

	// In proxy mode an unknown project is fetched from upstream on demand.
	if len(files) == 0 && r.canProxy(project) {
		if _, syncErr := r.syncProjectFromUpstream(req.Context(), project); syncErr == nil {
			files, versions, err = r.projectFiles(req.Context(), project)
			if err != nil {
				writeProblem(w, http.StatusInternalServerError, "Internal Server Error", err.Error())
				return
			}
		}
	}

	if len(files) == 0 {
		writeProblem(w, http.StatusNotFound, "Not Found", "Unknown project.")
		return
	}

	baseURL := r.baseURL(req)

	if format == formatJSON {
		detail := simpleDetail{
			Meta:     simpleMeta{APIVersion: APIVersion},
			Name:     project,
			Files:    buildSimpleFiles(files, baseURL, true),
			Versions: versions,
		}
		renderJSON(w, http.StatusOK, detail)
		return
	}

	entries := make([]htmlFileEntry, 0, len(files))
	for _, f := range files {
		entry := htmlFileEntry{
			URL:            fileDownloadURL(baseURL, f),
			Text:           f.Filename,
			RequiresPython: f.RequiresPython,
			Yanked:         f.Yanked,
			YankedReason:   f.YankedReason,
		}
		if f.HasMetadata && f.MetadataDigest != "" {
			entry.Hashes = map[string]string{"sha256": f.MetadataDigest}
		}
		entries = append(entries, entry)
	}
	renderHTML(w, http.StatusOK, contentType, htmlDetailPage(project, entries))
}

// handlePackageFile serves a distribution file or its PEP 658 core metadata.
func (r *PyPIRegistry) handlePackageFile(w http.ResponseWriter, req *http.Request) {
	raw := strings.TrimPrefix(req.URL.Path, "/packages/")
	parts := strings.Split(raw, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		writeProblem(w, http.StatusNotFound, "Not Found", "Unknown file.")
		return
	}

	project := NormalizeProjectName(parts[0])
	version := parts[1]
	filename := parts[2]

	wantsMetadata := strings.HasSuffix(filename, ".metadata")
	if wantsMetadata {
		filename = strings.TrimSuffix(filename, ".metadata")
		if !r.cfg.ServeCoreMetadata {
			writeProblem(w, http.StatusNotFound, "Not Found", "Core metadata is not enabled for this registry.")
			return
		}
	}

	file, err := r.store.GetFile(req.Context(), r.inst.ID, project, version, filename)
	if err != nil {
		if errors.Is(err, ErrFileNotFound) && r.canProxy(project) {
			// The file may not be mirrored yet; try to fetch and cache it.
			if cached, cErr := r.cacheUpstreamFile(req.Context(), project, version, filename); cErr == nil {
				file = cached
			} else {
				_ = cErr
				writeProblem(w, http.StatusNotFound, "Not Found", "Unknown file.")
				return
			}
		} else {
			writeProblem(w, http.StatusNotFound, "Not Found", "Unknown file.")
			return
		}
	}

	if wantsMetadata {
		r.serveMetadata(w, req, file)
		return
	}
	r.serveDistribution(w, req, file)
}

// serveDistribution streams a distribution file from the blob store.
//
// A file that is known in the metadata store but whose bytes have not been
// downloaded yet (the usual state of a freshly mirrored project) is fetched from
// upstream on demand and cached before it is streamed.
func (r *PyPIRegistry) serveDistribution(w http.ResponseWriter, req *http.Request, file *PyFile) {
	key := artifactKey(file.ProjectName, file.Version, file.Filename)
	f, ref, err := r.deps.Storage.OpenArtifact(req.Context(), r.inst.ID, key)
	if err != nil {
		if !r.canProxy(file.ProjectName) {
			writeProblem(w, http.StatusNotFound, "Not Found", "Unknown file.")
			return
		}
		if _, cErr := r.cacheUpstreamFile(req.Context(), file.ProjectName, file.Version, file.Filename); cErr != nil {
			writeProblem(w, http.StatusNotFound, "Not Found", "Unknown file.")
			return
		}
		f, ref, err = r.deps.Storage.OpenArtifact(req.Context(), r.inst.ID, key)
		if err != nil {
			writeProblem(w, http.StatusNotFound, "Not Found", "Unknown file.")
			return
		}
	}
	defer f.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", ref.SizeBytes))
	w.Header().Set("X-Checksum-Sha256", file.Digest)
	w.WriteHeader(http.StatusOK)

	if req.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, f)

	_ = r.store.IncrementProjectDownloads(req.Context(), r.inst.ID, file.ProjectName)
}

// serveMetadata streams the PEP 658 core metadata sidecar of a distribution file.
func (r *PyPIRegistry) serveMetadata(w http.ResponseWriter, req *http.Request, file *PyFile) {
	f, _, err := r.deps.Storage.OpenArtifact(req.Context(), r.inst.ID, metadataArtifactKey(file.ProjectName, file.Version, file.Filename))
	if err != nil {
		writeProblem(w, http.StatusNotFound, "Not Found", "Core metadata is not available for this file.")
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if file.MetadataDigest != "" {
		w.Header().Set("X-Checksum-Sha256", file.MetadataDigest)
	}
	w.WriteHeader(http.StatusOK)
	if req.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, f)
}

// handleWarehouseJSON serves the classic Warehouse-style JSON API for a project.
func (r *PyPIRegistry) handleWarehouseJSON(w http.ResponseWriter, req *http.Request) {
	raw := strings.TrimPrefix(req.URL.Path, "/pypi/")
	raw = strings.TrimSuffix(raw, "/")
	project := raw
	if strings.HasSuffix(raw, "/json") {
		project = strings.TrimSuffix(raw, "/json")
	}
	project = NormalizeProjectName(project)
	if project == "" || !r.allowsProject(project) {
		writeProblem(w, http.StatusNotFound, "Not Found", "Unknown project.")
		return
	}

	files, _, err := r.projectFiles(req.Context(), project)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Internal Server Error", err.Error())
		return
	}
	if len(files) == 0 {
		writeProblem(w, http.StatusNotFound, "Not Found", "Unknown project.")
		return
	}

	proj, _ := r.store.GetProject(req.Context(), r.inst.ID, project)
	baseURL := r.baseURL(req)

	type warehouseFile struct {
		Filename       string            `json:"filename"`
		URL            string            `json:"url"`
		Hashes         map[string]string `json:"hashes"`
		Size           int64             `json:"size,omitempty"`
		RequiresPython string            `json:"requires_python,omitempty"`
		UploadTime     string            `json:"upload_time_iso_8601,omitempty"`
		Yanked         bool              `json:"yanked"`
		Packagetype    string            `json:"packagetype,omitempty"`
		PythonVersion  string            `json:"python_version,omitempty"`
	}

	entries := make([]warehouseFile, 0, len(files))
	for _, f := range files {
		hashes := map[string]string{}
		if f.Digest != "" {
			hashes["sha256"] = f.Digest
		}
		e := warehouseFile{
			Filename:       f.Filename,
			URL:            fileDownloadURL(baseURL, f),
			Hashes:         hashes,
			Size:           f.SizeBytes,
			RequiresPython: f.RequiresPython,
			Yanked:         f.Yanked,
			Packagetype:    f.Packagetype,
			PythonVersion:  f.PythonVersion,
		}
		if !f.UploadTime.IsZero() {
			e.UploadTime = f.UploadTime.UTC().Format("2006-01-02T15:04:05.000000Z")
		}
		entries = append(entries, e)
	}

	payload := map[string]any{
		"info": map[string]any{
			"name":            project,
			"summary":         proj.Summary,
			"version":         proj.LatestVersion,
			"home_page":       proj.Homepage,
			"author":          proj.Author,
			"author_email":    proj.AuthorEmail,
			"license":         proj.License,
			"requires_python": proj.RequiresPython,
			"classifiers":     proj.Classifiers,
			"project_urls":    proj.ProjectURLs,
			"yanked":          false,
		},
		"last_serial":     0,
		"releases":        map[string][]warehouseFile{},
		"urls":            entries,
		"vulnerabilities": []any{},
	}

	releases := map[string][]warehouseFile{}
	for _, e := range entries {
		version := versionForFilename(e.Filename)
		releases[version] = append(releases[version], e)
	}
	payload["releases"] = releases

	renderJSON(w, http.StatusOK, payload)
}

// versionForFilename recovers the version of a distribution file from its name.
func versionForFilename(filename string) string {
	// Wheel: {name}-{version}(-{build})?-{py}-{abi}-{platform}.whl
	// sdist: {name}-{version}.tar.gz
	base := filename
	for _, ext := range []string{".whl", ".tar.gz", ".tgz", ".zip", ".tar.bz2", ".tar.xz"} {
		if strings.HasSuffix(strings.ToLower(base), ext) {
			base = base[:len(base)-len(ext)]
			break
		}
	}
	parts := strings.Split(base, "-")
	if len(parts) >= 2 {
		return parts[1]
	}
	return base
}

// projectNames returns the sorted list of project names known to this instance.
func (r *PyPIRegistry) projectNames(ctx context.Context) ([]string, error) {
	projects, _, err := r.store.ListProjects(ctx, ProjectListQuery{
		RegistryID: r.inst.ID,
		Limit:      500,
	})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(projects))
	for _, p := range projects {
		names = append(names, p.Name)
	}
	sort.Strings(names)
	return names, nil
}

// projectFiles returns the distribution files and versions of a project.
func (r *PyPIRegistry) projectFiles(ctx context.Context, project string) ([]*PyFile, []string, error) {
	files, _, err := r.store.ListFiles(ctx, FileListQuery{
		RegistryID:  r.inst.ID,
		ProjectName: project,
		IncludePre:  true,
		Limit:       1000,
	})
	if err != nil {
		return nil, nil, err
	}
	// Sort by PEP 440 version ordering, newest last, so the index lists the
	// newest release first for clients that read the file order.
	sort.SliceStable(files, func(i, j int) bool {
		if c := CompareVersions(files[i].Version, files[j].Version); c != 0 {
			return c > 0
		}
		return files[i].Filename < files[j].Filename
	})

	versions := make([]string, 0, len(files))
	seen := make(map[string]bool)
	for _, f := range files {
		if seen[f.Version] {
			continue
		}
		seen[f.Version] = true
		versions = append(versions, f.Version)
	}
	return files, versions, nil
}
