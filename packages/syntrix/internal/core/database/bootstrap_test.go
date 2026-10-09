package database

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/storage/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockDatabaseStore is a mock implementation of DatabaseStore for testing
type mockDatabaseStore struct {
	getBySlugFunc func(ctx context.Context, slug string) (*Database, error)
	createFunc    func(ctx context.Context, db *Database) error
}

func (m *mockDatabaseStore) Create(ctx context.Context, db *Database) error {
	if m.createFunc != nil {
		return m.createFunc(ctx, db)
	}
	return nil
}

func (m *mockDatabaseStore) Get(ctx context.Context, id string) (*Database, error) {
	return nil, ErrDatabaseNotFound
}

func (m *mockDatabaseStore) GetBySlug(ctx context.Context, slug string) (*Database, error) {
	if m.getBySlugFunc != nil {
		return m.getBySlugFunc(ctx, slug)
	}
	return nil, ErrDatabaseNotFound
}

func (m *mockDatabaseStore) List(ctx context.Context, opts ListOptions) ([]*Database, int, error) {
	return nil, 0, nil
}

func (m *mockDatabaseStore) Update(ctx context.Context, db *Database) error {
	return nil
}

func (m *mockDatabaseStore) Delete(ctx context.Context, id string) error {
	return nil
}

func (m *mockDatabaseStore) CountByOwner(ctx context.Context, ownerID string) (int, error) {
	return 0, nil
}

func (m *mockDatabaseStore) Exists(ctx context.Context, id string) (bool, error) {
	return false, nil
}

func (m *mockDatabaseStore) Close(ctx context.Context) error {
	return nil
}

// mockUserStore is a mock implementation of types.UserStore for testing
type mockUserStore struct {
	getUserByUsernameFunc func(ctx context.Context, username string) (*types.User, error)
}

func (m *mockUserStore) CreateUser(ctx context.Context, user *types.User) error {
	return nil
}

func (m *mockUserStore) GetUser(ctx context.Context, id string) (*types.User, error) {
	return nil, types.ErrUserNotFound
}

func (m *mockUserStore) GetUserByUsername(ctx context.Context, username string) (*types.User, error) {
	if m.getUserByUsernameFunc != nil {
		return m.getUserByUsernameFunc(ctx, username)
	}
	return nil, types.ErrUserNotFound
}

func (m *mockUserStore) GetUserByEmail(ctx context.Context, email string) (*types.User, error) {
	return nil, types.ErrUserNotFound
}

func (m *mockUserStore) UpdateUser(ctx context.Context, user *types.User) error {
	return nil
}

func (m *mockUserStore) DeleteUser(ctx context.Context, id string) error {
	return nil
}

func (m *mockUserStore) ListUsers(ctx context.Context, limit, offset int) ([]*types.User, error) {
	return nil, nil
}

func (m *mockUserStore) Close(ctx context.Context) error {
	return nil
}

func (m *mockUserStore) EnsureIndexes(ctx context.Context) error {
	return nil
}

func (m *mockUserStore) GetUserByID(ctx context.Context, id string) (*types.User, error) {
	return nil, types.ErrUserNotFound
}

func (m *mockUserStore) UpdateUserLoginStats(ctx context.Context, id string, lastLogin time.Time, attempts int, lockoutUntil time.Time) error {
	return nil
}

func TestEnsureDefaultDatabase_NoAdminUsername(t *testing.T) {
	dbStore := &mockDatabaseStore{}
	userStore := &mockUserStore{}

	err := EnsureDefaultDatabase(context.Background(), dbStore, userStore, BootstrapConfig{
		AdminUsername: "",
	})

	assert.NoError(t, err)
}

func TestEnsureDefaultDatabase_AlreadyExists(t *testing.T) {
	slug := "default"
	dbStore := &mockDatabaseStore{
		getBySlugFunc: func(ctx context.Context, s string) (*Database, error) {
			if s == DefaultDatabaseSlug {
				return &Database{
					ID:          "existing123",
					Slug:        &slug,
					DisplayName: DefaultDatabaseDisplayName,
				}, nil
			}
			return nil, ErrDatabaseNotFound
		},
	}
	userStore := &mockUserStore{}

	err := EnsureDefaultDatabase(context.Background(), dbStore, userStore, BootstrapConfig{
		AdminUsername: "admin",
	})

	assert.NoError(t, err)
}

