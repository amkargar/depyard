package upstream

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Config configures global defaults for outbound upstream requests.
// All fields can be updated safely at runtime from Admin Settings without restarting.
type Config struct {
	ProxyURL           string
	DefaultTimeout     time.Duration
	UserAgent          string
	InsecureSkipVerify bool
}

// RequestOptions allows per-registry or per-request overrides on top of global Client defaults.
type RequestOptions struct {
	Headers            map[string]string
	Timeout            time.Duration
	ProxyURL           string
	InsecureSkipVerify bool
}

// ValidateProxyURL verifies an optional outbound proxy URL (http, https, socks5, socks5h).
func ValidateProxyURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid proxy URL %q", raw)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
		return u, nil
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q (expected http, https, socks5, or socks5h)", u.Scheme)
	}
}

// Client is an ecosystem-agnostic streaming HTTP client for upstream package registries.
type Client struct {
	mu                 sync.RWMutex
	transport          *http.Transport
	defaultTimeout     time.Duration
	userAgent          string
	proxyURL           string
	insecureSkipVerify bool

	// Cache per-override transports keyed by "<proxyURL>|<insecure>" to preserve connection pooling.
	overrideMu         sync.RWMutex
	overrideTransports map[string]*http.Transport
}

func NewClient(defaultTimeout time.Duration) *Client {
	if defaultTimeout <= 0 {
		defaultTimeout = 30 * time.Second
	}
	c := &Client{
		defaultTimeout:     defaultTimeout,
		userAgent:          "depyard/1.0",
		overrideTransports: make(map[string]*http.Transport),
	}
	c.transport = buildTransport(nil, false)
	return c
}

func buildTransport(proxyURL *url.URL, insecureSkipVerify bool) *http.Transport {
	proxyFunc := http.ProxyFromEnvironment
	if proxyURL != nil {
		proxyFunc = http.ProxyURL(proxyURL)
	}
	tr := &http.Transport{
		Proxy: proxyFunc,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	if insecureSkipVerify {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // Explicitly configured by admin for internal self-signed mirrors
	}
	return tr
}

// ApplyConfig updates the global upstream client configuration live without restarting the server.
func (c *Client) ApplyConfig(cfg Config) error {
	parsedProxy, err := ValidateProxyURL(cfg.ProxyURL)
	if err != nil {
		return err
	}
	timeout := cfg.DefaultTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ua := strings.TrimSpace(cfg.UserAgent)
	if ua == "" {
		ua = "depyard/1.0"
	}

	newTr := buildTransport(parsedProxy, cfg.InsecureSkipVerify)

	c.mu.Lock()
	oldTr := c.transport
	c.transport = newTr
	c.defaultTimeout = timeout
	c.userAgent = ua
	c.proxyURL = strings.TrimSpace(cfg.ProxyURL)
	c.insecureSkipVerify = cfg.InsecureSkipVerify
	c.mu.Unlock()

	if oldTr != nil {
		oldTr.CloseIdleConnections()
	}
	return nil
}

// CurrentConfig returns a snapshot of the active global upstream settings.
func (c *Client) CurrentConfig() Config {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return Config{
		ProxyURL:           c.proxyURL,
		DefaultTimeout:     c.defaultTimeout,
		UserAgent:          c.userAgent,
		InsecureSkipVerify: c.insecureSkipVerify,
	}
}

func (c *Client) resolveTransport(proxyOverride string, insecureOverride bool) (*http.Transport, string, time.Duration, error) {
	c.mu.RLock()
	baseTr := c.transport
	ua := c.userAgent
	defTimeout := c.defaultTimeout
	baseProxy := c.proxyURL
	baseInsecure := c.insecureSkipVerify
	c.mu.RUnlock()

	effectiveProxy := strings.TrimSpace(proxyOverride)
	effectiveInsecure := baseInsecure || insecureOverride

	if effectiveProxy == "" && effectiveInsecure == baseInsecure {
		return baseTr, ua, defTimeout, nil
	}
	if effectiveProxy == "" {
		effectiveProxy = baseProxy
	}

	key := fmt.Sprintf("%s|%t", effectiveProxy, effectiveInsecure)
	c.overrideMu.RLock()
	if tr, ok := c.overrideTransports[key]; ok {
		c.overrideMu.RUnlock()
		return tr, ua, defTimeout, nil
	}
	c.overrideMu.RUnlock()

	parsed, err := ValidateProxyURL(effectiveProxy)
	if err != nil {
		return nil, "", 0, err
	}
	tr := buildTransport(parsed, effectiveInsecure)

	c.overrideMu.Lock()
	c.overrideTransports[key] = tr
	c.overrideMu.Unlock()
	return tr, ua, defTimeout, nil
}

// Get performs a streaming GET request against an upstream URL using global defaults.
// Callers are responsible for closing resp.Body when err == nil.
func (c *Client) Get(ctx context.Context, rawURL string, headers map[string]string, timeout time.Duration) (*http.Response, error) {
	return c.GetWithOptions(ctx, rawURL, RequestOptions{
		Headers: headers,
		Timeout: timeout,
	})
}

// GetWithOptions performs a streaming GET request with optional per-registry proxy, TLS, headers, and timeout.
func (c *Client) GetWithOptions(ctx context.Context, rawURL string, opts RequestOptions) (*http.Response, error) {
	return c.doStreaming(ctx, http.MethodGet, rawURL, opts)
}

// HeadWithOptions performs a HEAD request with optional per-registry proxy, TLS, headers, and timeout.
func (c *Client) HeadWithOptions(ctx context.Context, rawURL string, opts RequestOptions) (*http.Response, error) {
	return c.doStreaming(ctx, http.MethodHead, rawURL, opts)
}

func (c *Client) doStreaming(ctx context.Context, method, rawURL string, opts RequestOptions) (*http.Response, error) {
	tr, ua, defTimeout, err := c.resolveTransport(opts.ProxyURL, opts.InsecureSkipVerify)
	if err != nil {
		return nil, err
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defTimeout
	}

	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	}

	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		if cancel != nil {
			cancel()
		}
		return nil, err
	}
	applyHeaders(req, ua, opts.Headers)

	// Use Timeout=0 on the shallow http.Client so streaming large blobs respects ctx deadline until Body.Close().
	cl := &http.Client{Transport: tr}
	resp, err := cl.Do(req)
	if err != nil {
		if cancel != nil {
			cancel()
		}
		return nil, err
	}
	if cancel != nil {
		resp.Body = &cancelReadCloser{ReadCloser: resp.Body, cancel: cancel}
	}
	return resp, nil
}

