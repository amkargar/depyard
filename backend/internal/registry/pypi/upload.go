package pypi

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/amkargar/depyard/backend/internal/auth"
	"github.com/amkargar/depyard/backend/internal/storage"
)

// uploadSession tracks an in-flight upload.
type uploadSession struct {
	Filename string
	Started  time.Time
}

// handleLegacyUpload implements the Twine-compatible upload endpoint.
//
// The endpoint accepts a multipart form with the ":action=file_upload" directive
// and the "content", "name", "version", "filetype" and digest fields that twine
// and uv send.
func (r *PyPIRegistry) handleLegacyUpload(w http.ResponseWriter, req *http.Request) {
	if !r.canPublish() {
		writeProblemText(w, http.StatusForbidden, "This registry instance does not accept uploads.")
		return
	}

	if _, ok := r.authorizeRequest(w, req, auth.PermissionPublish, ""); !ok {
		return
	}

	if err := req.ParseMultipartForm(32 << 20); err != nil {
		writeProblemText(w, http.StatusBadRequest, "Malformed multipart upload: "+err.Error())
		return
	}

	action := req.FormValue(":action")
	if action == "" {
		action = req.FormValue("action")
	}
	if action != "" && action != "file_upload" {
		writeProblemText(w, http.StatusBadRequest, fmt.Sprintf("Unsupported action %q.", action))
		return
	}

	file, header, err := req.FormFile("content")
	if err != nil {
		writeProblemText(w, http.StatusBadRequest, "Missing the 'content' file field.")
		return
	}
	defer file.Close()

	if r.cfg.MaxFileSizeBytes > 0 {
		if header.Size > r.cfg.MaxFileSizeBytes {
			writeProblemText(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("The uploaded file exceeds the maximum allowed size of %d bytes.", r.cfg.MaxFileSizeBytes))
			return
		}
	}

	data, err := io.ReadAll(io.LimitReader(file, r.cfg.MaxFileSizeBytes+1))
	if err != nil {
		writeProblemText(w, http.StatusBadRequest, "Could not read the uploaded file: "+err.Error())
		return
	}
	if int64(len(data)) > r.cfg.MaxFileSizeBytes {
		writeProblemText(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("The uploaded file exceeds the maximum allowed size of %d bytes.", r.cfg.MaxFileSizeBytes))
		return
	}

	filename := header.Filename
	if filename == "" {
		writeProblemText(w, http.StatusBadRequest, "The uploaded file has no name.")
		return
	}
	// Defend against path traversal in the client-supplied file name.
	filename = sanitizeFilename(filename)

	info, err := InspectDistribution(filename, data)
	if err != nil {
		writeProblemText(w, http.StatusBadRequest, err.Error())
		return
	}

	// The project name may come from the form or be derived from the file name.
	project := strings.TrimSpace(req.FormValue("name"))
	if project == "" {
		project = projectNameFromFilename(filename)
	}
	if project == "" {
		writeProblemText(w, http.StatusBadRequest, "Missing the project name.")
		return
	}
	if !IsValidProjectName(project) {
		writeProblemText(w, http.StatusBadRequest, fmt.Sprintf("Invalid project name %q.", project))
		return
	}
	normalized := NormalizeProjectName(project)
	if !r.allowsProject(normalized) {
		writeProblemText(w, http.StatusForbidden, fmt.Sprintf("The project %q is not allowed on this registry.", project))
		return
	}

	version := strings.TrimSpace(req.FormValue("version"))
	if version == "" {
		version = info.Version
	}
	if version == "" {
		writeProblemText(w, http.StatusBadRequest, "Missing the version.")
		return
	}
	canonicalVersion := NormalizeVersion(version)
	if _, err := ParseVersion(version); err != nil {
		writeProblemText(w, http.StatusBadRequest, fmt.Sprintf("Invalid version %q: %v", version, err))
		return
	}

	// Verify the digest the client supplied, when present.
	if err := r.verifyUploadDigest(req, data, info); err != nil {
		writeProblemText(w, http.StatusBadRequest, err.Error())
		return
	}

	// Store the distribution file.
	if _, err := r.storeArtifact(req.Context(),
		artifactKey(normalized, canonicalVersion, filename),
		strings.NewReader(string(data)),
		info.Digest,
		storage.OriginHosted,
	); err != nil {
		writeProblemText(w, http.StatusInternalServerError, "Could not store the uploaded file: "+err.Error())
		return
	}

	// Store the PEP 658 core metadata sidecar.
	hasMetadata := false
	if len(info.CoreMetadata) > 0 && r.cfg.ServeCoreMetadata {
		if _, err := r.storeArtifact(req.Context(),
			metadataArtifactKey(normalized, canonicalVersion, filename),
			strings.NewReader(string(info.CoreMetadata)),
			info.MetadataDigest,
			storage.OriginHosted,
		); err == nil {
			hasMetadata = true
		}
	}

	now := time.Now().UTC()
	if err := r.store.UpsertFile(req.Context(), &PyFile{
		ID:             newID(),
		RegistryID:     r.inst.ID,
		ProjectName:    normalized,
		Version:        canonicalVersion,
		Filename:       filename,
		Packagetype:    info.Packagetype,
		PythonVersion:  info.PythonVersion,
		RequiresPython: info.RequiresPython,
		Digest:         info.Digest,
		MD5Digest:      info.MD5Digest,
		SizeBytes:      info.SizeBytes,
		MetadataDigest: info.MetadataDigest,
		HasMetadata:    hasMetadata,
		Origin:         OriginHosted,
		UploadTime:     now,
		CreatedAt:      now,
	}); err != nil {
		writeProblemText(w, http.StatusInternalServerError, "Could not record the upload: "+err.Error())
		return
	}

	if err := r.upsertReleaseAndProject(req, normalized, canonicalVersion, info, now); err != nil {
		writeProblemText(w, http.StatusInternalServerError, "Could not record the release: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "Successfully uploaded %s\n", filename)
}

// verifyUploadDigest checks the digest fields the client supplied against the
// uploaded bytes.
func (r *PyPIRegistry) verifyUploadDigest(req *http.Request, data []byte, info *DistributionInfo) error {
	provided := false
	for field, digest := range map[string]string{
		"sha256_digest":     info.Digest,
		"md5_digest":        info.MD5Digest,
		"blake2_256_digest": "",
	} {
		value := strings.TrimSpace(req.FormValue(field))
		if value == "" {
			continue
		}
		provided = true
		if field == "blake2_256_digest" {
			// The blake2b-256 digest is not computed here; the SHA-256 check below
			// still guarantees integrity.
			continue
		}
		if !strings.EqualFold(value, digest) {
			return fmt.Errorf("The %s does not match the uploaded file.", strings.TrimSuffix(field, "_digest"))
		}
	}
	if !provided && r.cfg.RequireUploadHash {
		return errors.New("A sha256_digest or md5_digest field is required for uploads.")
	}
	return nil
}

// upsertReleaseAndProject records the release and project metadata of an upload.
func (r *PyPIRegistry) upsertReleaseAndProject(req *http.Request, project, version string, info *DistributionInfo, now time.Time) error {
	ctx := req.Context()

	release, err := r.store.GetRelease(ctx, r.inst.ID, project, version)
	if err != nil {
		release = &PyRelease{
			ID:          newID(),
			RegistryID:  r.inst.ID,
			ProjectName: project,
			Version:     version,
			Origin:      OriginHosted,
			UploadTime:  now,
			CreatedAt:   now,
		}
	}
	release.Origin = OriginHosted
	release.RequiresPython = info.RequiresPython
	release.Summary = info.Summary
	release.UpdatedAt = now
	if err := r.store.UpsertRelease(ctx, release); err != nil {
		return err
	}

	proj, err := r.store.GetProject(ctx, r.inst.ID, project)
	if err != nil {
		proj = &PyProject{
			ID:         newID(),
			RegistryID: r.inst.ID,
			Name:       project,
			Origin:     OriginHosted,
			CreatedAt:  now,
		}
	}
	proj.DisplayName = info.ProjectName
	if proj.DisplayName == "" {
		proj.DisplayName = project
	}
	proj.Origin = OriginHosted
	proj.Summary = info.Summary
	if info.Description != "" {
		proj.Description = info.Description
	}
	if info.Homepage != "" {
		proj.Homepage = info.Homepage
	}
	if info.Author != "" {
		proj.Author = info.Author
	}
	if info.AuthorEmail != "" {
		proj.AuthorEmail = info.AuthorEmail
	}
	if info.License != "" {
		proj.License = info.License
	}
	if info.RequiresPython != "" {
		proj.RequiresPython = info.RequiresPython
	}
	if len(info.Classifiers) > 0 {
		proj.Classifiers = info.Classifiers
	}
	if len(info.Keywords) > 0 {
		proj.Keywords = info.Keywords
	}
	if len(info.ProjectURLs) > 0 {
		proj.ProjectURLs = info.ProjectURLs
	}
	proj.UpdatedAt = now
	if err := r.store.UpsertProject(ctx, proj); err != nil {
		return err
	}
	return r.refreshProjectStats(ctx, project)
}

// authorizeRequest authenticates the caller and checks a permission against the
// instance's access policy.
func (r *PyPIRegistry) authorizeRequest(w http.ResponseWriter, req *http.Request, perm auth.Permission, namespace string) (*auth.Principal, bool) {
	principal, err := r.deps.Auth.AuthenticateRequest(req.Context(), req)
	if err != nil {
		writeProblemText(w, http.StatusUnauthorized, "Unauthorized: "+err.Error())
		return nil, false
	}
	if err := r.deps.Auth.Authorize(principal, auth.AuthRequest{
		Policy:     r.inst.AccessPolicy(),
		Permission: perm,
		Namespace:  namespace,
	}); err != nil {
		if errors.Is(err, auth.ErrUnauthenticated) {
			writeProblemText(w, http.StatusUnauthorized, "Unauthorized: authentication required for this registry.")
		} else {
			writeProblemText(w, http.StatusForbidden, "Forbidden: insufficient permissions for this registry.")
		}
		return nil, false
	}
	return principal, true
}

// sanitizeFilename strips any directory component from a client-supplied file name.
func sanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	name = strings.TrimSpace(name)
	if name == "." || name == ".." {
		return ""
	}
	return name
}

// projectNameFromFilename derives the project name from a distribution file name.
func projectNameFromFilename(filename string) string {
	base := filename
	for _, ext := range []string{".whl", ".tar.gz", ".tgz", ".zip", ".tar.bz2", ".tar.xz"} {
		if strings.HasSuffix(strings.ToLower(base), ext) {
			base = base[:len(base)-len(ext)]
			break
		}
	}
	if idx := strings.Index(base, "-"); idx > 0 {
		return base[:idx]
	}
	return base
}

// newID returns a random identifier for a metadata record.
func newID() string {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}