func TestEnsureDefaultDatabase_GetBySlugError(t *testing.T) {
	dbStore := &mockDatabaseStore{
		getBySlugFunc: func(ctx context.Context, slug string) (*Database, error) {
			return nil, errors.New("database error")
		},
	}
	userStore := &mockUserStore{}

	err := EnsureDefaultDatabase(context.Background(), dbStore, userStore, BootstrapConfig{
		AdminUsername: "admin",
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "database error")
}

func TestEnsureDefaultDatabase_AdminUserNotFound(t *testing.T) {
	dbStore := &mockDatabaseStore{
		getBySlugFunc: func(ctx context.Context, slug string) (*Database, error) {
			return nil, ErrDatabaseNotFound
		},
	}
	userStore := &mockUserStore{
		getUserByUsernameFunc: func(ctx context.Context, username string) (*types.User, error) {
			return nil, types.ErrUserNotFound
		},
	}

	err := EnsureDefaultDatabase(context.Background(), dbStore, userStore, BootstrapConfig{
		AdminUsername: "admin",
	})

	assert.NoError(t, err) // should not error, just skip
}

func TestEnsureDefaultDatabase_GetUserError(t *testing.T) {
	dbStore := &mockDatabaseStore{
		getBySlugFunc: func(ctx context.Context, slug string) (*Database, error) {
			return nil, ErrDatabaseNotFound
		},
	}
	userStore := &mockUserStore{
		getUserByUsernameFunc: func(ctx context.Context, username string) (*types.User, error) {
			return nil, errors.New("user store error")
		},
	}

	err := EnsureDefaultDatabase(context.Background(), dbStore, userStore, BootstrapConfig{
		AdminUsername: "admin",
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "user store error")
}

func TestEnsureDefaultDatabase_Success(t *testing.T) {
	var createdDB *Database
	dbStore := &mockDatabaseStore{
		getBySlugFunc: func(ctx context.Context, slug string) (*Database, error) {
			return nil, ErrDatabaseNotFound
		},
		createFunc: func(ctx context.Context, db *Database) error {
			createdDB = db
			return nil
		},
	}
	userStore := &mockUserStore{
		getUserByUsernameFunc: func(ctx context.Context, username string) (*types.User, error) {
			return &types.User{
				ID:       "admin-user-id",
				Username: "admin",
			}, nil
		},
	}

	err := EnsureDefaultDatabase(context.Background(), dbStore, userStore, BootstrapConfig{
		AdminUsername: "admin",
	})

	require.NoError(t, err)
	require.NotNil(t, createdDB)
	assert.Equal(t, DefaultDatabaseDisplayName, createdDB.DisplayName)
	assert.Equal(t, "admin-user-id", createdDB.OwnerID)
	assert.Equal(t, StatusActive, createdDB.Status)
	assert.NotNil(t, createdDB.Slug)
	assert.Equal(t, DefaultDatabaseSlug, *createdDB.Slug)
}

func TestEnsureDefaultDatabase_RaceCondition_SlugExists(t *testing.T) {
	dbStore := &mockDatabaseStore{
		getBySlugFunc: func(ctx context.Context, slug string) (*Database, error) {
			return nil, ErrDatabaseNotFound
		},
		createFunc: func(ctx context.Context, db *Database) error {
			return ErrSlugExists
		},
	}
	userStore := &mockUserStore{
		getUserByUsernameFunc: func(ctx context.Context, username string) (*types.User, error) {
			return &types.User{
				ID:       "admin-user-id",
				Username: "admin",
			}, nil
		},
	}

	err := EnsureDefaultDatabase(context.Background(), dbStore, userStore, BootstrapConfig{
		AdminUsername: "admin",
	})

	assert.NoError(t, err) // race condition handled gracefully
}

func TestEnsureDefaultDatabase_RaceCondition_DatabaseExists(t *testing.T) {
	dbStore := &mockDatabaseStore{
		getBySlugFunc: func(ctx context.Context, slug string) (*Database, error) {
			return nil, ErrDatabaseNotFound
		},
		createFunc: func(ctx context.Context, db *Database) error {
			return ErrDatabaseExists
		},
	}
	userStore := &mockUserStore{
		getUserByUsernameFunc: func(ctx context.Context, username string) (*types.User, error) {
			return &types.User{
				ID:       "admin-user-id",
				Username: "admin",
			}, nil
		},
	}

	err := EnsureDefaultDatabase(context.Background(), dbStore, userStore, BootstrapConfig{
		AdminUsername: "admin",
	})

	assert.NoError(t, err) // race condition handled gracefully
}

func TestEnsureDefaultDatabase_CreateError(t *testing.T) {
	dbStore := &mockDatabaseStore{
		getBySlugFunc: func(ctx context.Context, slug string) (*Database, error) {
			return nil, ErrDatabaseNotFound
		},
		createFunc: func(ctx context.Context, db *Database) error {
			return errors.New("create failed")
		},
	}
	userStore := &mockUserStore{
		getUserByUsernameFunc: func(ctx context.Context, username string) (*types.User, error) {
			return &types.User{
				ID:       "admin-user-id",
				Username: "admin",
			}, nil
		},
	}

	err := EnsureDefaultDatabase(context.Background(), dbStore, userStore, BootstrapConfig{
		AdminUsername: "admin",
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "create failed")
}
