package auth_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/amkargar/mirror-registry/backend/internal/auth"
	memstore "github.com/amkargar/mirror-registry/backend/internal/store/memory"
)

const testPassword = "correct-horse-battery"

func newService(t *testing.T) (*auth.Service, *memstore.Store) {
	t.Helper()
	store := memstore.New()
	return auth.NewService(store, time.Hour), store
}

// --- Password policy ---

func TestPasswordPolicyRejectsShortPasswords(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t)

	_, err := svc.CreateUser(ctx, auth.CreateUserInput{
		Username: "shorty",
		Password: "sixchars",
	})
	if err == nil {
		t.Fatal("expected an error for a 9-character password")
	}

	// The floor is 12 characters by default.
	_, err = svc.CreateUser(ctx, auth.CreateUserInput{
		Username: "elevenchars",
		Password: "elevenchars",
	})
	if err == nil {
		t.Fatal("expected an error for an 11-character password")
	}

	if _, err := svc.CreateUser(ctx, auth.CreateUserInput{
		Username: "gooduser",
		Password: "twelvechars12",
	}); err != nil {
		t.Fatalf("expected a 12+ character password to be accepted, got %v", err)
	}
}

func TestPasswordPolicyIsConfigurable(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t)
	svc.SetMinPasswordLength(8)

	if _, err := svc.CreateUser(ctx, auth.CreateUserInput{
		Username: "legacy",
		Password: "8charpass",
	}); err != nil {
		t.Fatalf("expected the relaxed policy to accept an 8-character password, got %v", err)
	}
}

// --- Bootstrap admin ---

func TestBootstrapAdminRecreatedWhenNoActiveAdminRemains(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t)

	created, err := svc.EnsureBootstrapAdmin(ctx, "admin", "admin@example.com", testPassword)
	if err != nil {
		t.Fatalf("EnsureBootstrapAdmin: %v", err)
	}
	if !created {
		t.Fatal("expected the first call to create the admin")
	}

	// A second call must not create another admin while one is active.
	created, err = svc.EnsureBootstrapAdmin(ctx, "admin", "admin@example.com", testPassword)
	if err != nil {
		t.Fatalf("EnsureBootstrapAdmin: %v", err)
	}
	if created {
		t.Fatal("expected no second admin while an active admin exists")
	}

	admins, err := svc.ListUsers(ctx)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(admins) != 1 {
		t.Fatalf("expected exactly 1 user, got %d", len(admins))
	}

	// The last-admin guard refuses to demote the only administrator, so the
	// only way to reach "no active admin" is to delete the account outright.
	if err := svc.DeleteUser(ctx, admins[0].ID); !errors.Is(err, auth.ErrLastAdmin) {
		t.Fatalf("expected ErrLastAdmin, got %v", err)
	}

	// With the guard in place an administrator always exists, so the bootstrap
	// correctly does not run a second time.
	created, err = svc.EnsureBootstrapAdmin(ctx, "admin2", "admin2@example.com", testPassword)
	if err != nil {
		t.Fatalf("EnsureBootstrapAdmin(after demotion): %v", err)
	}
	if created {
		t.Fatal("expected no bootstrap admin while an active admin exists")
	}
}

func TestBootstrapAdminRunsWhenNoActiveAdminRemains(t *testing.T) {
	ctx := context.Background()
	svc, store := newService(t)

	if _, err := svc.EnsureBootstrapAdmin(ctx, "admin", "admin@example.com", testPassword); err != nil {
		t.Fatalf("EnsureBootstrapAdmin: %v", err)
	}
	users, _ := svc.ListUsers(ctx)

	// Remove the administrator directly from the store, simulating an
	// installation whose only admin was deleted outside the service layer.
	if err := store.DeleteUser(ctx, users[0].ID); err != nil {
		t.Fatalf("store.DeleteUser: %v", err)
	}

	created, err := svc.EnsureBootstrapAdmin(ctx, "admin2", "admin2@example.com", testPassword)
	if err != nil {
		t.Fatalf("EnsureBootstrapAdmin: %v", err)
	}
	if !created {
		t.Fatal("expected a bootstrap admin to be created once no active admin remained")
	}
}

