package npm

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/amkargar/depyard/backend/internal/audit"
	"github.com/amkargar/depyard/backend/internal/auth"
	"github.com/amkargar/depyard/backend/internal/storage"
)

// publishBody is the subset of an npm publish request this registry consumes.
// Unmodelled fields of each version remain in RawVersions.
type publishBody struct {
	Name        string                     `json:"name"`
	Description string                     `json:"description"`
	Readme      string                     `json:"readme"`
	Homepage    string                     `json:"homepage"`
	Repository  json.RawMessage            `json:"repository"`
	License     json.RawMessage            `json:"license"`
	Keywords    []string                   `json:"keywords"`
	Maintainers []json.RawMessage          `json:"maintainers"`
	DistTags    map[string]string          `json:"dist-tags"`
	Versions    map[string]json.RawMessage `json:"versions"`
	Attachments map[string]npmAttachment   `json:"_attachments"`
}

type npmAttachment struct {
	DataType string `json:"data_type"`
	Data     string `json:"data"`
	Length   int64  `json:"length"`
}

// handlePublish implements PUT /{package} as performed by `npm publish`.
func (r *NpmRegistry) handlePublish(w http.ResponseWriter, req *http.Request) {
	if !r.canPublish() {
		writeNpmError(w, http.StatusForbidden, "this registry instance does not accept publishes")
		return
	}
	// Authenticate before reading the (potentially large) request body, then
	// re-check with the package name once it is known for namespace scoping.
	principal, ok := r.authorize(w, req, auth.PermissionPublish, "")
	if !ok {
		return
	}

	name, _, err := ParseRequestPath(req.URL.EscapedPath())
	if err != nil {
		writeNpmError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !r.allowsPackage(name) {
		writeNpmError(w, http.StatusForbidden, "package is not allowed on this registry")
		return
	}

	limit := r.cfg.MaxTarballSizeBytes*2 + 16<<20
	body, err := io.ReadAll(io.LimitReader(req.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		writeNpmError(w, http.StatusRequestEntityTooLarge, "publish request exceeds the configured size limit")
		return
	}
	var publish publishBody
	if err := json.Unmarshal(body, &publish); err != nil {
		writeNpmError(w, http.StatusBadRequest, "invalid publish payload: "+err.Error())
		return
	}
	if bodyName := NormalizePackageName(publish.Name); bodyName != "" && bodyName != name {
		writeNpmError(w, http.StatusBadRequest, "package name in payload does not match the request path")
		return
	}
	if _, ok := r.authorize(w, req, auth.PermissionPublish, name); !ok {
		return
	}

	version, rawManifest, filename, tarball, err := extractPublishVersion(publish, name)
	if err != nil {
		writeNpmError(w, http.StatusBadRequest, err.Error())
		return
	}
	if r.cfg.VerifyIntegrity {
		if err := verifyPublishIntegrity(rawManifest, tarball); err != nil {
			writeNpmError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	sha256Sum := sha256.Sum256(tarball)
	digest := hex.EncodeToString(sha256Sum[:])
	manifest := decodeRawVersion(string(rawManifest))
	if manifest == nil {
		manifest = map[string]any{}
	}
	dist, _ := manifest["dist"].(map[string]any)
	if dist == nil {
		dist = map[string]any{}
	}
	sha1Sum := sha1.Sum(tarball)
	sha512Sum := sha512.Sum512(tarball)
	dist["shasum"] = hex.EncodeToString(sha1Sum[:])
	dist["integrity"] = "sha512-" + base64.StdEncoding.EncodeToString(sha512Sum[:])
	manifest["dist"] = dist

	r.publishMu.Lock()
	defer r.publishMu.Unlock()

	pkg, err := r.store.GetPackage(req.Context(), r.inst.ID, name)
	if err != nil {
		pkg = &NpmPackage{
			ID:         newID(),
			RegistryID: r.inst.ID,
			Name:       name,
			DistTags:   map[string]string{},
			CreatedAt:  time.Now().UTC(),
		}
	}
	if findVersion(pkg, version) != nil {
		writeNpmError(w, http.StatusForbidden, "cannot publish over an existing version")
		return
	}

	ref, err := r.storeArtifact(req.Context(), artifactKey(name, version, filename), digest,
		r.cfg.MaxTarballSizeBytes, bytes.NewReader(tarball), storage.OriginHosted)
	if err != nil {
		writeNpmError(w, http.StatusInternalServerError, "could not store tarball: "+err.Error())
		return
	}

	now := time.Now().UTC()
	published := NpmVersion{
		Version:     version,
		Filename:    filename,
		Digest:      ref.Digest,
		Shasum:      hex.EncodeToString(sha1Sum[:]),
		Integrity:   "sha512-" + base64.StdEncoding.EncodeToString(sha512Sum[:]),
		SizeBytes:   ref.SizeBytes,
		Origin:      storage.OriginHosted,
		Description: stringField(manifest["description"]),
		Deprecated:  stringField(manifest["deprecated"]),
		PublishedAt: now,
		RawMetadata: encodeRawVersion(manifest),
	}
	pkg.Versions = append(pkg.Versions, published)
	if publish.Description != "" {
		pkg.Description = publish.Description
	}
	if publish.Readme != "" {
		pkg.Readme = publish.Readme
	}
	if publish.Homepage != "" {
		pkg.Homepage = publish.Homepage
	}
	if len(publish.Keywords) > 0 {
		pkg.Keywords = publish.Keywords
	}
	if repository := strings.TrimSpace(string(publish.Repository)); repository != "" && repository != "null" {
		pkg.Repository = repository
	}
	if license := strings.TrimSpace(string(publish.License)); license != "" && license != "null" {
		pkg.License = license
	}
	for _, m := range publish.Maintainers {
		var maintainer struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(m, &maintainer); err == nil && maintainer.Name != "" {
			pkg.Maintainers = append(pkg.Maintainers, maintainer.Name)
		}
	}
	if pkg.DistTags == nil {
		pkg.DistTags = map[string]string{}
	}
	for tag, taggedVersion := range publish.DistTags {
		if taggedVersion == "" {
			continue
		}
		pkg.DistTags[tag] = taggedVersion
	}
	pkg.UpdatedAt = now
	recalculatePackage(pkg, r.cfg.IncludePrerelease)
	if err := r.store.UpsertPackage(req.Context(), pkg); err != nil {
		writeNpmError(w, http.StatusInternalServerError, "could not record package metadata: "+err.Error())
		return
	}
	if r.deps.Metrics != nil {
		r.deps.Metrics.RecordUpload(r.inst.ID, ref.SizeBytes)
	}
	r.deps.Audit.Record(req.Context(), audit.Entry{
		ActorID:    principal.ActorID(),
		ActorName:  principal.ActorName(),
		ActorType:  auditActorType(principal),
		Action:     "npm.publish",
		RegistryID: r.inst.ID,
		Target:     name + "@" + version,
		Success:    true,
	})
	writeNpmJSON(w, http.StatusCreated, map[string]any{"ok": "package published", "id": name, "version": version}, ContentTypeJSON)
}

// extractPublishVersion resolves the version, manifest, filename and tarball
// bytes of an npm publish request.
func extractPublishVersion(publish publishBody, name string) (string, json.RawMessage, string, []byte, error) {
	if len(publish.Attachments) == 0 {
		return "", nil, "", nil, fmt.Errorf("publish payload has no _attachments")
	}
	version := publish.DistTags["latest"]
	if version == "" && len(publish.Versions) == 1 {
		for v := range publish.Versions {
			version = v
		}
	}
	if version == "" {
		for filename := range publish.Attachments {
			version = versionFromTarballName(filename)
			break
		}
	}
	if _, err := ParseVersion(version); err != nil {
		return "", nil, "", nil, err
	}

	raw, ok := publish.Versions[version]
	if !ok {
		// Some clients publish a manifest without a versions entry. Reconstruct
		// the minimum needed for a valid packument.
		raw, _ = json.Marshal(map[string]any{"name": name, "version": version})
	}
	attachmentKey := ""
	for candidate := range publish.Attachments {
		if strings.HasSuffix(candidate, ".tgz") {
			attachmentKey = candidate
			break
		}
	}
	if attachmentKey == "" {
		attachmentKey = defaultTarballFilename(name, version)
	}
	attachment, ok := publish.Attachments[attachmentKey]
	if !ok {
		return "", nil, "", nil, fmt.Errorf("missing tarball attachment %q", attachmentKey)
	}
	// npm names scoped attachments "@scope/name-version.tgz". Keep the raw key
	// for the map lookup but flatten it for the blob key and public URL.
	filename := strings.TrimPrefix(strings.ReplaceAll(attachmentKey, "/", "-"), "@")
	filename = strings.ReplaceAll(filename, `\`, "-")
	if filename == "" || strings.ContainsAny(filename, `/\`) {
		return "", nil, "", nil, fmt.Errorf("invalid tarball attachment name %q", attachmentKey)
	}
	encoded := strings.NewReplacer("\r", "", "\n", "", " ", "", "\t", "").Replace(attachment.Data)
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", nil, "", nil, fmt.Errorf("invalid base64 in tarball attachment: %w", err)
	}
	if len(data) == 0 {
		return "", nil, "", nil, fmt.Errorf("tarball attachment is empty")
	}
	if attachment.Length > 0 && attachment.Length != int64(len(data)) {
		return "", nil, "", nil, fmt.Errorf("tarball attachment length mismatch: expected %d bytes, got %d", attachment.Length, len(data))
	}
	return version, raw, filename, data, nil
}

// versionFromTarballName derives a semver identifier from "<name>-<version>.tgz".
func versionFromTarballName(filename string) string {
	base := strings.TrimSuffix(path.Base(filename), ".tgz")
	parts := strings.Split(base, "-")
	for i := len(parts) - 1; i >= 1; i-- {
		candidate := strings.Join(parts[i:], "-")
		if _, err := ParseVersion(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

// verifyPublishIntegrity checks shasum/integrity values in the uploaded
// manifest against the actual tarball bytes when integrity checks are enabled.
func verifyPublishIntegrity(raw json.RawMessage, tarball []byte) error {
	var manifest struct {
		Dist struct {
			Shasum    string `json:"shasum"`
			Integrity string `json:"integrity"`
		} `json:"dist"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return fmt.Errorf("invalid version manifest: %w", err)
	}
	if shasum := strings.ToLower(strings.TrimSpace(manifest.Dist.Shasum)); shasum != "" {
		sum := sha1.Sum(tarball)
		if hex.EncodeToString(sum[:]) != shasum {
			return fmt.Errorf("tarball sha1 digest does not match the manifest")
		}
	}
	if integrity := strings.TrimSpace(manifest.Dist.Integrity); integrity != "" {
		algorithm, encoded, ok := strings.Cut(integrity, "-")
		if !ok {
			return fmt.Errorf("invalid integrity value %q", integrity)
		}
		want, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return fmt.Errorf("invalid integrity value %q", integrity)
		}
		var got []byte
		switch algorithm {
		case "sha512":
			sum := sha512.Sum512(tarball)
			got = sum[:]
		case "sha256":
			sum := sha256.Sum256(tarball)
			got = sum[:]
		case "sha1":
			sum := sha1.Sum(tarball)
			got = sum[:]
		default:
			return nil // unknown algorithms are recorded, not silently trusted
		}
		if !bytes.Equal(got, want) {
			return fmt.Errorf("tarball %s integrity does not match the manifest", algorithm)
		}
	}
	return nil
}

func stringField(value any) string {
	s, _ := value.(string)
	return s
}

func auditActorType(p *auth.Principal) string {
	if p == nil || !p.Authenticated {
		return "anonymous"
	}
	if p.Kind == "token" {
		return "token"
	}
	return "user"
}

// handleUnpublish implements DELETE /{package} and the single-version form
// DELETE /{package}/-/{file}.tgz used by `npm unpublish`.
func (r *NpmRegistry) handleUnpublish(w http.ResponseWriter, req *http.Request) {
	if !r.canDelete() {
		writeNpmError(w, http.StatusForbidden, "this registry instance does not accept unpublish requests")
		return
	}
	name, rest, err := ParseRequestPath(stripRevisionSuffix(req.URL.EscapedPath()))
	if err != nil {
		writeNpmError(w, http.StatusBadRequest, err.Error())
		return
	}
	principal, ok := r.authorize(w, req, auth.PermissionDelete, name)
	if !ok {
		return
	}

	version := strings.TrimSpace(req.URL.Query().Get("version"))
	if rest != "" {
		filename, err := tarballFilename(rest)
		if err != nil {
			writeNpmError(w, http.StatusBadRequest, err.Error())
			return
		}
		pkg, err := r.store.GetPackage(req.Context(), r.inst.ID, name)
		if err != nil {
			writeNpmError(w, http.StatusNotFound, "package not found")
			return
		}
		for _, v := range pkg.Versions {
			if v.Filename == filename {
				version = v.Version
				break
			}
		}
		if version == "" {
			writeNpmError(w, http.StatusNotFound, "tarball not found")
			return
		}
	}

	r.publishMu.Lock()
	defer r.publishMu.Unlock()
	pkg, err := r.store.GetPackage(req.Context(), r.inst.ID, name)
	if err != nil {
		writeNpmError(w, http.StatusNotFound, "package not found")
		return
	}
	if version != "" {
		v := findVersion(pkg, version)
		if v == nil {
			writeNpmError(w, http.StatusNotFound, "version not found")
			return
		}
		_ = r.deps.Storage.DeleteArtifactRef(req.Context(), r.inst.ID, artifactKey(name, version, v.Filename))
		removeVersion(pkg, version, r.cfg.IncludePrerelease)
		if len(pkg.Versions) == 0 {
			if err := r.store.DeletePackage(req.Context(), r.inst.ID, name); err != nil {
				writeNpmError(w, http.StatusInternalServerError, err.Error())
				return
			}
		} else {
			pkg.UpdatedAt = time.Now().UTC()
			if err := r.store.UpsertPackage(req.Context(), pkg); err != nil {
				writeNpmError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
	} else {
		for _, v := range pkg.Versions {
			_ = r.deps.Storage.DeleteArtifactRef(req.Context(), r.inst.ID, artifactKey(name, v.Version, v.Filename))
		}
		if err := r.store.DeletePackage(req.Context(), r.inst.ID, name); err != nil {
			writeNpmError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	r.deps.Audit.Record(req.Context(), audit.Entry{
		ActorID:    principal.ActorID(),
		ActorName:  principal.ActorName(),
		ActorType:  auditActorType(principal),
		Action:     "npm.unpublish",
		RegistryID: r.inst.ID,
		Target:     name + "@" + version,
		Success:    true,
	})
	writeNpmJSON(w, http.StatusOK, map[string]any{"ok": "unpublished"}, ContentTypeJSON)
}
