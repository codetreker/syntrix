package types

import (
	"time"

	"github.com/codetreker/syntrix/internal/core/storage"
	"github.com/codetreker/syntrix/internal/ctxkeys"
	"github.com/golang-jwt/jwt/v5"
)

// ContextKey is used for storing identity data in the request context.
// Deprecated: Use ctxkeys.Key directly for new code.
type ContextKey = ctxkeys.Key

// Context keys shared with authentication middleware.
// These are aliases to the unified ctxkeys package for backward compatibility.
const (
	ContextKeyUser     ContextKey = "user"
	ContextKeyUserID   ContextKey = ctxkeys.KeyUserID
	ContextKeyUsername ContextKey = ctxkeys.KeyUsername
	ContextKeyRoles    ContextKey = ctxkeys.KeyRoles
	ContextKeyClaims   ContextKey = ctxkeys.KeyClaims
	ContextKeyDBAdmin  ContextKey = ctxkeys.KeyDBAdmin
)

// Claims represents JWT claims returned by token validation.
type Claims struct {
	Username string   `json:"username"`
	Roles    []string `json:"roles,omitempty"`
	Disabled bool     `json:"disabled"`
	DBAdmin  []string `json:"db_admin,omitempty"` // Databases with admin access (bypass authz)
	UserID   string   `json:"oid"`
	jwt.RegisteredClaims
}

// TokenPair contains access and refresh tokens.
type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// SignupRequest represents the signup payload.
type SignupRequest struct {
	Username string `json:"username" validate:"required,min=3,max=128"`
	Password string `json:"password" validate:"required,min=8"`
}

// LoginRequest represents the login payload.
type LoginRequest struct {
	Username string `json:"username" validate:"required,min=1,max=128"`
	Password string `json:"password" validate:"required,min=1"`
}

// RefreshRequest represents the refresh payload.
type RefreshRequest struct {
	RefreshToken string `json:"refresh_token" validate:"required"`
}

// RuleSet defines authorization rules.
type RuleSet struct {
	Database string                `json:"database" yaml:"database"`
	Version  string                `json:"rules_version" yaml:"rules_version"`
	Service  string                `json:"service" yaml:"service"`
	Match    map[string]MatchBlock `json:"match" yaml:"match"`
}

// MatchBlock defines nested authorization rules for a path segment.
type MatchBlock struct {
	Allow map[string]string     `json:"allow" yaml:"allow"`
	Match map[string]MatchBlock `json:"match" yaml:"match"`
}

// AuthzRequest captures authorization evaluation inputs.
type AuthzRequest struct {
	Auth     Authenticated `json:"auth"`
	Resource *Resource     `json:"resource,omitempty"`
	Time     time.Time     `json:"time"`
}

// Authenticated stores authentication context for authorization evaluation.
type Authenticated struct {
	UID      interface{}            `json:"userId"`
	Username string                 `json:"username,omitempty"`
	Roles    []string               `json:"roles"`
	DBAdmin  []string               `json:"db_admin,omitempty"` // Databases with admin access
	Claims   map[string]interface{} `json:"claims,omitempty"`
}

// Resource describes the target resource for authorization evaluation.
type Resource struct {
	Data map[string]interface{} `json:"data"`
	ID   string                 `json:"id"`
}

type User = storage.User
