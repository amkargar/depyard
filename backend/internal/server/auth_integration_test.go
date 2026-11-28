package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/amkargar/depyard/backend/internal/admin"
	"github.com/amkargar/depyard/backend/internal/audit"
	"github.com/amkargar/depyard/backend/internal/auth"
	"github.com/amkargar/depyard/backend/internal/cache"
	"github.com/amkargar/depyard/backend/internal/metrics"
	"github.com/amkargar/depyard/backend/internal/registry"
	"github.com/amkargar/depyard/backend/internal/server"
	"github.com/amkargar/depyard/backend/internal/storage"
	memstore "github.com/amkargar/depyard/backend/internal/store/memory"
	"github.com/amkargar/depyard/backend/internal/upstream"
)

const authTestPassword = "correct-horse-battery"

// newAuthHarness spins up a full server with an admin and a regular user.
func newAuthHarness(t *testing.T) (*httptest.Server, *auth.Service) {
	t.Helper()

	ms := memstore.New()
	fsStore, err := storage.NewFSBlobStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFSBlobStore: %v", err)
	}
	storageSvc := storage.NewService(fsStore, ms)
	cacheCoord := cache.NewCoordinator(storageSvc)
	upstreamClient := upstream.NewClient(10 * time.Second)
	authSvc := auth.NewService(ms, 3600*time.Second)
	metricsCol := metrics.NewCollector(ms)
	auditSvc := audit.NewService(ms)

	deps := registry.ServiceDeps{
		Storage:  storageSvc,
		Cache:    cacheCoord,
		Upstream: upstreamClient,
		Auth:     authSvc,
		Metrics:  metricsCol,
		Audit:    auditSvc,
	}
	mgr := registry.NewManager(ms, deps)

	ctx := context.Background()
	if _, err := authSvc.EnsureBootstrapAdmin(ctx, "admin", "admin@test.local", authTestPassword); err != nil {
		t.Fatalf("EnsureBootstrapAdmin: %v", err)
	}
	if _, err := authSvc.CreateUser(ctx, auth.CreateUserInput{
		Username: "member",
		Email:    "member@test.local",
		Password: authTestPassword,
		Role:     auth.RoleUser,
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	settings := admin.DefaultSettings("", false)
	adminHandler := admin.NewHandler(mgr, authSvc, storageSvc, cacheCoord, metricsCol, auditSvc, ms, settings)

	srv := httptest.NewServer(server.New(server.Options{
		Manager:               mgr,
		AdminHandler:          adminHandler,
		PublicBaseURLFunc:     func() string { return "" },
		EnableRootCompatRoute: func() bool { return false },
		AllowedOrigins:        []string{"*"},
	}))
	t.Cleanup(srv.Close)

	return srv, authSvc
}

// doJSON performs a JSON request and decodes the response.
func doJSON(t *testing.T, srv *httptest.Server, method, path string, body any, cookie *http.Cookie, bearer string) (int, map[string]any, http.Header) {
	t.Helper()

	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}

	req, err := http.NewRequest(method, srv.URL+path, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("request %s %s: %v", method, path, err)
	}
	defer res.Body.Close()

	out := map[string]any{}
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out, res.Header
}

// login performs a login and returns the session cookie.
func login(t *testing.T, srv *httptest.Server, username, password string) *http.Cookie {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/admin/auth/login", bytes.NewReader(mustJSON(t, map[string]string{
		"username": username,
		"password": password,
	})))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login %s: expected 200, got %d", username, res.StatusCode)
	}
	for _, c := range res.Cookies() {
		if c.Name == auth.SessionCookieName {
			return c
		}
	}
	t.Fatalf("login %s: no session cookie returned", username)
	return nil
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// --- Session cookie hardening ---

// Use a non-loopback URL and let a cookie jar (not AddCookie) decide what to
// send. Only the TCP connection is redirected to the local test server.
func TestRemoteHTTPBrowserSession(t *testing.T) {
	srv, _ := newAuthHarness(t)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Jar: jar,
		Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
		}},
	}
	defer client.CloseIdleConnections()
	const origin = "http://registry.example.test:8080"
	do := func(method, path string, body io.Reader, want int) {
		t.Helper()
		req, err := http.NewRequest(method, origin+path, body)
		if err != nil {
			t.Fatal(err)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("%s %s: got %d, want %d", method, path, res.StatusCode, want)
		}
	}
	do("POST", "/api/admin/auth/login", bytes.NewReader(mustJSON(t, map[string]string{"username": "admin", "password": authTestPassword})), http.StatusOK)
	do("GET", "/api/admin/auth/me", nil, http.StatusOK)
	do("GET", "/api/admin/users", nil, http.StatusOK)
	do("POST", "/api/admin/auth/logout", nil, http.StatusOK)
	do("GET", "/api/admin/auth/me", nil, http.StatusUnauthorized)
}

