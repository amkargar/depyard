package goproxy

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/amkargar/depyard/backend/internal/auth"
	repo "github.com/amkargar/depyard/backend/internal/registry/repository"
	"github.com/amkargar/depyard/backend/internal/storage"
)

// importZip is a Depyard publishing extension, not a GOPROXY upstream method.
// A local module owns its name in hybrid mode. Never replace an existing
// upstream or hosted version: Go clients depend on immutable checksums.
func (r *GoRegistry) importZip(w http.ResponseWriter, req *http.Request) {
	name, file, ok := splitProxyPath(req.URL.Path)
	v, zipFile := strings.CutSuffix(file, ".zip")
	if !ok || !zipFile || !isValidVersion(v) || !r.isModuleAllowed(name) {
		repo.Error(w, repo.Status(404))
		return
	}
	ctx := req.Context()
	if !r.authorize(w, req, name, auth.PermissionPublish) {
		return
	}
	f, err := os.CreateTemp("", "depyard-gomod-*")
	if err != nil {
		repo.Error(w, err)
		return
	}
	defer os.Remove(f.Name())
	defer f.Close()
	maxSize := r.maxModuleBytes()
	if q := r.inst.StorageQuotaBytes; q > 0 && q < maxSize {
		maxSize = q
	}
	n, err := io.Copy(f, io.LimitReader(req.Body, maxSize+1))
	if err != nil {
		repo.Error(w, err)
		return
	}
	if n > maxSize {
		repo.Error(w, repo.Status(413))
		return
	}
	z, err := zip.NewReader(f, n)
	if err != nil || len(z.File) == 0 {
		repo.Error(w, repo.Status(400))
		return
	}
	prefix := name + "@" + v + "/"
	var mod []byte
	for _, entry := range z.File {
		rel, ok := strings.CutPrefix(entry.Name, prefix)
		if ok && rel == "" && entry.FileInfo().IsDir() {
			continue
		}
		if !ok || !fs.ValidPath(strings.TrimSuffix(rel, "/")) || strings.Contains(rel, "\\") {
			repo.Error(w, repo.Status(400))
			return
		}
		if rel != "go.mod" {
			continue
		}
		if mod != nil {
			repo.Error(w, repo.Status(400))
			return
		}
		reader, err := entry.Open()
		if err != nil {
			repo.Error(w, repo.Status(400))
			return
		}
		mod, err = repo.Limited(reader, 16<<20)
		reader.Close()
		if err != nil {
			repo.Error(w, err)
			return
		}
	}
	if mod == nil {
		mod = []byte("module " + name + "\n")
	}
	r.importMu.Lock()
	defer r.importMu.Unlock()
	m, err := r.store.GetModule(ctx, r.inst.ID, name)
	if err != nil && !errors.Is(err, ErrModuleNotFound) {
		repo.Error(w, err)
		return
	}
	if m != nil && m.Origin != storage.OriginHosted {
		repo.Error(w, repo.Status(409))
		return
	}
	if _, err = r.store.GetVersion(ctx, r.inst.ID, name, v); err == nil {
		repo.Error(w, repo.Status(409))
		return
	} else if !errors.Is(err, ErrVersionNotFound) {
		repo.Error(w, err)
		return
	}
	put := func(key string, in io.Reader) (*storage.ArtifactRef, error) {
		res, err := r.deps.Storage.PutArtifact(ctx, storage.PutArtifactInput{
			RegistryID:       r.inst.ID,
			ArtifactKey:      key,
			Reader:           in,
			Origin:           storage.OriginHosted,
			RegistryQuota:    r.inst.StorageQuotaBytes,
			MaxArtifactBytes: r.maxModuleBytes(),
		})
		return res.Ref, err
	}
	zk, mk := zipArtifactKey(name, v), modArtifactKey(name, v)
	zr, err := put(zk, io.NewSectionReader(f, 0, n))
	if err != nil {
		repo.Error(w, err)
		return
	}
	mr, err := put(mk, bytes.NewReader(mod))
	if err != nil {
		_ = r.deps.Storage.DeleteArtifactRef(ctx, r.inst.ID, zk)
		repo.Error(w, err)
		return
	}
	now := time.Now().UTC()
	ver := &GoVersion{
		RegistryID:  r.inst.ID,
		ModulePath:  name,
		Version:     v,
		Origin:      storage.OriginHosted,
		InfoTime:    now,
		PublishedAt: now,
		CreatedAt:   now,
		ZipDigest:   zr.Digest,
		ZipSize:     n,
		ZipCached:   true,
		GoModDigest: mr.Digest,
		ModSize:     mr.SizeBytes,
		ModCached:   true,
	}
	if m == nil {
		m = &GoModule{RegistryID: r.inst.ID, Path: name, Origin: storage.OriginHosted, CreatedAt: now}
	}
	m.UpdatedAt = now
	m.VersionCount++
	m.TotalSizeBytes += n
	if m.LatestVersion == "" || compareVersions(v, m.LatestVersion) > 0 {
		m.LatestVersion = v
	}
	if err = r.store.UpsertVersion(ctx, ver); err == nil {
		if err = r.store.UpsertModule(ctx, m); err != nil {
			_ = r.store.DeleteVersion(ctx, r.inst.ID, name, v)
		}
	}
	if err != nil {
		_ = r.deps.Storage.DeleteArtifactRef(ctx, r.inst.ID, zk)
		_ = r.deps.Storage.DeleteArtifactRef(ctx, r.inst.ID, mk)
		repo.Error(w, err)
		return
	}
	r.deps.Metrics.RecordUpload(r.inst.ID, n)
	w.WriteHeader(201)
}
