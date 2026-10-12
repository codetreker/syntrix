package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/codetreker/syntrix/console-server/internal/auth/config"
	"golang.org/x/crypto/bcrypt"
)

var ErrUnauthorized = errors.New("unauthorized")

type Developer struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

type Session struct {
	Developer Developer `json:"developer"`
	ExpiresAt time.Time `json:"expires_at"`
	CSRFToken string    `json:"csrf_token"`
}

type Service struct {
	db        *sql.DB
	config    config.Config
	dummyHash []byte
}

func NewService(db *sql.DB, cfg config.Config) (*Service, error) {
	if db == nil {
		return nil, errors.New("auth PostgreSQL connection is required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	dummyHash, err := bcrypt.GenerateFromPassword([]byte("console-password-timing-placeholder"), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("prepare password verification: %w", err)
	}
	cfg.Developers = append([]config.Developer(nil), cfg.Developers...)
	return &Service{db: db, config: cfg, dummyHash: dummyHash}, nil
}

// Initialize serializes schema creation and account bootstrap across processes.
// PostgreSQL owns credentials once an ID exists; configuration cannot reset them
// or reassign an existing username to a different developer.
func (s *Service) Initialize(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin auth initialization: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", int64(0x434f4e41555448)); err != nil {
		return fmt.Errorf("lock auth initialization: %w", err)
	}
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("create auth schema: %w", err)
	}
	for _, developer := range s.config.Developers {
		username := config.NormalizeUsername(developer.Username)
		var existingUsername string
		err := tx.QueryRowContext(ctx, "SELECT username FROM console_developers WHERE id = $1", developer.ID).Scan(&existingUsername)
		if err == nil {
			if username != existingUsername {
				return fmt.Errorf("bootstrap developer %q username conflicts with its persisted account", developer.ID)
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read bootstrap developer: %w", err)
		}
		var existingID string
		err = tx.QueryRowContext(ctx, "SELECT id FROM console_developers WHERE username = $1", username).Scan(&existingID)
		if err == nil {
			return fmt.Errorf("bootstrap developer %q username belongs to another persisted account", developer.ID)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("check bootstrap username ownership: %w", err)
		}
		passwordHash, err := bcrypt.GenerateFromPassword([]byte(developer.Password), bcrypt.DefaultCost)
		if err != nil {
			return fmt.Errorf("hash bootstrap password: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO console_developers (id, username, password_hash) VALUES ($1, $2, $3)", developer.ID, username, string(passwordHash)); err != nil {
			return fmt.Errorf("insert bootstrap developer: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit auth initialization: %w", err)
	}
	return nil
}

func (s *Service) Login(ctx context.Context, username, password string) (Session, string, error) {
	validUTF8 := utf8.ValidString(username)
	username = config.NormalizeUsername(username)
	if username == "" || len(username) > 128 || !validUTF8 || strings.ContainsRune(username, 0) {
		_ = bcrypt.CompareHashAndPassword(s.dummyHash, passwordForTiming(password))
		return Session{}, "", ErrUnauthorized
	}
	var developer Developer
	var passwordHash string
	err := s.db.QueryRowContext(ctx, "SELECT id, username, password_hash FROM console_developers WHERE username = $1", username).Scan(&developer.ID, &developer.Username, &passwordHash)
	if errors.Is(err, sql.ErrNoRows) {
		_ = bcrypt.CompareHashAndPassword(s.dummyHash, passwordForTiming(password))
		return Session{}, "", ErrUnauthorized
	}
	if err != nil {
		return Session{}, "", fmt.Errorf("read developer credentials: %w", err)
	}
	if len(password) > 72 {
		_ = bcrypt.CompareHashAndPassword(s.dummyHash, passwordForTiming(password))
		return Session{}, "", ErrUnauthorized
	}
	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)); err != nil {
		if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
			return Session{}, "", ErrUnauthorized
		}
		return Session{}, "", fmt.Errorf("verify developer credentials: %w", err)
	}
	token, tokenHash, err := newToken()
	if err != nil {
		return Session{}, "", fmt.Errorf("generate session token: %w", err)
	}
	csrfToken, _, err := newToken()
	if err != nil {
		return Session{}, "", fmt.Errorf("generate CSRF token: %w", err)
	}
	if err := s.PruneExpiredSessions(ctx); err != nil {
		return Session{}, "", err
	}
	session := Session{Developer: developer, CSRFToken: csrfToken}
	err = s.db.QueryRowContext(ctx, `INSERT INTO console_sessions (token_hash, developer_id, csrf_token, expires_at)
		VALUES ($1, $2, $3, CURRENT_TIMESTAMP + $4::double precision * INTERVAL '1 second')
		RETURNING expires_at`, tokenHash[:], developer.ID, csrfToken, s.config.SessionTTL.Seconds()).Scan(&session.ExpiresAt)
	if err != nil {
		return Session{}, "", fmt.Errorf("persist developer session: %w", err)
	}
	return session, token, nil
}

// Authenticate checks absolute expiry against the database clock. Tokens and
// sessions remain valid across process restarts without process-local state.
func (s *Service) Authenticate(ctx context.Context, token string) (Session, error) {
	tokenHash, valid := hashToken(token)
	if !valid {
		return Session{}, ErrUnauthorized
	}
	var session Session
	err := s.db.QueryRowContext(ctx, `SELECT d.id, d.username, s.expires_at, s.csrf_token
		FROM console_sessions AS s
		JOIN console_developers AS d ON d.id = s.developer_id
		WHERE s.token_hash = $1 AND s.expires_at > CURRENT_TIMESTAMP`, tokenHash[:]).Scan(&session.Developer.ID, &session.Developer.Username, &session.ExpiresAt, &session.CSRFToken)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrUnauthorized
	}
	if err != nil {
		return Session{}, fmt.Errorf("read developer session: %w", err)
	}
	return session, nil
}

// Logout is idempotent, including tokens that could never have been issued.
func (s *Service) Logout(ctx context.Context, token string) error {
	tokenHash, valid := hashToken(token)
	if !valid {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM console_sessions WHERE token_hash = $1", tokenHash[:]); err != nil {
		return fmt.Errorf("delete developer session: %w", err)
	}
	return nil
}

func (s *Service) PruneExpiredSessions(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM console_sessions WHERE expires_at <= CURRENT_TIMESTAMP"); err != nil {
		return fmt.Errorf("prune expired developer sessions: %w", err)
	}
	return nil
}

func passwordForTiming(password string) []byte {
	if len(password) > 72 {
		return []byte(password[:72])
	}
	return []byte(password)
}

func newToken() (string, [32]byte, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", [32]byte{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(random[:])
	return token, sha256.Sum256([]byte(token)), nil
}

func hashToken(token string) ([32]byte, bool) {
	if len(token) != 43 {
		return [32]byte{}, false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return [32]byte{}, false
	}
	return sha256.Sum256([]byte(token)), true
}

const schema = `
CREATE TABLE IF NOT EXISTS console_developers (
	id TEXT PRIMARY KEY,
	username TEXT NOT NULL UNIQUE,
	password_hash TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS console_sessions (
	token_hash BYTEA PRIMARY KEY CHECK (octet_length(token_hash) = 32),
	developer_id TEXT NOT NULL,
	csrf_token TEXT NOT NULL,
	expires_at TIMESTAMPTZ NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS console_sessions_expiry ON console_sessions (expires_at);
`
