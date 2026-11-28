// Package repository implements hosted and read-through object storage for HTTP
// repository formats. Adapters supply protocol routes; this package has no
// knowledge of package names, index schemas or database implementations.
package repository

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/amkargar/depyard/backend/internal/registry"
	"github.com/amkargar/depyard/backend/internal/storage"
	"github.com/amkargar/depyard/backend/internal/upstream"
)

type Format struct {
	Type, Name, Description, Upstream string
	Routes                            func(*Repository) http.Handler
	Compat                            func(string) bool
	RedirectHeaders                   []string
	ResponseHeaders                   []string
}

func (f Format) Registration() registry.Registration {
	caps := registry.Capabilities{Proxy: true, Hosted: true, Publish: true}
	return registry.Registration{Type: f.Type, DisplayName: f.Name, Description: f.Description, DefaultUpstreamURL: f.Upstream, Capabilities: caps,
		Create: func(_ context.Context, i *registry.Instance, d registry.ServiceDeps) (registry.Registry, error) {
			r := &Repository{Instance: i.Clone(), deps: d, format: f, caps: caps}
			if f.Routes == nil {
				r.handler = http.HandlerFunc(r.Files)
			} else {
				r.handler = f.Routes(r)
			}
			return runtime{r}, nil
		}}
}

type Repository struct {
	Instance *registry.Instance
	deps     registry.ServiceDeps
	format   Format
	caps     registry.Capabilities
	handler  http.Handler
}

func (r *Repository) Type() string                        { return r.format.Type }
func (r *Repository) Capabilities() registry.Capabilities { return r.caps }
func (r *Repository) Handler() http.Handler               { return http.HandlerFunc(r.serveHTTP) }
func (r *Repository) MatchesCompatPath(p string) bool {
	return r.format.Compat != nil && r.format.Compat(p)
}

func (r *Repository) CanHost() bool  { return r.Instance.Mode != registry.ModeProxy }
func (r *Repository) CanProxy() bool { return r.Instance.Mode != registry.ModeHosted }
func (r *Repository) CheckHealth(ctx context.Context) upstream.HealthCheckResult {
	if !r.CanProxy() {
		return upstream.HealthCheckResult{Available: true, CheckedAt: time.Now().UTC()}
	}
	return r.deps.Upstream.CheckHealthWithOptions(ctx, r.Instance.UpstreamURL, r.Instance.UpstreamRequestOptions(nil, 8*time.Second))
}

// Object keys are registry-relative decoded paths. Hosted and cached references
// use disjoint keys: a concurrent cache fill/eviction can never replace a publish.
type Object struct {
	Path       string
	Query      string // encoded upstream query, also part of the cache identity
	Digest     string // optional expected SHA-256 (verified before publishing any reference)
	Size       *int64 // optional exact size
	NoReplace  bool   // immutable hosted content, independently of cache TTL
	Immutable  bool
	RemoteOnly bool // resolve only from trusted upstream metadata, never a hosted override
	// Fetch is used for protocols requiring negotiation rather than a simple GET.
	Headers []string // additional representation fields needed for this resource
	Fetch   func(context.Context) (*http.Response, error)
}

const hosted = "host:"
const cached = "cache:"

func (r *Repository) HostedRefs(ctx context.Context, prefix string) ([]*storage.ArtifactRef, error) {
	if !r.CanHost() {
		return nil, nil
	}
	refs, _, err := r.deps.Storage.ListArtifactRefs(ctx, r.Instance.ID, storage.RefQuery{Prefix: hosted + prefix})
	return refs, err
}

func HostedPath(ref *storage.ArtifactRef) string { return strings.TrimPrefix(ref.ArtifactKey, hosted) }
func (r *Repository) HostedRef(ctx context.Context, p string) (*storage.ArtifactRef, error) {
	if !r.CanHost() {
		return nil, storage.ErrRefNotFound
	}
	return r.deps.Storage.GetArtifactRef(ctx, r.Instance.ID, hosted+p)
}

// Request accepts a trusted adapter-resolved absolute upstream URL, not a URL
// supplied by a downstream client. Only the configured origin gets its secrets.
func (r *Repository) Request(ctx context.Context, method, target string, body io.Reader, headers map[string]string) (*http.Response, error) {
	if !r.CanProxy() {
		return nil, storage.ErrRefNotFound
	}
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, Status(http.StatusBadGateway)
	}
	base, _ := url.Parse(r.Instance.UpstreamURL)
	opts := r.Instance.UpstreamRequestOptions(headers, 0)
	opts.RedirectResponseHeaders = r.format.RedirectHeaders
	if base == nil || !strings.EqualFold(u.Host, base.Host) || u.Scheme != base.Scheme {
		opts.Headers = headers
		opts.InsecureSkipVerify = false
	}
	resp, err := r.deps.Upstream.DoWithOptions(ctx, method, target, body, opts)
	if err != nil {
		return nil, Status(http.StatusBadGateway)
	}
	return resp, nil
}

