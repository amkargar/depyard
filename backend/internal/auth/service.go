package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const SessionCookieName = "mr_session"

// DefaultMinPasswordLength is the minimum length of a user password.
//
// The platform used to accept six characters, which is far too weak for an
// account that can administer a package registry, so the floor is now twelve.
const DefaultMinPasswordLength = 12

// Service handles centralized authentication and registry-scoped authorization.
type Service struct {
	store          Store
	sessionTTL     time.Duration
	minPasswordLen int
}

func NewService(store Store, sessionTTL time.Duration) *Service {
	if sessionTTL <= 0 {
		sessionTTL = 24 * time.Hour
	}
	return &Service{
		store:          store,
		sessionTTL:     sessionTTL,
		minPasswordLen: DefaultMinPasswordLength,
	}
}

// SetMinPasswordLength overrides the minimum accepted password length.
//
// It exists so an operator with an existing corpus of short passwords can relax
// the policy deliberately. A value below 1 is ignored.
func (s *Service) SetMinPasswordLength(n int) {
	if n < 1 {
		return
	}
	s.minPasswordLen = n
}

// MinPasswordLength returns the minimum accepted password length.
func (s *Service) MinPasswordLength() int {
	if s.minPasswordLen < 1 {
		return DefaultMinPasswordLength
	}
	return s.minPasswordLen
}

