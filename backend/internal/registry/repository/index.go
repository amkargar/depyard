package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/amkargar/depyard/backend/internal/storage"
)

// LinkedIndex adapts a native index that embeds URLs. The format parser visits
// link fields only: package metadata, signatures and digests are not string-replaced.
type LinkedIndex struct {
	Match     func(string) bool
	MediaType string
	MaxBytes  int64
	Transform func(data []byte, rewrite func(link string, archive bool) string) ([]byte, error)
}

func (idx LinkedIndex) Routes(h *Repository) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/_dist/") {
			idx.download(h, w, r)
			return
		}
		if !idx.Match(p) || !IsRead(r) {
			h.Files(w, r)
			return
		}
		if !h.ReadOK(w, r, strings.TrimPrefix(p, "/")) {
			return
		}
		src, ref, err := h.Open(r.Context(), Object{Path: p})
		if err != nil {
			Error(w, err)
			return
		}
		defer src.Close()
		data, err := Limited(src, idx.MaxBytes)
		if err != nil {
			Error(w, err)
			return
		}
		base := Base(r)
		up := strings.TrimRight(h.Instance.UpstreamURL, "/")
		data, err = idx.Transform(data, func(s string, archive bool) string {
			if up != "" && strings.HasPrefix(s, up+"/") {
				return base + strings.TrimPrefix(s, up)
			}
			if strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "//") {
				if u, err := url.Parse(up); err == nil && u.Path != "" && strings.HasPrefix(s, u.Path+"/") {
					s = strings.TrimPrefix(s, u.Path)
				}
				return base + s
			}
			if archive && ref.Origin == storage.OriginCached && publicRelease(s) {
				return base + distributionPath(p, s)
			}
			return s
		})
		if err != nil {
			Error(w, Status(502))
			return
		}
		w.Header().Set("Content-Type", idx.MediaType)
		_, _ = w.Write(data)
	})
}

// Public registries may contain publisher-supplied URLs. Only known public
// archive origins and routes can become server-side downloads. Others remain
// client-side links. Never treat a URL in a request parameter as a fetch target.
func publicRelease(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || !ValidPath(u.Path) {
		return false
	}
	s := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	switch u.Host {
	case "api.github.com":
		return len(s) == 5 && s[0] == "repos" && (s[3] == "zipball" || s[3] == "tarball")
	case "github.com":
		return len(s) >= 6 && s[2] == "releases" && s[3] == "download"
	case "codeload.github.com":
		return len(s) >= 4 && (s[2] == "zip" || s[2] == "tar.gz" || s[2] == "legacy.zip" || s[2] == "legacy.tar.gz")
	}
	return false
}

func distributionPath(index, raw string) string {
	sum := sha256.Sum256([]byte(raw))
	u, _ := url.Parse(raw)
	return "/_dist/" + hex.EncodeToString(sum[:]) + index + "/" + path.Base(u.Path)
}

func (idx LinkedIndex) download(h *Repository, w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	hash, rest, ok := strings.Cut(strings.TrimPrefix(p, "/_dist/"), "/")
	source := path.Dir("/" + rest)
	if !ok || !storage.IsValidSHA256(hash) || !idx.Match(source) || !h.CanProxy() {
		Error(w, Status(404))
		return
	}
	if !h.ReadOK(w, r, strings.TrimPrefix(source, "/")) {
		return
	}
	h.Serve(w, r, Object{Path: p, RemoteOnly: true, Immutable: true, Fetch: func(ctx context.Context) (*http.Response, error) {
		data, err := h.Bytes(ctx, Object{Path: source, RemoteOnly: true}, idx.MaxBytes)
		if err != nil {
			return nil, err
		}
		target := ""
		_, err = idx.Transform(data, func(link string, archive bool) string {
			if archive && publicRelease(link) && distributionPath(source, link) == p {
				target = link
			}
			return link
		})
		if err != nil {
			return nil, Status(502)
		}
		if target == "" {
			return nil, Status(404)
		}
		return h.Request(ctx, "GET", target, nil, nil)
	}})
}