func TestSessionCookieAttributes(t *testing.T) {
	srv, _ := newAuthHarness(t)
	credentials := mustJSON(t, map[string]string{"username": "member", "password": authTestPassword})
	for _, tt := range []struct {
		name, origin, forwardedProto string
		secure                       bool
	}{
		{"loopback HTTP", "http://localhost:8080", "", false},
		{"forwarded HTTP", "http://192.0.2.10:8080", "http", false},
		{"forwarded HTTPS", "http://backend:8080", "https", true},
		{"forwarded HTTPS chain", "http://backend:8080", "HTTPS, http", true},
		{"forwarded HTTP chain", "http://backend:8080", "http, https", false},
		{"direct TLS cannot be downgraded", "https://registry.example.test", "http", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, action := range []string{"login", "logout"} {
				req := httptest.NewRequest(http.MethodPost, tt.origin+"/api/admin/auth/"+action, bytes.NewReader(credentials))
				req.Header.Set("Content-Type", "application/json")
				req.URL.Scheme = "" // ordinary server requests carry the path, not an absolute URL
				req.Header.Set("X-Forwarded-Proto", tt.forwardedProto)
				w := httptest.NewRecorder()
				srv.Config.Handler.ServeHTTP(w, req)
				cookies := w.Result().Cookies()
				if w.Code != http.StatusOK || len(cookies) != 1 || cookies[0].Name != auth.SessionCookieName {
					t.Fatalf("%s: missing session cookie (status %d)", action, w.Code)
				}
				c := cookies[0]
				if c.Secure != tt.secure {
					t.Errorf("%s: Secure=%t, want %t", action, c.Secure, tt.secure)
				}
				if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Domain != "" || c.Path != "/" {
					t.Errorf("%s: expected a host-only, HttpOnly, SameSite=Lax cookie on /", action)
				}
				if action == "logout" && (c.MaxAge >= 0 || c.Value != "") {
					t.Fatal("logout did not expire cookie")
				}
			}
		})
	}
}

// --- Authentication vs authorization ---

func TestRegularUserCanUseSelfServiceEndpoints(t *testing.T) {
	srv, _ := newAuthHarness(t)
	cookie := login(t, srv, "member", authTestPassword)

	// The profile endpoint must be reachable for a regular user.
	status, body, _ := doJSON(t, srv, http.MethodGet, "/api/admin/profile", nil, cookie, "")
	if status != http.StatusOK {
		t.Fatalf("expected 200 for the profile endpoint, got %d (%v)", status, body)
	}
	if body["is_admin"] != false {
		t.Fatalf("expected is_admin=false for a regular user, got %v", body["is_admin"])
	}

	// The admin-only endpoints must stay out of reach.
	status, _, _ = doJSON(t, srv, http.MethodGet, "/api/admin/users", nil, cookie, "")
	if status != http.StatusForbidden {
		t.Fatalf("expected 403 for a regular user listing users, got %d", status)
	}
	status, _, _ = doJSON(t, srv, http.MethodGet, "/api/admin/audit-logs", nil, cookie, "")
	if status != http.StatusForbidden {
		t.Fatalf("expected 403 for a regular user reading audit logs, got %d", status)
	}
}

func TestAnonymousCannotReachSelfServiceEndpoints(t *testing.T) {
	srv, _ := newAuthHarness(t)

	status, _, _ := doJSON(t, srv, http.MethodGet, "/api/admin/profile", nil, nil, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("expected 401 for an anonymous profile request, got %d", status)
	}
	status, _, _ = doJSON(t, srv, http.MethodGet, "/api/admin/my/tokens", nil, nil, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("expected 401 for an anonymous token request, got %d", status)
	}
}

