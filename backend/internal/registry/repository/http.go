package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"unicode"

	"github.com/amkargar/depyard/backend/internal/auth"
	"github.com/amkargar/depyard/backend/internal/registry"
	"github.com/amkargar/depyard/backend/internal/storage"
)

type Status int

func (s Status) Error() string { return http.StatusText(int(s)) }
func UpstreamStatus(code int) error {
	switch code {
	case 404, 410:
		return storage.ErrRefNotFound
	case 429:
		return Status(code)
	default:
		return Status(http.StatusBadGateway)
	}
}

func Error(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	var status Status
	switch {
	case errors.As(err, &status):
		code = int(status)
	case errors.Is(err, storage.ErrRefNotFound), errors.Is(err, storage.ErrBlobNotFound):
		code = 404
	case errors.Is(err, storage.ErrQuotaExceeded):
		code = 507
	case errors.Is(err, storage.ErrSizeExceeded):
		code = 413
	case errors.Is(err, storage.ErrArtifactConflict):
		code = http.StatusConflict
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, storage.ErrSizeMismatch), errors.Is(err, storage.ErrDigestMismatch), errors.Is(err, storage.ErrInvalidDigest):
		code = 400
	}
	http.Error(w, http.StatusText(code), code)
}

func JSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func Base(req *http.Request) string { return registry.BaseURLFromContext(req.Context()) }
func IsRead(req *http.Request) bool {
	return req.Method == http.MethodGet || req.Method == http.MethodHead
}

// Reject rather than clean: the name authorized, the cache key and the upstream
// resource must all identify the same object. The path has already been decoded.
func ValidPath(p string) bool {
	if !strings.HasPrefix(p, "/") || len(p) > 4096 || strings.ContainsAny(p, "\\%?#") || strings.Contains(p, "//") || strings.IndexFunc(p, unicode.IsControl) >= 0 {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

func (r *Repository) serveHTTP(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	if !ValidPath(req.URL.Path) || strings.EqualFold(req.Header.Get("Service-Worker"), "script") {
		Error(w, Status(400))
		return
	}
	if req.Method != http.MethodGet || r.deps.Metrics == nil {
		r.handler.ServeHTTP(w, req)
		return
	}
	defer r.deps.Metrics.BeginTransfer(r.Instance.ID)()
	transfer := &countResponse{ResponseWriter: w, status: 200}
	r.handler.ServeHTTP(transfer, req)
	if transfer.status == 200 || transfer.status == 206 {
		r.deps.Metrics.RecordServed(r.Instance.ID, transfer.n)
	}
}

type countResponse struct {
	http.ResponseWriter
	status int
	n      int64
}

func (w *countResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *countResponse) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *countResponse) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	w.n += int64(n)
	return n, err
}

func (r *Repository) Authorize(w http.ResponseWriter, req *http.Request, perm auth.Permission, namespace string) bool {
	if perm == auth.PermissionPublish && !r.CanHost() {
		Error(w, Status(405))
		return false
	}
	principal, err := r.deps.Auth.AuthenticateRequest(req.Context(), req)
	if err != nil {
		err = auth.ErrUnauthenticated
	} else {
		err = r.deps.Auth.Authorize(principal, auth.AuthRequest{Policy: r.Instance.AccessPolicy(), Permission: perm, Namespace: namespace})
	}
	if err == nil {
		return true
	}
	if errors.Is(err, auth.ErrUnauthenticated) {
		w.Header().Set("WWW-Authenticate", `Basic realm="Depyard"`)
		Error(w, Status(401))
	} else {
		Error(w, Status(403))
	}
	return false
}

func (r *Repository) ReadOK(w http.ResponseWriter, req *http.Request, ns string) bool {
	if !IsRead(req) {
		Error(w, Status(405))
		return false
	}
	return r.Authorize(w, req, auth.PermissionRead, ns)
}

func (r *Repository) WriteOK(w http.ResponseWriter, req *http.Request, ns string) bool {
	return r.Authorize(w, req, auth.PermissionPublish, ns)
}

// Serve implements conditional and ranged reads (including HEAD) from seekable
// CAS blobs. Only selected representation headers survive a round trip to disk.
func (r *Repository) Serve(w http.ResponseWriter, req *http.Request, o Object) {
	f, ref, err := r.Open(req.Context(), o)
	if err != nil {
		Error(w, err)
		return
	}
	defer f.Close()
	for k, v := range ref.Headers {
		w.Header().Set(k, v)
	}
	if w.Header().Get("ETag") == "" {
		w.Header().Set("ETag", `"`+ref.Digest+`"`)
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	http.ServeContent(w, req, path.Base(o.Path), ref.UpdatedAt, f)
}

func (r *Repository) Files(w http.ResponseWriter, req *http.Request) {
	p := req.URL.Path
	if strings.HasSuffix(p, "/") {
		Error(w, Status(404))
		return
	}
	r.File(w, req, Object{Path: p, Immutable: archive(p)}, strings.TrimPrefix(p, "/"))
}

func archive(p string) bool {
	for _, ext := range []string{".deb", ".rpm", ".apk", ".conda", ".crate", ".nupkg", ".gem", ".jar", ".aar", ".whl"} {
		if strings.HasSuffix(p, ext) {
			return true
		}
	}
	return false
}

// File exposes one protocol object as GET/HEAD or authenticated hosted PUT.
func (r *Repository) File(w http.ResponseWriter, req *http.Request, o Object, ns string) {
	switch {
	case IsRead(req):
		if r.ReadOK(w, req, ns) {
			r.Serve(w, req, o)
		}
	case req.Method == http.MethodPut:
		if !r.WriteOK(w, req, ns) {
			return
		}
		headers := map[string]string{"Content-Type": req.Header.Get("Content-Type")}
		_, err := r.Put(req.Context(), o, req.Body, headers)
		if err != nil {
			Error(w, err)
			return
		}
		w.WriteHeader(http.StatusCreated)
	default:
		Error(w, Status(405))
	}
}

// Bytes reads bounded metadata. Large package bodies must use Open/Put instead.
func (r *Repository) Bytes(ctx context.Context, o Object, max int64) ([]byte, error) {
	f, _, err := r.Open(ctx, o)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Limited(f, max)
}

func Limited(src io.Reader, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(src, max+1))
	if int64(len(b)) > max {
		return nil, storage.ErrSizeExceeded
	}
	return b, err
}

func Decode(req *http.Request, out any) error {
	b, err := Limited(req.Body, 4<<20)
	if err != nil {
		return err
	}
	if json.Unmarshal(b, out) != nil {
		return Status(400)
	}
	return nil
}

func Digest(p string) (string, error) {
	v, ok := strings.CutPrefix(p, "sha256:")
	if !ok || !storage.IsValidSHA256(v) || strings.ToLower(v) != v {
		return "", fmt.Errorf("%w: expected sha256", storage.ErrInvalidDigest)
	}
	return v, nil
}
