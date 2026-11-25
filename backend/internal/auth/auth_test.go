package auth_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/amkargar/mirror-registry/backend/internal/auth"
	memstore "github.com/amkargar/mirror-registry/backend/internal/store/memory"
)

func TestRegistryScopedTokenIsolationAndNamespaces(t *testing.T) {
	ctx := context.Background()
	svc := auth.NewService(memstore.New(), time.Hour)

	_, err := svc.EnsureBootstrapAdmin(ctx, "admin", "admin@example.com", "secret123")
	if err != nil {
		t.Fatalf("EnsureBootstrapAdmin: %v", err)
	}

	user, _, _, err := svc.Login(ctx, "admin", "secret123", "test-agent", "127.0.0.1")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	// Token scoped strictly to reg-pub-private with read+publish and acme_* namespace
	_, rawToken, err := svc.CreateToken(ctx, auth.CreateTokenInput{
		Name:         "flutter-ci",
		UserID:       user.ID,
		RegistryID:   "reg-pub-private",
		RegistrySlug: "pub-private",
		RegistryType: "pub",
		Scopes:       []auth.Permission{auth.PermissionRead, auth.PermissionPublish},
		Namespaces:   []string{"acme_*"},
	})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+rawToken)
	principal, err := svc.AuthenticateRequest(ctx, req)
	if err != nil {
		t.Fatalf("AuthenticateRequest: %v", err)
	}

	privPolicy := auth.RegistryAccessPolicy{
		RegistryID:         "reg-pub-private",
		RegistrySlug:       "pub-private",
		RegistryType:       "pub",
		Public:             false,
		AllowAnonymousRead: false,
	}
	otherPubPolicy := auth.RegistryAccessPolicy{
		RegistryID:         "reg-pub-team-b",
		RegistrySlug:       "pub-team-b",
		RegistryType:       "pub",
		Public:             false,
		AllowAnonymousRead: false,
	}
	publicPolicy := auth.RegistryAccessPolicy{
		RegistryID:         "reg-pub-public",
		RegistrySlug:       "pub-public",
		RegistryType:       "pub",
		Public:             true,
		AllowAnonymousRead: true,
	}

	// Allowed: publish acme_kit to pub-private
	if err := svc.Authorize(principal, auth.AuthRequest{
		Policy:     privPolicy,
		Permission: auth.PermissionPublish,
		Namespace:  "acme_kit",
	}); err != nil {
		t.Fatalf("expected allowed publish to pub-private acme_kit, got %v", err)
	}

	// Denied: wrong namespace on same registry
	if err := svc.Authorize(principal, auth.AuthRequest{
		Policy:     privPolicy,
		Permission: auth.PermissionPublish,
		Namespace:  "other_pkg",
	}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for disallowed namespace, got %v", err)
	}

	// Denied: wrong scope (delete) on same registry
	if err := svc.Authorize(principal, auth.AuthRequest{
		Policy:     privPolicy,
		Permission: auth.PermissionDelete,
		Namespace:  "acme_kit",
	}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for missing delete scope, got %v", err)
	}

	// Denied: different registry instance of the same type
	if err := svc.Authorize(principal, auth.AuthRequest{
		Policy:     otherPubPolicy,
		Permission: auth.PermissionRead,
		Namespace:  "acme_kit",
	}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("expected ErrForbidden across registry instances, got %v", err)
	}

	// Anonymous access: allowed on public registry, denied on private registry
	anon := &auth.Principal{Authenticated: false, Kind: "anonymous"}
	if err := svc.Authorize(anon, auth.AuthRequest{
		Policy:     publicPolicy,
		Permission: auth.PermissionRead,
	}); err != nil {
		t.Fatalf("expected anonymous read on public registry, got %v", err)
	}
	if err := svc.Authorize(anon, auth.AuthRequest{
		Policy:     privPolicy,
		Permission: auth.PermissionRead,
	}); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated for anonymous read on private registry, got %v", err)
	}
}
