package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/database"
)

func TestStore_Create(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "databases")

	slug := "my-app"
	desc := "Test database"
	testDB := &database.Database{
		ID:          "a1b2c3d4e5f67890",
		Slug:        &slug,
		DisplayName: "My App",
		Description: &desc,
		OwnerID:     "user-123",
		Status:      database.StatusActive,
	}

	mock.ExpectExec(`INSERT INTO databases`).
		WithArgs(
			testDB.ID, testDB.Slug, testDB.DisplayName, testDB.Description, testDB.OwnerID,
			sqlmock.AnyArg(), sqlmock.AnyArg(), int64(0), int64(0), database.StatusActive,
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err = store.Create(context.Background(), testDB)
	assert.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestStore_Get(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "databases")

	slug := "my-app"
	desc := "Test database"
	now := time.Now()

	rows := sqlmock.NewRows([]string{
		"id", "slug", "display_name", "description", "owner_id",
		"created_at", "updated_at", "max_documents", "max_storage_bytes", "status",
	}).AddRow(
		"a1b2c3d4e5f67890", slug, "My App", desc, "user-123",
		now, now, int64(0), int64(0), "active",
	)

	mock.ExpectQuery(`SELECT .+ FROM databases WHERE id = \$1`).
		WithArgs("a1b2c3d4e5f67890").
		WillReturnRows(rows)

	result, err := store.Get(context.Background(), "a1b2c3d4e5f67890")
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "a1b2c3d4e5f67890", result.ID)
	assert.Equal(t, &slug, result.Slug)
	assert.Equal(t, "My App", result.DisplayName)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestStore_Get_NotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "databases")

	rows := sqlmock.NewRows([]string{
		"id", "slug", "display_name", "description", "owner_id",
		"created_at", "updated_at", "max_documents", "max_storage_bytes", "status",
	})

	mock.ExpectQuery(`SELECT .+ FROM databases WHERE id = \$1`).
		WithArgs("nonexistent").
		WillReturnRows(rows)

	result, err := store.Get(context.Background(), "nonexistent")
	assert.ErrorIs(t, err, database.ErrDatabaseNotFound)
	assert.Nil(t, result)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestStore_GetBySlug(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "databases")

	slug := "my-app"
	now := time.Now()

	rows := sqlmock.NewRows([]string{
		"id", "slug", "display_name", "description", "owner_id",
		"created_at", "updated_at", "max_documents", "max_storage_bytes", "status",
	}).AddRow(
		"a1b2c3d4e5f67890", slug, "My App", nil, "user-123",
		now, now, int64(0), int64(0), "active",
	)

	mock.ExpectQuery(`SELECT .+ FROM databases WHERE slug = \$1`).
		WithArgs("my-app").
		WillReturnRows(rows)

	result, err := store.GetBySlug(context.Background(), "my-app")
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "a1b2c3d4e5f67890", result.ID)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestStore_List(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "databases")

	now := time.Now()

	// Count query
	countRows := sqlmock.NewRows([]string{"count"}).AddRow(2)
	mock.ExpectQuery(`SELECT COUNT\(\*\)`).
		WithArgs(nil, nil).
		WillReturnRows(countRows)

	// List query
	listRows := sqlmock.NewRows([]string{
		"id", "slug", "display_name", "description", "owner_id",
		"created_at", "updated_at", "max_documents", "max_storage_bytes", "status",
	}).AddRow(
		"a1b2c3d4e5f67890", "app-1", "App 1", nil, "user-123",
		now, now, int64(0), int64(0), "active",
	).AddRow(
		"b2c3d4e5f6789012", "app-2", "App 2", nil, "user-123",
		now, now, int64(0), int64(0), "active",
	)

	mock.ExpectQuery(`SELECT .+ FROM databases .+ ORDER BY created_at DESC, id LIMIT`).
		WithArgs(nil, nil, 20, 0).
		WillReturnRows(listRows)

	results, total, err := store.List(context.Background(), database.ListOptions{})
	assert.NoError(t, err)
	assert.Equal(t, 2, total)
	assert.Len(t, results, 2)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestStore_List_WithFilters(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "databases")

	now := time.Now()

	// Count query with owner filter
	countRows := sqlmock.NewRows([]string{"count"}).AddRow(1)
	mock.ExpectQuery(`SELECT COUNT\(\*\)`).
		WithArgs("user-123", "active").
		WillReturnRows(countRows)

	// List query with owner filter
	listRows := sqlmock.NewRows([]string{
		"id", "slug", "display_name", "description", "owner_id",
		"created_at", "updated_at", "max_documents", "max_storage_bytes", "status",
	}).AddRow(
		"a1b2c3d4e5f67890", "app-1", "App 1", nil, "user-123",
		now, now, int64(0), int64(0), "active",
	)

	mock.ExpectQuery(`SELECT .+ FROM databases`).
		WithArgs("user-123", "active", 10, 5).
		WillReturnRows(listRows)

	results, total, err := store.List(context.Background(), database.ListOptions{
		OwnerID: "user-123",
		Status:  database.StatusActive,
		Limit:   10,
		Offset:  5,
	})
	assert.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Len(t, results, 1)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestStore_Update(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "databases")

	slug := "my-app"
	desc := "Updated description"
	testDB := &database.Database{
		ID:              "a1b2c3d4e5f67890",
		Slug:            &slug,
		DisplayName:     "My App Updated",
		Description:     &desc,
		OwnerID:         "user-123",
		Status:          database.StatusActive,
		MaxDocuments:    1000,
		MaxStorageBytes: 10485760,
	}

	mock.ExpectExec(`UPDATE databases SET`).
		WithArgs(
			testDB.ID, testDB.Slug, testDB.DisplayName, testDB.Description,
			testDB.Status, testDB.MaxDocuments, testDB.MaxStorageBytes, sqlmock.AnyArg(),
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err = store.Update(context.Background(), testDB)
	assert.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestStore_Update_NotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "databases")

	testDB := &database.Database{
		ID:          "nonexistent",
		DisplayName: "Test",
		OwnerID:     "user-123",
		Status:      database.StatusActive,
	}

	mock.ExpectExec(`UPDATE databases SET`).
		WithArgs(
			testDB.ID, nil, testDB.DisplayName, nil,
			testDB.Status, int64(0), int64(0), sqlmock.AnyArg(),
		).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err = store.Update(context.Background(), testDB)
	assert.ErrorIs(t, err, database.ErrDatabaseNotFound)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestStore_Delete(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "databases")

	mock.ExpectExec(`DELETE FROM databases WHERE id = \$1`).
		WithArgs("a1b2c3d4e5f67890").
		WillReturnResult(sqlmock.NewResult(0, 1))

	err = store.Delete(context.Background(), "a1b2c3d4e5f67890")
	assert.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestStore_Delete_NotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "databases")

	mock.ExpectExec(`DELETE FROM databases WHERE id = \$1`).
		WithArgs("nonexistent").
		WillReturnResult(sqlmock.NewResult(0, 0))

	err = store.Delete(context.Background(), "nonexistent")
	assert.ErrorIs(t, err, database.ErrDatabaseNotFound)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestStore_CountByOwner(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "databases")

	rows := sqlmock.NewRows([]string{"count"}).AddRow(3)
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM databases WHERE owner_id = \$1`).
		WithArgs("user-123").
		WillReturnRows(rows)

	count, err := store.CountByOwner(context.Background(), "user-123")
	assert.NoError(t, err)
	assert.Equal(t, 3, count)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestStore_Exists(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "databases")

	rows := sqlmock.NewRows([]string{"exists"}).AddRow(true)
	mock.ExpectQuery(`SELECT EXISTS`).
		WithArgs("a1b2c3d4e5f67890").
		WillReturnRows(rows)

	exists, err := store.Exists(context.Background(), "a1b2c3d4e5f67890")
	assert.NoError(t, err)
	assert.True(t, exists)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestStore_Exists_NotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "databases")

	rows := sqlmock.NewRows([]string{"exists"}).AddRow(false)
	mock.ExpectQuery(`SELECT EXISTS`).
		WithArgs("nonexistent").
		WillReturnRows(rows)

	exists, err := store.Exists(context.Background(), "nonexistent")
	assert.NoError(t, err)
	assert.False(t, exists)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestStore_Close(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "databases")
	err = store.Close(context.Background())
	assert.NoError(t, err)
}

func TestNewStore_DefaultTableName(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "")
	assert.NotNil(t, store)

	// Verify it uses "databases" as the default table name
	s := store.(*Store)
	assert.Equal(t, "databases", s.tableName)
}

func TestStore_Create_UniqueViolation(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "databases")

	testDB := &database.Database{
		ID:          "a1b2c3d4e5f67890",
		DisplayName: "My App",
		OwnerID:     "user-123",
		Status:      database.StatusActive,
	}

	mock.ExpectExec(`INSERT INTO databases`).
		WithArgs(
			testDB.ID, nil, testDB.DisplayName, nil, testDB.OwnerID,
			sqlmock.AnyArg(), sqlmock.AnyArg(), int64(0), int64(0), database.StatusActive,
		).
		WillReturnError(&uniqueViolationError{column: "id"})

	err = store.Create(context.Background(), testDB)
	assert.ErrorIs(t, err, database.ErrDatabaseExists)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestStore_Create_SlugViolation(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "databases")

	slug := "my-app"
	testDB := &database.Database{
		ID:          "a1b2c3d4e5f67890",
		Slug:        &slug,
		DisplayName: "My App",
		OwnerID:     "user-123",
		Status:      database.StatusActive,
	}

	mock.ExpectExec(`INSERT INTO databases`).
		WithArgs(
			testDB.ID, testDB.Slug, testDB.DisplayName, nil, testDB.OwnerID,
			sqlmock.AnyArg(), sqlmock.AnyArg(), int64(0), int64(0), database.StatusActive,
		).
		WillReturnError(&uniqueViolationError{column: "slug"})

	err = store.Create(context.Background(), testDB)
	assert.ErrorIs(t, err, database.ErrSlugExists)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestStore_Update_SlugViolation(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "databases")

	slug := "existing-slug"
	testDB := &database.Database{
		ID:          "a1b2c3d4e5f67890",
		Slug:        &slug,
		DisplayName: "My App",
		OwnerID:     "user-123",
		Status:      database.StatusActive,
	}

	mock.ExpectExec(`UPDATE databases SET`).
		WithArgs(
			testDB.ID, testDB.Slug, testDB.DisplayName, nil,
			testDB.Status, int64(0), int64(0), sqlmock.AnyArg(),
		).
		WillReturnError(&uniqueViolationError{column: "slug"})

	err = store.Update(context.Background(), testDB)
	assert.ErrorIs(t, err, database.ErrSlugExists)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// uniqueViolationError simulates a PostgreSQL unique constraint violation error
type uniqueViolationError struct {
	column string
}

func (e *uniqueViolationError) Error() string {
	if e.column == "slug" {
		return "pq: duplicate key value violates unique constraint \"databases_slug_key\""
	}
	return "pq: duplicate key value violates unique constraint"
}

func TestContains(t *testing.T) {
	tests := []struct {
		name     string
		s        string
		substr   string
		expected bool
	}{
		{"exact match", "hello", "hello", true},
		{"contains at start", "hello world", "hello", true},
		{"contains at end", "hello world", "world", true},
		{"contains in middle", "hello world here", "world", true},
		{"not contains", "hello world", "foo", false},
		{"empty substr", "hello", "", true},
		{"empty string", "", "hello", false},
		{"both empty", "", "", true},
		{"substr longer than s", "hi", "hello", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := contains(tt.s, tt.substr)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFindSubstring(t *testing.T) {
	tests := []struct {
		name     string
		s        string
		substr   string
		expected bool
	}{
		{"exact match", "hello", "hello", true},
		{"contains at start", "hello world", "hello", true},
		{"contains at end", "hello world", "world", true},
		{"contains in middle", "hello world here", "world", true},
		{"not contains", "hello world", "foo", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := findSubstring(tt.s, tt.substr)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsUniqueViolation(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil error", nil, false},
		{"unique constraint error", &uniqueViolationError{column: "id"}, true},
		{"23505 error code", &pgError{code: "23505"}, true},
		{"other error", &pgError{code: "12345"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isUniqueViolation(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsSlugViolation(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil error", nil, false},
		{"slug violation", &uniqueViolationError{column: "slug"}, true},
		{"id violation", &uniqueViolationError{column: "id"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isSlugViolation(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// pgError simulates a PostgreSQL error with error code
type pgError struct {
	code string
}

func (e *pgError) Error() string {
	return "pq: error code " + e.code
}

func TestStore_List_LimitCapping(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "databases")

	// Count query
	countRows := sqlmock.NewRows([]string{"count"}).AddRow(0)
	mock.ExpectQuery(`SELECT COUNT\(\*\)`).
		WithArgs(nil, nil).
		WillReturnRows(countRows)

	// List query - limit should be capped to 100
	listRows := sqlmock.NewRows([]string{
		"id", "slug", "display_name", "description", "owner_id",
		"created_at", "updated_at", "max_documents", "max_storage_bytes", "status",
	})

	mock.ExpectQuery(`SELECT .+ FROM databases`).
		WithArgs(nil, nil, 100, 0). // limit capped to 100
		WillReturnRows(listRows)

	_, _, err = store.List(context.Background(), database.ListOptions{
		Limit: 500, // exceeds max, should be capped to 100
	})
	assert.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestStore_List_DefaultLimit(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "databases")

	// Count query
	countRows := sqlmock.NewRows([]string{"count"}).AddRow(0)
	mock.ExpectQuery(`SELECT COUNT\(\*\)`).
		WithArgs(nil, nil).
		WillReturnRows(countRows)

	// List query - limit should default to 20
	listRows := sqlmock.NewRows([]string{
		"id", "slug", "display_name", "description", "owner_id",
		"created_at", "updated_at", "max_documents", "max_storage_bytes", "status",
	})

	mock.ExpectQuery(`SELECT .+ FROM databases`).
		WithArgs(nil, nil, 20, 0). // default limit
		WillReturnRows(listRows)

	_, _, err = store.List(context.Background(), database.ListOptions{
		Limit: 0, // should default to 20
	})
	assert.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestEnsureSchema(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS databases`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err = EnsureSchema(db)
	assert.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestEnsureSchema_Error(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS databases`).
		WillReturnError(errors.New("schema creation failed"))

	err = EnsureSchema(db)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "schema creation failed")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestStore_Delete_ExecError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "databases")

	mock.ExpectExec(`DELETE FROM databases WHERE id = \$1`).
		WithArgs("a1b2c3d4e5f67890").
		WillReturnError(errors.New("connection error"))

	err = store.Delete(context.Background(), "a1b2c3d4e5f67890")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "connection error")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestStore_Delete_RowsAffectedError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "databases")

	mock.ExpectExec(`DELETE FROM databases WHERE id = \$1`).
		WithArgs("a1b2c3d4e5f67890").
		WillReturnResult(sqlmock.NewErrorResult(errors.New("rows affected error")))

	err = store.Delete(context.Background(), "a1b2c3d4e5f67890")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "rows affected error")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestStore_Update_ExecError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "databases")

	testDB := &database.Database{
		ID:          "a1b2c3d4e5f67890",
		DisplayName: "Test",
		OwnerID:     "user-123",
		Status:      database.StatusActive,
	}

	mock.ExpectExec(`UPDATE databases SET`).
		WithArgs(
			testDB.ID, nil, testDB.DisplayName, nil,
			testDB.Status, int64(0), int64(0), sqlmock.AnyArg(),
		).
		WillReturnError(errors.New("connection error"))

	err = store.Update(context.Background(), testDB)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "connection error")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestStore_Update_RowsAffectedError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "databases")

	testDB := &database.Database{
		ID:          "a1b2c3d4e5f67890",
		DisplayName: "Test",
		OwnerID:     "user-123",
		Status:      database.StatusActive,
	}

	mock.ExpectExec(`UPDATE databases SET`).
		WithArgs(
			testDB.ID, nil, testDB.DisplayName, nil,
			testDB.Status, int64(0), int64(0), sqlmock.AnyArg(),
		).
		WillReturnResult(sqlmock.NewErrorResult(errors.New("rows affected error")))

	err = store.Update(context.Background(), testDB)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "rows affected error")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestStore_List_ScanError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "databases")

	// Count query
	countRows := sqlmock.NewRows([]string{"count"}).AddRow(1)
	mock.ExpectQuery(`SELECT COUNT\(\*\)`).
		WithArgs(nil, nil).
		WillReturnRows(countRows)

	// List query with invalid data that will cause scan error
	listRows := sqlmock.NewRows([]string{
		"id", "slug", "display_name", "description", "owner_id",
		"created_at", "updated_at", "max_documents", "max_storage_bytes", "status",
	}).AddRow(
		"a1b2c3d4e5f67890", "app-1", "App 1", nil, "user-123",
		"invalid-time", // This will cause scan error
		time.Now(), int64(0), int64(0), "active",
	)

	mock.ExpectQuery(`SELECT .+ FROM databases`).
		WithArgs(nil, nil, 20, 0).
		WillReturnRows(listRows)

	_, _, err = store.List(context.Background(), database.ListOptions{})
	assert.Error(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestStore_List_RowsError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "databases")

	now := time.Now()

	// Count query
	countRows := sqlmock.NewRows([]string{"count"}).AddRow(1)
	mock.ExpectQuery(`SELECT COUNT\(\*\)`).
		WithArgs(nil, nil).
		WillReturnRows(countRows)

	// List query
	listRows := sqlmock.NewRows([]string{
		"id", "slug", "display_name", "description", "owner_id",
		"created_at", "updated_at", "max_documents", "max_storage_bytes", "status",
	}).AddRow(
		"a1b2c3d4e5f67890", "app-1", "App 1", nil, "user-123",
		now, now, int64(0), int64(0), "active",
	).RowError(0, errors.New("row error"))

	mock.ExpectQuery(`SELECT .+ FROM databases`).
		WithArgs(nil, nil, 20, 0).
		WillReturnRows(listRows)

	_, _, err = store.List(context.Background(), database.ListOptions{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "row error")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestStore_List_CountError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "databases")

	// Count query returns error
	mock.ExpectQuery(`SELECT COUNT\(\*\)`).
		WithArgs(nil, nil).
		WillReturnError(errors.New("count error"))

	_, _, err = store.List(context.Background(), database.ListOptions{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "count error")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestStore_List_QueryError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "databases")

	// Count query succeeds
	countRows := sqlmock.NewRows([]string{"count"}).AddRow(1)
	mock.ExpectQuery(`SELECT COUNT\(\*\)`).
		WithArgs(nil, nil).
		WillReturnRows(countRows)

	// List query returns error
	mock.ExpectQuery(`SELECT .+ FROM databases`).
		WithArgs(nil, nil, 20, 0).
		WillReturnError(errors.New("query error"))

	_, _, err = store.List(context.Background(), database.ListOptions{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "query error")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestStore_Get_ScanError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "databases")

	// Return row with invalid data that will cause scan error
	rows := sqlmock.NewRows([]string{
		"id", "slug", "display_name", "description", "owner_id",
		"created_at", "updated_at", "max_documents", "max_storage_bytes", "status",
	}).AddRow(
		"a1b2c3d4e5f67890", "my-app", "My App", nil, "user-123",
		"invalid-time", // This will cause scan error
		time.Now(), int64(0), int64(0), "active",
	)

	mock.ExpectQuery(`SELECT .+ FROM databases WHERE id = \$1`).
		WithArgs("a1b2c3d4e5f67890").
		WillReturnRows(rows)

	result, err := store.Get(context.Background(), "a1b2c3d4e5f67890")
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestStore_GetBySlug_NotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "databases")

	rows := sqlmock.NewRows([]string{
		"id", "slug", "display_name", "description", "owner_id",
		"created_at", "updated_at", "max_documents", "max_storage_bytes", "status",
	})

	mock.ExpectQuery(`SELECT .+ FROM databases WHERE slug = \$1`).
		WithArgs("nonexistent").
		WillReturnRows(rows)

	result, err := store.GetBySlug(context.Background(), "nonexistent")
	assert.ErrorIs(t, err, database.ErrDatabaseNotFound)
	assert.Nil(t, result)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestStore_CountByOwner_Error(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "databases")

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM databases WHERE owner_id = \$1`).
		WithArgs("user-123").
		WillReturnError(errors.New("count error"))

	count, err := store.CountByOwner(context.Background(), "user-123")
	assert.Error(t, err)
	assert.Equal(t, 0, count)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestStore_Exists_Error(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "databases")

	mock.ExpectQuery(`SELECT EXISTS`).
		WithArgs("a1b2c3d4e5f67890").
		WillReturnError(errors.New("exists error"))

	exists, err := store.Exists(context.Background(), "a1b2c3d4e5f67890")
	assert.Error(t, err)
	assert.False(t, exists)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestStore_Create_GenericError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "databases")

	testDB := &database.Database{
		ID:          "a1b2c3d4e5f67890",
		DisplayName: "My App",
		OwnerID:     "user-123",
		Status:      database.StatusActive,
	}

	mock.ExpectExec(`INSERT INTO databases`).
		WithArgs(
			testDB.ID, nil, testDB.DisplayName, nil, testDB.OwnerID,
			sqlmock.AnyArg(), sqlmock.AnyArg(), int64(0), int64(0), database.StatusActive,
		).
		WillReturnError(errors.New("connection error"))

	err = store.Create(context.Background(), testDB)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "connection error")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestStore_List_WithDescription(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewStore(db, "databases")

	now := time.Now()
	desc := "Test description"

	// Count query
	countRows := sqlmock.NewRows([]string{"count"}).AddRow(1)
	mock.ExpectQuery(`SELECT COUNT\(\*\)`).
		WithArgs(nil, nil).
		WillReturnRows(countRows)

	// List query with description
	listRows := sqlmock.NewRows([]string{
		"id", "slug", "display_name", "description", "owner_id",
		"created_at", "updated_at", "max_documents", "max_storage_bytes", "status",
	}).AddRow(
		"a1b2c3d4e5f67890", "app-1", "App 1", desc, "user-123",
		now, now, int64(0), int64(0), "active",
	)

	mock.ExpectQuery(`SELECT .+ FROM databases`).
		WithArgs(nil, nil, 20, 0).
		WillReturnRows(listRows)

	results, total, err := store.List(context.Background(), database.ListOptions{})
	assert.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Len(t, results, 1)
	assert.NotNil(t, results[0].Description)
	assert.Equal(t, desc, *results[0].Description)
	assert.NoError(t, mock.ExpectationsWereMet())
}
