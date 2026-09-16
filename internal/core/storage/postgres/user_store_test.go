package postgres

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
)

func setupMock(t *testing.T) (*sql.DB, sqlmock.Sqlmock, types.UserStore) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	store := NewUserStore(db, "auth_users")
	return db, mock, store
}

func TestCreateUser_Success(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, mock, store := setupMock(t)
	defer db.Close()

	user := &types.User{
		Username:     "TestUser",
		PasswordHash: "hash",
		PasswordAlgo: "argon2id",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
		Roles:        []string{"user"},
		DBAdmin:      []string{},
	}

	// Expect count query
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM auth_users WHERE username = \$1`).
		WithArgs("testuser").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	// Expect insert
	mock.ExpectExec(`INSERT INTO auth_users`).
		WithArgs(
			sqlmock.AnyArg(), // id
			"testuser",       // username (lowercased)
			"hash",
			"argon2id",
			sqlmock.AnyArg(), // created_at
			sqlmock.AnyArg(), // updated_at
			false,            // disabled
			pq.Array([]string{"user"}),
			pq.Array([]string{}),
			sqlmock.AnyArg(), // profile JSON
			nil,              // last_login_at
			0,                // login_attempts
			nil,              // lockout_until
		).
		WillReturnResult(sqlmock.NewResult(1, 1))

	err := store.CreateUser(ctx, user)
	assert.NoError(t, err)
	assert.NotEmpty(t, user.ID)
	assert.Equal(t, "testuser", user.Username) // Should be lowercased
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestCreateUser_AlreadyExists(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, mock, store := setupMock(t)
	defer db.Close()

	user := &types.User{
		Username:     "ExistingUser",
		PasswordHash: "hash",
		PasswordAlgo: "argon2id",
	}

	// Expect count query to return 1 (user exists)
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM auth_users WHERE username = \$1`).
		WithArgs("existinguser").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	err := store.CreateUser(ctx, user)
	assert.ErrorIs(t, err, types.ErrUserExists)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestGetUserByUsername_Success(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, mock, store := setupMock(t)
	defer db.Close()

	now := time.Now()
	mock.ExpectQuery(`SELECT id, username, password_hash, password_algo`).
		WithArgs("testuser").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "username", "password_hash", "password_algo",
			"created_at", "updated_at", "disabled",
			"roles", "db_admin", "profile",
			"last_login_at", "login_attempts", "lockout_until",
		}).AddRow(
			"user-id", "testuser", "hash", "argon2id",
			now, now, false,
			pq.Array([]string{"user"}), pq.Array([]string{}), []byte("{}"),
			now, 0, nil,
		))

	user, err := store.GetUserByUsername(ctx, "TestUser")
	assert.NoError(t, err)
	assert.Equal(t, "user-id", user.ID)
	assert.Equal(t, "testuser", user.Username)
	assert.Equal(t, []string{"user"}, user.Roles)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestGetUserByUsername_NotFound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, mock, store := setupMock(t)
	defer db.Close()

	mock.ExpectQuery(`SELECT id, username, password_hash, password_algo`).
		WithArgs("notfound").
		WillReturnError(sql.ErrNoRows)

	user, err := store.GetUserByUsername(ctx, "notfound")
	assert.Nil(t, user)
	assert.ErrorIs(t, err, types.ErrUserNotFound)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestGetUserByID_Success(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, mock, store := setupMock(t)
	defer db.Close()

	now := time.Now()
	mock.ExpectQuery(`SELECT id, username, password_hash, password_algo`).
		WithArgs("user-id").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "username", "password_hash", "password_algo",
			"created_at", "updated_at", "disabled",
			"roles", "db_admin", "profile",
			"last_login_at", "login_attempts", "lockout_until",
		}).AddRow(
			"user-id", "testuser", "hash", "argon2id",
			now, now, false,
			pq.Array([]string{"admin"}), pq.Array([]string{"db1"}), []byte(`{"name":"Test"}`),
			now, 0, nil,
		))

	user, err := store.GetUserByID(ctx, "user-id")
	assert.NoError(t, err)
	assert.Equal(t, "user-id", user.ID)
	assert.Equal(t, []string{"admin"}, user.Roles)
	assert.Equal(t, []string{"db1"}, user.DBAdmin)
	assert.Equal(t, "Test", user.Profile["name"])
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestGetUserByID_NotFound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, mock, store := setupMock(t)
	defer db.Close()

	mock.ExpectQuery(`SELECT id, username, password_hash, password_algo`).
		WithArgs("notfound").
		WillReturnError(sql.ErrNoRows)

	user, err := store.GetUserByID(ctx, "notfound")
	assert.Nil(t, user)
	assert.ErrorIs(t, err, types.ErrUserNotFound)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestListUsers_Success(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, mock, store := setupMock(t)
	defer db.Close()

	now := time.Now()
	mock.ExpectQuery(`SELECT id, username, password_hash, password_algo`).
		WithArgs(10, 0).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "username", "password_hash", "password_algo",
			"created_at", "updated_at", "disabled",
			"roles", "db_admin", "profile",
			"last_login_at", "login_attempts", "lockout_until",
		}).
			AddRow("id1", "user1", "hash", "argon2id", now, now, false,
				pq.Array([]string{"user"}), pq.Array([]string{}), []byte("{}"), nil, 0, nil).
			AddRow("id2", "user2", "hash", "argon2id", now, now, false,
				pq.Array([]string{"admin"}), pq.Array([]string{}), []byte("{}"), now, 1, nil))

	users, err := store.ListUsers(ctx, 10, 0)
	assert.NoError(t, err)
	assert.Len(t, users, 2)
	assert.Equal(t, "id1", users[0].ID)
	assert.Equal(t, "id2", users[1].ID)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestListUsers_Empty(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, mock, store := setupMock(t)
	defer db.Close()

	mock.ExpectQuery(`SELECT id, username, password_hash, password_algo`).
		WithArgs(10, 0).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "username", "password_hash", "password_algo",
			"created_at", "updated_at", "disabled",
			"roles", "db_admin", "profile",
			"last_login_at", "login_attempts", "lockout_until",
		}))

	users, err := store.ListUsers(ctx, 10, 0)
	assert.NoError(t, err)
	assert.Empty(t, users)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdateUser_Success(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, mock, store := setupMock(t)
	defer db.Close()

	user := &types.User{
		ID:       "user-id",
		Roles:    []string{"admin"},
		DBAdmin:  []string{"db1", "db2"},
		Disabled: true,
	}

	mock.ExpectExec(`UPDATE auth_users SET`).
		WithArgs(
			pq.Array([]string{"admin"}),
			pq.Array([]string{"db1", "db2"}),
			true,
			sqlmock.AnyArg(), // updated_at
			"user-id",
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := store.UpdateUser(ctx, user)
	assert.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdateUserLoginStats_Success(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, mock, store := setupMock(t)
	defer db.Close()

	lastLogin := time.Now()
	lockoutUntil := time.Now().Add(time.Hour)

	mock.ExpectExec(`UPDATE auth_users SET`).
		WithArgs(
			sqlmock.AnyArg(), // last_login_at
			3,                // attempts
			sqlmock.AnyArg(), // lockout_until
			"user-id",
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := store.UpdateUserLoginStats(ctx, "user-id", lastLogin, 3, lockoutUntil)
	assert.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdateUserLoginStats_WithZeroTimes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, mock, store := setupMock(t)
	defer db.Close()

	mock.ExpectExec(`UPDATE auth_users SET`).
		WithArgs(
			nil, // last_login_at (zero time becomes nil)
			0,   // attempts
			nil, // lockout_until (zero time becomes nil)
			"user-id",
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := store.UpdateUserLoginStats(ctx, "user-id", time.Time{}, 0, time.Time{})
	assert.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestEnsureIndexes_Success(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, mock, store := setupMock(t)
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(\$1\)`).
		WithArgs(int64(0x53594e5452495801)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_auth_users_username`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_auth_users_disabled`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	mock.ExpectCommit()

	err := store.EnsureIndexes(ctx)
	assert.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestEnsureIndexes_Error(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, mock, store := setupMock(t)
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(\$1\)`).
		WithArgs(int64(0x53594e5452495801)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_auth_users_username`).
		WillReturnError(errors.New("index creation failed"))

	mock.ExpectRollback()

	err := store.EnsureIndexes(ctx)
	assert.Error(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestClose(t *testing.T) {
	ctx := context.Background()
	db, _, store := setupMock(t)
	defer db.Close()

	err := store.Close(ctx)
	assert.NoError(t, err)
}

func TestCreateUser_DatabaseError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, mock, store := setupMock(t)
	defer db.Close()

	user := &types.User{
		Username:     "TestUser",
		PasswordHash: "hash",
		PasswordAlgo: "argon2id",
	}

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM auth_users WHERE username = \$1`).
		WithArgs("testuser").
		WillReturnError(errors.New("database error"))

	err := store.CreateUser(ctx, user)
	assert.Error(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestGetUserByUsername_ScanError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, mock, store := setupMock(t)
	defer db.Close()

	// Return invalid data that will cause a scan error
	mock.ExpectQuery(`SELECT id, username, password_hash, password_algo`).
		WithArgs("testuser").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "username", "password_hash", "password_algo",
			"created_at", "updated_at", "disabled",
			"roles", "db_admin", "profile",
			"last_login_at", "login_attempts", "lockout_until",
		}).AddRow(
			"user-id", "testuser", "hash", "argon2id",
			"not-a-time", time.Now(), false, // Invalid time format
			pq.Array([]string{}), pq.Array([]string{}), []byte("{}"),
			nil, 0, nil,
		))

	user, err := store.GetUserByUsername(ctx, "TestUser")
	assert.Nil(t, user)
	assert.Error(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// Tests for EnsureSchema
func TestEnsureSchema_Success(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(\$1\)`).
		WithArgs(int64(0x53594e5452495801)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("CREATE TABLE IF NOT EXISTS auth_users").
		WillReturnResult(sqlmock.NewResult(0, 0))

	mock.ExpectCommit()

	err = EnsureSchema(context.Background(), db)
	assert.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestEnsureSchema_Error(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(\$1\)`).
		WithArgs(int64(0x53594e5452495801)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("CREATE TABLE IF NOT EXISTS auth_users").
		WillReturnError(errors.New("db error"))

	mock.ExpectRollback()

	err = EnsureSchema(context.Background(), db)
	assert.Error(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// Test NewUserStore with empty table name
func TestNewUserStore_EmptyTableName(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	// Empty table name should default to "auth_users"
	store := NewUserStore(db, "")
	assert.NotNil(t, store)
}

// Test UpdateUser with nil slices
func TestUpdateUser_NilSlices(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, mock, store := setupMock(t)
	defer db.Close()

	user := &types.User{
		ID:       "user-123",
		Roles:    nil, // nil slice
		DBAdmin:  nil, // nil slice
		Disabled: false,
	}

	mock.ExpectExec(`UPDATE auth_users SET`).
		WithArgs(
			pq.Array([]string{}), // roles should be empty array, not nil
			pq.Array([]string{}), // db_admin should be empty array, not nil
			false,
			sqlmock.AnyArg(), // updated_at
			"user-123",
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := store.UpdateUser(ctx, user)
	assert.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// Test UpdateUser with populated slices
func TestUpdateUser_PopulatedSlices(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, mock, store := setupMock(t)
	defer db.Close()

	user := &types.User{
		ID:       "user-456",
		Roles:    []string{"admin", "user"},
		DBAdmin:  []string{"db1"},
		Disabled: true,
	}

	mock.ExpectExec(`UPDATE auth_users SET`).
		WithArgs(
			pq.Array([]string{"admin", "user"}),
			pq.Array([]string{"db1"}),
			true,
			sqlmock.AnyArg(),
			"user-456",
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := store.UpdateUser(ctx, user)
	assert.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// Test CreateUser with nil slices and profile
func TestCreateUser_NilFields(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, mock, store := setupMock(t)
	defer db.Close()

	user := &types.User{
		Username:     "NilUser",
		PasswordHash: "hash",
		PasswordAlgo: "argon2id",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
		Roles:        nil, // nil
		DBAdmin:      nil, // nil
		Profile:      nil, // nil
	}

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM auth_users WHERE username = \$1`).
		WithArgs("niluser").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	mock.ExpectExec(`INSERT INTO auth_users`).
		WithArgs(
			sqlmock.AnyArg(),
			"niluser",
			"hash",
			"argon2id",
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			false,
			pq.Array([]string{}), // roles should be empty array
			pq.Array([]string{}), // db_admin should be empty array
			[]byte("{}"),         // profile should be empty object
			nil,
			0,
			nil,
		).
		WillReturnResult(sqlmock.NewResult(1, 1))

	err := store.CreateUser(ctx, user)
	assert.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}
