package pypi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/amkargar/depyard/backend/internal/registry"
	"github.com/amkargar/depyard/backend/internal/storage"
	"github.com/amkargar/depyard/backend/internal/upstream"
)

// RegistryType is the identifier of the PyPI / Python registry type.
const RegistryType = "pypi"

// defaultMaxFileSizeBytes is the default maximum distribution file size (100 MiB).
const defaultMaxFileSizeBytes int64 = 100 * 1024 * 1024

// Config holds validated PyPI-specific registry settings stored inside Instance.Config.
type Config struct {
	// UpstreamTimeoutSeconds bounds upstream index and file requests.
	UpstreamTimeoutSeconds int `json:"upstream_timeout_seconds,omitempty"`
	// MaxFileSizeBytes bounds a single uploaded distribution file.
	MaxFileSizeBytes int64 `json:"max_file_size_bytes,omitempty"`
	// IncludePreRelease serves pre-release versions to clients that do not opt in.
	IncludePreRelease bool `json:"include_prerelease"`
	// AllowedProjects is an allow-list of project name patterns (empty = allow all).
	AllowedProjects []string `json:"allowed_projects,omitempty"`
	// BlockedProjects is a deny-list of project name patterns.
	BlockedProjects []string `json:"blocked_projects,omitempty"`
	// ProtectedProjects are never served from upstream in hybrid mode.
	ProtectedProjects []string `json:"protected_projects,omitempty"`
	// RequireUploadHash rejects uploads that do not carry a digest.
	RequireUploadHash bool `json:"require_upload_hash"`
	// ServeCoreMetadata enables the PEP 658 ".metadata" endpoints.
	ServeCoreMetadata bool `json:"serve_core_metadata"`
}

// DefaultConfig returns the default PyPI registry configuration.
func DefaultConfig() Config {
	return Config{
		UpstreamTimeoutSeconds: 60,
		MaxFileSizeBytes:       defaultMaxFileSizeBytes,
		IncludePreRelease:      false,
		RequireUploadHash:      true,
		ServeCoreMetadata:      true,
	}
}

