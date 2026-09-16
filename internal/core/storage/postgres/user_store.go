package postgres

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/lib/pq"
	"github.com/syntrixbase/syntrix/internal/core/storage/postgres/schema"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/zeebo/blake3"
)

type userStore struct {
	db        *sql.DB
	tableName string
}

// NewUserStore creates a new PostgreSQL-backed UserStore.
func NewUserStore(db *sql.DB, tableName string) types.UserStore {
	if tableName == "" {
		tableName = "auth_users"
	}
	return &userStore{
		db:        db,
		tableName: tableName,
	}
}

// EnsureSchema creates the auth_users table and indexes if they don't exist.
func EnsureSchema(ctx context.Context, db *sql.DB) error {
	statements := `
CREATE TABLE IF NOT EXISTS auth_users (
    id              VARCHAR(64) PRIMARY KEY,
    username        VARCHAR(255) NOT NULL,
    password_hash   TEXT NOT NULL,
    password_algo   VARCHAR(32) NOT NULL DEFAULT 'argon2id',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    disabled        BOOLEAN NOT NULL DEFAULT FALSE,
    roles           TEXT[] NOT NULL DEFAULT '{}',
    db_admin        TEXT[] NOT NULL DEFAULT '{}',
    profile         JSONB NOT NULL DEFAULT '{}',
    last_login_at   TIMESTAMPTZ,
    login_attempts  INTEGER NOT NULL DEFAULT 0,
    lockout_until   TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_auth_users_username ON auth_users(username);
CREATE INDEX IF NOT EXISTS idx_auth_users_disabled ON auth_users(disabled) WHERE disabled = true;
`
	return schema.Ensure(ctx, db, statements)
}

func (s *userStore) CreateUser(ctx context.Context, user *types.User) error {
	// Ensure username is lowercase
	user.Username = strings.ToLower(user.Username)

	// Check if user exists
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM auth_users WHERE username = $1`, user.Username).Scan(&count)
	if err != nil {
		return err
	}
	if count > 0 {
		return types.ErrUserExists
	}

	// Generate ID if empty
	if user.ID == "" {
		hash := blake3.Sum256([]byte(user.Username))
		user.ID = hex.EncodeToString(hash[:16])
	}

	// Ensure slices are not nil (PostgreSQL doesn't accept NULL for NOT NULL array columns)
	roles := user.Roles
	if roles == nil {
		roles = []string{}
	}
	dbAdmin := user.DBAdmin
	if dbAdmin == nil {
		dbAdmin = []string{}
	}
	profile := user.Profile
	if profile == nil {
		profile = map[string]interface{}{}
	}

	// Serialize profile to JSON
	profileJSON, err := json.Marshal(profile)
	if err != nil {
		return err
	}

	// Handle nullable time fields
	var lastLoginAt, lockoutUntil *time.Time
	if !user.LastLoginAt.IsZero() {
		lastLoginAt = &user.LastLoginAt
	}
	if !user.LockoutUntil.IsZero() {
		lockoutUntil = &user.LockoutUntil
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO auth_users (
			id, username, password_hash, password_algo,
			created_at, updated_at, disabled,
			roles, db_admin, profile,
			last_login_at, login_attempts, lockout_until
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`,
		user.ID, user.Username, user.PasswordHash, user.PasswordAlgo,
		user.CreatedAt, user.UpdatedAt, user.Disabled,
		pq.Array(roles), pq.Array(dbAdmin), profileJSON,
		lastLoginAt, user.LoginAttempts, lockoutUntil,
	)
	return err
}

func (s *userStore) GetUserByUsername(ctx context.Context, username string) (*types.User, error) {
	username = strings.ToLower(username)
	return s.scanUser(s.db.QueryRowContext(ctx, `
		SELECT id, username, password_hash, password_algo,
		       created_at, updated_at, disabled,
		       roles, db_admin, profile,
		       last_login_at, login_attempts, lockout_until
		FROM auth_users WHERE username = $1
	`, username))
}

func (s *userStore) GetUserByID(ctx context.Context, id string) (*types.User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx, `
		SELECT id, username, password_hash, password_algo,
		       created_at, updated_at, disabled,
		       roles, db_admin, profile,
		       last_login_at, login_attempts, lockout_until
		FROM auth_users WHERE id = $1
	`, id))
}