func TestProfileUpdateChangesUsernameAndEmail(t *testing.T) {
	srv, _ := newAuthHarness(t)
	cookie := login(t, srv, "member", authTestPassword)

	status, body, _ := doJSON(t, srv, http.MethodPut, "/api/admin/profile", map[string]string{
		"username": "member2",
		"email":    "member2@test.local",
	}, cookie, "")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d (%v)", status, body)
	}

	// The new username must work for signing in.
	login(t, srv, "member2", authTestPassword)
}

func TestProfileUpdateCannotEscalateRole(t *testing.T) {
	srv, _ := newAuthHarness(t)
	cookie := login(t, srv, "member", authTestPassword)

	// The endpoint ignores role and disabled fields entirely.
	status, _, _ := doJSON(t, srv, http.MethodPut, "/api/admin/profile", map[string]any{
		"username": "member",
		"role":     "admin",
		"disabled": false,
	}, cookie, "")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}

	// The account must still be a regular user.
	status, body, _ := doJSON(t, srv, http.MethodGet, "/api/admin/profile", nil, cookie, "")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	user, _ := body["user"].(map[string]any)
	if user["role"] != "user" {
		t.Fatalf("expected the role to stay 'user', got %v", user["role"])
	}
}

func TestSelfServicePasswordChangeRequiresCurrentPassword(t *testing.T) {
	srv, _ := newAuthHarness(t)
	cookie := login(t, srv, "member", authTestPassword)

	status, _, _ := doJSON(t, srv, http.MethodPost, "/api/admin/profile/password", map[string]string{
		"current_password": "wrong-password-here",
		"new_password":     "a-brand-new-password",
	}, cookie, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("expected 401 for a wrong current password, got %d", status)
	}

	status, _, _ = doJSON(t, srv, http.MethodPost, "/api/admin/profile/password", map[string]string{
		"current_password": authTestPassword,
		"new_password":     "short",
	}, cookie, "")
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400 for a password below the policy minimum, got %d", status)
	}

	status, _, _ = doJSON(t, srv, http.MethodPost, "/api/admin/profile/password", map[string]string{
		"current_password": authTestPassword,
		"new_password":     "a-brand-new-password",
	}, cookie, "")
	if status != http.StatusOK {
		t.Fatalf("expected 200 for a valid password change, got %d", status)
	}

	// The old password must stop working and the new one must work.
	login(t, srv, "member", "a-brand-new-password")
}

// --- Personal access tokens ---

func TestPersonalTokenLifecycle(t *testing.T) {
	srv, _ := newAuthHarness(t)
	cookie := login(t, srv, "member", authTestPassword)

	status, body, _ := doJSON(t, srv, http.MethodPost, "/api/admin/my/tokens", map[string]any{
		"name":         "my-ci",
		"scopes":       []string{"read"},
		"expires_days": 30,
	}, cookie, "")
	if status != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%v)", status, body)
	}
	plaintext, _ := body["plaintext"].(string)
	if plaintext == "" {
		t.Fatal("expected the plaintext token to be returned once")
	}
	token, _ := body["token"].(map[string]any)
	tokenID, _ := token["id"].(string)
	if tokenID == "" {
		t.Fatal("expected a token id")
	}
	if userID, _ := token["user_id"].(string); userID == "" {
		t.Fatal("expected the token to be bound to a user")
	}

	// The token must appear in the caller's own list.
	status, list, _ := doJSON(t, srv, http.MethodGet, "/api/admin/my/tokens", nil, cookie, "")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	tokens, _ := list["tokens"].([]any)
	if len(tokens) != 1 {
		t.Fatalf("expected exactly 1 token, got %d", len(tokens))
	}

	// The token must authenticate.
	status, _, _ = doJSON(t, srv, http.MethodGet, "/api/admin/auth/me", nil, nil, plaintext)
	if status != http.StatusOK {
		t.Fatalf("expected the personal token to authenticate, got %d", status)
	}

	// Revoke it.
	status, _, _ = doJSON(t, srv, http.MethodPost, "/api/admin/my/tokens/"+tokenID+"/revoke", nil, cookie, "")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	status, _, _ = doJSON(t, srv, http.MethodGet, "/api/admin/auth/me", nil, nil, plaintext)
	if status != http.StatusUnauthorized {
		t.Fatalf("expected the revoked token to stop authenticating, got %d", status)
	}

	// Delete it.
	status, _, _ = doJSON(t, srv, http.MethodDelete, "/api/admin/my/tokens/"+tokenID, nil, cookie, "")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
}

