package goproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/amkargar/mirror-registry/backend/internal/auth"
	"github.com/amkargar/mirror-registry/backend/internal/registry"
	"github.com/amkargar/mirror-registry/backend/internal/storage"
)

// VersionInfo is the JSON document returned by GET $base/<module>/@v/<version>.info.
type VersionInfo struct {
	Version string    `json:"Version"`
	Time    time.Time `json:"Time"`
}

// buildHandler wires the GOPROXY protocol routes.
//
// Go's http.ServeMux requires a trailing wildcard, which cannot express
// "<module>/@v/<file>" with a variable-length module path. The protocol has
// exactly four request shapes, so the path is parsed here instead.
func (r *GoRegistry) buildHandler() http.Handler {
	explorer := r.buildExplorerHandler()

	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// The explorer endpoints live under /api/ and are handled separately from
		// the GOPROXY protocol paths.
		if strings.HasPrefix(req.URL.Path, "/api/modules") {
			explorer.ServeHTTP(w, req)
			return
		}

		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			writeGoError(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "the GOPROXY protocol only supports GET")
			return
		}

		modulePath, file, ok := splitProxyPath(req.URL.Path)
		if !ok {
			writeGoError(w, http.StatusNotFound, "NotFound", "invalid GOPROXY request path")
			return
		}

		switch file {
		case "list":
			r.handleListVersions(w, req, modulePath)
		default:
			r.handleModuleFile(w, req, modulePath, file)
		}
	})
}

// splitProxyPath parses a GOPROXY request path into its module path and file parts.
//
// It expects the shape "/<module>/@v/<file>". The module path may contain slashes,
// so the "/@v/" separator is located from the right.
func splitProxyPath(rawPath string) (modulePath, file string, ok bool) {
	trimmed := strings.TrimPrefix(rawPath, "/")
	if trimmed == "" {
		return "", "", false
	}

	idx := strings.LastIndex(trimmed, "/@v/")
	if idx <= 0 {
		return "", "", false
	}
	modulePath = trimmed[:idx]
	file = trimmed[idx+len("/@v/"):]

	if modulePath == "" || file == "" || strings.Contains(file, "/") {
		return "", "", false
	}
	if !isValidModulePath(modulePath) {
		return "", "", false
	}
	return modulePath, file, true
}

func (r *GoRegistry) authorizeRequest(w http.ResponseWriter, req *http.Request) bool {
	principal, err := r.deps.Auth.AuthenticateRequest(req.Context(), req)
	if err != nil {
		writeGoError(w, http.StatusUnauthorized, "Unauthorized", err.Error())
		return false
	}
	if err := r.deps.Auth.Authorize(principal, auth.AuthRequest{
		Policy:     r.inst.AccessPolicy(),
		Permission: auth.PermissionRead,
	}); err != nil {
		if errors.Is(err, auth.ErrUnauthenticated) {
			writeGoError(w, http.StatusUnauthorized, "Unauthorized", "authentication required for this Go proxy")
		} else {
			writeGoError(w, http.StatusForbidden, "Forbidden", "insufficient permissions for this Go proxy")
		}
		return false
	}
	return true
}