// EnsureBootstrapAdmin creates an initial admin user when the installation has
// no usable administrator.
//
// The check is deliberately "does an active admin exist" rather than "does any
// user exist": an installation whose only administrator was deleted or demoted
// would otherwise be permanently locked out of its own admin console.
func (s *Service) EnsureBootstrapAdmin(ctx context.Context, username, email, password string) (bool, error) {
	admins, err := s.store.CountActiveAdmins(ctx)
	if err != nil {
		return false, err
	}
	if admins > 0 {
		return false, nil
	}
	_, err = s.CreateUser(ctx, CreateUserInput{
		Username: username,
		Email:    email,
		Password: password,
		Role:     RoleAdmin,
	})
	if errors.Is(err, ErrAlreadyExists) {
		// The installation has no active administrator, yet the configured
		// identity is already in use by an account that cannot administer it.
		// Refusing to start is correct — running without an administrator is
		// not a usable state — but a bare "already exists" leaves the operator
		// with no way forward, so say exactly which identity clashes and how to
		// resolve it.
		if existing, lookupErr := s.store.GetUserByUsername(ctx, username); lookupErr == nil && existing != nil {
			return false, fmt.Errorf(
				"cannot bootstrap administrator: username %q is already used by a non-administrator account "+
					"(role %q, disabled %t); set ADMIN_USERNAME to a different name and restart, "+
					"or restore administrator access for that account",
				username, existing.Role, existing.Disabled)
		}
		return false, fmt.Errorf(
			"cannot bootstrap administrator: email %q is already used by another account; "+
				"set ADMIN_EMAIL to a different address and restart", email)
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// CountActiveAdmins returns the number of administrators that can still sign in.
func (s *Service) CountActiveAdmins(ctx context.Context) (int64, error) {
	return s.store.CountActiveAdmins(ctx)
}

type CreateUserInput struct {
	Username string
	Email    string
	Password string
	Role     Role
}

func (s *Service) CreateUser(ctx context.Context, in CreateUserInput) (*User, error) {
	username := strings.TrimSpace(in.Username)
	if username == "" {
		return nil, fmt.Errorf("username is required")
	}
	if err := s.validatePassword(in.Password); err != nil {
		return nil, err
	}
	role := in.Role
	if role == "" {
		role = RoleUser
	}
	if role != RoleAdmin && role != RoleUser {
		return nil, fmt.Errorf("invalid role %q", role)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	now := time.Now().UTC()
	u := &User{
		ID:           newID(),
		Username:     username,
		Email:        strings.TrimSpace(in.Email),
		PasswordHash: string(hash),
		Role:         role,
		Disabled:     false,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.store.CreateUser(ctx, u); err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Service) ListUsers(ctx context.Context) ([]*User, error) {
	return s.store.ListUsers(ctx)
}

func (s *Service) GetUserByID(ctx context.Context, id string) (*User, error) {
	return s.store.GetUserByID(ctx, id)
}

type UpdateUserInput struct {
	Username *string
	Email    *string
	Role     *Role
	Disabled *bool
	Password *string
}

// UpdateUser applies a partial update to a user account.
//
// Credential-affecting changes (password, role, disabled state) invalidate the
// user's sessions, and a password change also revokes their access tokens,
// because a token is an alternative credential that would otherwise outlive the
// password it was issued alongside.
//
// Demoting or disabling the last active administrator is refused so an
// installation can never lock every administrator out.
func (s *Service) UpdateUser(ctx context.Context, id string, in UpdateUserInput) (*User, error) {
	u, err := s.store.GetUserByID(ctx, id)
	if err != nil {
		return nil, err
	}

	// Refuse to remove the last way into the admin console.
	if u.Role == RoleAdmin && !u.Disabled {
		losesAdmin := false
		if in.Role != nil && *in.Role != RoleAdmin {
			losesAdmin = true
		}
		if in.Disabled != nil && *in.Disabled {
			losesAdmin = true
		}
		if losesAdmin {
			admins, err := s.store.CountActiveAdmins(ctx)
			if err != nil {
				return nil, err
			}
			if admins <= 1 {
				return nil, ErrLastAdmin
			}
		}
	}

	if in.Username != nil {
		username := strings.TrimSpace(*in.Username)
		if username == "" {
			return nil, fmt.Errorf("username is required")
		}
		if username != u.Username {
			if existing, err := s.store.GetUserByUsername(ctx, username); err == nil && existing.ID != u.ID {
				return nil, ErrAlreadyExists
			} else if err != nil && !errors.Is(err, ErrNotFound) {
				return nil, err
			}
		}
		u.Username = username
	}
	if in.Email != nil {
		u.Email = strings.TrimSpace(*in.Email)
	}
	// Capture the previous role before it is overwritten, so the session
	// invalidation below knows whether the role actually changed.
	roleChanged := in.Role != nil && *in.Role != u.Role
	if in.Role != nil {
		if *in.Role != RoleAdmin && *in.Role != RoleUser {
			return nil, fmt.Errorf("invalid role %q", *in.Role)
		}
		u.Role = *in.Role
	}
	if in.Disabled != nil {
		u.Disabled = *in.Disabled
	}

	passwordChanged := false
	if in.Password != nil && *in.Password != "" {
		if err := s.validatePassword(*in.Password); err != nil {
			return nil, err
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(*in.Password), bcrypt.DefaultCost)
		if err != nil {
			return nil, err
		}
		u.PasswordHash = string(hash)
		passwordChanged = true
	}

	u.UpdatedAt = time.Now().UTC()
	if err := s.store.UpdateUser(ctx, u); err != nil {
		return nil, err
	}

	// A disabled account must not keep an active session.
	if u.Disabled {
		_ = s.store.DeleteSessionsByUser(ctx, u.ID)
	}
	// Changing the password invalidates sessions and tokens issued under the old
	// credential.
	if passwordChanged {
		_ = s.store.DeleteSessionsByUser(ctx, u.ID)
		_ = s.store.RevokeTokensByUser(ctx, u.ID)
	}
	// A role change alters what the account is allowed to do, so the sessions
	// that were opened under the old role are no longer trustworthy.
	if roleChanged {
		_ = s.store.DeleteSessionsByUser(ctx, u.ID)
	}
	return u, nil
}

// DeleteUser removes a user account and revokes every credential it owned.
//
// Sessions and access tokens are deleted alongside the account so that a token
// belonging to a deleted user cannot keep authenticating.
func (s *Service) DeleteUser(ctx context.Context, id string) error {
	u, err := s.store.GetUserByID(ctx, id)
	if err != nil {
		return err
	}
	if u.Role == RoleAdmin && !u.Disabled {
		admins, err := s.store.CountActiveAdmins(ctx)
		if err != nil {
			return err
		}
		if admins <= 1 {
			return ErrLastAdmin
		}
	}
	_ = s.store.DeleteSessionsByUser(ctx, id)
	_ = s.store.RevokeTokensByUser(ctx, id)
	return s.store.DeleteUser(ctx, id)
}

// ChangePassword lets a user replace their own password.
//
// The current password must be supplied, and every other session and token of
// that user is revoked so a stolen credential cannot outlive the change.
func (s *Service) ChangePassword(ctx context.Context, id, currentPassword, newPassword string) (*User, error) {
	u, err := s.store.GetUserByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(currentPassword)); err != nil {
		return nil, ErrInvalidCreds
	}
	if err := s.validatePassword(newPassword); err != nil {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	u.PasswordHash = string(hash)
	u.UpdatedAt = time.Now().UTC()
	if err := s.store.UpdateUser(ctx, u); err != nil {
		return nil, err
	}
	_ = s.store.DeleteSessionsByUser(ctx, u.ID)
	_ = s.store.RevokeTokensByUser(ctx, u.ID)
	return u, nil
}

// validatePassword enforces the password policy.
func (s *Service) validatePassword(password string) error {
	if len(password) < s.MinPasswordLength() {
		return fmt.Errorf("password must be at least %d characters", s.MinPasswordLength())
	}
	return nil
}

// Login validates credentials and returns a new opaque session token along with the user.
func (s *Service) Login(ctx context.Context, username, password, userAgent, ipAddr string) (*User, string, *Session, error) {
	u, err := s.store.GetUserByUsername(ctx, strings.TrimSpace(username))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, "", nil, ErrInvalidCreds
		}
		return nil, "", nil, err
	}
	if u.Disabled {
		return nil, "", nil, ErrUserDisabled
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return nil, "", nil, ErrInvalidCreds
	}

	rawToken := "mrs_" + randomSecret(32)
	now := time.Now().UTC()
	sess := &Session{
		ID:        newID(),
		TokenHash: hashSecret(rawToken),
		UserID:    u.ID,
		UserAgent: userAgent,
		IPAddress: ipAddr,
		ExpiresAt: now.Add(s.sessionTTL),
		CreatedAt: now,
	}
	if err := s.store.CreateSession(ctx, sess); err != nil {
		return nil, "", nil, err
	}
	return u, rawToken, sess, nil
}

func (s *Service) Logout(ctx context.Context, rawSessionToken string) error {
	if rawSessionToken == "" {
		return nil
	}
	return s.store.DeleteSession(ctx, hashSecret(rawSessionToken))
}

type CreateTokenInput struct {
	Name         string
	UserID       string
	RegistryID   string
	RegistrySlug string
	RegistryType string
	Scopes       []Permission
	Namespaces   []string
	ExpiresIn    *time.Duration
}

// CreateToken generates a new scoped access token. The plaintext token is returned only once.
func (s *Service) CreateToken(ctx context.Context, in CreateTokenInput) (*AccessToken, string, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, "", fmt.Errorf("token name is required")
	}
	if len(in.Scopes) == 0 {
		return nil, "", fmt.Errorf("at least one permission scope is required")
	}
	seen := map[Permission]bool{}
	cleanScopes := make([]Permission, 0, len(in.Scopes))
	for _, sc := range in.Scopes {
		switch sc {
		case PermissionRead, PermissionPublish, PermissionDelete, PermissionAdmin:
			if !seen[sc] {
				seen[sc] = true
				cleanScopes = append(cleanScopes, sc)
			}
		default:
			return nil, "", fmt.Errorf("%w: %q", ErrInvalidScope, sc)
		}
	}

	cleanNS := make([]string, 0, len(in.Namespaces))
	for _, ns := range in.Namespaces {
		if trimmed := strings.TrimSpace(ns); trimmed != "" {
			cleanNS = append(cleanNS, trimmed)
		}
	}

	// A token may never grant more than its owner has. Owners that are not
	// administrators cannot mint admin-scoped tokens, and admin scope is
	// stripped from the requested scopes rather than rejected so that a
	// well-intentioned request still yields a usable (narrower) token.
	if in.UserID != "" {
		owner, err := s.store.GetUserByID(ctx, in.UserID)
		if err != nil {
			return nil, "", err
		}
		if owner.Disabled {
			return nil, "", ErrUserDisabled
		}
		if owner.Role != RoleAdmin {
			filtered := make([]Permission, 0, len(cleanScopes))
			for _, sc := range cleanScopes {
				if sc == PermissionAdmin {
					continue
				}
				filtered = append(filtered, sc)
			}
			if len(filtered) != len(cleanScopes) {
				// The request asked for administrative rights the owner does not
				// have. Silently narrowing it would hide the mistake, so the
				// request is refused.
				return nil, "", fmt.Errorf("%w: token scope exceeds the owner's permissions", ErrInvalidScope)
			}
			cleanScopes = filtered
		}
	}

	rawSecret := "mr_" + randomSecret(32)
	prefix := rawSecret[:11]
	now := time.Now().UTC()
	var expiresAt *time.Time
	if in.ExpiresIn != nil && *in.ExpiresIn > 0 {
		t := now.Add(*in.ExpiresIn)
		expiresAt = &t
	}

	tok := &AccessToken{
		ID:           newID(),
		Name:         name,
		Prefix:       prefix,
		TokenHash:    hashSecret(rawSecret),
		UserID:       in.UserID,
		RegistryID:   strings.TrimSpace(in.RegistryID),
		RegistrySlug: strings.TrimSpace(in.RegistrySlug),
		RegistryType: strings.TrimSpace(in.RegistryType),
		Scopes:       cleanScopes,
		Namespaces:   cleanNS,
		ExpiresAt:    expiresAt,
		CreatedAt:    now,
	}
	if err := s.store.CreateToken(ctx, tok); err != nil {
		return nil, "", err
	}
	return tok, rawSecret, nil
}

func (s *Service) ListTokens(ctx context.Context, registryID string) ([]*AccessToken, error) {
	return s.store.ListTokens(ctx, registryID)
}

// ListUserTokens returns the access tokens owned by a single user.
func (s *Service) ListUserTokens(ctx context.Context, userID string) ([]*AccessToken, error) {
	return s.store.ListTokensByUser(ctx, userID)
}

// TokenOwnerID returns the id of the user that owns a token.
func (s *Service) TokenOwnerID(ctx context.Context, tokenID string) (string, error) {
	tok, err := s.store.GetTokenByID(ctx, tokenID)
	if err != nil {
		return "", err
	}
	return tok.UserID, nil
}

// RevokeToken revokes a token after checking that the caller may do so.
//
// When caller is empty the revocation is administrative and unrestricted.
// Otherwise the token must belong to the caller: a user can never revoke
// somebody else's token.
func (s *Service) RevokeToken(ctx context.Context, id string) error {
	return s.revokeTokenAs(ctx, id, "")
}

// RevokeTokenForUser revokes a token on behalf of a specific user, enforcing
// ownership.
func (s *Service) RevokeTokenForUser(ctx context.Context, id, callerUserID string) error {
	return s.revokeTokenAs(ctx, id, callerUserID)
}

func (s *Service) revokeTokenAs(ctx context.Context, id, callerUserID string) error {
	if callerUserID != "" {
		tok, err := s.store.GetTokenByID(ctx, id)
		if err != nil {
			return err
		}
		if tok.UserID != callerUserID {
			return ErrForbidden
		}
	}
	return s.store.RevokeToken(ctx, id)
}

// DeleteToken removes a token after checking that the caller may do so.
//
// Ownership rules mirror RevokeToken: a non-admin caller may only delete their
// own tokens.
func (s *Service) DeleteToken(ctx context.Context, id string) error {
	return s.DeleteTokenForUser(ctx, id, "")
}

// DeleteTokenForUser deletes a token on behalf of a specific user, enforcing
// ownership.
func (s *Service) DeleteTokenForUser(ctx context.Context, id, callerUserID string) error {
	if callerUserID != "" {
		tok, err := s.store.GetTokenByID(ctx, id)
		if err != nil {
			return err
		}
		if tok.UserID != callerUserID {
			return ErrForbidden
		}
	}
	return s.store.DeleteToken(ctx, id)
}

// AuthenticateRequest resolves the caller from Authorization Bearer header or session cookie.
// If no credentials are present, it returns an anonymous Principal with nil error.
// If credentials ARE present but invalid/expired/revoked, it returns an error so callers can return 401.
func (s *Service) AuthenticateRequest(ctx context.Context, r *http.Request) (*Principal, error) {
	_, rawBearer, hasCreds := extractCredentials(r)
	if hasCreds {
		if strings.HasPrefix(rawBearer, "mrs_") {
			return s.authenticateSessionToken(ctx, rawBearer)
		}
		return s.authenticateAccessToken(ctx, rawBearer)
	}

	if cookie, err := r.Cookie(SessionCookieName); err == nil && cookie.Value != "" {
		return s.authenticateSessionToken(ctx, cookie.Value)
	}

	return &Principal{Authenticated: false, Kind: "anonymous"}, nil
}

func (s *Service) authenticateSessionToken(ctx context.Context, raw string) (*Principal, error) {
	sess, err := s.store.GetSessionByTokenHash(ctx, hashSecret(raw))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrUnauthenticated
		}
		return nil, err
	}
	if time.Now().UTC().After(sess.ExpiresAt) {
		_ = s.store.DeleteSession(ctx, sess.TokenHash)
		return nil, ErrTokenExpired
	}
	user, err := s.store.GetUserByID(ctx, sess.UserID)
	if err != nil {
		return nil, ErrUnauthenticated
	}
	if user.Disabled {
		return nil, ErrUserDisabled
	}
	return &Principal{
		Authenticated: true,
		Kind:          "user",
		User:          user,
	}, nil
}

