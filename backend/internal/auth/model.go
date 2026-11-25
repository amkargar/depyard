package auth

import (
	"errors"
	"time"
)

// Permission represents an action scope checked against a registry instance.
type Permission string

const (
	PermissionRead    Permission = "read"
	PermissionPublish Permission = "publish"
	PermissionDelete  Permission = "delete"
	PermissionAdmin   Permission = "admin"
)

// Role represents a user role in the platform.
type Role string

const (
	RoleAdmin Role = "admin"
	RoleUser  Role = "user"
)

var (
	ErrUnauthenticated  = errors.New("authentication required")
	ErrForbidden        = errors.New("permission denied")
	ErrInvalidCreds     = errors.New("invalid username or password")
	ErrNotFound         = errors.New("not found")
	ErrAlreadyExists    = errors.New("already exists")
	ErrTokenExpired     = errors.New("token expired")
	ErrTokenRevoked     = errors.New("token revoked")
	ErrUserDisabled     = errors.New("user account is disabled")
	ErrInvalidScope     = errors.New("invalid permission scope")
)

// User represents a platform user stored in the `users` collection.
type User struct {
	ID           string    `json:"id" bson:"_id"`
	Username     string    `json:"username" bson:"username"`
	Email        string    `json:"email" bson:"email"`
	PasswordHash string    `json:"-" bson:"password_hash"`
	Role         Role      `json:"role" bson:"role"`
	Disabled     bool      `json:"disabled" bson:"disabled"`
	CreatedAt    time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt    time.Time `json:"updated_at" bson:"updated_at"`
}

// Session represents an interactive dashboard session stored in the `sessions` collection.
type Session struct {
	ID        string    `json:"id" bson:"_id"`
	TokenHash string    `json:"-" bson:"token_hash"`
	UserID    string    `json:"user_id" bson:"user_id"`
	UserAgent string    `json:"user_agent,omitempty" bson:"user_agent,omitempty"`
	IPAddress string    `json:"ip_address,omitempty" bson:"ip_address,omitempty"`
	ExpiresAt time.Time `json:"expires_at" bson:"expires_at"`
	CreatedAt time.Time `json:"created_at" bson:"created_at"`
}

// AccessToken represents a scoped programmatic access token stored in the `tokens` collection.
type AccessToken struct {
	ID           string       `json:"id" bson:"_id"`
	Name         string       `json:"name" bson:"name"`
	Prefix       string       `json:"prefix" bson:"prefix"`
	TokenHash    string       `json:"-" bson:"token_hash"`
	UserID       string       `json:"user_id" bson:"user_id"`
	RegistryID   string       `json:"registry_id,omitempty" bson:"registry_id,omitempty"`     // Empty = all registries
	RegistrySlug string       `json:"registry_slug,omitempty" bson:"registry_slug,omitempty"` // Informational / matched alongside RegistryID
	RegistryType string       `json:"registry_type,omitempty" bson:"registry_type,omitempty"` // Optional ecosystem type restriction
	Scopes       []Permission `json:"scopes" bson:"scopes"`
	Namespaces   []string     `json:"namespaces,omitempty" bson:"namespaces,omitempty"` // Optional artifact name/prefix globs
	ExpiresAt    *time.Time   `json:"expires_at,omitempty" bson:"expires_at,omitempty"`
	LastUsedAt   *time.Time   `json:"last_used_at,omitempty" bson:"last_used_at,omitempty"`
	Revoked      bool         `json:"revoked" bson:"revoked"`
	CreatedAt    time.Time    `json:"created_at" bson:"created_at"`
}

// Principal represents the authenticated caller of an HTTP request.
type Principal struct {
	Authenticated bool
	Kind          string       // "anonymous", "user", "token"
	User          *User        // Set for both session users and token owners
	Token         *AccessToken // Set when authenticated via access token
}

func (p *Principal) ActorID() string {
	if p == nil || !p.Authenticated {
		return ""
	}
	if p.Token != nil {
		return p.Token.ID
	}
	if p.User != nil {
		return p.User.ID
	}
	return ""
}

func (p *Principal) ActorName() string {
	if p == nil || !p.Authenticated {
		return "anonymous"
	}
	if p.Token != nil {
		return "token:" + p.Token.Name
	}
	if p.User != nil {
		return p.User.Username
	}
	return "unknown"
}

func (p *Principal) IsAdmin() bool {
	if p == nil || !p.Authenticated {
		return false
	}
	if p.Kind == "user" && p.User != nil && !p.User.Disabled {
		return p.User.Role == RoleAdmin
	}
	if p.Kind == "token" && p.Token != nil && !p.Token.Revoked {
		for _, s := range p.Token.Scopes {
			if s == PermissionAdmin {
				return true
			}
		}
	}
	return false
}

// RegistryAccessPolicy captures the access rules of a registry instance needed by centralized authorization.
type RegistryAccessPolicy struct {
	RegistryID         string
	RegistrySlug       string
	RegistryType       string
	Public             bool
	AllowAnonymousRead bool
}

// AuthRequest specifies what operation is being requested on a registry instance.
type AuthRequest struct {
	Policy     RegistryAccessPolicy
	Permission Permission
	Namespace  string // Optional artifact / package name checked against token namespace restrictions
}
