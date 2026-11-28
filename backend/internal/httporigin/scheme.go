// Package httporigin describes the client-facing HTTP connection to Depyard.
package httporigin

import (
	"net/http"
	"strings"
)

// Scheme is used for session cookies and absolute URLs. A hostname or IP
// address does not tell us whether the client used TLS. A TLS-terminating
// proxy must set X-Forwarded-Proto to the original scheme at the edge; in a
// chain, subsequent proxies may append their own schemes.
func Scheme(r *http.Request) string {
	if r.TLS != nil || (r.URL != nil && strings.EqualFold(r.URL.Scheme, "https")) {
		return "https"
	}
	proto, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	if strings.EqualFold(strings.TrimSpace(proto), "https") {
		return "https"
	}
	return "http"
}