func applyHeaders(req *http.Request, defaultUA string, headers map[string]string) {
	req.Header.Set("User-Agent", defaultUA)
	for k, v := range headers {
		if strings.TrimSpace(k) != "" {
			req.Header.Set(k, v)
		}
	}
}

// HealthCheckResult reports upstream reachability and latency.
type HealthCheckResult struct {
	Available  bool      `json:"available"`
	StatusCode int       `json:"status_code,omitempty"`
	LatencyMs  int64     `json:"latency_ms"`
	Error      string    `json:"error,omitempty"`
	CheckedAt  time.Time `json:"checked_at"`
}

// CheckHealth probes an upstream URL with a lightweight HEAD (falling back to GET on 405).
func (c *Client) CheckHealth(ctx context.Context, rawURL string, timeout time.Duration) HealthCheckResult {
	return c.CheckHealthWithOptions(ctx, rawURL, RequestOptions{Timeout: timeout})
}

// CheckHealthWithOptions probes an upstream URL with custom auth/proxy/TLS options.
func (c *Client) CheckHealthWithOptions(ctx context.Context, rawURL string, opts RequestOptions) HealthCheckResult {
	if opts.Timeout <= 0 {
		opts.Timeout = 8 * time.Second
	}

	start := time.Now()
	resp, err := c.doStreaming(ctx, http.MethodHead, rawURL, opts)
	if err != nil {
		return HealthCheckResult{
			Available: false,
			LatencyMs: time.Since(start).Milliseconds(),
			Error:     err.Error(),
			CheckedAt: time.Now().UTC(),
		}
	}
	_ = resp.Body.Close()

	if resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusNotImplemented {
		resp2, err2 := c.doStreaming(ctx, http.MethodGet, rawURL, opts)
		if err2 != nil {
			return HealthCheckResult{
				Available: false,
				LatencyMs: time.Since(start).Milliseconds(),
				Error:     err2.Error(),
				CheckedAt: time.Now().UTC(),
			}
		}
		_ = resp2.Body.Close()
		resp = resp2
	}

	available := resp.StatusCode < 500
	errMsg := ""
	if !available {
		errMsg = fmt.Sprintf("upstream returned HTTP %d", resp.StatusCode)
	}
	return HealthCheckResult{
		Available:  available,
		StatusCode: resp.StatusCode,
		LatencyMs:  time.Since(start).Milliseconds(),
		Error:      errMsg,
		CheckedAt:  time.Now().UTC(),
	}
}

type cancelReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelReadCloser) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}
