package auth_test

import (
	"context"
	"encoding/base64"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/amkargar/mirror-registry/backend/internal/auth"
	memstore "github.com/amkargar/mirror-registry/backend/internal/store/memory"
)

// TestBasicAuthTokenAuthentication verifies that HTTP Basic credentials are
// accepted by the auth service, which is what pip and twine use.
func TestBasicAuthTokenAuthentication(t *testing.T) {
	ms := memstore.New()
	svc := auth.NewService(ms, time.Hour)

	user, err := svc.CreateUser(context.Background(), auth.CreateUserInput{
		Username: "publisher",
		Email:    "publisher@example.com",
		Password: "publisher-password",
		Role:     auth.RoleUser,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	_, plaintext, err := svc.CreateToken(context.Background(), auth.CreateTokenInput{
		Name:   "twine",
		UserID: user.ID,
		Scopes: []auth.Permission{auth.PermissionPublish, auth.PermissionRead},
	})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	policy := auth.RegistryAccessPolicy{
		RegistryID:         "reg-1",
		RegistrySlug:       "python-packages",
		RegistryType:       "pypi",
		Public:             true,
		AllowAnonymousRead: true,
	}

	// Basic auth with the token as the password must authenticate.
	basic := base64.StdEncoding.EncodeToString([]byte("__token__:" + plaintext))
	req := httptest.NewRequest("POST", "/r/python-packages/legacy/", nil)
	req.Header.Set("Authorization", "Basic "+basic)

	principal, err := svc.AuthenticateRequest(req.Context(), req)
	if err != nil {
		t.Fatalf("AuthenticateRequest with Basic auth: %v", err)
	}
	if !principal.Authenticated {
		t.Fatalf("expected an authenticated principal, got %+v", principal)
	}
	if principal.Kind != "token" {
		t.Fatalf("expected a token principal, got %q", principal.Kind)
	}

	if err := svc.Authorize(principal, auth.AuthRequest{
		Policy:     policy,
		Permission: auth.PermissionPublish,
	}); err != nil {
		t.Fatalf("Authorize publish: %v", err)
	}

	// Bearer auth must keep working exactly as before.
	req = httptest.NewRequest("GET", "/r/python-packages/simple/", nil)
	req.Header.Set("Authorization", "Bearer "+plaintext)
	principal, err = svc.AuthenticateRequest(req.Context(), req)
	if err != nil {
		t.Fatalf("AuthenticateRequest with Bearer auth: %v", err)
	}
	if !principal.Authenticated {
		t.Fatalf("expected an authenticated principal for Bearer auth")
	}

	// A Basic credential with an empty password carries no token, so the request
	// is treated as anonymous rather than authenticated.
	basic = base64.StdEncoding.EncodeToString([]byte("__token__:"))
	req = httptest.NewRequest("POST", "/r/python-packages/legacy/", nil)
	req.Header.Set("Authorization", "Basic "+basic)
	principal, err = svc.AuthenticateRequest(req.Context(), req)
	if err != nil {
		t.Fatalf("expected an empty Basic password to be anonymous, got error: %v", err)
	}
	if principal.Authenticated {
		t.Fatalf("expected an empty Basic password to stay anonymous, got %+v", principal)
	}
	// And it must not be allowed to publish.
	if err := svc.Authorize(principal, auth.AuthRequest{
		Policy:     policy,
		Permission: auth.PermissionPublish,
	}); err == nil {
		t.Fatalf("expected an anonymous publish to be rejected")
	}

	// A malformed Basic credential carries no usable token, so the request stays
	// anonymous instead of being authenticated as somebody.
	req = httptest.NewRequest("POST", "/r/python-packages/legacy/", nil)
	req.Header.Set("Authorization", "Basic !!!not-base64!!!")
	principal, err = svc.AuthenticateRequest(req.Context(), req)
	if err != nil {
		t.Fatalf("expected a malformed Basic credential to be anonymous, got error: %v", err)
	}
	if principal.Authenticated {
		t.Fatalf("expected a malformed Basic credential to stay anonymous, got %+v", principal)
	}
}
