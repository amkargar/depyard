// Package cargo implements the sparse registry index, crate downloads, and
// Cargo's length-prefixed publish API (the git index protocol is not used).
package cargo

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/amkargar/depyard/backend/internal/registry"
	repo "github.com/amkargar/depyard/backend/internal/registry/repository"
	"github.com/amkargar/depyard/backend/internal/storage"
)

var nameRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var versionRE = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.+-]+)?$`)

func Registration() registry.Registration {
	return (repo.Format{
		Type:        "cargo",
		Name:        "Cargo (Rust)",
		Description: "Sparse registry index, crate downloads and cargo publish.",
		Upstream:    "https://index.crates.io",
		Routes:      func(r *repo.Repository) http.Handler { return &handler{Repository: r} },
	}).Registration()
}

type handler struct {
	*repo.Repository
	publish sync.Mutex
}

func prefix(name string) string {
	switch len(name) {
	case 1:
		return "1"
	case 2:
		return "2"
	case 3:
		return "3/" + name[:1]
	default:
		return name[:2] + "/" + name[2:4]
	}
}

func cratePath(n, v string) string { return "/crates/" + n + "/" + n + "-" + v + ".crate" }
func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if v := r.Header.Get("Authorization"); v != "" && !strings.Contains(v, " ") {
		r.Header.Set("Authorization", "Bearer "+v)
	}
	p := r.URL.Path
	switch {
	case p == "/config.json":
		if !h.ReadOK(w, r, "") {
			return
		}
		base := repo.Base(r)
		cfg := map[string]any{"dl": base + "/dl/{crate}/{version}", "auth-required": !h.Instance.AccessPolicy().Public || !h.Instance.AuthPolicy.AllowAnonymousRead}
		if h.CanHost() {
			cfg["api"] = base
		}
		repo.JSON(w, 200, cfg)
	case p == "/api/v1/crates/new" && r.Method == http.MethodPut:
		h.upload(w, r)
	case strings.HasPrefix(p, "/dl/"):
		parts := strings.Split(strings.TrimPrefix(p, "/dl/"), "/")
		if len(parts) != 2 || !nameRE.MatchString(parts[0]) || !versionRE.MatchString(parts[1]) {
			repo.Error(w, repo.Status(404))
			return
		}
		n, v := parts[0], parts[1]
		if !h.ReadOK(w, r, n) {
			return
		}
		h.Serve(w, r, repo.Object{Path: cratePath(n, v), Immutable: true, Fetch: func(ctx context.Context) (*http.Response, error) {
			return h.fetchCrate(ctx, n, v)
		}})
	default:
		parts := strings.Split(p, "/")
		n := parts[len(parts)-1]
		if !nameRE.MatchString(n) || p != "/"+prefix(n)+"/"+n {
			repo.Error(w, repo.Status(404))
			return
		}
		if h.ReadOK(w, r, n) {
			h.Serve(w, r, repo.Object{Path: p})
		}
	}
}

func (h *handler) upload(w http.ResponseWriter, r *http.Request) {
	if !h.WriteOK(w, r, "") {
		return
	}
	var size uint32
	if binary.Read(r.Body, binary.LittleEndian, &size) != nil || size == 0 || size > 1<<20 {
		repo.Error(w, repo.Status(400))
		return
	}
	raw := make([]byte, size)
	if _, err := io.ReadFull(r.Body, raw); err != nil {
		repo.Error(w, repo.Status(400))
		return
	}
	var meta struct {
		Name, Vers  string
		Deps        []map[string]any
		Features    map[string][]string
		Links       *string
		RustVersion *string `json:"rust_version"`
	}
	if json.Unmarshal(raw, &meta) != nil || !nameRE.MatchString(meta.Name) || !versionRE.MatchString(meta.Vers) {
		repo.Error(w, repo.Status(400))
		return
	}
	if !h.WriteOK(w, r, meta.Name) {
		return
	}
	if binary.Read(r.Body, binary.LittleEndian, &size) != nil || size == 0 || size > 512<<20 {
		repo.Error(w, repo.Status(400))
		return
	}
	n := strings.ToLower(meta.Name)
	ip := "/" + prefix(n) + "/" + n
	h.publish.Lock()
	defer h.publish.Unlock()
	// A locally published index shadows the upstream crate of the same name.
	var index []byte
	if _, err := h.HostedRef(r.Context(), ip); err == nil {
		index, err = h.Bytes(r.Context(), repo.Object{Path: ip}, 16<<20)
		if err != nil {
			repo.Error(w, err)
			return
		}
	} else if !errors.Is(err, storage.ErrRefNotFound) {
		repo.Error(w, err)
		return
	}
	for _, line := range bytes.Split(index, []byte("\n")) {
		var old struct{ Vers string }
		if json.Unmarshal(line, &old) == nil && old.Vers == meta.Vers {
			repo.Error(w, repo.Status(409))
			return
		}
	}
	src := &io.LimitedReader{R: r.Body, N: int64(size)}
	ref, err := h.Put(r.Context(), repo.Object{Path: cratePath(meta.Name, meta.Vers), NoReplace: true}, exactReader{src}, nil)
	if err != nil {
		repo.Error(w, err)
		return
	}
	if meta.Deps == nil {
		meta.Deps = []map[string]any{}
	}
	if meta.Features == nil {
		meta.Features = map[string][]string{}
	}
	for _, dep := range meta.Deps {
		dep["req"] = dep["version_req"]
		delete(dep, "version_req")
		if alias, ok := dep["explicit_name_in_toml"].(string); ok {
			dep["package"] = dep["name"]
			dep["name"] = alias
		}
		delete(dep, "explicit_name_in_toml")
	}
	// Older Cargo clients cannot parse namespaced or weak dependency features.
	// Mark those entries as schema v2 instead of emitting an invalid v1 index.
	features2 := map[string][]string{}
	for name, items := range meta.Features {
		for _, item := range items {
			if strings.HasPrefix(item, "dep:") || strings.Contains(item, "?/") {
				features2[name] = items
				delete(meta.Features, name)
				break
			}
		}
	}
	entry := map[string]any{
		"name":     meta.Name,
		"vers":     meta.Vers,
		"deps":     meta.Deps,
		"features": meta.Features,
		"cksum":    ref.Digest,
		"yanked":   false,
		"links":    meta.Links,
	}
	if len(features2) > 0 {
		entry["v"] = 2
		entry["features2"] = features2
	}
	if meta.RustVersion != nil {
		entry["rust_version"] = meta.RustVersion
	}
	b, _ := json.Marshal(entry)
	index = append(index, append(b, '\n')...)
	_, err = h.Put(r.Context(), repo.Object{Path: ip}, bytes.NewReader(index), nil)
	if err != nil {
		repo.Error(w, err)
		return
	}
	repo.JSON(w, 200, map[string]any{"ok": true})
}

// Turn premature EOF into a write failure instead of committing a truncated frame.
type exactReader struct{ r *io.LimitedReader }

func (e exactReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err == io.EOF && e.r.N > 0 {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}
