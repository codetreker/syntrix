package identity

import (
	"context"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrAccountDisabled    = errors.New("account disabled")
	ErrAccountLocked      = errors.New("account locked")
	ErrInvalidToken       = errors.New("invalid token")
	ErrUserNotFound       = errors.New("user not found")
	ErrUserExists         = errors.New("user already exists")
	ErrAdminRequired      = errors.New("admin access required")
)

type AccountService interface {
	SignIn(context.Context, LoginRequest) (*TokenPair, error)
	SignUp(context.Context, SignupRequest) (*TokenPair, error)
	Refresh(context.Context, RefreshRequest) (*TokenPair, error)
	Logout(context.Context, string) error
	ListUsers(context.Context, *VerifiedIdentity, int, int) ([]*User, error)
	UpdateUser(context.Context, *VerifiedIdentity, string, []string, []string, bool) error
}

type TokenVerifier interface {
	VerifyToken(string) (*VerifiedIdentity, error)
}

type SystemTokenIssuer interface {
	GenerateSystemToken(string) (string, error)
}

type Claims struct {
	Username string   `json:"username"`
	Roles    []string `json:"roles,omitempty"`
	Disabled bool     `json:"disabled"`
	DBAdmin  []string `json:"db_admin,omitempty"`
	UserID   string   `json:"oid"`
	jwt.RegisteredClaims
}

type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

type SignupRequest struct {
	Username string `json:"username" validate:"required,min=3,max=128"`
	Password string `json:"password" validate:"required,min=8"`
}

type LoginRequest struct {
	Username string `json:"username" validate:"required,min=1,max=128"`
	Password string `json:"password" validate:"required,min=1"`
}

type RefreshRequest struct {
	RefreshToken string `json:"refresh_token" validate:"required"`
}

type User struct {
	ID            string                 `json:"id"`
	Username      string                 `json:"username"`
	CreatedAt     time.Time              `json:"createdAt"`
	UpdatedAt     time.Time              `json:"updatedAt"`
	Disabled      bool                   `json:"disabled"`
	Roles         []string               `json:"roles"`
	DBAdmin       []string               `json:"db_admin"`
	Profile       map[string]interface{} `json:"profile"`
	LastLoginAt   time.Time              `json:"last_login_at"`
	LoginAttempts int                    `json:"login_attempts"`
	LockoutUntil  time.Time              `json:"lockout_until"`
}
