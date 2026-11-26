// Package npm implements the npm Registry HTTP API for a single Depyard
// registry instance. It is intentionally self-contained: shared core packages
// never import it, and the composition root registers it like any other type.
package npm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/amkargar/depyard/backend/internal/registry"
	"github.com/amkargar/depyard/backend/internal/storage"
)

// RegistryType is the identifier of the npm registry type.
const RegistryType = "npm"

// ContentTypeJSON is the regular npm packument content type.
const ContentTypeJSON = "application/json"

// ContentTypeAbbreviated is the install-optimized packument content type used
// by npm, yarn and pnpm when resolving a package for installation.
const ContentTypeAbbreviated = "application/vnd.npm.install-v1+json"

const defaultMaxTarballSizeBytes int64 = 100 * 1024 * 1024

// Config holds validated npm-specific settings stored in Instance.Config.
type Config struct {
	UpstreamTimeoutSeconds int      `json:"upstream_timeout_seconds,omitempty"`
	MaxTarballSizeBytes    int64    `json:"max_tarball_size_bytes,omitempty"`
	AllowedPackages        []string `json:"allowed_packages,omitempty"`
	BlockedPackages        []string `json:"blocked_packages,omitempty"`
	ProtectedPackages      []string `json:"protected_packages,omitempty"`
	IncludePrerelease      bool     `json:"include_prerelease"`
	VerifyIntegrity        bool     `json:"verify_integrity"`
}

func DefaultConfig() Config {
	return Config{
		UpstreamTimeoutSeconds: 60,
		MaxTarballSizeBytes:    defaultMaxTarballSizeBytes,
		IncludePrerelease:      false,
		VerifyIntegrity:        true,
	}
}

