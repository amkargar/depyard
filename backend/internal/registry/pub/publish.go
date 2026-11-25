package pub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/amkargar/mirror-registry/backend/internal/audit"
	"github.com/amkargar/mirror-registry/backend/internal/auth"
	"github.com/amkargar/mirror-registry/backend/internal/registry"
	"github.com/amkargar/mirror-registry/backend/internal/storage"
)

type uploadSession struct {
	ID         string
	Principal  *auth.Principal
	CreatedAt  time.Time
	ExpiresAt  time.Time
	Finished   bool
	Package    string
	Version    string
	ErrMessage string
}

func (r *PubRegistry) handleNewVersion(w http.ResponseWriter, req *http.Request) {
	if r.inst.Mode == registry.ModeProxy {
		writePubError(w, http.StatusMethodNotAllowed, "PublishNotAllowed", "publishing is disabled for proxy-only registries")
		return
	}

	principal, ok := r.authorizeRequest(w, req, auth.PermissionPublish, "")
	if !ok {
		return
	}

	uploadID := newUploadID()
	now := time.Now().UTC()
	r.uploadMu.Lock()
	r.pruneExpiredUploadsLocked(now)
	r.uploads[uploadID] = &uploadSession{
		ID:        uploadID,
		Principal: principal,
		CreatedAt: now,
		ExpiresAt: now.Add(15 * time.Minute),
	}
	r.uploadMu.Unlock()

	baseURL := registry.BaseURLFromContext(req.Context())
	uploadURL := strings.TrimRight(baseURL, "/") + "/api/packages/versions/newUpload/" + uploadID

	writePubJSON(w, http.StatusOK, UploadInfoResponse{
		URL:    uploadURL,
		Fields: map[string]string{},
	})
}

func (r *PubRegistry) handleNewUpload(w http.ResponseWriter, req *http.Request) {
	if r.inst.Mode == registry.ModeProxy {
		writePubError(w, http.StatusMethodNotAllowed, "PublishNotAllowed", "publishing is disabled for proxy-only registries")
		return
	}

	uploadID := req.PathValue("id")
	r.uploadMu.Lock()
	sess, exists := r.uploads[uploadID]
	r.uploadMu.Unlock()
	if !exists || time.Now().UTC().After(sess.ExpiresAt) {
		writePubError(w, http.StatusBadRequest, "InvalidUploadSession", "upload session is invalid or expired")
		return
	}

	endTransfer := r.deps.Metrics.BeginTransfer(r.inst.ID)
	defer endTransfer()

	// Parse multipart upload (standard Dart pub client sends part named "file")
	maxBytes := r.maxArchiveBytes()
	req.Body = http.MaxBytesReader(w, req.Body, maxBytes+2*1024*1024)
	mr, err := req.MultipartReader()
	if err != nil {
		r.failUpload(w, req, uploadID, http.StatusBadRequest, "InvalidUpload", "expected multipart/form-data archive upload")
		return
	}

	tmpFile, err := os.CreateTemp("", "pub-upload-*.tar.gz")
	if err != nil {
		r.failUpload(w, req, uploadID, http.StatusInternalServerError, "ServerError", "failed to prepare upload buffer")
		return
	}
	tmpPath := tmpFile.Name()
	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
	}()

	var foundFile bool
	var written int64
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			r.failUpload(w, req, uploadID, http.StatusBadRequest, "InvalidUpload", fmt.Sprintf("read multipart stream: %v", err))
			return
		}
		if part.FormName() == "file" {
			foundFile = true
			n, copyErr := io.Copy(tmpFile, io.LimitReader(part, maxBytes+1))
			_ = part.Close()
			if copyErr != nil {
				r.failUpload(w, req, uploadID, http.StatusBadRequest, "InvalidUpload", fmt.Sprintf("read archive file: %v", copyErr))
				return
			}
			if n > maxBytes {
				r.failUpload(w, req, uploadID, http.StatusRequestEntityTooLarge, "ArchiveTooLarge", "package archive exceeds maximum allowed size")
				return
			}
			written = n
			break
		}
		_ = part.Close()
	}

	if !foundFile || written == 0 {
		r.failUpload(w, req, uploadID, http.StatusBadRequest, "MissingFile", "multipart form must include a non-empty 'file' part")
		return
	}

	if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
		r.failUpload(w, req, uploadID, http.StatusInternalServerError, "ServerError", "failed to rewind upload")
		return
	}

	extracted, err := ExtractPubArchive(tmpFile)
	if err != nil {
		r.failUpload(w, req, uploadID, http.StatusBadRequest, "InvalidPackage", err.Error())
		return
	}

	if !r.isPackageAllowed(extracted.Name) {
		r.failUpload(w, req, uploadID, http.StatusForbidden, "PackageBlocked", fmt.Sprintf("package %q is blocked by registry policy", extracted.Name))
		return
	}

	// Enforce namespace-scoped token authorization against the actual package name inside pubspec.yaml!
	if err := r.deps.Auth.Authorize(sess.Principal, auth.AuthRequest{
		Policy:     r.inst.AccessPolicy(),
		Permission: auth.PermissionPublish,
		Namespace:  extracted.Name,
	}); err != nil {
		r.failUpload(w, req, uploadID, http.StatusForbidden, "InsufficientPermissions", fmt.Sprintf("not authorized to publish package %q", extracted.Name))
		return
	}

	if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
		r.failUpload(w, req, uploadID, http.StatusInternalServerError, "ServerError", "failed to rewind upload")
		return
	}

	if err := r.PublishArchive(req.Context(), sess.Principal, extracted, tmpFile); err != nil {
		status := http.StatusBadRequest
		code := "PublishFailed"
		if errors.Is(err, ErrVersionExists) {
			status = http.StatusConflict
			code = "VersionAlreadyExists"
		} else if errors.Is(err, storage.ErrQuotaExceeded) {
			status = http.StatusInsufficientStorage
			code = "QuotaExceeded"
		} else if errors.Is(err, auth.ErrForbidden) {
			status = http.StatusForbidden
			code = "Forbidden"
		}
		r.failUpload(w, req, uploadID, status, code, err.Error())
		return
	}

	r.uploadMu.Lock()
	if s, ok := r.uploads[uploadID]; ok {
		s.Finished = true
		s.Package = extracted.Name
		s.Version = extracted.Version
	}
	r.uploadMu.Unlock()

	baseURL := registry.BaseURLFromContext(req.Context())
	finishURL := strings.TrimRight(baseURL, "/") + "/api/packages/versions/newUploadFinish/" + uploadID
	w.Header().Set("Location", finishURL)
	w.WriteHeader(http.StatusNoContent)
}

