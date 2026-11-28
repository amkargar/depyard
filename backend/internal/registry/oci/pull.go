package oci

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	repo "github.com/amkargar/depyard/backend/internal/registry/repository"
)

var challengeField = regexp.MustCompile(`(realm|service)="([^"]+)"`)

// Anonymous Docker Hub (and other Distribution servers) require a scoped
// bearer challenge even for public pulls. Never reuse a client's Depyard token.
func (h *handler) pull(ctx context.Context, name, p string) (*http.Response, error) {
	target := h.RemoteURL(p)
	headers := map[string]string{"Accept": accept}
	resp, err := h.Request(ctx, "GET", target, nil, headers)
	if err != nil || resp.StatusCode != 401 {
		return resp, err
	}
	challenge := resp.Header.Get("WWW-Authenticate")
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		return resp, nil
	}
	resp.Body.Close()
	fields := map[string]string{}
	for _, m := range challengeField.FindAllStringSubmatch(challenge, -1) {
		fields[m[1]] = m[2]
	}
	u, err := url.Parse(fields["realm"])
	if err != nil || u.Host == "" {
		return nil, repo.Status(502)
	}
	q := u.Query()
	q.Set("scope", "repository:"+name+":pull")
	if fields["service"] != "" {
		q.Set("service", fields["service"])
	}
	u.RawQuery = q.Encode()
	res, err := h.Request(ctx, "GET", u.String(), nil, nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, repo.UpstreamStatus(res.StatusCode)
	}
	var token struct {
		Token  string
		Access string `json:"access_token"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&token) != nil {
		return nil, repo.Status(502)
	}
	if token.Token == "" {
		token.Token = token.Access
	}
	if token.Token == "" {
		return nil, repo.Status(502)
	}
	headers["Authorization"] = "Bearer " + token.Token
	return h.Request(ctx, "GET", target, nil, headers)
}
