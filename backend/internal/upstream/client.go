package upstream

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Client is an ecosystem-agnostic streaming HTTP client for upstream package registries.
type Client struct {
	httpClient *http.Client
	userAgent  string
}

func NewClient(defaultTimeout time.Duration) *Client {
	if defaultTimeout <= 0 {
		defaultTimeout = 30 * time.Second
	}
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
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
	return &Client{
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   defaultTimeout,
		},
		userAgent: "mirror-registry/1.0",
	}
}

// Get performs a streaming GET request against an upstream URL.
// Callers are responsible for closing resp.Body when err == nil.
func (c *Client) Get(ctx context.Context, rawURL string, headers map[string]string, timeout time.Duration) (*http.Response, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		// Wrap response body close so cancel fires only after body is closed.
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			cancel()
			return nil, err
		}
		c.applyHeaders(req, headers)
		resp, err := c.doWithoutClientTimeout(req)
		if err != nil {
			cancel()
			return nil, err
		}
		resp.Body = &cancelReadCloser{ReadCloser: resp.Body, cancel: cancel}
		return resp, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	c.applyHeaders(req, headers)
	return c.httpClient.Do(req)
}

func (c *Client) doWithoutClientTimeout(req *http.Request) (*http.Response, error) {
	// Use transport directly via a shallow client copy with Timeout=0 so streaming large blobs respects ctx deadline.
	cl := &http.Client{
		Transport:     c.httpClient.Transport,
		CheckRedirect: c.httpClient.CheckRedirect,
	}
	return cl.Do(req)
}

func (c *Client) applyHeaders(req *http.Request, headers map[string]string) {
	req.Header.Set("User-Agent", c.userAgent)
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
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil)
	if err != nil {
		return HealthCheckResult{
			Available: false,
			Error:     fmt.Sprintf("invalid url: %v", err),
			CheckedAt: time.Now().UTC(),
		}
	}
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.doWithoutClientTimeout(req)
	if err != nil {
		return HealthCheckResult{
			Available: false,
			LatencyMs: time.Since(start).Milliseconds(),
			Error:     err.Error(),
			CheckedAt: time.Now().UTC(),
		}
	}
	_ = resp.Body.Close()

	if resp.StatusCode == http.StatusMethodNotAllowed {
		req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		req2.Header.Set("User-Agent", c.userAgent)
		resp2, err2 := c.doWithoutClientTimeout(req2)
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