func (r *PubRegistry) handleUploadFinish(w http.ResponseWriter, req *http.Request) {
	uploadID := req.PathValue("id")
	r.uploadMu.Lock()
	sess, exists := r.uploads[uploadID]
	if exists {
		delete(r.uploads, uploadID)
	}
	r.uploadMu.Unlock()

	if !exists {
		writePubError(w, http.StatusNotFound, "InvalidUploadSession", "upload session not found")
		return
	}
	if sess.ErrMessage != "" {
		writePubError(w, http.StatusBadRequest, "UploadFailed", sess.ErrMessage)
		return
	}
	if !sess.Finished {
		writePubError(w, http.StatusBadRequest, "UploadIncomplete", "upload has not been completed")
		return
	}
	writePubSuccess(w, fmt.Sprintf("Successfully uploaded package %s %s", sess.Package, sess.Version))
}

// PublishArchive validates immutability & ownership, persists the archive as a protected hosted blob,
// updates Pub metadata, and records upload metrics + audit logs.
func (r *PubRegistry) PublishArchive(ctx context.Context, principal *auth.Principal, extracted *ExtractedArchive, archiveReader io.Reader) error {
	existingPkg, err := r.store.GetPackage(ctx, r.inst.ID, extracted.Name)
	if err != nil && !errors.Is(err, ErrPackageNotFound) {
		return err
	}

	uploader := uploaderIdentity(principal)
	if existingPkg != nil && existingPkg.Origin == storage.OriginHosted {
		// Enforce package ownership if uploaders are configured and caller isn't an admin
		if len(existingPkg.Uploaders) > 0 && !principal.IsAdmin() && !containsString(existingPkg.Uploaders, uploader) {
			return fmt.Errorf("%w: you are not an uploader of package %q", auth.ErrForbidden, extracted.Name)
		}
		// Enforce version immutability
		if _, err := r.store.GetVersion(ctx, r.inst.ID, extracted.Name, extracted.Version); err == nil {
			return fmt.Errorf("%w: %s %s already exists", ErrVersionExists, extracted.Name, extracted.Version)
		}
	} else if existingPkg != nil && existingPkg.Origin == storage.OriginCached {
		// Promoting a package name to hosted clears cached upstream versions so hybrid namespace stays clean.
		if oldVersions, lerr := r.store.ListVersions(ctx, r.inst.ID, extracted.Name); lerr == nil {
			for _, ov := range oldVersions {
				_ = r.deps.Storage.DeleteArtifactRef(ctx, r.inst.ID, artifactKey(extracted.Name, ov.Version))
				_ = r.store.DeleteVersion(ctx, r.inst.ID, extracted.Name, ov.Version)
			}
		}
	}

	putRes, err := r.deps.Storage.PutArtifact(ctx, storage.PutArtifactInput{
		RegistryID:       r.inst.ID,
		ArtifactKey:      artifactKey(extracted.Name, extracted.Version),
		Origin:           storage.OriginHosted, // Marks blob ref Protected=true (immune to cache eviction)
		MaxArtifactBytes: r.maxArchiveBytes(),
		RegistryQuota:    r.inst.StorageQuotaBytes,
		Reader:           archiveReader,
	})
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	ver := &PubVersion{
		ID:            verDocID(r.inst.ID, extracted.Name, extracted.Version),
		RegistryID:    r.inst.ID,
		PackageName:   extracted.Name,
		Version:       extracted.Version,
		Origin:        storage.OriginHosted,
		Pubspec:       extracted.Pubspec,
		ArchiveDigest: putRes.Ref.Digest,
		ArchiveSize:   putRes.Ref.SizeBytes,
		ArchiveCached: true,
		PublishedAt:   now,
		CreatedAt:     now,
	}
	if err := r.store.UpsertVersion(ctx, ver); err != nil {
		_ = r.deps.Storage.DeleteArtifactRef(ctx, r.inst.ID, artifactKey(extracted.Name, extracted.Version))
		return err
	}

	versions, err := r.store.ListVersions(ctx, r.inst.ID, extracted.Name)
	if err != nil {
		return err
	}
	latest := SelectLatestVersion(versions)

	pkg := existingPkg
	if pkg == nil || pkg.Origin != storage.OriginHosted {
		pkg = &PubPackage{
			ID:         pkgDocID(r.inst.ID, extracted.Name),
			RegistryID: r.inst.ID,
			Name:       extracted.Name,
			Origin:     storage.OriginHosted,
			Uploaders:  []string{uploader},
			CreatedAt:  now,
		}
	} else if uploader != "" && !containsString(pkg.Uploaders, uploader) {
		pkg.Uploaders = append(pkg.Uploaders, uploader)
	}

	pkg.Origin = storage.OriginHosted
	pkg.VersionCount = len(versions)
	pkg.TotalSizeBytes = r.recalcPackageSize(ctx, extracted.Name)
	pkg.UpdatedAt = now
	if latest != nil && latest.Version == extracted.Version {
		pkg.LatestVersion = extracted.Version
		pkg.Description = extracted.Description
		pkg.Homepage = extracted.Homepage
		pkg.Repository = extracted.Repository
		pkg.Documentation = extracted.Documentation
		if extracted.Readme != "" {
			pkg.Readme = extracted.Readme
		}
		if extracted.Changelog != "" {
			pkg.Changelog = extracted.Changelog
		}
	} else if latest != nil {
		pkg.LatestVersion = latest.Version
	}

	if err := r.store.UpsertPackage(ctx, pkg); err != nil {
		return err
	}

	r.deps.Metrics.RecordUpload(r.inst.ID, putRes.Ref.SizeBytes)
	r.deps.Audit.Record(ctx, audit.Entry{
		ActorID:    principal.ActorID(),
		ActorName:  principal.ActorName(),
		ActorType:  principal.Kind,
		Action:     "artifact.publish",
		RegistryID: r.inst.ID,
		Target:     extracted.Name + "@" + extracted.Version,
		Success:    true,
		Details:    map[string]any{"digest": putRes.Ref.Digest, "size_bytes": putRes.Ref.SizeBytes},
	})
	return nil
}