func (s *userStore) scanUser(row *sql.Row) (*types.User, error) {
	var user types.User
	var profileJSON []byte
	var lastLoginAt, lockoutUntil sql.NullTime

	err := row.Scan(
		&user.ID, &user.Username, &user.PasswordHash, &user.PasswordAlgo,
		&user.CreatedAt, &user.UpdatedAt, &user.Disabled,
		pq.Array(&user.Roles), pq.Array(&user.DBAdmin), &profileJSON,
		&lastLoginAt, &user.LoginAttempts, &lockoutUntil,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, types.ErrUserNotFound
		}
		return nil, err
	}

	if lastLoginAt.Valid {
		user.LastLoginAt = lastLoginAt.Time
	}
	if lockoutUntil.Valid {
		user.LockoutUntil = lockoutUntil.Time
	}

	if len(profileJSON) > 0 {
		if err := json.Unmarshal(profileJSON, &user.Profile); err != nil {
			return nil, err
		}
	}

	return &user, nil
}

func (s *userStore) ListUsers(ctx context.Context, limit int, offset int) ([]*types.User, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, username, password_hash, password_algo,
		       created_at, updated_at, disabled,
		       roles, db_admin, profile,
		       last_login_at, login_attempts, lockout_until
		FROM auth_users
		ORDER BY created_at DESC
		LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []*types.User
	for rows.Next() {
		var user types.User
		var profileJSON []byte
		var lastLoginAt, lockoutUntil sql.NullTime

		err := rows.Scan(
			&user.ID, &user.Username, &user.PasswordHash, &user.PasswordAlgo,
			&user.CreatedAt, &user.UpdatedAt, &user.Disabled,
			pq.Array(&user.Roles), pq.Array(&user.DBAdmin), &profileJSON,
			&lastLoginAt, &user.LoginAttempts, &lockoutUntil,
		)
		if err != nil {
			return nil, err
		}

		if lastLoginAt.Valid {
			user.LastLoginAt = lastLoginAt.Time
		}
		if lockoutUntil.Valid {
			user.LockoutUntil = lockoutUntil.Time
		}

		if len(profileJSON) > 0 {
			if err := json.Unmarshal(profileJSON, &user.Profile); err != nil {
				return nil, err
			}
		}

		users = append(users, &user)
	}

	return users, rows.Err()
}

func (s *userStore) UpdateUser(ctx context.Context, user *types.User) error {
	// Ensure slices are not nil (PostgreSQL doesn't accept NULL for NOT NULL array columns)
	roles := user.Roles
	if roles == nil {
		roles = []string{}
	}
	dbAdmin := user.DBAdmin
	if dbAdmin == nil {
		dbAdmin = []string{}
	}

	_, err := s.db.ExecContext(ctx, `
		UPDATE auth_users SET
			roles = $1,
			db_admin = $2,
			disabled = $3,
			updated_at = $4
		WHERE id = $5
	`, pq.Array(roles), pq.Array(dbAdmin), user.Disabled, time.Now(), user.ID)
	return err
}

func (s *userStore) UpdateUserLoginStats(ctx context.Context, id string, lastLogin time.Time, attempts int, lockoutUntil time.Time) error {
	// Handle nullable time fields
	var lastLoginAt, lockoutUntilPtr *time.Time
	if !lastLogin.IsZero() {
		lastLoginAt = &lastLogin
	}
	if !lockoutUntil.IsZero() {
		lockoutUntilPtr = &lockoutUntil
	}

	_, err := s.db.ExecContext(ctx, `
		UPDATE auth_users SET
			last_login_at = $1,
			login_attempts = $2,
			lockout_until = $3
		WHERE id = $4
	`, lastLoginAt, attempts, lockoutUntilPtr, id)
	return err
}

func (s *userStore) EnsureIndexes(ctx context.Context) error {
	return schema.Ensure(ctx, s.db,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_auth_users_username ON auth_users(username)`,
		`CREATE INDEX IF NOT EXISTS idx_auth_users_disabled ON auth_users(disabled) WHERE disabled = true`,
	)
}

func (s *userStore) Close(ctx context.Context) error {
	return nil
}
