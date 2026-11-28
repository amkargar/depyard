package oci

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	repo "github.com/amkargar/depyard/backend/internal/registry/repository"
)

func sha(b []byte) string { v := sha256.Sum256(b); return hex.EncodeToString(v[:]) }

// Uploads are transient, repository-scoped and bounded; only digest-verified
// complete blobs enter CAS. Abandoned sessions expire even on an idle server.
type sessions struct {
	mu      sync.Mutex
	uploads map[string]*upload
	closed  bool
}
type upload struct {
	mu    sync.Mutex
	name  string
	file  *os.File
	size  int64
	timer *time.Timer
}

func (h *handler) discard(id string) {
	h.mu.Lock()
	u := h.uploads[id]
	delete(h.uploads, id)
	h.mu.Unlock()
	if u != nil {
		u.mu.Lock()
		defer u.mu.Unlock()
		u.timer.Stop()
		u.file.Close()
		os.Remove(u.file.Name())
		u.file = nil
	}
}

func (h *handler) Close() error {
	h.mu.Lock()
	h.closed = true
	var ids []string
	for id := range h.uploads {
		ids = append(ids, id)
	}
	h.mu.Unlock()
	for _, id := range ids {
		h.discard(id)
	}
	return nil
}

func (h *handler) start(name string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || len(h.uploads) >= 64 {
		return "", repo.Status(503)
	}
	f, err := os.CreateTemp("", "depyard-oci-*")
	if err != nil {
		return "", err
	}
	// On Unix the open descriptor survives unlink, but an abrupt process
	// exit will not strand the partial upload. Close/discard also handles
	// platforms that do not allow unlinking open files.
	_ = os.Remove(f.Name())
	id := rand.Text()
	u := &upload{name: name, file: f}
	u.timer = time.AfterFunc(time.Hour, func() { h.discard(id) })
	h.uploads[id] = u
	return id, nil
}

func (h *handler) upload(w http.ResponseWriter, r *http.Request, name, id string) {
	if !h.WriteOK(w, r, name) {
		return
	}
	base := repo.Base(r) + "/v2/" + name
	digest := r.URL.Query().Get("digest")
	p := "/v2/" + name + "/blobs/" + digest
	commit := func(src io.Reader) bool {
		d, err := repo.Digest(digest)
		if err == nil {
			_, err = h.Put(r.Context(), repo.Object{Path: p, Digest: d}, src, nil)
		}
		if err != nil {
			repo.Error(w, err)
			return false
		}
		w.Header().Set("Docker-Content-Digest", digest)
		w.Header().Set("Location", repo.Base(r)+p)
		w.WriteHeader(201)
		return true
	}
	if id == "" {
		if r.Method != "POST" {
			repo.Error(w, repo.Status(405))
			return
		}
		if digest != "" {
			commit(r.Body)
			return
		}
		var err error
		id, err = h.start(name)
		if err != nil {
			repo.Error(w, err)
			return
		}
		w.Header().Set("Docker-Upload-UUID", id)
		w.Header().Set("Location", base+"/blobs/uploads/"+id)
		w.Header().Set("Range", "0-0")
		w.WriteHeader(202)
		return
	}
	h.mu.Lock()
	u := h.uploads[id]
	h.mu.Unlock()
	if u == nil || u.name != name {
		repo.Error(w, repo.Status(404))
		return
	}
	done := false
	defer func() {
		if done {
			h.discard(id)
		}
	}()
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.file == nil {
		repo.Error(w, repo.Status(404))
		return
	}
	w.Header().Set("Docker-Upload-UUID", id)
	w.Header().Set("Location", base+"/blobs/uploads/"+id)
	setRange := func() { last := max(u.size-1, 0); w.Header().Set("Range", fmt.Sprintf("0-%d", last)) }
	setRange()
	switch r.Method {
	case "DELETE":
		done = true
		w.WriteHeader(204)
	case "GET":
		w.WriteHeader(204)
	case "PATCH", "PUT":
		if v := r.Header.Get("Content-Range"); v != "" {
			start, _, ok := strings.Cut(v, "-")
			n, err := strconv.ParseInt(start, 10, 64)
			if !ok || err != nil || n != u.size {
				repo.Error(w, repo.Status(416))
				return
			}
		}
		limit := int64(16 << 30)
		if q := h.Instance.StorageQuotaBytes; q > 0 && q < limit {
			limit = q
		}
		n, err := io.Copy(u.file, io.LimitReader(r.Body, limit-u.size+1))
		u.size += n
		if err != nil || u.size > limit {
			done = true
			if err == nil {
				err = repo.Status(413)
			}
			repo.Error(w, err)
			return
		}
		setRange()
		if r.Method == "PATCH" {
			w.WriteHeader(202)
			return
		}
		done = true
		commit(io.NewSectionReader(u.file, 0, u.size))
	default:
		repo.Error(w, repo.Status(405))
	}
}