func TestPersonalTokenOwnershipIsEnforced(t *testing.T) {
	srv, _ := newAuthHarness(t)

	adminCookie := login(t, srv, "admin", authTestPassword)
	memberCookie := login(t, srv, "member", authTestPassword)

	// The admin creates a token for themselves through the admin API.
	status, body, _ := doJSON(t, srv, http.MethodPost, "/api/admin/tokens", map[string]any{
		"name":   "admin-token",
		"scopes": []string{"read"},
	}, adminCookie, "")
	if status != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%v)", status, body)
	}
	token, _ := body["token"].(map[string]any)
	tokenID, _ := token["id"].(string)

	// The member must not be able to revoke or delete it.
	status, _, _ = doJSON(t, srv, http.MethodPost, "/api/admin/my/tokens/"+tokenID+"/revoke", nil, memberCookie, "")
	if status != http.StatusForbidden {
		t.Fatalf("expected 403 when revoking somebody else's token, got %d", status)
	}
	status, _, _ = doJSON(t, srv, http.MethodDelete, "/api/admin/my/tokens/"+tokenID, nil, memberCookie, "")
	if status != http.StatusForbidden {
		t.Fatalf("expected 403 when deleting somebody else's token, got %d", status)
	}

	// The member's own list must not contain the admin's token.
	status, list, _ := doJSON(t, srv, http.MethodGet, "/api/admin/my/tokens", nil, memberCookie, "")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	tokens, _ := list["tokens"].([]any)
	if len(tokens) != 0 {
		t.Fatalf("expected the member to have no tokens, got %d", len(tokens))
	}
}

func TestPersonalTokenCannotExceedOwnerPermissions(t *testing.T) {
	srv, _ := newAuthHarness(t)
	memberCookie := login(t, srv, "member", authTestPassword)

	status, body, _ := doJSON(t, srv, http.MethodPost, "/api/admin/my/tokens", map[string]any{
		"name":   "escalation",
		"scopes": []string{"read", "admin"},
	}, memberCookie, "")
	if status != http.StatusForbidden {
		t.Fatalf("expected 403 for an admin-scoped personal token, got %d (%v)", status, body)
	}
}

// --- Admin user management ---

func TestAdminCanEditUsernameRoleAndStatus(t *testing.T) {
	srv, authSvc := newAuthHarness(t)
	adminCookie := login(t, srv, "admin", authTestPassword)

	users, err := authSvc.ListUsers(context.Background())
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	var memberID string
	for _, u := range users {
		if u.Username == "member" {
			memberID = u.ID
		}
	}
	if memberID == "" {
		t.Fatal("member not found")
	}

	status, body, _ := doJSON(t, srv, http.MethodPut, "/api/admin/users/"+memberID, map[string]any{
		"username": "renamed",
		"email":    "renamed@test.local",
	}, adminCookie, "")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d (%v)", status, body)
	}
	updated, _ := body["user"].(map[string]any)
	if updated["username"] != "renamed" {
		t.Fatalf("expected the username to be renamed, got %v", updated["username"])
	}

	// The renamed account must be able to sign in.
	login(t, srv, "renamed", authTestPassword)
}

func TestAdminCannotRemoveTheLastAdministrator(t *testing.T) {
	srv, authSvc := newAuthHarness(t)
	adminCookie := login(t, srv, "admin", authTestPassword)

	users, err := authSvc.ListUsers(context.Background())
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	var adminID string
	for _, u := range users {
		if u.Username == "admin" {
			adminID = u.ID
		}
	}

	// Demotion is refused.
	status, _, _ := doJSON(t, srv, http.MethodPut, "/api/admin/users/"+adminID, map[string]any{
		"role": "user",
	}, adminCookie, "")
	if status != http.StatusConflict {
		t.Fatalf("expected 409 when demoting the last admin, got %d", status)
	}

	// Disabling is refused.
	status, _, _ = doJSON(t, srv, http.MethodPut, "/api/admin/users/"+adminID, map[string]any{
		"disabled": true,
	}, adminCookie, "")
	if status != http.StatusConflict {
		t.Fatalf("expected 409 when disabling the last admin, got %d", status)
	}

	// Deletion is refused.
	status, _, _ = doJSON(t, srv, http.MethodDelete, "/api/admin/users/"+adminID, nil, adminCookie, "")
	if status != http.StatusConflict {
		t.Fatalf("expected 409 when deleting the last admin, got %d", status)
	}
}

