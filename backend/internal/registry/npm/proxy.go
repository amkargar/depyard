package npm

import (
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/amkargar/depyard/backend/internal/storage"
)

// upstreamPackument is the subset of an upstream npm packument needed to build
// local metadata. Version manifests are kept verbatim as json.RawMessage.
type upstreamPackument struct {
	Name        string                     `json:"name"`
	Description string                     `json:"description"`
	Readme      string                     `json:"readme"`
	Homepage    string                     `json:"homepage"`
	Repository  json.RawMessage            `json:"repository"`
	License     json.RawMessage            `json:"license"`
	Keywords    []string                   `json:"keywords"`
	Maintainers []json.RawMessage          `json:"maintainers"`
	DistTags    map[string]string          `json:"dist-tags"`
	Time        map[string]string          `json:"time"`
	Versions    map[string]json.RawMessage `json:"versions"`
}

type upstreamVersion struct {
	Description string `json:"description"`
	Deprecated  string `json:"deprecated"`
	Dist        struct {
		Tarball   string `json:"tarball"`
		Shasum    string `json:"shasum"`
		Integrity string `json:"integrity"`
	} `json:"dist"`
}

// syncPackageFromUpstream mirrors an upstream packument into local metadata.
// It records metadata only; tarballs are fetched lazily on first download.
func (r *NpmRegistry) syncPackageFromUpstream(ctx context.Context, name string) (bool, error) {
	if !r.canProxy(name) || r.inst.UpstreamURL == "" {
		return false, nil
	}
	target := upstreamPackumentURL(r.inst.UpstreamURL, name)
	opts := r.inst.UpstreamRequestOptions(map[string]string{"Accept": ContentTypeJSON}, r.upstreamTimeout())
	resp, err := r.deps.Upstream.GetWithOptions(ctx, target, opts)
	if err != nil {
		r.recordUpstreamError()
		return false, fmt.Errorf("fetch upstream packument: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		r.recordUpstreamError()
		return false, fmt.Errorf("upstream packument returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return false, fmt.Errorf("read upstream packument: %w", err)
	}
	var doc upstreamPackument
	if err := json.Unmarshal(body, &doc); err != nil {
		return false, fmt.Errorf("parse upstream packument: %w", err)
	}
	if len(doc.Versions) == 0 {
		return false, nil
	}

	now := time.Now().UTC()
	pkg, err := r.store.GetPackage(ctx, r.inst.ID, name)
	if err != nil {
		pkg = &NpmPackage{
			ID:         newID(),
			RegistryID: r.inst.ID,
			Name:       name,
			CreatedAt:  now,
			DistTags:   map[string]string{},
		}
	}
	if pkg.DistTags == nil {
		pkg.DistTags = map[string]string{}
	}
	if pkg.Description == "" {
		pkg.Description = doc.Description
	}
	if pkg.Readme == "" {
		pkg.Readme = doc.Readme
	}
	if pkg.Homepage == "" {
		pkg.Homepage = doc.Homepage
	}
	if pkg.Repository == "" {
		pkg.Repository = strings.TrimSpace(string(doc.Repository))
	}
	if pkg.License == "" {
		pkg.License = strings.TrimSpace(string(doc.License))
	}
	if len(pkg.Keywords) == 0 {
		pkg.Keywords = doc.Keywords
	}
	for _, m := range doc.Maintainers {
		var maintainer struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(m, &maintainer); err == nil && maintainer.Name != "" {
			pkg.Maintainers = append(pkg.Maintainers, maintainer.Name)
		}
	}

	for rawVersion, rawManifest := range doc.Versions {
		if _, err := ParseVersion(rawVersion); err != nil {
			continue // skip non-semver upstream entries
		}
		if findVersion(pkg, rawVersion) != nil {
			continue // never overwrite a locally published version
		}
		var info upstreamVersion
		if err := json.Unmarshal(rawManifest, &info); err != nil {
			continue
		}
		filename := tarballFilenameFromURL(info.Dist.Tarball)
		if filename == "" {
			filename = defaultTarballFilename(name, rawVersion)
		}
		published := now
		if stamp, ok := doc.Time[rawVersion]; ok {
			if parsed, err := time.Parse(time.RFC3339, stamp); err == nil {
				published = parsed.UTC()
			}
		}
		pkg.Versions = append(pkg.Versions, NpmVersion{
			Version:            rawVersion,
			Filename:           filename,
			Shasum:             strings.ToLower(info.Dist.Shasum),
			Integrity:          info.Dist.Integrity,
			Origin:             storage.OriginCached,
			Description:        info.Description,
			Deprecated:         info.Deprecated,
			PublishedAt:        published,
			UpstreamTarballURL: info.Dist.Tarball,
			RawMetadata:        string(rawManifest),
		})
	}

	if len(pkg.Versions) == 0 {
		return false, nil
	}
	for tag, version := range doc.DistTags {
		pkg.DistTags[tag] = version
	}
	pkg.UpstreamSyncedAt = &now
	pkg.UpdatedAt = now
	recalculatePackage(pkg, r.cfg.IncludePrerelease)
	if err := r.store.UpsertPackage(ctx, pkg); err != nil {
		return false, err
	}
	return true, nil
}

// tarballFilenameFromURL extracts the final path segment of an upstream URL.
func tarballFilenameFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	name := path.Base(u.Path)
	if name == "." || name == "/" || !strings.HasSuffix(name, ".tgz") {
		return ""
	}
	return name
}

// cacheUpstreamTarball downloads and caches one tarball. Concurrent misses for
// the same version are deduplicated with singleflight.
func (r *NpmRegistry) cacheUpstreamTarball(ctx context.Context, name, version string) (bool, error) {
	if !r.canProxy(name) || r.inst.UpstreamURL == "" {
		return false, ErrPackageNotFound
	}
	key := name + "@" + version
	res, err, _ := r.tarballFlight.Do(key, func() (any, error) {
		pkg, err := r.store.GetPackage(ctx, r.inst.ID, name)
		if err != nil {
			return false, err
		}
		v := findVersion(pkg, version)
		if v == nil || v.UpstreamTarballURL == "" {
			return false, ErrVersionNotFound
		}
		blobKey := artifactKey(name, version, v.Filename)
		if _, err := r.deps.Storage.GetArtifactRef(ctx, r.inst.ID, blobKey); err == nil {
			return true, nil
		}

		target, err := resolveUpstreamTarball(r.inst.UpstreamURL, v.UpstreamTarballURL)
		if err != nil {
			return false, err
		}
		opts := r.inst.UpstreamRequestOptions(nil, r.upstreamTimeout())
		resp, err := r.deps.Upstream.GetWithOptions(ctx, target, opts)
		if err != nil {
			r.recordUpstreamError()
			return false, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			r.recordUpstreamError()
			return false, fmt.Errorf("upstream tarball returned status %d", resp.StatusCode)
		}

		// Hash with sha512 as the bytes stream into storage so a published
		// integrity value can be checked without buffering the whole tarball.
		hasher := sha512.New()
		limited := io.LimitReader(resp.Body, r.cfg.MaxTarballSizeBytes+1)
		ref, err := r.storeArtifact(ctx, blobKey, "", r.cfg.MaxTarballSizeBytes, io.TeeReader(limited, hasher), storage.OriginCached)
		if err != nil {
			return false, err
		}
		if v.Integrity != "" && !verifyIntegrity(hasher.Sum(nil), v.Integrity) {
			_ = r.deps.Storage.DeleteArtifactRef(ctx, r.inst.ID, blobKey)
			return false, fmt.Errorf("upstream tarball failed integrity verification")
		}

		// Re-read after the (potentially slow) fetch so concurrent metadata
		// changes are not lost.
		pkg, err = r.store.GetPackage(ctx, r.inst.ID, name)
		if err != nil {
			return false, err
		}
		if stored := findVersion(pkg, version); stored != nil {
			stored.Digest = ref.Digest
			stored.SizeBytes = ref.SizeBytes
			stored.UpstreamTarballURL = v.UpstreamTarballURL
			recalculatePackage(pkg, r.cfg.IncludePrerelease)
			if err := r.store.UpsertPackage(ctx, pkg); err != nil {
				return false, err
			}
		}
		return true, nil
	})
	return res == true, err
}

// resolveUpstreamTarball resolves a URL from an upstream packument while
// refusing cross-host URLs. This keeps a malicious packument from turning the
// mirror into an arbitrary HTTP client (SSRF).
func resolveUpstreamTarball(base, raw string) (string, error) {
	baseURL, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || baseURL.Host == "" {
		return "", fmt.Errorf("invalid upstream URL")
	}
	ref, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || ref.Host == "" {
		return "", fmt.Errorf("invalid upstream tarball URL %q", raw)
	}
	if ref.Scheme != "http" && ref.Scheme != "https" {
		return "", fmt.Errorf("unsupported upstream tarball URL %q", raw)
	}
	if !strings.EqualFold(ref.Host, baseURL.Host) {
		return "", fmt.Errorf("upstream tarball host %q does not match configured upstream %q", ref.Host, baseURL.Host)
	}
	return ref.String(), nil
}

// verifyIntegrity checks a streamed sha512 digest against npm's
// "<algorithm>-<base64>" integrity syntax. Other algorithms are not rejected:
// upstream may advertise a digest this implementation cannot verify here.
func verifyIntegrity(digest []byte, integrity string) bool {
	algorithm, encoded, ok := strings.Cut(integrity, "-")
	if !ok || algorithm != "sha512" {
		return true
	}
	want, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(want) != len(digest) {
		return false
	}
	// digest is the raw sha512 sum produced while streaming the tarball.
	return string(want) == string(digest)
}