// ParseConfig validates and normalizes a raw instance configuration map.
func ParseConfig(raw map[string]any) (Config, error) {
	cfg := DefaultConfig()
	if len(raw) == 0 {
		return cfg, nil
	}

	allowedKeys := map[string]bool{
		"upstream_timeout_seconds": true,
		"max_file_size_bytes":      true,
		"include_prerelease":       true,
		"allowed_projects":         true,
		"blocked_projects":         true,
		"protected_projects":       true,
		"require_upload_hash":      true,
		"serve_core_metadata":      true,
	}
	for k := range raw {
		if !allowedKeys[k] {
			return cfg, fmt.Errorf("unknown pypi config field %q", k)
		}
	}

	b, err := json.Marshal(raw)
	if err != nil {
		return cfg, fmt.Errorf("encode pypi config: %w", err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("invalid pypi config: %w", err)
	}

	if cfg.UpstreamTimeoutSeconds < 0 || cfg.UpstreamTimeoutSeconds > 600 {
		return cfg, fmt.Errorf("upstream_timeout_seconds must be between 0 and 600")
	}
	if cfg.UpstreamTimeoutSeconds == 0 {
		cfg.UpstreamTimeoutSeconds = 60
	}
	if cfg.MaxFileSizeBytes < 0 || cfg.MaxFileSizeBytes > 1024*1024*1024 {
		return cfg, fmt.Errorf("max_file_size_bytes must be between 0 and 1 GiB")
	}
	if cfg.MaxFileSizeBytes == 0 {
		cfg.MaxFileSizeBytes = defaultMaxFileSizeBytes
	}
	return cfg, nil
}

// Capabilities returns the declared capabilities of the PyPI registry type.
func Capabilities() registry.Capabilities {
	return registry.Capabilities{
		Proxy:                 true,
		Hosted:                true,
		Publish:               true,
		Search:                false, // PyPI has no standard search protocol.
		ListVersions:          true,
		CacheWarm:             true,
		MetadataSync:          true,
		ArtifactDeletion:      true,
		VersionRetraction:     true, // PEP 592 yanking.
		PackageOwnership:      true,
		SignatureVerification: true, // Digest verification on upload.
	}
}

// Registration returns the in-process registration descriptor used by the composition root.
func Registration(store MetadataStore) registry.Registration {
	return registry.Registration{
		Type:        RegistryType,
		DisplayName: "Python Package Index (PyPI)",
		Description: "Python Simple Repository API (PEP 503/691) with a PEP 592 yanking, " +
			"PEP 658 core metadata, and a Twine-compatible legacy upload endpoint. " +
			"Supports proxy, hosted and hybrid modes.",
		DefaultUpstreamURL: "https://pypi.org",
		Capabilities:       Capabilities(),
		ConfigHints: []registry.ConfigFieldHint{
			{
				Key:          "upstream_timeout_seconds",
				Label:        "Upstream Timeout (seconds)",
				Type:         "number",
				Description:  "Timeout for upstream index and file requests (1-600s).",
				DefaultValue: 60,
			},
			{
				Key:          "max_file_size_bytes",
				Label:        "Max Distribution Size (bytes)",
				Type:         "number",
				Description:  "Maximum allowed size of a single uploaded distribution file (default 100 MiB).",
				DefaultValue: defaultMaxFileSizeBytes,
			},
			{
				Key:          "include_prerelease",
				Label:        "Include Pre-release Versions",
				Type:         "boolean",
				Description:  "Serve alpha, beta, rc and dev releases to clients that do not opt in.",
				DefaultValue: false,
			},
			{
				Key:          "require_upload_hash",
				Label:        "Require Upload Digest",
				Type:         "boolean",
				Description:  "Reject uploads that do not provide a sha256/md5 digest.",
				DefaultValue: true,
			},
			{
				Key:          "serve_core_metadata",
				Label:        "Serve Core Metadata (PEP 658)",
				Type:         "boolean",
				Description:  "Expose the .metadata sidecar files and the dist-info-metadata attributes.",
				DefaultValue: true,
			},
			{
				Key:         "allowed_projects",
				Label:       "Allowed Project Patterns",
				Type:        "string_list",
				Description: "Allow-list of project name patterns (e.g. acme-*). Empty allows every project.",
			},
			{
				Key:         "blocked_projects",
				Label:       "Blocked Project Patterns",
				Type:        "string_list",
				Description: "Project patterns denied from being mirrored or published.",
			},
			{
				Key:         "protected_projects",
				Label:       "Protected Local Projects",
				Type:        "string_list",
				Description: "Project name patterns (e.g. acme_*) that are never mirrored from upstream in hybrid mode.",
			},
		},
		ValidateConfig: func(cfg map[string]any) error {
			_, err := ParseConfig(cfg)
			return err
		},
		Create: func(_ context.Context, inst *registry.Instance, deps registry.ServiceDeps) (registry.Registry, error) {
			return New(inst, deps, store)
		},
	}
}

// PyPIRegistry implements the Python Simple Repository API for a single configured instance.
type PyPIRegistry struct {
	inst    *registry.Instance
	cfg     Config
	deps    registry.ServiceDeps
	store   MetadataStore
	handler http.Handler

	uploadMu sync.Mutex
	uploads  map[string]*uploadSession
}

// New builds a PyPI registry for the given instance.
func New(inst *registry.Instance, deps registry.ServiceDeps, store MetadataStore) (*PyPIRegistry, error) {
	cfg, err := ParseConfig(inst.Config)
	if err != nil {
		return nil, err
	}
	if store == nil {
		store = NewMemoryStore()
	}
	r := &PyPIRegistry{
		inst:    inst.Clone(),
		cfg:     cfg,
		deps:    deps,
		store:   store,
		uploads: make(map[string]*uploadSession),
	}
	r.handler = r.buildHandler()
	return r, nil
}

// Type returns the registry type identifier.
func (r *PyPIRegistry) Type() string { return RegistryType }

// Capabilities returns the effective capabilities of this instance.
func (r *PyPIRegistry) Capabilities() registry.Capabilities {
	return Capabilities().EffectiveForMode(r.inst.Mode)
}

// Handler returns the HTTP handler serving the registry protocol.
func (r *PyPIRegistry) Handler() http.Handler { return r.handler }

// upstreamTimeout returns the configured upstream request timeout.
func (r *PyPIRegistry) upstreamTimeout() time.Duration {
	if r.cfg.UpstreamTimeoutSeconds > 0 {
		return time.Duration(r.cfg.UpstreamTimeoutSeconds) * time.Second
	}
	return 60 * time.Second
}

// allowsProject reports whether a project may be served or published.
func (r *PyPIRegistry) allowsProject(name string) bool {
	normalized := NormalizeProjectName(name)
	if len(r.cfg.AllowedProjects) > 0 {
		allowed := false
		for _, pattern := range r.cfg.AllowedProjects {
			if matchProjectPattern(pattern, normalized) {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	for _, pattern := range r.cfg.BlockedProjects {
		if matchProjectPattern(pattern, normalized) {
			return false
		}
	}
	return true
}

// isProtected reports whether a project is protected from upstream mirroring.
func (r *PyPIRegistry) isProtected(name string) bool {
	normalized := NormalizeProjectName(name)
	for _, pattern := range r.cfg.ProtectedProjects {
		if matchProjectPattern(pattern, normalized) {
			return true
		}
	}
	return false
}

// canProxy reports whether this instance may mirror a project from upstream.
func (r *PyPIRegistry) canProxy(name string) bool {
	if r.inst.Mode == registry.ModeHosted {
		return false
	}
	if !r.allowsProject(name) {
		return false
	}
	if r.inst.Mode == registry.ModeHybrid && r.isProtected(name) {
		return false
	}
	return true
}

// canPublish reports whether this instance accepts uploads.
func (r *PyPIRegistry) canPublish() bool {
	return r.inst.Mode != registry.ModeProxy
}

// artifactKey builds the storage key of a distribution file.
func artifactKey(project, version, filename string) string {
	return "pypi/" + NormalizeProjectName(project) + "/" + version + "/" + filename
}

// metadataArtifactKey builds the storage key of a PEP 658 core metadata file.
func metadataArtifactKey(project, version, filename string) string {
	return "pypi/" + NormalizeProjectName(project) + "/" + version + "/" + filename + ".metadata"
}

// baseURL returns the absolute base URL of this registry instance.
func (r *PyPIRegistry) baseURL(req *http.Request) string {
	if v := registry.BaseURLFromContext(req.Context()); v != "" {
		return v
	}
	return strings.TrimRight(r.inst.UpstreamURL, "/")
}

// artifactKeyFor returns the storage key of a distribution file.
func (r *PyPIRegistry) artifactKeyFor(project, version, filename string) string {
	return artifactKey(project, version, filename)
}

// storeArtifact writes a distribution file into the content-addressed store.
func (r *PyPIRegistry) storeArtifact(ctx context.Context, key string, reader interface{ Read([]byte) (int, error) }, digest string, origin string) (*storage.ArtifactRef, error) {
	res, err := r.deps.Storage.PutArtifact(ctx, storage.PutArtifactInput{
		RegistryID:       r.inst.ID,
		ArtifactKey:      key,
		Origin:           origin,
		ExpectedDigest:   digest,
		MaxArtifactBytes: r.cfg.MaxFileSizeBytes,
		RegistryQuota:    r.inst.StorageQuotaBytes,
		Reader:           reader,
	})
	if err != nil {
		return nil, err
	}
	return res.Ref, nil
}

// ListArtifacts implements registry.ArtifactBrowser for the admin console.
func (r *PyPIRegistry) ListArtifacts(ctx context.Context, q registry.ArtifactQuery) (registry.ArtifactPage, error) {
	projects, total, err := r.store.ListProjects(ctx, ProjectListQuery{
		RegistryID: r.inst.ID,
		Search:     q.Search,
		Origin:     q.Origin,
		Page:       q.Page,
		Limit:      q.Limit,
	})
	if err != nil {
		return registry.ArtifactPage{}, err
	}
	items := make([]registry.ArtifactDescriptor, 0, len(projects))
	for _, p := range projects {
		items = append(items, registry.ArtifactDescriptor{
			ID:           p.ID,
			RegistryID:   p.RegistryID,
			Name:         p.Name,
			Version:      p.LatestVersion,
			VersionCount: p.ReleaseCount,
			SizeBytes:    p.TotalSize,
			Origin:       p.Origin,
			Description:  p.Summary,
			Downloads:    p.Downloads,
			CreatedAt:    p.CreatedAt,
			UpdatedAt:    p.UpdatedAt,
		})
	}
	return registry.ArtifactPage{Items: items, Total: total, Page: q.Page, Limit: q.Limit}, nil
}

// DeleteArtifact implements registry.ArtifactDeleter.
//
// A project name deletes the whole project; a "project==version" name deletes a
// single release (and all of its distribution files).
func (r *PyPIRegistry) DeleteArtifact(ctx context.Context, name, version string) error {
	project := name
	relVersion := version
	if project == "" {
		return fmt.Errorf("project name is required")
	}
	// The admin API passes "name" and "version" separately, but the generic
	// descriptor may carry a combined "project/version" identifier.
	if relVersion == "" && strings.Contains(project, "/") {
		parts := strings.SplitN(project, "/", 2)
		project, relVersion = parts[0], parts[1]
	}
	normalized := NormalizeProjectName(project)

	if relVersion != "" {
		files, _, err := r.store.ListFiles(ctx, FileListQuery{
			RegistryID:  r.inst.ID,
			ProjectName: normalized,
			Version:     relVersion,
			IncludePre:  true,
		})
		if err != nil {
			return err
		}
		for _, f := range files {
			if err := r.deleteFileArtifacts(ctx, f); err != nil {
				return err
			}
			if err := r.store.DeleteFile(ctx, r.inst.ID, normalized, f.Version, f.Filename); err != nil {
				return err
			}
		}
		if err := r.store.DeleteRelease(ctx, r.inst.ID, normalized, relVersion); err != nil {
			return err
		}
		return r.refreshProjectStats(ctx, normalized)
	}

	allFiles, _, err := r.store.ListFiles(ctx, FileListQuery{
		RegistryID:  r.inst.ID,
		ProjectName: normalized,
		IncludePre:  true,
	})
	if err != nil {
		return err
	}
	for _, f := range allFiles {
		if err := r.deleteFileArtifacts(ctx, f); err != nil {
			return err
		}
	}
	if err := r.store.DeleteProject(ctx, r.inst.ID, normalized); err != nil {
		return err
	}
	return nil
}

// deleteFileArtifacts removes the stored blobs of a distribution file.
func (r *PyPIRegistry) deleteFileArtifacts(ctx context.Context, f *PyFile) error {
	if err := r.deps.Storage.DeleteArtifactRef(ctx, r.inst.ID, artifactKey(f.ProjectName, f.Version, f.Filename)); err != nil {
		return err
	}
	if f.HasMetadata {
		_ = r.deps.Storage.DeleteArtifactRef(ctx, r.inst.ID, metadataArtifactKey(f.ProjectName, f.Version, f.Filename))
	}
	return nil
}

// PurgeCache implements registry.CachePurger.
func (r *PyPIRegistry) PurgeCache(ctx context.Context) (storage.EvictResult, error) {
	res, err := r.deps.Cache.PurgeRegistry(ctx, r.inst.ID)
	if err != nil {
		return storage.EvictResult{}, err
	}
	if err := r.store.DeleteCachedProjects(ctx, r.inst.ID); err != nil {
		return res, err
	}
	return res, nil
}

// CheckHealth implements registry.HealthChecker.
func (r *PyPIRegistry) CheckHealth(ctx context.Context) upstream.HealthCheckResult {
	if r.inst.Mode == registry.ModeHosted {
		return upstream.HealthCheckResult{Available: true, CheckedAt: time.Now().UTC()}
	}
	base := strings.TrimRight(r.inst.UpstreamURL, "/")
	if base == "" {
		return upstream.HealthCheckResult{Available: false, Error: "no upstream URL configured", CheckedAt: time.Now().UTC()}
	}
	target := base + "/simple/"
	opts := r.inst.UpstreamRequestOptions(map[string]string{
		"Accept": "application/vnd.pypi.simple.v1+json, text/html;q=0.01",
	}, r.upstreamTimeout())
	return r.deps.Upstream.CheckHealthWithOptions(ctx, target, opts)
}

// MatchesCompatPath implements registry.CompatPathMatcher.
//
// It recognises the PEP 503 and PEP 691 paths so the platform can route legacy
// client requests such as "/simple/requests/" to this registry.
func (r *PyPIRegistry) MatchesCompatPath(p string) bool {
	trimmed := strings.TrimPrefix(p, "/")
	switch {
	case trimmed == "simple", trimmed == "simple/":
		return true
	case strings.HasPrefix(trimmed, "simple/"):
		return true
	case trimmed == "pypi" || trimmed == "pypi/":
		return true
	case strings.HasPrefix(trimmed, "packages/"):
		return true
	}
	return false
}

// CleanupRegistry implements registry.RegistryCleaner.
func (r *PyPIRegistry) CleanupRegistry(ctx context.Context) error {
	return r.store.DeleteRegistryMetadata(ctx, r.inst.ID)
}

// refreshProjectStats recomputes the aggregate counters of a project.
func (r *PyPIRegistry) refreshProjectStats(ctx context.Context, project string) error {
	project = NormalizeProjectName(project)
	p, err := r.store.GetProject(ctx, r.inst.ID, project)
	if err != nil {
		return nil // The project was removed; nothing to refresh.
	}
	releases, _, err := r.store.ListReleases(ctx, ReleaseListQuery{
		RegistryID:  r.inst.ID,
		ProjectName: project,
		IncludePre:  true,
	})
	if err != nil {
		return err
	}
	files, _, err := r.store.ListFiles(ctx, FileListQuery{
		RegistryID:  r.inst.ID,
		ProjectName: project,
		IncludePre:  true,
	})
	if err != nil {
		return err
	}
	var totalSize int64
	for _, f := range files {
		totalSize += f.SizeBytes
	}
	p.ReleaseCount = len(releases)
	p.FileCount = len(files)
	p.TotalSize = totalSize
	if len(releases) > 0 {
		latest := releases[len(releases)-1]
		p.LatestVersion = latest.Version
	}
	p.UpdatedAt = time.Now().UTC()
	return r.store.UpsertProject(ctx, p)
}