// ParseConfig validates and normalizes a raw instance configuration map.
// Unknown keys are rejected so typos in the Admin API cannot silently change behaviour.
func ParseConfig(raw map[string]any) (Config, error) {
	cfg := DefaultConfig()
	if len(raw) == 0 {
		return cfg, nil
	}
	allowed := map[string]bool{
		"upstream_timeout_seconds": true,
		"max_tarball_size_bytes":   true,
		"allowed_packages":         true,
		"blocked_packages":         true,
		"protected_packages":       true,
		"include_prerelease":       true,
		"verify_integrity":         true,
	}
	for k := range raw {
		if !allowed[k] {
			return cfg, fmt.Errorf("unknown npm config field %q", k)
		}
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return cfg, fmt.Errorf("encode npm config: %w", err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("invalid npm config: %w", err)
	}
	if cfg.UpstreamTimeoutSeconds < 0 || cfg.UpstreamTimeoutSeconds > 600 {
		return cfg, fmt.Errorf("upstream_timeout_seconds must be between 0 and 600")
	}
	if cfg.UpstreamTimeoutSeconds == 0 {
		cfg.UpstreamTimeoutSeconds = 60
	}
	if cfg.MaxTarballSizeBytes < 0 || cfg.MaxTarballSizeBytes > 1024*1024*1024 {
		return cfg, fmt.Errorf("max_tarball_size_bytes must be between 0 and 1 GiB")
	}
	if cfg.MaxTarballSizeBytes == 0 {
		cfg.MaxTarballSizeBytes = defaultMaxTarballSizeBytes
	}
	return cfg, nil
}

// Capabilities declares what npm genuinely supports here.
func Capabilities() registry.Capabilities {
	return registry.Capabilities{
		Proxy:                 true,
		Hosted:                true,
		Publish:               true,
		Search:                true,
		ListVersions:          true,
		CacheWarm:             true,
		MetadataSync:          true,
		ArtifactDeletion:      true,
		VersionRetraction:     true,  // npm unpublish / version removal
		PackageOwnership:      false, // access control is token-based, not per-package ACLs
		SignatureVerification: true,  // shasum / integrity checks on publish
	}
}

// Registration returns the in-process registration descriptor for the composition root.
func Registration(store MetadataStore) registry.Registration {
	if store == nil {
		store = NewMemoryStore()
	}
	return registry.Registration{
		Type:               RegistryType,
		DisplayName:        "npm Registry",
		Description:        "npm Registry HTTP API with packuments, scoped packages, tarball caching and npm-compatible publishing. Supports proxy, hosted and hybrid modes.",
		DefaultUpstreamURL: "https://registry.npmjs.org",
		Capabilities:       Capabilities(),
		ConfigHints: []registry.ConfigFieldHint{
			{
				Key: "upstream_timeout_seconds", Label: "Upstream Timeout (seconds)", Type: "number",
				Description: "Timeout for upstream packument and tarball requests (1-600s).", DefaultValue: 60,
			},
			{
				Key: "max_tarball_size_bytes", Label: "Max Tarball Size (bytes)", Type: "number",
				Description: "Maximum size of a published or cached .tgz file (default 100 MiB).", DefaultValue: defaultMaxTarballSizeBytes,
			},
			{
				Key: "include_prerelease", Label: "Allow Pre-release 'latest'", Type: "boolean",
				Description: "Allow a pre-release version to become the automatically selected latest dist-tag.", DefaultValue: false,
			},
			{
				Key: "verify_integrity", Label: "Verify Upload Integrity", Type: "boolean",
				Description: "Reject publishes whose shasum or integrity digest does not match the uploaded tarball.", DefaultValue: true,
			},
			{
				Key: "allowed_packages", Label: "Allowed Package Patterns", Type: "string_list",
				Description: "Allow-list of package name patterns (e.g. @acme/*). Empty allows every package.",
			},
			{
				Key: "blocked_packages", Label: "Blocked Package Patterns", Type: "string_list",
				Description: "Package patterns denied from being mirrored or published.",
			},
			{
				Key: "protected_packages", Label: "Protected Local Packages", Type: "string_list",
				Description: "Package patterns never mirrored from upstream in hybrid mode.",
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

// NpmRegistry serves one configured npm registry instance.
type NpmRegistry struct {
	inst    *registry.Instance
	cfg     Config
	deps    registry.ServiceDeps
	store   MetadataStore
	handler http.Handler

	publishMu     sync.Mutex
	tarballFlight singleflight.Group
}

func New(inst *registry.Instance, deps registry.ServiceDeps, store MetadataStore) (*NpmRegistry, error) {
	if inst == nil {
		return nil, fmt.Errorf("npm: instance is required")
	}
	cfg, err := ParseConfig(inst.Config)
	if err != nil {
		return nil, err
	}
	if store == nil {
		store = NewMemoryStore()
	}
	r := &NpmRegistry{
		inst:  inst.Clone(),
		cfg:   cfg,
		deps:  deps,
		store: store,
	}
	r.handler = r.buildHandler()
	return r, nil
}

func (r *NpmRegistry) Type() string { return RegistryType }

func (r *NpmRegistry) Capabilities() registry.Capabilities {
	return Capabilities().EffectiveForMode(r.inst.Mode)
}

func (r *NpmRegistry) Handler() http.Handler { return r.handler }

func (r *NpmRegistry) upstreamTimeout() time.Duration {
	return time.Duration(r.cfg.UpstreamTimeoutSeconds) * time.Second
}

// baseURL is the externally visible base path of this instance (injected by the
// router). All dist.tarball URLs in served packuments point back here.
func (r *NpmRegistry) baseURL(req *http.Request) string {
	if v := registry.BaseURLFromContext(req.Context()); v != "" {
		return strings.TrimRight(v, "/")
	}
	return strings.TrimRight(r.inst.UpstreamURL, "/")
}

func (r *NpmRegistry) allowsPackage(name string) bool {
	name = NormalizePackageName(name)
	if len(r.cfg.AllowedPackages) > 0 {
		allowed := false
		for _, pattern := range r.cfg.AllowedPackages {
			if matchPackagePattern(pattern, name) {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	for _, pattern := range r.cfg.BlockedPackages {
		if matchPackagePattern(pattern, name) {
			return false
		}
	}
	return true
}

func (r *NpmRegistry) canProxy(name string) bool {
	if r.inst.Mode == registry.ModeHosted || !r.allowsPackage(name) {
		return false
	}
	if r.inst.Mode == registry.ModeHybrid {
		for _, pattern := range r.cfg.ProtectedPackages {
			if matchPackagePattern(pattern, name) {
				return false
			}
		}
	}
	return true
}

func (r *NpmRegistry) canPublish() bool { return r.inst.Mode != registry.ModeProxy }

func (r *NpmRegistry) canDelete() bool { return r.inst.Mode != registry.ModeProxy }

// artifactKey is the opaque, registry-scoped blob key of a tarball.
func artifactKey(name, version, filename string) string {
	return "npm/" + NormalizePackageName(name) + "/" + version + "/" + filename
}

// tarballURL builds the client-facing download URL of a version.
func tarballURL(baseURL, name, version, filename string) string {
	if filename == "" {
		filename = defaultTarballFilename(name, version)
	}
	return baseURL + "/" + escapePackagePath(name) + "/-/" + url.PathEscape(filename)
}

// upstreamPackumentURL builds the upstream request URL for a package.
func upstreamPackumentURL(base, name string) string {
	return strings.TrimRight(base, "/") + "/" + escapePackagePath(name)
}

// storeArtifact writes a tarball into the shared content-addressed blob store.
func (r *NpmRegistry) storeArtifact(ctx context.Context, key, digest string, maxBytes int64, reader io.Reader, origin string) (*storage.ArtifactRef, error) {
	res, err := r.deps.Storage.PutArtifact(ctx, storage.PutArtifactInput{
		RegistryID:       r.inst.ID,
		ArtifactKey:      key,
		Origin:           origin,
		ExpectedDigest:   digest,
		MaxArtifactBytes: maxBytes,
		RegistryQuota:    r.inst.StorageQuotaBytes,
		Reader:           reader,
	})
	if err != nil {
		return nil, err
	}
	return res.Ref, nil
}

func newID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