func TestBootstrapAdminRunsWhenTheOnlyAdminIsGone(t *testing.T) {
	ctx := context.Background()
	svc, store := newService(t)

	if _, err := svc.EnsureBootstrapAdmin(ctx, "admin", "admin@example.com", testPassword); err != nil {
		t.Fatalf("EnsureBootstrapAdmin: %v", err)
	}
	users, _ := svc.ListUsers(ctx)
	// The last-admin guard refuses to disable the only administrator, so the
	// account is removed from the store directly.
	if err := store.DeleteUser(ctx, users[0].ID); err != nil {
		t.Fatalf("store.DeleteUser: %v", err)
	}

	created, err := svc.EnsureBootstrapAdmin(ctx, "admin2", "admin2@example.com", testPassword)
	if err != nil {
		t.Fatalf("EnsureBootstrapAdmin: %v", err)
	}
	if !created {
		t.Fatal("expected a bootstrap admin to be created when the only admin was disabled")
	}
}

// --- Last administrator protection ---

func TestLastAdminCannotBeDemotedDisabledOrDeleted(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t)

	if _, err := svc.EnsureBootstrapAdmin(ctx, "admin", "admin@example.com", testPassword); err != nil {
		t.Fatalf("EnsureBootstrapAdmin: %v", err)
	}
	users, _ := svc.ListUsers(ctx)
	adminID := users[0].ID

	// Demotion is refused.
	role := auth.RoleUser
	if _, err := svc.UpdateUser(ctx, adminID, auth.UpdateUserInput{Role: &role}); !errors.Is(err, auth.ErrLastAdmin) {
		t.Fatalf("expected ErrLastAdmin when demoting the last admin, got %v", err)
	}

	// Disabling is refused.
	disabled := true
	if _, err := svc.UpdateUser(ctx, adminID, auth.UpdateUserInput{Disabled: &disabled}); !errors.Is(err, auth.ErrLastAdmin) {
		t.Fatalf("expected ErrLastAdmin when disabling the last admin, got %v", err)
	}

	// Deletion is refused.
	if err := svc.DeleteUser(ctx, adminID); !errors.Is(err, auth.ErrLastAdmin) {
		t.Fatalf("expected ErrLastAdmin when deleting the last admin, got %v", err)
	}
}

func TestAdminMayBeDemotedWhenAnotherAdminRemains(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t)

	if _, err := svc.EnsureBootstrapAdmin(ctx, "admin", "admin@example.com", testPassword); err != nil {
		t.Fatalf("EnsureBootstrapAdmin: %v", err)
	}
	second, err := svc.CreateUser(ctx, auth.CreateUserInput{
		Username: "admin2",
		Password: testPassword,
		Role:     auth.RoleAdmin,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	users, _ := svc.ListUsers(ctx)
	var admin1 string
	for _, u := range users {
		if u.ID != second.ID {
			admin1 = u.ID
		}
	}

	role := auth.RoleUser
	if _, err := svc.UpdateUser(ctx, admin1, auth.UpdateUserInput{Role: &role}); err != nil {
		t.Fatalf("expected demotion to be allowed while another admin exists, got %v", err)
	}
}

// --- Session invalidation ---

func TestPasswordChangeInvalidatesSessionsAndTokens(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t)

	if _, err := svc.EnsureBootstrapAdmin(ctx, "admin", "admin@example.com", testPassword); err != nil {
		t.Fatalf("EnsureBootstrapAdmin: %v", err)
	}
	users, _ := svc.ListUsers(ctx)
	adminID := users[0].ID

	_, rawSession, _, err := svc.Login(ctx, "admin", testPassword, "agent", "127.0.0.1")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	_, rawToken, err := svc.CreateToken(ctx, auth.CreateTokenInput{
		Name:   "ci",
		UserID: adminID,
		Scopes: []auth.Permission{auth.PermissionRead},
	})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	// Both credentials work before the change.
	if _, err := svc.AuthenticateRequest(ctx, bearerRequest(rawSession)); err != nil {
		t.Fatalf("expected the session to authenticate: %v", err)
	}
	if _, err := svc.AuthenticateRequest(ctx, bearerRequest(rawToken)); err != nil {
		t.Fatalf("expected the token to authenticate: %v", err)
	}

	newPassword := "a-brand-new-password"
	if _, err := svc.ChangePassword(ctx, adminID, testPassword, newPassword); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}

	// The session and the token must both stop working.
	if _, err := svc.AuthenticateRequest(ctx, bearerRequest(rawSession)); err == nil {
		t.Fatal("expected the session to be invalidated after a password change")
	}
	if _, err := svc.AuthenticateRequest(ctx, bearerRequest(rawToken)); err == nil {
		t.Fatal("expected the token to be revoked after a password change")
	}
}