func TestAdminPasswordResetInvalidatesTheUserSession(t *testing.T) {
	srv, authSvc := newAuthHarness(t)
	adminCookie := login(t, srv, "admin", authTestPassword)
	memberCookie := login(t, srv, "member", authTestPassword)

	users, err := authSvc.ListUsers(context.Background())
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	var memberID string
	for _, u := range users {
		if u.Username == "member" {
			memberID = u.ID
		}
	}

	status, _, _ := doJSON(t, srv, http.MethodPut, "/api/admin/users/"+memberID, map[string]any{
		"password": "reset-password-123",
	}, adminCookie, "")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}

	// The member's old session must be gone.
	status, _, _ = doJSON(t, srv, http.MethodGet, "/api/admin/profile", nil, memberCookie, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("expected 401 for the invalidated session, got %d", status)
	}

	// The new password works.
	login(t, srv, "member", "reset-password-123")
}

func TestDeletedUserTokensStopWorking(t *testing.T) {
	srv, authSvc := newAuthHarness(t)
	adminCookie := login(t, srv, "admin", authTestPassword)
	memberCookie := login(t, srv, "member", authTestPassword)

	// The member creates a personal token.
	status, body, _ := doJSON(t, srv, http.MethodPost, "/api/admin/my/tokens", map[string]any{
		"name":   "member-token",
		"scopes": []string{"read"},
	}, memberCookie, "")
	if status != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%v)", status, body)
	}
	plaintext, _ := body["plaintext"].(string)

	// The token authenticates.
	status, _, _ = doJSON(t, srv, http.MethodGet, "/api/admin/auth/me", nil, nil, plaintext)
	if status != http.StatusOK {
		t.Fatalf("expected the token to authenticate, got %d", status)
	}

	// The admin deletes the account.
	users, _ := authSvc.ListUsers(context.Background())
	var memberID string
	for _, u := range users {
		if u.Username == "member" {
			memberID = u.ID
		}
	}
	status, _, _ = doJSON(t, srv, http.MethodDelete, "/api/admin/users/"+memberID, nil, adminCookie, "")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}

	// The deleted user's token must stop working.
	status, _, _ = doJSON(t, srv, http.MethodGet, "/api/admin/auth/me", nil, nil, plaintext)
	if status != http.StatusUnauthorized {
		t.Fatalf("expected the deleted user's token to stop authenticating, got %d", status)
	}
}

func TestMeEndpointReportsAuthenticationState(t *testing.T) {
	srv, _ := newAuthHarness(t)
	cookie := login(t, srv, "member", authTestPassword)

	status, body, _ := doJSON(t, srv, http.MethodGet, "/api/admin/auth/me", nil, cookie, "")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if body["authenticated"] != true {
		t.Fatalf("expected authenticated=true, got %v", body["authenticated"])
	}
	// The bug under repair: is_admin must be false, and the presence of the
	// user must be reported independently of the role.
	if body["is_admin"] != false {
		t.Fatalf("expected is_admin=false for a regular user, got %v", body["is_admin"])
	}
	if body["user"] == nil {
		t.Fatal("expected the user object to be returned for a signed-in regular user")
	}
}

// --- Administrator token access ---

// findUser resolves a username through the admin listing, which is the only
// lookup the service exposes by name.
func findUser(t *testing.T, svc *auth.Service, username string) *auth.User {
	t.Helper()
	users, err := svc.ListUsers(context.Background())
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	for _, u := range users {
		if u.Username == username {
			return u
		}
	}
	t.Fatalf("user %q not found", username)
	return nil
}

