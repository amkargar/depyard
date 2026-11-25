package registry

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/amkargar/mirror-registry/backend/internal/audit"
	"github.com/amkargar/mirror-registry/backend/internal/auth"
	"github.com/amkargar/mirror-registry/backend/internal/cache"
	"github.com/amkargar/mirror-registry/backend/internal/metrics"
	"github.com/amkargar/mirror-registry/backend/internal/storage"
	"github.com/amkargar/mirror-registry/backend/internal/upstream"
)

var (
	ErrInstanceNotFound  = errors.New("registry instance not found")
	ErrSlugExists        = errors.New("registry slug already exists")
	ErrTypeNotRegistered = errors.New("registry type is not installed")
	ErrRegistryDisabled  = errors.New("registry instance is disabled")
	ErrUnsupportedOp     = errors.New("operation not supported by this registry")
	ErrInvalidConfig     = errors.New("invalid registry configuration")
)

// UpstreamConfig holds optional per-registry upstream authentication, proxy, TLS, and timeout overrides.
type UpstreamConfig struct {
	AuthType           string            `json:"auth_type,omitempty" bson:"auth_type,omitempty"` // "", "none", "bearer", "basic", "header"
	AuthHeader         string            `json:"auth_header,omitempty" bson:"auth_header,omitempty"`
	AuthUsername       string            `json:"auth_username,omitempty" bson:"auth_username,omitempty"`
	AuthToken          string            `json:"auth_token,omitempty" bson:"auth_token,omitempty"`
	HasAuthSecret      bool              `json:"has_auth_secret,omitempty" bson:"-"`
	ProxyURL           string            `json:"proxy_url,omitempty" bson:"proxy_url,omitempty"`
	TimeoutSeconds     int               `json:"timeout_seconds,omitempty" bson:"timeout_seconds,omitempty"`
	InsecureSkipVerify bool              `json:"insecure_skip_verify,omitempty" bson:"insecure_skip_verify,omitempty"`
	CustomHeaders      map[string]string `json:"custom_headers,omitempty" bson:"custom_headers,omitempty"`
}

func (u *UpstreamConfig) Clone() *UpstreamConfig {
	if u == nil {
		return nil
	}
	cp := *u
	if u.CustomHeaders != nil {
		cp.CustomHeaders = make(map[string]string, len(u.CustomHeaders))
		for k, v := range u.CustomHeaders {
			cp.CustomHeaders[k] = v
		}
	}
	return &cp
}

// Sanitized returns a copy safe for Admin API responses (never leaks stored AuthToken).
func (u *UpstreamConfig) Sanitized() *UpstreamConfig {
	if u == nil {
		return nil
	}
	cp := u.Clone()
	if cp.AuthToken != "" {
		cp.HasAuthSecret = true
		cp.AuthToken = ""
	}
	return cp
}

// Validate normalizes and validates per-registry upstream overrides.
func (u *UpstreamConfig) Validate() error {
	if u == nil {
		return nil
	}
	u.AuthType = strings.ToLower(strings.TrimSpace(u.AuthType))
	u.AuthHeader = strings.TrimSpace(u.AuthHeader)
	u.AuthUsername = strings.TrimSpace(u.AuthUsername)
	u.ProxyURL = strings.TrimSpace(u.ProxyURL)
	switch u.AuthType {
	case "", "none":
		u.AuthType = ""
		u.AuthHeader = ""
		u.AuthUsername = ""
		u.AuthToken = ""
	case "bearer", "basic":
		// valid
	case "header":
		if u.AuthHeader == "" {
			return fmt.Errorf("%w: upstream_config.auth_header is required when auth_type is header", ErrInvalidConfig)
		}
	default:
		return fmt.Errorf("%w: unsupported upstream_config.auth_type %q", ErrInvalidConfig, u.AuthType)
	}
	if u.ProxyURL != "" {
		if _, err := upstream.ValidateProxyURL(u.ProxyURL); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidConfig, err)
		}
	}
	if u.TimeoutSeconds < 0 || u.TimeoutSeconds > 3600 {
		return fmt.Errorf("%w: upstream_config.timeout_seconds must be between 0 and 3600", ErrInvalidConfig)
	}
	return nil
}

