package auth

import (
	"context"
	"time"
)

// Store defines persistence operations for users, sessions, and access tokens.
type Store interface {
	// Users
	CreateUser(ctx context.Context, user *User) error
	GetUserByID(ctx context.Context, id string) (*User, error)
	GetUserByUsername(ctx context.Context, username string) (*User, error)
	ListUsers(ctx context.Context) ([]*User, error)
	UpdateUser(ctx context.Context, user *User) error
	DeleteUser(ctx context.Context, id string) error
	CountUsers(ctx context.Context) (int64, error)
	// CountActiveAdmins counts users that can still administer the platform:
	// administrators that are not disabled. It backs the last-admin guard that
	// prevents an installation from locking every administrator out.
	CountActiveAdmins(ctx context.Context) (int64, error)

	// Sessions
	CreateSession(ctx context.Context, session *Session) error
	GetSessionByTokenHash(ctx context.Context, tokenHash string) (*Session, error)
	DeleteSession(ctx context.Context, tokenHash string) error
	DeleteSessionsByUser(ctx context.Context, userID string) error

	// Tokens
	CreateToken(ctx context.Context, token *AccessToken) error
	GetTokenByID(ctx context.Context, id string) (*AccessToken, error)
	GetTokenByHash(ctx context.Context, tokenHash string) (*AccessToken, error)
	ListTokens(ctx context.Context, registryID string) ([]*AccessToken, error)
	// ListTokensByUser returns the access tokens owned by a single user. It backs
	// personal token management, where a user may only see and revoke their own.
	ListTokensByUser(ctx context.Context, userID string) ([]*AccessToken, error)
	UpdateTokenLastUsed(ctx context.Context, id string, ts time.Time) error
	RevokeToken(ctx context.Context, id string) error
	DeleteToken(ctx context.Context, id string) error
	// RevokeTokensByUser revokes every active access token owned by a user. It is
	// used when a user's credentials change or the account is deleted, so that a
	// leaked token cannot outlive the account it belongs to.
	RevokeTokensByUser(ctx context.Context, userID string) error
}
