package nuget

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"net/http"
	"os"
	"strings"

	repo "github.com/amkargar/depyard/backend/internal/registry/repository"
)

func push(h *repo.Repository, w http.ResponseWriter, r *http.Request) {
	if k := r.Header.Get("X-NuGet-ApiKey"); k != "" && r.Header.Get("Authorization") == "" {
		r.Header.Set("Authorization", "Bearer "+k)
	}
	if !h.WriteOK(w, r, "") {
		return
	}
	// dotnet sends multipart/form-data with the package as its first part. Spool
	// to disk for zip.ReaderAt; never inflate the entire nupkg into memory.
	var src io.Reader = r.Body
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		mr, err := r.MultipartReader()
		if err != nil {
			repo.Error(w, repo.Status(400))
			return
		}
		part, err := mr.NextPart()
		if err != nil {
			repo.Error(w, repo.Status(400))
			return
		}
		src = part
	}
	f, err := os.CreateTemp("", "depyard-nuget-*")
	if err != nil {
		repo.Error(w, err)
		return
	}
	defer os.Remove(f.Name())
	defer f.Close()
	max := int64(1 << 30)
	if q := h.Instance.StorageQuotaBytes; q > 0 && q < max {
		max = q
	}
	n, err := io.Copy(f, io.LimitReader(src, max+1))
	if err != nil {
		repo.Error(w, err)
		return
	}
	if n > max {
		repo.Error(w, repo.Status(413))
		return
	}
	z, err := zip.NewReader(f, n)
	if err != nil {
		repo.Error(w, repo.Status(400))
		return
	}
	for _, entry := range z.File {
		if strings.Contains(entry.Name, "/") || !strings.HasSuffix(strings.ToLower(entry.Name), ".nuspec") {
			continue
		}
		rc, err := entry.Open()
		if err != nil {
			repo.Error(w, repo.Status(400))
			return
		}
		data, err := repo.Limited(rc, 2<<20)
		rc.Close()
		var spec struct {
			ID      string `xml:"metadata>id"`
			Version string `xml:"metadata>version"`
		}
		if err != nil || xml.Unmarshal(data, &spec) != nil {
			repo.Error(w, repo.Status(400))
			return
		}
		id := strings.ToLower(strings.TrimSpace(spec.ID))
		v := strings.ToLower(strings.SplitN(strings.TrimSpace(spec.Version), "+", 2)[0])
		if !idRE.MatchString(id) || !verRE.MatchString(v) {
			repo.Error(w, repo.Status(400))
			return
		}
		v = normalize(v)
		if !h.WriteOK(w, r, id) {
			return
		}
		base := "/flat/" + id + "/" + v + "/"
		pkg := repo.Object{Path: base + id + "." + v + ".nupkg", NoReplace: true}
		if _, err := h.HostedRef(r.Context(), pkg.Path); err == nil {
			repo.Error(w, repo.Status(409))
			return
		}
		if _, err = h.Put(r.Context(), repo.Object{Path: base + id + ".nuspec", NoReplace: true}, bytes.NewReader(data), map[string]string{"Content-Type": "application/xml"}); err == nil {
			_, err = h.Put(r.Context(), pkg, io.NewSectionReader(f, 0, n), nil)
		}
		if err != nil {
			repo.Error(w, err)
			return
		}
		w.WriteHeader(201)
		return
	}
	repo.Error(w, repo.Status(400))
}

func normalize(v string) string {
	core, pre, has := strings.Cut(v, "-")
	parts := strings.Split(core, ".")
	for i, p := range parts {
		parts[i] = strings.TrimLeft(p, "0")
		if parts[i] == "" {
			parts[i] = "0"
		}
	}
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	if len(parts) == 4 && parts[3] == "0" {
		parts = parts[:3]
	}
	core = strings.Join(parts, ".")
	if has {
		return core + "-" + pre
	}
	return core
}