// BuildRequestOptions merges per-registry upstream config with caller-provided headers and fallback timeout.
func (u *UpstreamConfig) BuildRequestOptions(extraHeaders map[string]string, fallbackTimeout time.Duration) upstream.RequestOptions {
	headers := make(map[string]string)
	if u != nil {
		for k, v := range u.CustomHeaders {
			if strings.TrimSpace(k) != "" {
				headers[k] = v
			}
		}
		switch strings.ToLower(strings.TrimSpace(u.AuthType)) {
		case "bearer":
			if u.AuthToken != "" {
				headers["Authorization"] = "Bearer " + u.AuthToken
			}
		case "basic":
			if u.AuthUsername != "" || u.AuthToken != "" {
				cred := base64.StdEncoding.EncodeToString([]byte(u.AuthUsername + ":" + u.AuthToken))
				headers["Authorization"] = "Basic " + cred
			}
		case "header":
			if strings.TrimSpace(u.AuthHeader) != "" && u.AuthToken != "" {
				headers[strings.TrimSpace(u.AuthHeader)] = u.AuthToken
			}
		}
	}
	for k, v := range extraHeaders {
		if strings.TrimSpace(k) != "" {
			headers[k] = v
		}
	}

	opts := upstream.RequestOptions{
		Headers: headers,
		Timeout: fallbackTimeout,
	}
	if u != nil {
		opts.ProxyURL = strings.TrimSpace(u.ProxyURL)
		opts.InsecureSkipVerify = u.InsecureSkipVerify
		if u.TimeoutSeconds > 0 {
			opts.Timeout = time.Duration(u.TimeoutSeconds) * time.Second
		}
	}
	return opts
}

// Instance represents a configured repository backed by an installed registry type.
// Stored in the shared `registries` MongoDB collection.
type Instance struct {
	ID                string          `json:"id" bson:"_id"`
	Name              string          `json:"name" bson:"name"`
	Slug              string          `json:"slug" bson:"slug"`
	Type              string          `json:"type" bson:"type"`
	Description       string          `json:"description" bson:"description"`
	Enabled           bool            `json:"enabled" bson:"enabled"`
	Mode              RepositoryMode  `json:"mode" bson:"mode"`
	UpstreamURL       string          `json:"upstream_url,omitempty" bson:"upstream_url,omitempty"`
	UpstreamConfig    *UpstreamConfig `json:"upstream_config,omitempty" bson:"upstream_config,omitempty"`
	Visibility        Visibility      `json:"visibility" bson:"visibility"`
	AuthPolicy        AuthPolicy      `json:"auth_policy" bson:"auth_policy"`
	StorageQuotaBytes int64           `json:"storage_quota_bytes" bson:"storage_quota_bytes"`
	CachePolicy       CachePolicy     `json:"cache_policy" bson:"cache_policy"`
	Config            map[string]any  `json:"config,omitempty" bson:"config,omitempty"`
	IsDefault         bool            `json:"is_default" bson:"is_default"`
	Hostname          string          `json:"hostname,omitempty" bson:"hostname,omitempty"` // Optional dedicated hostname for protocols needing root host routing
	CreatedAt         time.Time       `json:"created_at" bson:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at" bson:"updated_at"`
}

// AccessPolicy returns the shared auth policy descriptor for this instance.
func (i *Instance) AccessPolicy() auth.RegistryAccessPolicy {
	return auth.RegistryAccessPolicy{
		RegistryID:         i.ID,
		RegistrySlug:       i.Slug,
		RegistryType:       i.Type,
		Public:             i.Visibility == VisibilityPublic,
		AllowAnonymousRead: i.AuthPolicy.AllowAnonymousRead,
	}
}

// UpstreamRequestOptions builds upstream.RequestOptions combining instance upstream settings and extra headers.
func (i *Instance) UpstreamRequestOptions(extraHeaders map[string]string, fallbackTimeout time.Duration) upstream.RequestOptions {
	if i == nil {
		return upstream.RequestOptions{Headers: extraHeaders, Timeout: fallbackTimeout}
	}
	return i.UpstreamConfig.BuildRequestOptions(extraHeaders, fallbackTimeout)
}

// Clone returns a deep copy of the instance, its UpstreamConfig, and its Config map.
func (i *Instance) Clone() *Instance {
	if i == nil {
		return nil
	}
	cp := *i
	cp.UpstreamConfig = i.UpstreamConfig.Clone()
	if i.Config != nil {
		cp.Config = make(map[string]any, len(i.Config))
		for k, v := range i.Config {
			cp.Config[k] = v
		}
	}
	return &cp
}

// SanitizedClone returns a clone with upstream secrets masked for API responses.
func (i *Instance) SanitizedClone() *Instance {
	if i == nil {
		return nil
	}
	cp := i.Clone()
	cp.UpstreamConfig = cp.UpstreamConfig.Sanitized()
	return cp
}