// handleListVersions serves GET $base/<module>/@v/list.
//
// The response is a plain-text list of versions, one per line, ordered by the go
// command's version ordering rules.
func (r *GoRegistry) handleListVersions(w http.ResponseWriter, req *http.Request, modulePath string) {
	if !r.authorizeRequest(w, req) {
		return
	}
	if !r.isModuleAllowed(modulePath) {
		writeGoError(w, http.StatusNotFound, "NotFound", fmt.Sprintf("module %q not found", modulePath))
		return
	}

	versions, err := r.listVersions(req.Context(), modulePath)
	if err != nil {
		if errors.Is(err, ErrModuleNotFound) {
			writeGoError(w, http.StatusNotFound, "NotFound", fmt.Sprintf("module %q not found", modulePath))
			return
		}
		writeGoError(w, http.StatusBadGateway, "UpstreamError", err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=UTF-8")
	w.WriteHeader(http.StatusOK)
	for _, v := range versions {
		_, _ = io.WriteString(w, v+"\n")
	}
}

// handleModuleFile serves GET $base/<module>/@v/<version>.info|.mod|.zip.
func (r *GoRegistry) handleModuleFile(w http.ResponseWriter, req *http.Request, modulePath, file string) {
	if !r.authorizeRequest(w, req) {
		return
	}
	if !r.isModuleAllowed(modulePath) {
		writeGoError(w, http.StatusNotFound, "NotFound", fmt.Sprintf("module %q not found", modulePath))
		return
	}

	switch {
	case strings.HasSuffix(file, ".info"):
		version := strings.TrimSuffix(file, ".info")
		if !isValidVersion(version) {
			writeGoError(w, http.StatusNotFound, "NotFound", "invalid version")
			return
		}
		r.serveVersionInfo(w, req, modulePath, version)
	case strings.HasSuffix(file, ".mod"):
		version := strings.TrimSuffix(file, ".mod")
		if !isValidVersion(version) {
			writeGoError(w, http.StatusNotFound, "NotFound", "invalid version")
			return
		}
		r.serveGoMod(w, req, modulePath, version)
	case strings.HasSuffix(file, ".zip"):
		version := strings.TrimSuffix(file, ".zip")
		if !isValidVersion(version) {
			writeGoError(w, http.StatusNotFound, "NotFound", "invalid version")
			return
		}
		r.serveZip(w, req, modulePath, version)
	default:
		// The GOPROXY protocol only defines .info, .mod and .zip. Anything else must
		// 404 (never a directory listing).
		writeGoError(w, http.StatusNotFound, "NotFound", fmt.Sprintf("unsupported file %q", file))
	}
}

func (r *GoRegistry) serveVersionInfo(w http.ResponseWriter, req *http.Request, modulePath, version string) {
	ver, err := r.resolveVersion(req.Context(), modulePath, version)
	if err != nil {
		writeUpstreamFailure(w, err, fmt.Sprintf("version %s of module %q not found", version, modulePath))
		return
	}

	info := VersionInfo{Version: ver.Version, Time: ver.InfoTime}
	if info.Time.IsZero() {
		info.Time = ver.PublishedAt
	}
	if info.Time.IsZero() {
		info.Time = time.Now().UTC()
	}

	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(info)
}

func (r *GoRegistry) serveGoMod(w http.ResponseWriter, req *http.Request, modulePath, version string) {
	if _, err := r.resolveVersion(req.Context(), modulePath, version); err != nil {
		writeUpstreamFailure(w, err, fmt.Sprintf("version %s of module %q not found", version, modulePath))
		return
	}

	// Fast path: the go.mod file is already in local storage.
	if ref, rerr := r.deps.Storage.GetArtifactRef(req.Context(), r.inst.ID, modArtifactKey(modulePath, version)); rerr == nil && ref != nil {
		if f, _, oerr := r.deps.Storage.OpenArtifact(req.Context(), r.inst.ID, modArtifactKey(modulePath, version)); oerr == nil {
			defer f.Close()
			w.Header().Set("Content-Type", "text/plain; charset=UTF-8")
			w.WriteHeader(http.StatusOK)
			_, _ = io.Copy(w, f)
			return
		}
	}

	data, err := r.fetchGoMod(req.Context(), modulePath, version)
	if err != nil {
		writeUpstreamFailure(w, err, fmt.Sprintf("go.mod for %s@%s not found", modulePath, version))
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=UTF-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (r *GoRegistry) serveZip(w http.ResponseWriter, req *http.Request, modulePath, version string) {
	endTransfer := r.deps.Metrics.BeginTransfer(r.inst.ID)
	defer endTransfer()

	_, cacheHit, err := r.ensureZipCached(req.Context(), modulePath, version)
	if err != nil {
		switch {
		case errors.Is(err, ErrModuleNotFound), errors.Is(err, ErrVersionNotFound):
			writeGoError(w, http.StatusNotFound, "NotFound", fmt.Sprintf("module %s@%s not found", modulePath, version))
		case errors.Is(err, storage.ErrQuotaExceeded):
			writeGoError(w, http.StatusInsufficientStorage, "QuotaExceeded", "registry storage quota exceeded")
		case errors.Is(err, storage.ErrDigestMismatch):
			writeGoError(w, http.StatusBadGateway, "IntegrityError", "upstream module checksum verification failed")
		default:
			writeGoError(w, http.StatusBadGateway, "UpstreamError", err.Error())
		}
		return
	}

	file, openedRef, err := r.deps.Storage.OpenArtifact(req.Context(), r.inst.ID, zipArtifactKey(modulePath, version))
	if err != nil {
		writeGoError(w, http.StatusNotFound, "NotFound", "module archive unavailable")
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Length", strconv.FormatInt(openedRef.SizeBytes, 10))
	if cacheHit {
		w.Header().Set("X-Registry-Cache", "HIT")
	} else {
		w.Header().Set("X-Registry-Cache", "MISS")
	}
	w.WriteHeader(http.StatusOK)

	written, _ := io.Copy(w, file)
	r.deps.Metrics.RecordDownload(r.inst.ID, written, cacheHit)
	_ = r.store.IncrementModuleDownloads(req.Context(), r.inst.ID, modulePath)
}

// writeUpstreamFailure maps an upstream resolution error onto an HTTP status.
func writeUpstreamFailure(w http.ResponseWriter, err error, notFoundMsg string) {
	switch {
	case errors.Is(err, ErrModuleNotFound), errors.Is(err, ErrVersionNotFound):
		writeGoError(w, http.StatusNotFound, "NotFound", notFoundMsg)
	case errors.Is(err, storage.ErrQuotaExceeded):
		writeGoError(w, http.StatusInsufficientStorage, "QuotaExceeded", "registry storage quota exceeded")
	case errors.Is(err, storage.ErrDigestMismatch):
		writeGoError(w, http.StatusBadGateway, "IntegrityError", "upstream module checksum verification failed")
	default:
		writeGoError(w, http.StatusBadGateway, "UpstreamError", err.Error())
	}
}

func writeGoJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeGoError(w http.ResponseWriter, status int, code, message string) {
	writeGoJSON(w, status, map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}

// artifactKey helpers keep the shared blob store keys namespaced per artifact kind.
func modArtifactKey(modulePath, version string) string {
	return "mod:" + modulePath + "@" + version
}

func zipArtifactKey(modulePath, version string) string {
	return "zip:" + modulePath + "@" + version
}

// resolveVersion makes sure version metadata for a module is known locally,
// fetching it from upstream when necessary.
func (r *GoRegistry) resolveVersion(ctx context.Context, modulePath, version string) (*GoVersion, error) {
	ver, err := r.store.GetVersion(ctx, r.inst.ID, modulePath, version)
	if err == nil {
		return ver, nil
	}
	if !errors.Is(err, ErrVersionNotFound) {
		return nil, err
	}
	if r.inst.Mode == registry.ModeHosted {
		return nil, ErrVersionNotFound
	}
	if _, _, ferr := r.getOrFetchModule(ctx, modulePath); ferr != nil {
		return nil, ferr
	}
	return r.store.GetVersion(ctx, r.inst.ID, modulePath, version)
}