// TestAdminPersonalTokensStayOwnerScoped verifies that an administrator's
// "my tokens" view only ever exposes their own tokens. The platform-wide list
// is a separate, deliberately administrative view.
func TestAdminPersonalTokensStayOwnerScoped(t *testing.T) {
	srv, _ := newAuthHarness(t)

	adminCookie := login(t, srv, "admin", authTestPassword)
	memberCookie := login(t, srv, "member", authTestPassword)

	// Each account creates one token.
	for _, tc := range []struct {
		name   string
		cookie *http.Cookie
		path   string
	}{
		{"admin-personal", adminCookie, "/api/admin/my/tokens"},
		{"member-personal", memberCookie, "/api/admin/my/tokens"},
	} {
		status, body, _ := doJSON(t, srv, http.MethodPost, tc.path, map[string]any{
			"name":   tc.name,
			"scopes": []string{"read"},
		}, tc.cookie, "")
		if status != http.StatusCreated {
			t.Fatalf("%s: expected 201, got %d (%v)", tc.name, status, body)
		}
	}

	// The administrator's personal list contains only their own token.
	status, body, _ := doJSON(t, srv, http.MethodGet, "/api/admin/my/tokens", nil, adminCookie, "")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	tokens, _ := body["tokens"].([]any)
	if len(tokens) != 1 {
		t.Fatalf("expected exactly 1 personal token for the admin, got %d", len(tokens))
	}
	if name, _ := tokens[0].(map[string]any)["name"].(string); name != "admin-personal" {
		t.Fatalf("expected the admin's own token, got %q", name)
	}

	// The platform-wide administrative view sees both, each with its owner.
	status, body, _ = doJSON(t, srv, http.MethodGet, "/api/admin/tokens", nil, adminCookie, "")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	all, _ := body["tokens"].([]any)
	if len(all) != 2 {
		t.Fatalf("expected the administrative view to list both tokens, got %d", len(all))
	}
	owners := map[string]bool{}
	for _, raw := range all {
		tok, _ := raw.(map[string]any)
		id, _ := tok["user_id"].(string)
		if id == "" {
			t.Fatalf("administrative token view must expose the owning user_id: %v", tok)
		}
		owners[id] = true
	}
	if len(owners) != 2 {
		t.Fatalf("expected tokens owned by two distinct users, got %d", len(owners))
	}
}

// TestAdminCanIssueTokenForAnotherUser covers provisioning a token on behalf of
// a teammate, which is the reason the owner is selectable at all.
func TestAdminCanIssueTokenForAnotherUser(t *testing.T) {
	srv, authSvc := newAuthHarness(t)
	adminCookie := login(t, srv, "admin", authTestPassword)

	member := findUser(t, authSvc, "member")

	status, body, _ := doJSON(t, srv, http.MethodPost, "/api/admin/tokens", map[string]any{
		"name":    "member-ci",
		"user_id": member.ID,
		"scopes":  []string{"read", "publish"},
	}, adminCookie, "")
	if status != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%v)", status, body)
	}

	token, _ := body["token"].(map[string]any)
	if owner, _ := token["user_id"].(string); owner != member.ID {
		t.Fatalf("expected the token to belong to %s, got %s", member.ID, owner)
	}

	// The issued token authenticates as its owner, not as the administrator
	// who created it.
	plaintext, _ := body["plaintext"].(string)
	status, me, _ := doJSON(t, srv, http.MethodGet, "/api/admin/auth/me", nil, nil, plaintext)
	if status != http.StatusOK {
		t.Fatalf("expected the issued token to authenticate, got %d (%v)", status, me)
	}
	user, _ := me["user"].(map[string]any)
	if name, _ := user["username"].(string); name != "member" {
		t.Fatalf("expected the token to act as member, got %q", name)
	}
}

// TestAdminCannotIssueTokenForUnknownOrDisabledUser keeps the owner selector
// honest: a token bound to a nonexistent or disabled account would be dead on
// arrival.
func TestAdminCannotIssueTokenForUnknownOrDisabledUser(t *testing.T) {
	srv, authSvc := newAuthHarness(t)
	adminCookie := login(t, srv, "admin", authTestPassword)

	status, body, _ := doJSON(t, srv, http.MethodPost, "/api/admin/tokens", map[string]any{
		"name":    "ghost-token",
		"user_id": "000000000000000000000000",
		"scopes":  []string{"read"},
	}, adminCookie, "")
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unknown owner, got %d (%v)", status, body)
	}

	member := findUser(t, authSvc, "member")
	disabled := true
	if _, err := authSvc.UpdateUser(context.Background(), member.ID, auth.UpdateUserInput{Disabled: &disabled}); err != nil {
		t.Fatalf("disabling the member: %v", err)
	}

	status, body, _ = doJSON(t, srv, http.MethodPost, "/api/admin/tokens", map[string]any{
		"name":    "dead-token",
		"user_id": member.ID,
		"scopes":  []string{"read"},
	}, adminCookie, "")
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400 for a disabled owner, got %d (%v)", status, body)
	}
}
