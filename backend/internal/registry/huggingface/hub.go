// Package huggingface implements the Hub file-resolution and repository-info
// APIs used by hf_hub_download and snapshot_download. It is not a Git remote.
package huggingface

import (
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/amkargar/depyard/backend/internal/registry"
	repo "github.com/amkargar/depyard/backend/internal/registry/repository"
	"github.com/amkargar/depyard/backend/internal/storage"
)

var commit = regexp.MustCompile(`^[0-9a-f]{40}$`)
var component = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]*$`)

func Registration() registry.Registration {
	return (repo.Format{
		Type:            "huggingface",
		Name:            "Hugging Face Hub",
		Description:     "Model, dataset and space snapshots, revision resolution and Hub file downloads.",
		Upstream:        "https://huggingface.co",
		RedirectHeaders: hubHeaders,
		ResponseHeaders: hubHeaders,
		Routes:          func(r *repo.Repository) http.Handler { return &hub{r} },
	}).Registration()
}

var hubHeaders = []string{"X-Repo-Commit", "X-Linked-Etag", "X-Linked-Size"}

type hub struct{ *repo.Repository }

func repoID(name string) bool {
	p := strings.Split(name, "/")
	if len(p) > 2 {
		return false
	}
	for _, v := range p {
		if !component.MatchString(v) {
			return false
		}
	}
	return true
}

func (h *hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/")
	if strings.HasPrefix(p, "api/") {
		h.info(w, r, strings.TrimPrefix(p, "api/"))
		return
	}
	kind := "models"
	if strings.HasPrefix(p, "datasets/") || strings.HasPrefix(p, "spaces/") {
		kind, p, _ = strings.Cut(p, "/")
	}
	id, tail, ok := strings.Cut(p, "/resolve/")
	if !ok {
		id, tail, ok = strings.Cut(p, "/refs/")
		if !ok || !repoID(id) || !component.MatchString(tail) || r.Method != "PUT" {
			repo.Error(w, repo.Status(404))
			return
		}
		if !h.WriteOK(w, r, id) {
			return
		}
		b, err := repo.Limited(r.Body, 64)
		if err != nil {
			repo.Error(w, err)
			return
		}
		sha := strings.TrimSpace(string(b))
		if !commit.MatchString(sha) {
			repo.Error(w, repo.Status(400))
			return
		}
		_, err = h.Put(r.Context(), repo.Object{Path: refPath(kind, id, tail)}, strings.NewReader(sha), nil)
		if err != nil {
			repo.Error(w, err)
			return
		}
		w.WriteHeader(201)
		return
	}
	rev, file, ok := strings.Cut(tail, "/")
	if !repoID(id) || !ok || file == "" || !component.MatchString(rev) {
		repo.Error(w, repo.Status(404))
		return
	}
	root := fileRoot(kind, id)
	if r.Method == "PUT" {
		if !h.WriteOK(w, r, id) {
			return
		}
		if !commit.MatchString(rev) {
			repo.Error(w, repo.Status(400))
			return
		}
		_, err := h.Put(r.Context(), repo.Object{Path: root + rev + "/" + file, NoReplace: true}, r.Body, map[string]string{"X-Repo-Commit": rev})
		if err != nil {
			repo.Error(w, err)
			return
		}
		w.WriteHeader(201)
		return
	}
	if !h.ReadOK(w, r, id) {
		return
	}
	sha, local, err := h.localRevision(r, kind, id, rev)
	if err != nil {
		repo.Error(w, err)
		return
	}
	obj := repo.Object{Path: r.URL.Path, Immutable: commit.MatchString(rev)}
	if local {
		obj.Path = root + sha + "/" + file
		if _, err := h.HostedRef(r.Context(), obj.Path); err != nil {
			notFound(w)
			return
		}
	}
	// Hub clients HEAD files for commit/size/etag before deciding to download.
	// Do not fill a multi-GB cache entry on a cold metadata probe.
	if r.Method == "HEAD" && !local && h.CanProxy() {
		if obj.Immutable {
			if _, err := h.LocalRef(r.Context(), obj.Path); err == nil {
				h.Serve(w, r, obj)
				return
			}
		}
		resp, err := h.Request(r.Context(), "HEAD", h.RemoteURL(obj.Path), nil, nil)
		if err != nil {
			repo.Error(w, err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode == 404 {
			notFound(w)
			return
		}
		if resp.StatusCode != 200 {
			repo.Error(w, repo.UpstreamStatus(resp.StatusCode))
			return
		}
		for _, k := range append([]string{"ETag", "Content-Length", "Content-Type"}, hubHeaders...) {
			if v := resp.Header.Get(k); v != "" {
				w.Header().Set(k, v)
			}
		}
		w.WriteHeader(200)
		return
	}
	h.Serve(w, r, obj)
}

func fileRoot(kind, id string) string {
	if kind == "models" {
		return "/" + id + "/resolve/"
	}
	return "/" + kind + "/" + id + "/resolve/"
}

func refPath(kind, id, rev string) string { return "/@refs/" + kind + "/" + id + "/" + rev }
func notFound(w http.ResponseWriter) {
	w.Header().Set("X-Error-Code", "EntryNotFound")
	repo.Error(w, repo.Status(404))
}

func (h *hub) localRevision(r *http.Request, kind, id, rev string) (string, bool, error) {
	if !h.CanHost() {
		return rev, false, nil
	}
	if commit.MatchString(rev) {
		return rev, !h.CanProxy(), nil
	}
	p := refPath(kind, id, rev)
	if _, err := h.HostedRef(r.Context(), p); errors.Is(err, storage.ErrRefNotFound) {
		return rev, false, nil
	} else if err != nil {
		return "", false, err
	}
	b, err := h.Bytes(r.Context(), repo.Object{Path: p}, 64)
	return string(b), true, err
}

func (h *hub) info(w http.ResponseWriter, r *http.Request, p string) {
	kind, tail, ok := strings.Cut(p, "/")
	if !ok || (kind != "models" && kind != "datasets" && kind != "spaces") {
		repo.Error(w, repo.Status(404))
		return
	}
	id, rev, has := strings.Cut(tail, "/revision/")
	var tree bool
	var folder string
	if !has {
		id, rev, tree = strings.Cut(tail, "/tree/")
		if tree {
			rev, folder, _ = strings.Cut(rev, "/")
		} else {
			rev = "main"
		}
	}
	if !repoID(id) || !component.MatchString(rev) {
		repo.Error(w, repo.Status(404))
		return
	}
	if !h.ReadOK(w, r, id) {
		return
	}
	sha, local, err := h.localRevision(r, kind, id, rev)
	if err != nil {
		repo.Error(w, err)
		return
	}
	var refs []*storage.ArtifactRef
	prefix := fileRoot(kind, id) + sha + "/"
	if local || commit.MatchString(rev) {
		refs, err = h.HostedRefs(r.Context(), prefix)
		if err != nil {
			repo.Error(w, err)
			return
		}
	}
	if len(refs) == 0 {
		if local || !h.CanProxy() {
			repo.Error(w, repo.Status(404))
			return
		}
		obj := repo.Object{Path: r.URL.Path, Query: r.URL.Query().Encode(), Immutable: commit.MatchString(rev)}
		if tree {
			h.remoteTree(w, r, obj)
		} else {
			h.Serve(w, r, obj)
		}
		return
	}
	if tree {
		h.tree(w, r, refs, prefix, folder)
		return
	}
	type file struct {
		Name string `json:"rfilename"`
		Size int64  `json:"size"`
	}
	siblings := make([]file, 0, len(refs))
	for _, ref := range refs {
		siblings = append(siblings, file{strings.TrimPrefix(repo.HostedPath(ref), prefix), ref.SizeBytes})
	}
	sort.Slice(siblings, func(i, j int) bool { return siblings[i].Name < siblings[j].Name })
	repo.JSON(w, 200, map[string]any{
		"id":        id,
		"sha":       sha,
		"siblings":  siblings,
		"private":   !h.Instance.AccessPolicy().Public,
		"downloads": 0,
		"likes":     0,
		"tags":      []string{},
	})
}