// --- Version Retraction, Discontinuation, and Package Ownership ---

func (r *PubRegistry) handleRetractVersion(w http.ResponseWriter, req *http.Request, retract bool) {
	if r.inst.Mode == registry.ModeProxy || !r.cfg.AllowRetract {
		writePubError(w, http.StatusMethodNotAllowed, "RetractNotAllowed", "version retraction is not enabled on this registry")
		return
	}
	pkgName := req.PathValue("package")
	version := req.PathValue("version")

	principal, ok := r.authorizeRequest(w, req, auth.PermissionPublish, pkgName)
	if !ok {
		return
	}

	pkg, err := r.store.GetPackage(req.Context(), r.inst.ID, pkgName)
	if err != nil {
		writePubError(w, http.StatusNotFound, "NotFound", "package not found")
		return
	}
	if pkg.Origin != storage.OriginHosted {
		writePubError(w, http.StatusBadRequest, "NotHosted", "cannot retract versions of a cached upstream package")
		return
	}

	ver, err := r.store.GetVersion(req.Context(), r.inst.ID, pkgName, version)
	if err != nil {
		writePubError(w, http.StatusNotFound, "NotFound", "version not found")
		return
	}
	ver.Retracted = retract
	if err := r.store.UpsertVersion(req.Context(), ver); err != nil {
		writePubError(w, http.StatusInternalServerError, "ServerError", err.Error())
		return
	}

	if versions, err := r.store.ListVersions(req.Context(), r.inst.ID, pkgName); err == nil {
		if latest := SelectLatestVersion(versions); latest != nil {
			pkg.LatestVersion = latest.Version
		}
		pkg.UpdatedAt = time.Now().UTC()
		_ = r.store.UpsertPackage(req.Context(), pkg)
	}

	action := "artifact.retract"
	if !retract {
		action = "artifact.unretract"
	}
	r.deps.Audit.Record(req.Context(), audit.Entry{
		ActorID:    principal.ActorID(),
		ActorName:  principal.ActorName(),
		ActorType:  principal.Kind,
		Action:     action,
		RegistryID: r.inst.ID,
		Target:     pkgName + "@" + version,
		Success:    true,
	})
	writePubSuccess(w, fmt.Sprintf("Updated retraction status for %s %s", pkgName, version))
}