func (s *Service) authenticateAccessToken(ctx context.Context, raw string) (*Principal, error) {
	tok, err := s.store.GetTokenByHash(ctx, hashSecret(raw))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrUnauthenticated
		}
		return nil, err
	}
	if tok.Revoked {
		return nil, ErrTokenRevoked
	}
	if tok.ExpiresAt != nil && time.Now().UTC().After(*tok.ExpiresAt) {
		return nil, ErrTokenExpired
	}
	// Resolve the token owner. A token that references a user which no longer
	// exists (or cannot be loaded) must not authenticate: silently ignoring the
	// lookup failure would leave deleted users' tokens working.
	var owner *User
	if tok.UserID != "" {
		u, err := s.store.GetUserByID(ctx, tok.UserID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				_ = s.store.RevokeToken(ctx, tok.ID)
				return nil, ErrUnauthenticated
			}
			return nil, err
		}
		if u.Disabled {
			return nil, ErrUserDisabled
		}
		owner = u
	}
	now := time.Now().UTC()
	_ = s.store.UpdateTokenLastUsed(ctx, tok.ID, now)
	tok.LastUsedAt = &now

	return &Principal{
		Authenticated: true,
		Kind:          "token",
		User:          owner,
		Token:         tok,
	}, nil
}

// Authorize enforces centralized registry-scoped authorization.
// Protocol handlers call this and convert ErrUnauthenticated / ErrForbidden into ecosystem-appropriate responses.
func (s *Service) Authorize(principal *Principal, req AuthRequest) error {
	// Anonymous read is allowed ONLY on public registries with AllowAnonymousRead enabled.
	if principal == nil || !principal.Authenticated {
		if req.Permission == PermissionRead && req.Policy.Public && req.Policy.AllowAnonymousRead {
			return nil
		}
		return ErrUnauthenticated
	}

	// Interactive user sessions
	if principal.Kind == "user" && principal.User != nil {
		if principal.User.Disabled {
			return ErrUserDisabled
		}
		if principal.User.Role == RoleAdmin {
			return nil
		}
		// Non-admin logged-in users can read public registries.
		if req.Permission == PermissionRead && req.Policy.Public {
			return nil
		}
		return ErrForbidden
	}

	// Scoped Access Tokens — enforce strict registry isolation!
	if principal.Kind == "token" && principal.Token != nil {
		tok := principal.Token
		if tok.Revoked {
			return ErrTokenRevoked
		}
		if tok.ExpiresAt != nil && time.Now().UTC().After(*tok.ExpiresAt) {
			return ErrTokenExpired
		}

		// Check registry instance isolation
		if tok.RegistryID != "" && tok.RegistryID != req.Policy.RegistryID {
			return ErrForbidden
		}
		if tok.RegistryID == "" && tok.RegistrySlug != "" && tok.RegistrySlug != req.Policy.RegistrySlug {
			return ErrForbidden
		}

		// Check registry type restriction
		if tok.RegistryType != "" && tok.RegistryType != req.Policy.RegistryType {
			return ErrForbidden
		}

		// Check scope
		if !hasScope(tok.Scopes, req.Permission) {
			return ErrForbidden
		}

		// A token can never exceed the permissions of the user that owns it.
		// This is enforced here as well as at creation time so that a token
		// issued while the owner was an administrator stops working after the
		// owner is demoted.
		if principal.User != nil && principal.User.Role != RoleAdmin {
			if req.Permission == PermissionAdmin {
				return ErrForbidden
			}
			if tok.Scopes != nil && hasScope(tok.Scopes, PermissionAdmin) {
				return ErrForbidden
			}
		}

		// Check namespace restriction when a namespace/package name is specified
		if len(tok.Namespaces) > 0 && req.Namespace != "" {
			if !matchesAnyNamespace(tok.Namespaces, req.Namespace) {
				return ErrForbidden
			}
		}

		return nil
	}

	return ErrUnauthenticated
}