func TestChangePasswordRequiresTheCurrentPassword(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t)

	if _, err := svc.EnsureBootstrapAdmin(ctx, "admin", "admin@example.com", testPassword); err != nil {
		t.Fatalf("EnsureBootstrapAdmin: %v", err)
	}
	users, _ := svc.ListUsers(ctx)

	if _, err := svc.ChangePassword(ctx, users[0].ID, "not-the-password", "a-brand-new-password"); !errors.Is(err, auth.ErrInvalidCreds) {
		t.Fatalf("expected ErrInvalidCreds, got %v", err)
	}
}

func TestRoleChangeInvalidatesSessions(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t)

	if _, err := svc.EnsureBootstrapAdmin(ctx, "admin", "admin@example.com", testPassword); err != nil {
		t.Fatalf("EnsureBootstrapAdmin: %v", err)
	}
	member, err := svc.CreateUser(ctx, auth.CreateUserInput{
		Username: "member",
		Password: testPassword,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	_, rawSession, _, err := svc.Login(ctx, "member", testPassword, "agent", "127.0.0.1")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if _, err := svc.AuthenticateRequest(ctx, bearerRequest(rawSession)); err != nil {
		t.Fatalf("expected the session to authenticate: %v", err)
	}

	role := auth.RoleAdmin
	if _, err := svc.UpdateUser(ctx, member.ID, auth.UpdateUserInput{Role: &role}); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}

	if _, err := svc.AuthenticateRequest(ctx, bearerRequest(rawSession)); err == nil {
		t.Fatal("expected the session to be invalidated after a role change")
	}
}

func TestDisablingUserInvalidatesTheirSession(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t)

	member, err := svc.CreateUser(ctx, auth.CreateUserInput{
		Username: "member",
		Password: testPassword,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	_, rawSession, _, err := svc.Login(ctx, "member", testPassword, "agent", "127.0.0.1")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	disabled := true
	if _, err := svc.UpdateUser(ctx, member.ID, auth.UpdateUserInput{Disabled: &disabled}); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}

	if _, err := svc.AuthenticateRequest(ctx, bearerRequest(rawSession)); err == nil {
		t.Fatal("expected the session to be invalidated after the account was disabled")
	}
}

// --- Token lifecycle ---