func (r *PubRegistry) handleDiscontinuePackage(w http.ResponseWriter, req *http.Request) {
	if r.inst.Mode == registry.ModeProxy {
		writePubError(w, http.StatusMethodNotAllowed, "NotAllowed", "discontinuation is only supported for hosted packages")
		return
	}
	pkgName := req.PathValue("package")
	principal, ok := r.authorizeRequest(w, req, auth.PermissionPublish, pkgName)
	if !ok {
		return
	}
	var body struct {
		IsDiscontinued bool   `json:"is_discontinued"`
		ReplacedBy     string `json:"replaced_by"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		writePubError(w, http.StatusBadRequest, "InvalidPayload", "invalid JSON payload")
		return
	}
	pkg, err := r.store.GetPackage(req.Context(), r.inst.ID, pkgName)
	if err != nil {
		writePubError(w, http.StatusNotFound, "NotFound", "package not found")
		return
	}
	if pkg.Origin != storage.OriginHosted {
		writePubError(w, http.StatusBadRequest, "NotHosted", "cannot discontinue a cached upstream package")
		return
	}
	pkg.IsDiscontinued = body.IsDiscontinued
	pkg.ReplacedBy = strings.TrimSpace(body.ReplacedBy)
	pkg.UpdatedAt = time.Now().UTC()
	if err := r.store.UpsertPackage(req.Context(), pkg); err != nil {
		writePubError(w, http.StatusInternalServerError, "ServerError", err.Error())
		return
	}
	r.deps.Audit.Record(req.Context(), audit.Entry{
		ActorID:    principal.ActorID(),
		ActorName:  principal.ActorName(),
		ActorType:  principal.Kind,
		Action:     "artifact.discontinue",
		RegistryID: r.inst.ID,
		Target:     pkgName,
		Success:    true,
	})
	writePubSuccess(w, fmt.Sprintf("Updated discontinued status for %s", pkgName))
}

func (r *PubRegistry) handleListUploaders(w http.ResponseWriter, req *http.Request) {
	pkgName := req.PathValue("package")
	if _, ok := r.authorizeRequest(w, req, auth.PermissionRead, pkgName); !ok {
		return
	}
	pkg, err := r.store.GetPackage(req.Context(), r.inst.ID, pkgName)
	if err != nil {
		writePubError(w, http.StatusNotFound, "NotFound", "package not found")
		return
	}
	uploaders := pkg.Uploaders
	if uploaders == nil {
		uploaders = []string{}
	}
	writePubJSON(w, http.StatusOK, map[string]any{"uploaders": uploaders})
}

func (r *PubRegistry) handleAddUploader(w http.ResponseWriter, req *http.Request) {
	if r.inst.Mode == registry.ModeProxy {
		writePubError(w, http.StatusMethodNotAllowed, "NotAllowed", "uploaders only apply to hosted packages")
		return
	}
	pkgName := req.PathValue("package")
	if _, ok := r.authorizeRequest(w, req, auth.PermissionPublish, pkgName); !ok {
		return
	}
	email := strings.TrimSpace(req.FormValue("email"))
	if email == "" {
		var body struct {
			Email string `json:"email"`
		}
		_ = json.NewDecoder(req.Body).Decode(&body)
		email = strings.TrimSpace(body.Email)
	}
	if email == "" {
		writePubError(w, http.StatusBadRequest, "InvalidUploader", "uploader email is required")
		return
	}
	pkg, err := r.store.GetPackage(req.Context(), r.inst.ID, pkgName)
	if err != nil {
		writePubError(w, http.StatusNotFound, "NotFound", "package not found")
		return
	}
	if !containsString(pkg.Uploaders, email) {
		pkg.Uploaders = append(pkg.Uploaders, email)
		pkg.UpdatedAt = time.Now().UTC()
		_ = r.store.UpsertPackage(req.Context(), pkg)
	}
	writePubSuccess(w, fmt.Sprintf("Added uploader %s to %s", email, pkgName))
}

func (r *PubRegistry) handleRemoveUploader(w http.ResponseWriter, req *http.Request) {
	if r.inst.Mode == registry.ModeProxy {
		writePubError(w, http.StatusMethodNotAllowed, "NotAllowed", "uploaders only apply to hosted packages")
		return
	}
	pkgName := req.PathValue("package")
	email := strings.TrimSpace(req.PathValue("email"))
	if _, ok := r.authorizeRequest(w, req, auth.PermissionPublish, pkgName); !ok {
		return
	}
	pkg, err := r.store.GetPackage(req.Context(), r.inst.ID, pkgName)
	if err != nil {
		writePubError(w, http.StatusNotFound, "NotFound", "package not found")
		return
	}
	next := make([]string, 0, len(pkg.Uploaders))
	for _, u := range pkg.Uploaders {
		if u != email {
			next = append(next, u)
		}
	}
	if len(next) == 0 {
		writePubError(w, http.StatusBadRequest, "LastUploader", "cannot remove the last uploader of a package")
		return
	}
	pkg.Uploaders = next
	pkg.UpdatedAt = time.Now().UTC()
	_ = r.store.UpsertPackage(req.Context(), pkg)
	writePubSuccess(w, fmt.Sprintf("Removed uploader %s from %s", email, pkgName))
}

func (r *PubRegistry) failUpload(w http.ResponseWriter, _ *http.Request, uploadID string, status int, code, msg string) {
	r.uploadMu.Lock()
	if s, ok := r.uploads[uploadID]; ok {
		s.ErrMessage = msg
	}
	r.uploadMu.Unlock()
	writePubError(w, status, code, msg)
}

func (r *PubRegistry) pruneExpiredUploadsLocked(now time.Time) {
	for id, s := range r.uploads {
		if now.After(s.ExpiresAt) {
			delete(r.uploads, id)
		}
	}
}

func uploaderIdentity(p *auth.Principal) string {
	if p == nil {
		return "system"
	}
	if p.User != nil {
		if p.User.Email != "" {
			return p.User.Email
		}
		return p.User.Username
	}
	return p.ActorName()
}

func containsString(slice []string, target string) bool {
	for _, s := range slice {
		if s == target {
			return true
		}
	}
	return false
}

func newUploadID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