func (r *Repository) RemoteURL(p string) string {
	return strings.TrimRight(r.Instance.UpstreamURL, "/") + (&url.URL{Path: p}).EscapedPath()
}

func (r *Repository) Put(ctx context.Context, o Object, src io.Reader, headers map[string]string) (*storage.ArtifactRef, error) {
	if !r.CanHost() {
		return nil, Status(http.StatusMethodNotAllowed)
	}
	res, err := r.put(ctx, hosted+o.Path, storage.OriginHosted, o, src, headers)
	if err == nil && r.deps.Metrics != nil {
		r.deps.Metrics.RecordUpload(r.Instance.ID, res.SizeBytes)
	}
	return res, err
}

func (r *Repository) put(ctx context.Context, key, origin string, o Object, src io.Reader, headers map[string]string) (*storage.ArtifactRef, error) {
	quota := r.Instance.StorageQuotaBytes
	res, err := r.deps.Storage.PutArtifact(ctx, storage.PutArtifactInput{
		RegistryID:       r.Instance.ID,
		ArtifactKey:      key,
		Origin:           origin,
		Reader:           src,
		Headers:          headers,
		ExpectedDigest:   o.Digest,
		ExpectedSize:     o.Size,
		Immutable:        o.NoReplace,
		RegistryQuota:    quota,
		MaxArtifactBytes: quota,
	})
	return res.Ref, err
}

// Open resolves a protected local object first, then a fresh cached object, then
// a singleflight upstream fetch. Errors and partial HTTP responses are not cached.
func (r *Repository) Open(ctx context.Context, o Object) (*os.File, *storage.ArtifactRef, error) {
	id := r.Instance.ID
	if r.CanHost() && !o.RemoteOnly {
		f, ref, err := r.deps.Storage.OpenArtifact(ctx, id, hosted+o.Path)
		if !errors.Is(err, storage.ErrRefNotFound) {
			return f, ref, err
		}
	}
	if !r.CanProxy() {
		return nil, nil, storage.ErrRefNotFound
	}
	key := cached + o.Path
	if o.Query != "" {
		key += "?" + o.Query
	}
	isFresh := func() bool {
		ref, err := r.deps.Storage.GetArtifactRef(ctx, id, key)
		if err != nil {
			return false
		}
		ttl := r.Instance.CachePolicy.MetadataTTLSeconds
		if o.Immutable {
			ttl = r.Instance.CachePolicy.ArchiveTTLSeconds
			if ttl == 0 {
				return true
			}
		}
		return time.Since(ref.UpdatedAt) < time.Duration(ttl)*time.Second
	}
	hit := isFresh()
	if !hit {
		_, err, _ := r.deps.Cache.Do(id, key, func() (any, error) {
			if isFresh() {
				return nil, nil
			}
			fetch := o.Fetch
			if fetch == nil {
				fetch = func(ctx context.Context) (*http.Response, error) {
					u := r.RemoteURL(o.Path)
					if o.Query != "" {
						u += "?" + o.Query
					}
					return r.Request(ctx, http.MethodGet, u, nil, nil)
				}
			}
			resp, err := fetch(ctx)
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return nil, UpstreamStatus(resp.StatusCode)
			}
			headers := make(map[string]string)
			for _, k := range append(append([]string{"Content-Type", "Etag"}, r.format.ResponseHeaders...), o.Headers...) {
				if v := resp.Header.Get(k); v != "" {
					headers[k] = v
				}
			}
			_, err = r.put(ctx, key, storage.OriginCached, o, resp.Body, headers)
			if errors.Is(err, storage.ErrDigestMismatch) || errors.Is(err, storage.ErrSizeMismatch) {
				return nil, Status(502)
			}
			return nil, err
		})
		if err != nil {
			return nil, nil, err
		}
	}
	if r.deps.Metrics != nil {
		r.deps.Metrics.RecordCacheLookup(id, hit)
	}
	return r.deps.Storage.OpenArtifact(ctx, id, key)
}

// LocalRef checks for existing bytes without contacting the upstream. Useful
// for negotiation of immutable objects before their transfer (e.g. by digest).
func (r *Repository) LocalRef(ctx context.Context, p string) (*storage.ArtifactRef, error) {
	ref, err := r.HostedRef(ctx, p)
	if !errors.Is(err, storage.ErrRefNotFound) || !r.CanProxy() {
		return ref, err
	}
	return r.deps.Storage.GetArtifactRef(ctx, r.Instance.ID, cached+p)
}

// Keep lifecycle separate from Repository: protocol handlers may embed Repository.
type runtime struct{ *Repository }

func (r runtime) Close() error {
	if closer, ok := r.handler.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}