func hasScope(scopes []Permission, want Permission) bool {
	for _, s := range scopes {
		if s == PermissionAdmin || s == want {
			return true
		}
	}
	return false
}

func matchesAnyNamespace(patterns []string, target string) bool {
	for _, p := range patterns {
		if p == "*" || p == target {
			return true
		}
		if ok, err := path.Match(p, target); err == nil && ok {
			return true
		}
		if strings.HasSuffix(p, "*") && strings.HasPrefix(target, strings.TrimSuffix(p, "*")) {
			return true
		}
	}
	return false
}

// extractBearer returns the bearer token from an Authorization header, if present.
func extractBearer(r *http.Request) string {
	_, token, ok := extractCredentials(r)
	if !ok {
		return ""
	}
	return token
}

// extractCredentials resolves the caller's credentials from the request.
//
// Two schemes are supported:
//
//   - "Bearer <token>" — the platform's native scheme.
//   - "Basic <base64(user:pass)>" — the scheme used by Python tooling. pip and twine
//     authenticate with HTTP Basic, where the username is conventionally "__token__"
//     and the password carries the actual API token, so the password is treated as the
//     bearer token. The username is informational and returned for callers that need it.
//
// Returning ok=false means "no credentials were supplied"; the caller then treats the
// request as anonymous. Malformed credentials are reported as an authentication error by
// the caller, never silently downgraded to anonymous.
func extractCredentials(r *http.Request) (username, token string, ok bool) {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if h == "" {
		return "", "", false
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	scheme := strings.TrimSpace(parts[0])
	credential := strings.TrimSpace(parts[1])

	switch {
	case strings.EqualFold(scheme, "Bearer"):
		if credential == "" {
			return "", "", false
		}
		return "", credential, true
	case strings.EqualFold(scheme, "Basic"):
		decoded, err := base64.StdEncoding.DecodeString(credential)
		if err != nil {
			// Tolerate unpadded base64, which some clients emit.
			decoded, err = base64.RawStdEncoding.DecodeString(credential)
			if err != nil {
				return "", "", false
			}
		}
		user, pass, found := strings.Cut(string(decoded), ":")
		if !found {
			return "", "", false
		}
		if pass == "" {
			return "", "", false
		}
		return strings.TrimSpace(user), pass, true
	default:
		return "", "", false
	}
}

func hashSecret(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func randomSecret(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// GeneratePassword returns a cryptographically random password suitable for
// bootstrapping an administrator account.
//
// It is used when no ADMIN_PASSWORD is configured, so that a fresh
// installation never ships with a well-known default credential.
func GeneratePassword(length int) string {
	if length < 20 {
		length = 20
	}
	// 3 random bytes per character keeps the alphabet wide while staying
	// friendly to copy/paste; base64 raw-URL encoding has no padding.
	raw := randomSecret(length)
	for len(raw) < length {
		raw += randomSecret(length)
	}
	return raw[:length]
}

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