func TestDeletingUserRevokesTheirTokens(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t)

	if _, err := svc.EnsureBootstrapAdmin(ctx, "admin", "admin@example.com", testPassword); err != nil {
		t.Fatalf("EnsureBootstrapAdmin: %v", err)
	}
	member, err := svc.CreateUser(ctx, auth.CreateUserInput{
		Username: "member",
		Password: testPassword,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	_, rawToken, err := svc.CreateToken(ctx, auth.CreateTokenInput{
		Name:   "member-token",
		UserID: member.ID,
		Scopes: []auth.Permission{auth.PermissionRead},
	})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if _, err := svc.AuthenticateRequest(ctx, bearerRequest(rawToken)); err != nil {
		t.Fatalf("expected the token to authenticate: %v", err)
	}

	if err := svc.DeleteUser(ctx, member.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}

	// The token must not authenticate any more: the owner no longer exists.
	if _, err := svc.AuthenticateRequest(ctx, bearerRequest(rawToken)); err == nil {
		t.Fatal("expected the deleted user's token to stop authenticating")
	}
}

func TestTokenOfDeletedUserIsRejectedEvenWithoutRevocation(t *testing.T) {
	ctx := context.Background()
	svc, store := newService(t)

	if _, err := svc.EnsureBootstrapAdmin(ctx, "admin", "admin@example.com", testPassword); err != nil {
		t.Fatalf("EnsureBootstrapAdmin: %v", err)
	}
	member, err := svc.CreateUser(ctx, auth.CreateUserInput{
		Username: "member",
		Password: testPassword,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	_, rawToken, err := svc.CreateToken(ctx, auth.CreateTokenInput{
		Name:   "member-token",
		UserID: member.ID,
		Scopes: []auth.Permission{auth.PermissionRead},
	})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	// Remove the account behind the service's back so the token is still
	// present but its owner is gone — exactly the situation that used to let a
	// deleted user keep authenticating.
	if err := store.DeleteUser(ctx, member.ID); err != nil {
		t.Fatalf("store.DeleteUser: %v", err)
	}

	principal, err := svc.AuthenticateRequest(ctx, bearerRequest(rawToken))
	if err == nil {
		t.Fatal("expected authentication to fail for a token whose owner was deleted")
	}
	if principal != nil && principal.Authenticated {
		t.Fatal("expected the principal to be unauthenticated")
	}
}

func TestTokenCannotExceedTheOwnerPermissions(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t)

	member, err := svc.CreateUser(ctx, auth.CreateUserInput{
		Username: "member",
		Password: testPassword,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// A regular user must not be able to mint an admin-scoped token.
	_, _, err = svc.CreateToken(ctx, auth.CreateTokenInput{
		Name:   "escalation",
		UserID: member.ID,
		Scopes: []auth.Permission{auth.PermissionRead, auth.PermissionAdmin},
	})
	if !errors.Is(err, auth.ErrInvalidScope) {
		t.Fatalf("expected ErrInvalidScope for an admin-scoped token owned by a regular user, got %v", err)
	}

	// A mixed request that includes the admin scope is refused outright rather
	// than silently narrowed.
	if _, _, err := svc.CreateToken(ctx, auth.CreateTokenInput{
		Name:   "mixed",
		UserID: member.ID,
		Scopes: []auth.Permission{auth.PermissionRead, auth.PermissionAdmin},
	}); !errors.Is(err, auth.ErrInvalidScope) {
		t.Fatalf("expected ErrInvalidScope for a mixed request, got %v", err)
	}

	// A purely non-admin request is accepted.
	tok, _, err := svc.CreateToken(ctx, auth.CreateTokenInput{
		Name:   "allowed",
		UserID: member.ID,
		Scopes: []auth.Permission{auth.PermissionRead, auth.PermissionPublish},
	})
	if err != nil {
		t.Fatalf("expected a non-admin token to be created, got %v", err)
	}
	for _, sc := range tok.Scopes {
		if sc == auth.PermissionAdmin {
			t.Fatal("expected no admin scope on a regular user's token")
		}
	}
}

func TestDemotedOwnersTokenLosesAdminScope(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t)

	if _, err := svc.EnsureBootstrapAdmin(ctx, "admin", "admin@example.com", testPassword); err != nil {
		t.Fatalf("EnsureBootstrapAdmin: %v", err)
	}
	users, _ := svc.ListUsers(ctx)
	adminID := users[0].ID

	// A second admin so the first one can be demoted.
	if _, err := svc.CreateUser(ctx, auth.CreateUserInput{
		Username: "admin2",
		Password: testPassword,
		Role:     auth.RoleAdmin,
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	_, rawToken, err := svc.CreateToken(ctx, auth.CreateTokenInput{
		Name:   "admin-token",
		UserID: adminID,
		Scopes: []auth.Permission{auth.PermissionAdmin},
	})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	role := auth.RoleUser
	if _, err := svc.UpdateUser(ctx, adminID, auth.UpdateUserInput{Role: &role}); err != nil {
		t.Fatalf("UpdateUser(demote): %v", err)
	}

	principal, err := svc.AuthenticateRequest(ctx, bearerRequest(rawToken))
	if err != nil {
		t.Fatalf("AuthenticateRequest: %v", err)
	}
	err = svc.Authorize(principal, auth.AuthRequest{
		Permission: auth.PermissionAdmin,
		Policy:     auth.RegistryAccessPolicy{RegistryID: "reg-1"},
	})
	if !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("expected the demoted owner's admin token to be forbidden, got %v", err)
	}
}

// --- Token ownership ---

func TestPersonalTokenManagementEnforcesOwnership(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t)

	if _, err := svc.EnsureBootstrapAdmin(ctx, "admin", "admin@example.com", testPassword); err != nil {
		t.Fatalf("EnsureBootstrapAdmin: %v", err)
	}
	owner, err := svc.CreateUser(ctx, auth.CreateUserInput{
		Username: "owner",
		Password: testPassword,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	other, err := svc.CreateUser(ctx, auth.CreateUserInput{
		Username: "other",
		Password: testPassword,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	tok, _, err := svc.CreateToken(ctx, auth.CreateTokenInput{
		Name:   "owned",
		UserID: owner.ID,
		Scopes: []auth.Permission{auth.PermissionRead},
	})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	// The owner may revoke and delete their own token.
	if err := svc.RevokeTokenForUser(ctx, tok.ID, owner.ID); err != nil {
		t.Fatalf("expected the owner to revoke their own token, got %v", err)
	}
	if err := svc.DeleteTokenForUser(ctx, tok.ID, owner.ID); err != nil {
		t.Fatalf("expected the owner to delete their own token, got %v", err)
	}

	// Another user may not.
	tok2, _, err := svc.CreateToken(ctx, auth.CreateTokenInput{
		Name:   "owned2",
		UserID: owner.ID,
		Scopes: []auth.Permission{auth.PermissionRead},
	})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if err := svc.RevokeTokenForUser(ctx, tok2.ID, other.ID); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("expected ErrForbidden when revoking somebody else's token, got %v", err)
	}
	if err := svc.DeleteTokenForUser(ctx, tok2.ID, other.ID); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("expected ErrForbidden when deleting somebody else's token, got %v", err)
	}
}

func TestListUserTokensReturnsOnlyThatUsersTokens(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t)

	a, err := svc.CreateUser(ctx, auth.CreateUserInput{Username: "a", Password: testPassword})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	b, err := svc.CreateUser(ctx, auth.CreateUserInput{Username: "b", Password: testPassword})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, _, err := svc.CreateToken(ctx, auth.CreateTokenInput{
		Name: "a-token", UserID: a.ID, Scopes: []auth.Permission{auth.PermissionRead},
	}); err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if _, _, err := svc.CreateToken(ctx, auth.CreateTokenInput{
		Name: "b-token", UserID: b.ID, Scopes: []auth.Permission{auth.PermissionRead},
	}); err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	tokens, err := svc.ListUserTokens(ctx, a.ID)
	if err != nil {
		t.Fatalf("ListUserTokens: %v", err)
	}
	if len(tokens) != 1 || tokens[0].Name != "a-token" {
		t.Fatalf("expected exactly the owner's token, got %+v", tokens)
	}
}

// --- Username editing ---

func TestUsernameCanBeEditedAndMustBeUnique(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t)

	a, err := svc.CreateUser(ctx, auth.CreateUserInput{Username: "alice", Password: testPassword})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := svc.CreateUser(ctx, auth.CreateUserInput{Username: "bob", Password: testPassword}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	newName := "alice2"
	updated, err := svc.UpdateUser(ctx, a.ID, auth.UpdateUserInput{Username: &newName})
	if err != nil {
		t.Fatalf("UpdateUser(username): %v", err)
	}
	if updated.Username != newName {
		t.Fatalf("expected the username to be updated, got %q", updated.Username)
	}

	// The new username must be usable for sign-in.
	if _, _, _, err := svc.Login(ctx, newName, testPassword, "agent", "127.0.0.1"); err != nil {
		t.Fatalf("expected to sign in with the new username: %v", err)
	}

	// Taking another user's username is refused.
	taken := "bob"
	if _, err := svc.UpdateUser(ctx, a.ID, auth.UpdateUserInput{Username: &taken}); !errors.Is(err, auth.ErrAlreadyExists) {
		t.Fatalf("expected ErrAlreadyExists, got %v", err)
	}

	// An empty username is refused.
	empty := "  "
	if _, err := svc.UpdateUser(ctx, a.ID, auth.UpdateUserInput{Username: &empty}); err == nil {
		t.Fatal("expected an error for an empty username")
	}
}

func TestUsernameChangeKeepsTheOldUsernameUnusable(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t)

	a, err := svc.CreateUser(ctx, auth.CreateUserInput{Username: "alice", Password: testPassword})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	newName := "alice2"
	if _, err := svc.UpdateUser(ctx, a.ID, auth.UpdateUserInput{Username: &newName}); err != nil {
		t.Fatalf("UpdateUser(username): %v", err)
	}
	if _, _, _, err := svc.Login(ctx, "alice", testPassword, "agent", "127.0.0.1"); err == nil {
		t.Fatal("expected the old username to stop working")
	}
}

// --- Helpers ---

func bearerRequest(raw string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	return req
}

// TestBootstrapErrorIsActionableWhenUsernameIsTaken covers the recovery path
// where the installation has no active administrator but the configured
// administrator username belongs to a non-admin account. The server must
// refuse to start, but with a message that tells the operator how to recover
// rather than a bare "already exists".
func TestBootstrapErrorIsActionableWhenUsernameIsTaken(t *testing.T) {
	ms := memstore.New()
	svc := auth.NewService(ms, time.Hour)
	ctx := context.Background()

	if _, err := svc.CreateUser(ctx, auth.CreateUserInput{
		Username: "admin",
		Email:    "squatter@example.com",
		Password: "squatter-password-1",
		Role:     auth.RoleUser,
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	created, err := svc.EnsureBootstrapAdmin(ctx, "admin", "admin@example.com", "recovery-password-1")
	if created {
		t.Fatal("expected no administrator to be created")
	}
	if err == nil {
		t.Fatal("expected an error when the configured username is taken")
	}
	msg := err.Error()
	for _, want := range []string{"admin", "ADMIN_USERNAME", "non-administrator"} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected the error to mention %q so the operator can recover, got: %s", want, msg)
		}
	}

	// The existing account must be left untouched: bootstrap must never
	// silently promote an account or reset its password.
	existing, err := ms.GetUserByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if existing.Role != auth.RoleUser {
		t.Errorf("bootstrap must not change the existing account's role, got %q", existing.Role)
	}
	if _, _, _, err := svc.Login(ctx, "admin", "squatter-password-1", "test", "127.0.0.1"); err != nil {
		t.Errorf("the existing account's password must be unchanged: %v", err)
	}
}
