// Package gitlfs implements the Git LFS basic transfer adapter (not a Git server).
package gitlfs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/amkargar/depyard/backend/internal/auth"
	"github.com/amkargar/depyard/backend/internal/registry"
	repo "github.com/amkargar/depyard/backend/internal/registry/repository"
	"github.com/amkargar/depyard/backend/internal/storage"
)

const media = "application/vnd.git-lfs+json"

type object struct {
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}
type batch struct {
	Operation string   `json:"operation"`
	Objects   []object `json:"objects"`
}
type action struct {
	Href   string            `json:"href"`
	Header map[string]string `json:"header,omitempty"`
}
type result struct {
	object
	Actions map[string]action `json:"actions,omitempty"`
	Error   *objectError      `json:"error,omitempty"`
}
type objectError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
type response struct {
	Transfer string   `json:"transfer"`
	Objects  []result `json:"objects"`
}

func Registration() registry.Registration {
	return (repo.Format{
		Type:        "gitlfs",
		Name:        "Git LFS",
		Description: "LFS batch API, basic uploads and SHA-256 verified download cache.",
		Routes:      routes,
	}).Registration()
}

func valid(o object) bool {
	return storage.IsValidSHA256(o.OID) && strings.ToLower(o.OID) == o.OID && o.Size >= 0
}

func routes(h *repo.Repository) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/info/lfs")
		if p == "/objects/batch" && r.Method == "POST" {
			serveBatch(h, w, r)
			return
		}
		oid, ok := strings.CutPrefix(p, "/objects/")
		if !ok || !valid(object{OID: oid}) {
			repo.Error(w, repo.Status(404))
			return
		}
		size, err := strconv.ParseInt(r.URL.Query().Get("size"), 10, 64)
		if err != nil || size < 0 {
			repo.Error(w, repo.Status(400))
			return
		}
		o := object{oid, size}
		obj := repo.Object{Path: "/objects/" + oid, Digest: oid, Size: &size, Immutable: true, Fetch: func(ctx context.Context) (*http.Response, error) {
			objects, err := negotiate(ctx, h, []object{o})
			if err != nil {
				return nil, err
			}
			match, ok := objects[oid]
			if !ok || match.Error != nil {
				return nil, storage.ErrRefNotFound
			}
			a := match.Actions["download"]
			base, _ := url.Parse(h.Instance.UpstreamURL + "/")
			u, err := base.Parse(a.Href)
			if err != nil || a.Href == "" {
				return nil, repo.Status(502)
			}
			return h.Request(ctx, "GET", u.String(), nil, a.Header)
		}}
		if r.Method == "PUT" && r.ContentLength >= 0 && r.ContentLength != size {
			repo.Error(w, repo.Status(400))
			return
		}
		h.File(w, r, obj, oid)
	})
}

func serveBatch(h *repo.Repository, w http.ResponseWriter, r *http.Request) {
	var b batch
	if err := repo.Decode(r, &b); err != nil {
		repo.Error(w, err)
		return
	}
	perm := auth.PermissionRead
	if b.Operation == "upload" {
		perm = auth.PermissionPublish
	} else if b.Operation != "download" {
		repo.Error(w, repo.Status(422))
		return
	}
	if len(b.Objects) > 1000 {
		repo.Error(w, repo.Status(413))
		return
	}
	if !h.Authorize(w, r, perm, "") {
		return
	}
	res := response{Transfer: "basic", Objects: make([]result, 0, len(b.Objects))}
	var missing []object
	for _, o := range b.Objects {
		if !valid(o) {
			repo.Error(w, repo.Status(422))
			return
		}
		if !h.Authorize(w, r, perm, o.OID) {
			return
		}
		p := "/objects/" + o.OID
		v := result{object: o}
		var ref *storage.ArtifactRef
		var err error
		if perm == auth.PermissionPublish {
			ref, err = h.HostedRef(r.Context(), p)
		} else {
			ref, err = h.LocalRef(r.Context(), p)
		}
		if err != nil && !errors.Is(err, storage.ErrRefNotFound) {
			repo.Error(w, err)
			return
		}
		href := repo.Base(r) + p + "?size=" + strconv.FormatInt(o.Size, 10)
		if ref != nil && ref.SizeBytes != o.Size {
			v.Error = &objectError{422, "size mismatch"}
		} else if b.Operation == "download" {
			v.Actions = map[string]action{"download": {Href: href}}
			if ref == nil {
				missing = append(missing, o)
			}
		} else if ref == nil {
			v.Actions = map[string]action{"upload": {Href: href}}
		}
		res.Objects = append(res.Objects, v)
	}
	if len(missing) > 0 {
		found := map[string]result{}
		if h.CanProxy() {
			var err error
			found, err = negotiate(r.Context(), h, missing)
			if err != nil {
				repo.Error(w, err)
				return
			}
		}
		absent := map[string]bool{}
		for _, o := range missing {
			m, ok := found[o.OID]
			if !ok || m.Error != nil || m.Actions["download"].Href == "" {
				absent[o.OID] = true
			}
		}
		for i, o := range res.Objects {
			if absent[o.OID] {
				res.Objects[i].Actions = nil
				res.Objects[i].Error = &objectError{404, "object not found"}
			}
		}
	}
	w.Header().Set("Content-Type", media)
	_ = json.NewEncoder(w).Encode(res)
}

func negotiate(ctx context.Context, h *repo.Repository, objects []object) (map[string]result, error) {
	data, _ := json.Marshal(batch{Operation: "download", Objects: objects})
	resp, err := h.Request(ctx, "POST", h.RemoteURL("/objects/batch"), bytes.NewReader(data), map[string]string{"Accept": media, "Content-Type": media})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, repo.UpstreamStatus(resp.StatusCode)
	}
	raw, err := repo.Limited(resp.Body, 8<<20)
	if err != nil {
		return nil, err
	}
	var res response
	if json.Unmarshal(raw, &res) != nil {
		return nil, repo.Status(502)
	}
	m := make(map[string]result, len(res.Objects))
	for _, o := range res.Objects {
		m[o.OID] = o
	}
	return m, nil
}