// Registry is the minimal contract required to register and serve an ecosystem implementation.
type Registry interface {
	Type() string
	Capabilities() Capabilities
	Handler() http.Handler
}

// ArtifactDescriptor is a small shared view model used only by the registry-agnostic admin UI.
type ArtifactDescriptor struct {
	ID           string    `json:"id"`
	RegistryID   string    `json:"registry_id"`
	Name         string    `json:"name"`
	Version      string    `json:"version,omitempty"`
	VersionCount int       `json:"version_count,omitempty"`
	SizeBytes    int64     `json:"size_bytes"`
	Digest       string    `json:"digest,omitempty"`
	Origin       string    `json:"origin"` // "hosted", "cached", or "mixed"
	Description  string    `json:"description,omitempty"`
	Downloads    int64     `json:"downloads,omitempty"`
	Retracted    bool      `json:"retracted,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type ArtifactQuery struct {
	Search string
	Origin string
	Page   int
	Limit  int
}

type ArtifactPage struct {
	Items []ArtifactDescriptor `json:"items"`
	Total int64                `json:"total"`
	Page  int                  `json:"page"`
	Limit int                  `json:"limit"`
}

// Optional capability interfaces — implemented only by registries whose capabilities support them.
// Never force all registry types to implement these methods.

type ArtifactBrowser interface {
	ListArtifacts(ctx context.Context, q ArtifactQuery) (ArtifactPage, error)
}

type ArtifactDeleter interface {
	DeleteArtifact(ctx context.Context, name, version string) error
}

type CachePurger interface {
	PurgeCache(ctx context.Context) (storage.EvictResult, error)
}

type CacheWarmer interface {
	WarmArtifact(ctx context.Context, name string) error
}

type HealthChecker interface {
	CheckHealth(ctx context.Context) upstream.HealthCheckResult
}

type CompatPathMatcher interface {
	MatchesCompatPath(path string) bool
}

type RegistryCleaner interface {
	CleanupRegistry(ctx context.Context) error
}

type Closer interface {
	Close() error
}

// ServiceDeps passes shared ecosystem-agnostic core services into registry factories.
type ServiceDeps struct {
	Storage  *storage.Service
	Cache    *cache.Coordinator
	Upstream *upstream.Client
	Auth     *auth.Service
	Metrics  *metrics.Collector
	Audit    *audit.Service
}

// ConfigFieldHint describes an optional ecosystem-specific setting for admin UI rendering.
type ConfigFieldHint struct {
	Key          string `json:"key"`
	Label        string `json:"label"`
	Type         string `json:"type"` // "string", "number", "boolean", "string_list"
	Description  string `json:"description,omitempty"`
	DefaultValue any    `json:"default_value,omitempty"`
}

// Registration describes a compiled registry type and its factory.
type Registration struct {
	Type               string
	DisplayName        string
	Description        string
	DefaultUpstreamURL string
	Capabilities       Capabilities
	ConfigHints        []ConfigFieldHint
	ValidateConfig     func(cfg map[string]any) error
	Create             func(ctx context.Context, inst *Instance, deps ServiceDeps) (Registry, error)
}

// TypeInfo is the JSON view of an installed registry type exposed to the admin API.
type TypeInfo struct {
	Type               string            `json:"type"`
	DisplayName        string            `json:"display_name"`
	Description        string            `json:"description"`
	DefaultUpstreamURL string            `json:"default_upstream_url,omitempty"`
	Capabilities       Capabilities      `json:"capabilities"`
	SupportedModes     []RepositoryMode  `json:"supported_modes"`
	ConfigHints        []ConfigFieldHint `json:"config_hints,omitempty"`
}

// InstanceStore persists registry instance documents in the `registries` collection.
type InstanceStore interface {
	CreateInstance(ctx context.Context, inst *Instance) error
	GetInstanceByID(ctx context.Context, id string) (*Instance, error)
	GetInstanceBySlug(ctx context.Context, slug string) (*Instance, error)
	ListInstances(ctx context.Context) ([]*Instance, error)
	UpdateInstance(ctx context.Context, inst *Instance) error
	DeleteInstance(ctx context.Context, id string) error
}

// Context helpers for propagating resolved registry base URL into handlers without coupling routing details.
type baseURLCtxKey struct{}

func WithBaseURL(ctx context.Context, baseURL string) context.Context {
	return context.WithValue(ctx, baseURLCtxKey{}, baseURL)
}

func BaseURLFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(baseURLCtxKey{}).(string); ok {
		return v
	}
	return ""
}
