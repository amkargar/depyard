// Package conan serves the legacy Conan 1 REST API and the revision-aware
// protocol shared by Conan 1.x and 2. Metadata is derived from persisted file
// references, not an in-memory catalogue that disappears on restart.
package conan

import (
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/amkargar/depyard/backend/internal/registry"
	repo "github.com/amkargar/depyard/backend/internal/registry/repository"
)

func Registration() registry.Registration {
	return (repo.Format{
		Type:            "conan",
		Name:            "Conan (1.x and 2)",
		Description:     "Conan remotes: legacy download/upload URLs and revisioned recipe and binary package files.",
		Upstream:        "https://center2.conan.io",
		ResponseHeaders: []string{"X-Checksum-Md5", "X-Checksum-Sha1", "X-Checksum-Sha256"},
		Routes:          func(h *repo.Repository) http.Handler { return &handler{h} },
	}).Registration()
}

type handler struct{ *repo.Repository }

var segment = regexp.MustCompile(`^[a-zA-Z0-9_+.-]+$`)

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 || (parts[0] != "v1" && parts[0] != "v2") {
		repo.Error(w, repo.Status(404))
		return
	}
	route := strings.Join(parts[1:], "/")
	switch route {
	case "ping":
		if !h.ReadOK(w, r, "") {
			return
		}
		w.Header().Set("X-Conan-Server-Capabilities", "revisions")
		w.WriteHeader(200)
		return
	case "users/authenticate":
		_, pass, ok := r.BasicAuth()
		if !ok || pass == "" {
			repo.Error(w, repo.Status(401))
			return
		}
		if !h.ReadOK(w, r, "") {
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(pass))
		return
	case "users/check_credentials":
		if r.Header.Get("Authorization") == "" {
			repo.Error(w, repo.Status(401))
			return
		}
		if h.ReadOK(w, r, "") {
			w.WriteHeader(200)
		}
		return
	case "conans/search":
		h.search(w, r, parts[0])
		return
	}
	if len(parts) < 6 || parts[1] != "conans" {
		repo.Error(w, repo.Status(404))
		return
	}
	for _, s := range parts[2:6] {
		if !segment.MatchString(s) {
			repo.Error(w, repo.Status(404))
			return
		}
	}
	ns := parts[2]
	if parts[0] == "v1" {
		h.legacy(w, r, parts, ns)
		return
	}
	tail := parts[6:]
	p := r.URL.Path
	// Only actual files can be written, never the computed revision/snapshot endpoints.
	for _, i := range []int{2, 6} {
		if len(tail) > i+1 && tail[0] == "revisions" && tail[i] == "files" && (i == 2 || (tail[2] == "packages" && tail[4] == "revisions")) {
			h.File(w, r, repo.Object{Path: p, Immutable: true}, ns)
			return
		}
	}
	if !h.ReadOK(w, r, ns) {
		return
	}
	switch path.Base(p) {
	case "latest", "revisions":
		prefix := path.Dir(p) + "/revisions/"
		refs, err := h.HostedRefs(r.Context(), prefix)
		if err != nil {
			repo.Error(w, err)
			return
		}
		times := map[string]time.Time{}
		for _, ref := range refs {
			rev, rest, _ := strings.Cut(strings.TrimPrefix(repo.HostedPath(ref), prefix), "/")
			if strings.HasPrefix(rest, "files/") && ref.CreatedAt.After(times[rev]) {
				times[rev] = ref.CreatedAt
			}
		}
		if len(times) > 0 {
			type revInfo struct {
				Revision string    `json:"revision"`
				Time     time.Time `json:"time"`
			}
			var revs []revInfo
			for rev, t := range times {
				revs = append(revs, revInfo{rev, t})
			}
			sort.Slice(revs, func(i, j int) bool { return revs[i].Time.After(revs[j].Time) })
			if path.Base(p) == "latest" {
				repo.JSON(w, 200, revs[0])
			} else {
				repo.JSON(w, 200, map[string]any{"revisions": revs})
			}
			return
		}
	case "files":
		refs, err := h.HostedRefs(r.Context(), p+"/")
		if err != nil {
			repo.Error(w, err)
			return
		}
		if len(refs) > 0 {
			files := map[string]any{}
			for _, ref := range refs {
				files[strings.TrimPrefix(repo.HostedPath(ref), p+"/")] = struct{}{}
			}
			repo.JSON(w, 200, map[string]any{"files": files})
			return
		}
	case "search":
		if h.hostedPackages(w, r, path.Dir(p)+"/") {
			return
		}
	default:
		repo.Error(w, repo.Status(404))
		return
	}
	h.Serve(w, r, repo.Object{Path: p, Query: r.URL.Query().Encode()})
}

func (h *handler) search(w http.ResponseWriter, r *http.Request, version string) {
	if !h.ReadOK(w, r, "") {
		return
	}
	refs, err := h.HostedRefs(r.Context(), "/"+version+"/conans/")
	if err != nil {
		repo.Error(w, err)
		return
	}
	pattern := r.URL.Query().Get("q")
	names := map[string]bool{}
	for _, ref := range refs {
		p := strings.Split(strings.Trim(repo.HostedPath(ref), "/"), "/")
		if len(p) < 6 {
			continue
		}
		name := p[2] + "/" + p[3]
		if p[4] != "_" || p[5] != "_" {
			name += "@" + p[4] + "/" + p[5]
		}
		match, _ := path.Match(strings.ReplaceAll(pattern, "/", ":"), strings.ReplaceAll(name, "/", ":"))
		if pattern == "" || match {
			names[name] = true
		}
	}
	if len(names) == 0 && h.CanProxy() {
		h.Serve(w, r, repo.Object{Path: r.URL.Path, Query: r.URL.Query().Encode()})
		return
	}
	out := make([]string, 0, len(names))
	for n := range names {
		out = append(out, n)
	}
	sort.Strings(out)
	repo.JSON(w, 200, map[string]any{"results": out})
}

func (h *handler) hostedPackages(w http.ResponseWriter, r *http.Request, prefix string) bool {
	refs, err := h.HostedRefs(r.Context(), prefix)
	if err != nil {
		repo.Error(w, err)
		return true
	}
	ids := map[string]any{}
	for _, ref := range refs {
		_, tail, ok := strings.Cut(repo.HostedPath(ref), "/packages/")
		if ok {
			id, _, _ := strings.Cut(tail, "/")
			ids[id] = struct{}{}
		}
	}
	if len(ids) == 0 {
		return false
	}
	repo.JSON(w, 200, ids)
	return true
}
