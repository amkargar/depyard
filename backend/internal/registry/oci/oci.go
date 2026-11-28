// Package oci implements the pull/push portion of the OCI Distribution v2 API.
package oci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/amkargar/depyard/backend/internal/registry"
	repo "github.com/amkargar/depyard/backend/internal/registry/repository"
	"github.com/amkargar/depyard/backend/internal/storage"
)

const accept = "application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json"

var repositoryRE = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)
var tagRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)

func Registration() registry.Registration {
	return (repo.Format{Type: "oci", Name: "Docker / OCI", Description: "Distribution v2 registry: image manifests, indexes, tags and blob upload/pull.", Upstream: "https://registry-1.docker.io", ResponseHeaders: []string{"Docker-Content-Digest"}, Compat: func(p string) bool { return p == "/v2" || strings.HasPrefix(p, "/v2/") }, Routes: func(r *repo.Repository) http.Handler {
		return &handler{Repository: r, sessions: sessions{uploads: make(map[string]*upload)}}
	}}).Registration()
}

type handler struct {
	*repo.Repository
	sessions
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
	p := r.URL.Path
	if p == "/v2/" || p == "/v2" {
		if !h.ReadOK(w, r, "") {
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="Depyard"`)
		repo.JSON(w, 200, map[string]any{})
		return
	}
	tail, ok := strings.CutPrefix(p, "/v2/")
	if !ok {
		repo.Error(w, repo.Status(404))
		return
	}
	for _, route := range []string{"/blobs/uploads/", "/manifests/", "/blobs/", "/tags/"} {
		idx := strings.LastIndex(tail, route)
		if idx <= 0 {
			continue
		}
		name, id := tail[:idx], tail[idx+len(route):]
		if !repositoryRE.MatchString(name) || strings.Contains(id, "/") {
			continue
		}
		switch route {
		case "/blobs/uploads/":
			h.upload(w, r, name, id)
		case "/manifests/":
			h.manifest(w, r, name, id)
		case "/tags/":
			if id != "list" {
				repo.Error(w, repo.Status(404))
				return
			}
			h.tags(w, r, name)
		case "/blobs/":
			d, err := repo.Digest(id)
			if err != nil {
				repo.Error(w, repo.Status(400))
				return
			}
			if h.ReadOK(w, r, name) {
				w.Header().Set("Docker-Content-Digest", id)
				h.Serve(w, r, h.remoteObject(name, p, d))
			}
		}
		return
	}
	repo.Error(w, repo.Status(404))
}

func (h *handler) remoteObject(name, p, d string) repo.Object {
	return repo.Object{
		Path:      p,
		Digest:    d,
		Immutable: d != "",
		Fetch:     func(ctx context.Context) (*http.Response, error) { return h.pull(ctx, name, p) },
	}
}

func (h *handler) manifest(w http.ResponseWriter, r *http.Request, name, id string) {
	digest := ""
	if !tagRE.MatchString(id) {
		var err error
		digest, err = repo.Digest(id)
		if err != nil {
			repo.Error(w, repo.Status(400))
			return
		}
	}
	ctx := r.Context()
	tagKey := "/tags/" + name + "/" + id
	mp := "/v2/" + name + "/manifests/"
	if repo.IsRead(r) {
		if !h.ReadOK(w, r, name) {
			return
		}
		if digest == "" {
			if _, err := h.HostedRef(ctx, tagKey); err == nil {
				b, err := h.Bytes(ctx, repo.Object{Path: tagKey}, 128)
				if err != nil {
					repo.Error(w, err)
					return
				}
				id = string(b)
				digest, err = repo.Digest(id)
				if err != nil {
					repo.Error(w, err)
					return
				}
			} else if !errors.Is(err, storage.ErrRefNotFound) {
				repo.Error(w, err)
				return
			}
		}
		h.Serve(w, r, h.remoteObject(name, mp+id, digest))
		return
	}
	if r.Method != "PUT" {
		repo.Error(w, repo.Status(405))
		return
	}
	if !h.WriteOK(w, r, name) {
		return
	}
	b, err := repo.Limited(r.Body, 8<<20)
	if err != nil {
		repo.Error(w, err)
		return
	}
	type descriptor struct {
		Digest string
		Size   int64
	}
	var doc struct {
		SchemaVersion     int
		MediaType         string
		Config            descriptor
		Layers, Manifests []descriptor
	}
	if json.Unmarshal(b, &doc) != nil || doc.SchemaVersion != 2 {
		repo.Error(w, repo.Status(400))
		return
	}
	if doc.MediaType == "" {
		doc.MediaType = strings.Split(r.Header.Get("Content-Type"), ";")[0]
	}
	if !slices.Contains(strings.Split(accept, ", "), doc.MediaType) {
		repo.Error(w, repo.Status(415))
		return
	}
	refs := doc.Manifests
	group := "manifests"
	if strings.Contains(doc.MediaType, "image.manifest.") || doc.MediaType == "application/vnd.docker.distribution.manifest.v2+json" {
		refs = append(doc.Layers, doc.Config)
		group = "blobs"
	}
	for _, desc := range refs {
		if _, err := repo.Digest(desc.Digest); err != nil {
			repo.Error(w, err)
			return
		}
		ref, err := h.LocalRef(ctx, "/v2/"+name+"/"+group+"/"+desc.Digest)
		if errors.Is(err, storage.ErrRefNotFound) || (err == nil && ref.SizeBytes != desc.Size) {
			repo.JSON(w, 400, map[string]any{"errors": []map[string]string{{"code": "MANIFEST_BLOB_UNKNOWN", "message": "referenced content missing or incorrect size", "detail": desc.Digest}}})
			return
		}
		if err != nil {
			repo.Error(w, err)
			return
		}
	}
	sum := sha(b)
	canonical := "sha256:" + sum
	if digest != "" && digest != sum {
		repo.Error(w, storage.ErrDigestMismatch)
		return
	}
	_, err = h.Put(ctx, repo.Object{Path: mp + canonical, Digest: sum}, bytes.NewReader(b), map[string]string{"Content-Type": doc.MediaType, "Docker-Content-Digest": canonical})
	if err == nil && digest == "" {
		_, err = h.Put(ctx, repo.Object{Path: tagKey}, strings.NewReader(canonical), nil)
	}
	if err != nil {
		repo.Error(w, err)
		return
	}
	w.Header().Set("Location", repo.Base(r)+mp+canonical)
	w.Header().Set("Docker-Content-Digest", canonical)
	w.WriteHeader(201)
}

func (h *handler) tags(w http.ResponseWriter, r *http.Request, name string) {
	if !h.ReadOK(w, r, name) {
		return
	}
	prefix := "/tags/" + name + "/"
	refs, err := h.HostedRefs(r.Context(), prefix)
	if err != nil {
		repo.Error(w, err)
		return
	}
	tags := []string{}
	for _, ref := range refs {
		tag := strings.TrimPrefix(repo.HostedPath(ref), prefix)
		if tagRE.MatchString(tag) {
			tags = append(tags, tag)
		}
	}
	if len(tags) == 0 && h.CanProxy() {
		h.Serve(w, r, h.remoteObject(name, r.URL.Path, ""))
		return
	}
	sort.Strings(tags)
	repo.JSON(w, 200, map[string]any{"name": name, "tags": tags})
}
