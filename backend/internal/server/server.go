package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/amkargar/mirror-registry/backend/internal/admin"
	"github.com/amkargar/mirror-registry/backend/internal/registry"
)

// Server routes incoming HTTP traffic to the Admin API, public directory,
// multi-registry `/r/{slug}/...` endpoints, optional dedicated hostnames,
// and optional root compatibility routes for the default registry.
// It has zero knowledge of any specific package ecosystem.
type Server struct {
	manager               *registry.Manager
	adminHandler          *admin.Handler
	publicBaseURLFunc     func() string
	enableRootCompatRoute func() bool
	allowedOrigins        []string
}

type Options struct {
	Manager               *registry.Manager
	AdminHandler          *admin.Handler
	PublicBaseURL         string
	PublicBaseURLFunc     func() string
	EnableRootCompatRoute func() bool
	AllowedOrigins        []string
}

func New(opts Options) *Server {
	compatFn := opts.EnableRootCompatRoute
	if compatFn == nil {
		compatFn = func() bool { return true }
	}
	baseURLFn := opts.PublicBaseURLFunc
	if baseURLFn == nil {
		staticBase := strings.TrimRight(opts.PublicBaseURL, "/")
		baseURLFn = func() string { return staticBase }
	}
	return &Server{
		manager:               opts.Manager,
		adminHandler:          opts.AdminHandler,
		publicBaseURLFunc:     baseURLFn,
		enableRootCompatRoute: compatFn,
		allowedOrigins:        opts.AllowedOrigins,
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.applyCORS(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	rawPath := r.URL.EscapedPath()

	// Health endpoint
	if rawPath == "/healthz" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":          "ok",
			"installed_types": s.manager.ListTypes(),
		})
		return
	}

	// Admin & public platform APIs
	if strings.HasPrefix(rawPath, "/api/admin/") || strings.HasPrefix(rawPath, "/api/public/") {
		s.adminHandler.ServeHTTP(w, r)
		return
	}

	// Multi-registry prefix routing: /r/{slug}/...
	if strings.HasPrefix(rawPath, "/r/") {
		s.serveBySlugPrefix(w, r, rawPath)
		return
	}

	// Dedicated hostname routing (supports future protocols requiring root-level hostnames such as OCI)
	if rt, ok := s.manager.GetRuntimeByHostname(r.Host); ok {
		s.dispatchToRuntime(w, r, rt, s.resolveOrigin(r), false)
		return
	}

	// Optional backward-compatible root routing across default registry instances (e.g. PUB_HOSTED_URL & FLUTTER_STORAGE_BASE_URL)
	if s.enableRootCompatRoute() {
		for _, rt := range s.manager.GetCompatRuntimes() {
			if matcher, ok := rt.Registry.(registry.CompatPathMatcher); ok && matcher.MatchesCompatPath(r.URL.Path) {
				s.dispatchToRuntime(w, r, rt, s.resolveOrigin(r), false)
				return
			}
		}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": "route not found",
	})
}

func (s *Server) serveBySlugPrefix(w http.ResponseWriter, r *http.Request, escapedPath string) {
	rest := strings.TrimPrefix(escapedPath, "/r/")
	slug := rest
	subEscaped := "/"
	if idx := strings.IndexByte(rest, '/'); idx != -1 {
		slug = rest[:idx]
		subEscaped = rest[idx:]
	}
	if slug == "" {
		writeNotFound(w, "registry slug is required")
		return
	}

	rt, ok := s.manager.GetRuntimeBySlug(slug)
	if !ok {
		writeNotFound(w, "registry not found")
		return
	}

	prefix := "/r/" + slug
	r2 := stripPrefixPreservingEncoding(r, prefix, subEscaped)
	baseURL := s.resolveOrigin(r) + prefix
	s.dispatchToRuntime(w, r2, rt, baseURL, true)
}

func (s *Server) dispatchToRuntime(w http.ResponseWriter, r *http.Request, rt *registry.RuntimeInstance, baseURL string, alreadyCloned bool) {
	if !rt.Instance.Enabled {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "registry instance is disabled",
		})
		return
	}

	ctx := registry.WithBaseURL(r.Context(), strings.TrimRight(baseURL, "/"))
	if alreadyCloned {
		r = r.WithContext(ctx)
	} else {
		r = r.Clone(ctx)
	}
	rt.Registry.Handler().ServeHTTP(w, r)
}

// stripPrefixPreservingEncoding strips /r/{slug} without double-decoding protocol-specific path segments.
func stripPrefixPreservingEncoding(r *http.Request, prefix, subEscaped string) *http.Request {
	r2 := r.Clone(r.Context())
	u := new(url.URL)
	*u = *r.URL

	if subEscaped == "" {
		subEscaped = "/"
	}
	// Compute decoded sub-path safely from Path while retaining RawPath when percent-encoding was present.
	decodedSub := strings.TrimPrefix(r.URL.Path, prefix)
	if decodedSub == "" {
		decodedSub = "/"
	}
	u.Path = decodedSub
	if r.URL.RawPath != "" || subEscaped != decodedSub {
		u.RawPath = subEscaped
	} else {
		u.RawPath = ""
	}
	r2.URL = u
	return r2
}

func (s *Server) resolveOrigin(r *http.Request) string {
	if s.publicBaseURLFunc != nil {
		if base := strings.TrimRight(strings.TrimSpace(s.publicBaseURLFunc()), "/"); base != "" {
			return base
		}
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); proto != "" {
		scheme = strings.Split(proto, ",")[0]
	}
	host := r.Host
	if fwdHost := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); fwdHost != "" {
		host = strings.Split(fwdHost, ",")[0]
	}
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}

func (s *Server) applyCORS(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return
	}
	allowed := false
	for _, o := range s.allowedOrigins {
		if o == "*" || o == origin {
			allowed = true
			break
		}
	}
	if !allowed {
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Credentials", "true")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, X-Requested-With")
	w.Header().Set("Vary", "Origin")
}

func writeNotFound(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}
