package conan

import (
	"context"
	"crypto/md5" // required by the v1 snapshot wire format, not used for CAS integrity
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	repo "github.com/amkargar/depyard/backend/internal/registry/repository"
)

// A v1 file-server URL is resolved from the trusted upstream download_urls
// response; the client can choose a filename but cannot supply a fetch URL.
func (h *handler) legacy(w http.ResponseWriter, r *http.Request, parts []string, ns string) {
	n := 6
	if len(parts) >= 8 && parts[6] == "packages" {
		n = 8
	}
	base := "/" + strings.Join(parts[:n], "/")
	p := strings.Join(parts[n:], "/")
	prefix := base + "/files/"
	ctx := r.Context()
	if fn, ok := strings.CutPrefix(p, "files/"); ok && fn != "" {
		if r.Method == "PUT" && strings.EqualFold(r.Header.Get("X-Checksum-Deploy"), "true") {
			repo.Error(w, repo.Status(404))
			return
		}
		h.File(w, r, repo.Object{Path: prefix + fn, Fetch: func(ctx context.Context) (*http.Response, error) {
			urls, err := h.urls(ctx, base)
			if err != nil {
				return nil, err
			}
			remote := urls[fn]
			if remote == "" {
				return nil, repo.Status(404)
			}
			u, _ := url.Parse(h.Instance.UpstreamURL + "/")
			target, err := u.Parse(remote)
			if err != nil {
				return nil, repo.Status(502)
			}
			return h.Request(ctx, "GET", target.String(), nil, nil)
		}}, ns)
		return
	}
	links := func(files []string) map[string]string {
		m := make(map[string]string, len(files))
		for _, f := range files {
			m[f] = repo.Base(r) + (&url.URL{Path: prefix + f}).EscapedPath()
		}
		return m
	}
	if p == "upload_urls" && r.Method == "POST" {
		if !h.WriteOK(w, r, ns) {
			return
		}
		var sizes map[string]int64
		if err := repo.Decode(r, &sizes); err != nil {
			repo.Error(w, err)
			return
		}
		var files []string
		for f, n := range sizes {
			if n < 0 || f == "" || !repo.ValidPath("/"+f) {
				repo.Error(w, repo.Status(400))
				return
			}
			files = append(files, f)
		}
		repo.JSON(w, 200, links(files))
		return
	}
	if !h.ReadOK(w, r, ns) {
		return
	}
	if p == "search" {
		if !h.hostedPackages(w, r, base+"/") {
			h.Serve(w, r, repo.Object{Path: r.URL.Path, Query: r.URL.Query().Encode()})
		}
		return
	}
	if p != "digest" && p != "download_urls" && p != "" {
		repo.Error(w, repo.Status(404))
		return
	}
	refs, err := h.HostedRefs(ctx, prefix)
	if err != nil {
		repo.Error(w, err)
		return
	}
	var names []string
	hashes := map[string]string{}
	for _, ref := range refs {
		f := repo.HostedPath(ref)
		name := strings.TrimPrefix(f, prefix)
		names = append(names, name)
		if p == "" {
			src, _, err := h.Open(ctx, repo.Object{Path: f})
			if err != nil {
				repo.Error(w, err)
				return
			}
			sum := md5.New()
			_, err = io.Copy(sum, src)
			src.Close()
			if err != nil {
				repo.Error(w, err)
				return
			}
			hashes[name] = hex.EncodeToString(sum.Sum(nil))
		}
	}
	if len(names) == 0 {
		if !h.CanProxy() {
			repo.Error(w, repo.Status(404))
			return
		}
		if p == "" {
			h.Serve(w, r, repo.Object{Path: base})
			return
		}
		urls, err := h.urls(ctx, base)
		if err != nil {
			repo.Error(w, err)
			return
		}
		for f := range urls {
			names = append(names, f)
		}
	}
	if p == "" {
		repo.JSON(w, 200, hashes)
		return
	}
	if p == "digest" {
		names = []string{"conanmanifest.txt"}
	}
	repo.JSON(w, 200, links(names))
}

func (h *handler) urls(ctx context.Context, base string) (map[string]string, error) {
	resp, err := h.Request(ctx, "GET", h.RemoteURL(base+"/download_urls"), nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, repo.UpstreamStatus(resp.StatusCode)
	}
	var urls map[string]string
	if json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&urls) != nil {
		return nil, repo.Status(502)
	}
	return urls, nil
}
