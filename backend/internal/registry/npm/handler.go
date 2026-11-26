package npm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/amkargar/depyard/backend/internal/auth"
)

// buildHandler wires the npm Registry HTTP API.
//
//	GET    /-/ping                       npm ping
//	GET    /-/v1/search?text=...         npm search (over packages known locally)
//	GET    /{package}                    packument (full or abbreviated)
//	GET    /{package}/-/{file}.tgz       tarball, mirrored on demand
//	PUT    /{package}                    npm publish
//	DELETE /{package}                    unpublish a package or a single version
//	GET    /api/packages...              explorer API (see explorer.go)
func (r *NpmRegistry) buildHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /-/ping", r.handlePing)
	mux.HandleFunc("HEAD /-/ping", r.handlePing)
	mux.HandleFunc("GET /-/v1/search", r.handleSearch)
	r.registerExplorerRoutes(mux)
	// Scoped names contain an escaped slash (or, from some proxies, a literal
	// one), so a catch-all handler is used instead of a "/{package}" pattern.
	mux.HandleFunc("/", r.handleRoot)
	return mux
}

func (r *NpmRegistry) handleRoot(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodGet, http.MethodHead:
		r.handleRead(w, req)
	case http.MethodPut:
		r.handlePublish(w, req)
	case http.MethodDelete:
		r.handleUnpublish(w, req)
	default:
		writeNpmError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// authorize authenticates the caller and enforces the instance access policy.
func (r *NpmRegistry) authorize(w http.ResponseWriter, req *http.Request, perm auth.Permission, namespace string) (*auth.Principal, bool) {
	principal, err := r.deps.Auth.AuthenticateRequest(req.Context(), req)
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="Depyard npm registry"`)
		writeNpmError(w, http.StatusUnauthorized, "authentication required: "+err.Error())
		return nil, false
	}
	if err := r.deps.Auth.Authorize(principal, auth.AuthRequest{
		Policy:     r.inst.AccessPolicy(),
		Permission: perm,
		Namespace:  namespace,
	}); err != nil {
		if errors.Is(err, auth.ErrUnauthenticated) {
			w.Header().Set("WWW-Authenticate", `Basic realm="Depyard npm registry"`)
			writeNpmError(w, http.StatusUnauthorized, "authentication required for this registry")
		} else {
			writeNpmError(w, http.StatusForbidden, "insufficient permissions for this registry")
		}
		return nil, false
	}
	return principal, true
}

// handleRead serves either a packument or a tarball depending on the path.
func (r *NpmRegistry) handleRead(w http.ResponseWriter, req *http.Request) {
	name, rest, err := ParseRequestPath(req.URL.EscapedPath())
	if err != nil {
		writeNpmError(w, http.StatusNotFound, err.Error())
		return
	}
	if _, ok := r.authorize(w, req, auth.PermissionRead, name); !ok {
		return
	}
	if !r.allowsPackage(name) {
		writeNpmError(w, http.StatusNotFound, "package not found")
		return
	}

	filename, err := tarballFilename(rest)
	if err != nil {
		writeNpmError(w, http.StatusNotFound, err.Error())
		return
	}
	if filename != "" {
		r.serveTarball(w, req, name, filename)
		return
	}
	r.servePackument(w, req, name)
}

// servePackument serves the npm metadata document of one package.
func (r *NpmRegistry) servePackument(w http.ResponseWriter, req *http.Request, name string) {
	pkg, err := r.getPackageForRead(req.Context(), name)
	if err != nil || len(pkg.Versions) == 0 {
		writeNpmError(w, http.StatusNotFound, "package not found")
		return
	}

	contentType := ContentTypeJSON
	if strings.Contains(strings.ToLower(req.Header.Get("Accept")), "vnd.npm.install-v1+json") {
		contentType = ContentTypeAbbreviated
	}
	document := buildPackument(pkg, r.baseURL(req), contentType == ContentTypeAbbreviated)
	writeNpmJSON(w, http.StatusOK, document, contentType)
}

// getPackageForRead loads a package, mirroring its packument from upstream on a
// miss when the instance is allowed to proxy it.
func (r *NpmRegistry) getPackageForRead(ctx context.Context, name string) (*NpmPackage, error) {
	pkg, err := r.store.GetPackage(ctx, r.inst.ID, name)
	if err == nil && len(pkg.Versions) > 0 {
		return pkg, nil
	}
	if !r.canProxy(name) || r.inst.UpstreamURL == "" {
		return nil, ErrPackageNotFound
	}
	if _, syncErr := r.syncPackageFromUpstream(ctx, name); syncErr != nil {
		return nil, syncErr
	}
	return r.store.GetPackage(ctx, r.inst.ID, name)
}

// serveTarball streams a cached tarball, mirroring it from upstream on a miss.
func (r *NpmRegistry) serveTarball(w http.ResponseWriter, req *http.Request, name, filename string) {
	pkg, err := r.getPackageForRead(req.Context(), name)
	if err != nil {
		writeNpmError(w, http.StatusNotFound, "tarball not found")
		return
	}
	var version *NpmVersion
	for i := range pkg.Versions {
		if pkg.Versions[i].Filename == filename {
			version = &pkg.Versions[i]
			break
		}
	}
	if version == nil {
		writeNpmError(w, http.StatusNotFound, "tarball not found")
		return
	}

	key := artifactKey(name, version.Version, filename)
	file, ref, err := r.deps.Storage.OpenArtifact(req.Context(), r.inst.ID, key)
	cacheHit := err == nil
	if err != nil {
		if _, cacheErr := r.cacheUpstreamTarball(req.Context(), name, version.Version); cacheErr != nil {
			writeNpmError(w, http.StatusNotFound, "tarball not found")
			return
		}
		file, ref, err = r.deps.Storage.OpenArtifact(req.Context(), r.inst.ID, key)
		if err != nil {
			writeNpmError(w, http.StatusNotFound, "tarball not found")
			return
		}
	}
	defer file.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(ref.SizeBytes, 10))
	if version.Digest != "" {
		w.Header().Set("X-Checksum-Sha256", version.Digest)
	}
	if cacheHit {
		w.Header().Set("X-Registry-Cache", "HIT")
	} else {
		w.Header().Set("X-Registry-Cache", "MISS")
	}
	w.WriteHeader(http.StatusOK)
	if req.Method == http.MethodHead {
		return
	}

	stop := r.deps.Metrics.BeginTransfer(r.inst.ID)
	defer stop()
	written, _ := io.Copy(w, file)
	r.deps.Metrics.RecordDownload(r.inst.ID, written, cacheHit)
	_ = r.store.IncrementDownloads(req.Context(), r.inst.ID, name)
}

func (r *NpmRegistry) handlePing(w http.ResponseWriter, req *http.Request) {
	if _, ok := r.authorize(w, req, auth.PermissionRead, ""); !ok {
		return
	}
	writeNpmJSON(w, http.StatusOK, map[string]any{"ok": true}, ContentTypeJSON)
}

// handleSearch implements npm's /-/v1/search protocol over locally known
// packages. It never fans out to upstream: search is intentionally local so a
// mirror does not leak arbitrary queries or depend on upstream search APIs.
func (r *NpmRegistry) handleSearch(w http.ResponseWriter, req *http.Request) {
	if _, ok := r.authorize(w, req, auth.PermissionRead, ""); !ok {
		return
	}
	query := req.URL.Query()
	size, _ := strconv.Atoi(query.Get("size"))
	if size <= 0 || size > 250 {
		size = 20
	}
	from, _ := strconv.Atoi(query.Get("from"))
	if from < 0 {
		from = 0
	}
	packages, total, err := r.store.ListPackages(req.Context(), PackageListQuery{
		RegistryID: r.inst.ID,
		Search:     strings.TrimSpace(query.Get("text")),
		Page:       1,
		Limit:      500,
	})
	if err != nil {
		writeNpmError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]map[string]any, 0, size)
	for i := from; i < len(packages) && len(items) < size; i++ {
		p := packages[i]
		scope := ""
		if strings.HasPrefix(p.Name, "@") {
			scope, _, _ = strings.Cut(p.Name, "/")
		}
		items = append(items, map[string]any{
			"package": map[string]any{
				"name":        p.Name,
				"scope":       scope,
				"version":     p.LatestVersion,
				"description": p.Description,
				"keywords":    nonNilStrings(p.Keywords),
				"date":        timestamp(p.UpdatedAt),
				"links":       map[string]string{"npm": tarballBaseLink(r.baseURL(req), p.Name)},
			},
			"score": map[string]any{
				"final": 1, "detail": map[string]any{"quality": 1, "popularity": 0, "maintenance": 1},
			},
			"searchScore": 1,
		})
	}
	writeNpmJSON(w, http.StatusOK, map[string]any{
		"objects": items,
		"total":   total,
		"time":    map[string]string{},
	}, ContentTypeJSON)
}

func tarballBaseLink(baseURL, name string) string {
	return baseURL + "/" + escapePackagePath(name)
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func timestamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// writeNpmJSON writes a JSON response with an explicit content type.
func writeNpmJSON(w http.ResponseWriter, status int, payload any, contentType string) {
	w.Header().Set("Content-Type", contentType+"; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeNpmError(w http.ResponseWriter, status int, message string) {
	writeNpmJSON(w, status, map[string]string{"error": message}, ContentTypeJSON)
}

// recordUpstreamError is a nil-safe wrapper for metrics collected in tests
// where the shared collector is not installed.
func (r *NpmRegistry) recordUpstreamError() {
	if r.deps.Metrics != nil {
		r.deps.Metrics.RecordUpstreamError(r.inst.ID)
	}
}
