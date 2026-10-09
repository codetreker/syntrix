package database

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestService_CreateDatabase(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)

	slug := "my-app"
	db, err := svc.CreateDatabase(context.Background(), "user-123", false, CreateRequest{
		Slug:        &slug,
		DisplayName: "My App",
	})

	assert.NoError(t, err)
	assert.NotNil(t, db)
	assert.Equal(t, "user-123", db.OwnerID)
	assert.Equal(t, "My App", db.DisplayName)
	assert.Equal(t, &slug, db.Slug)
	assert.Equal(t, StatusActive, db.Status)
}

func TestService_CreateDatabase_AdminSpecifyOwner(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)

	db, err := svc.CreateDatabase(context.Background(), "admin-user", true, CreateRequest{
		DisplayName: "Customer DB",
		OwnerID:     "customer-123",
	})

	assert.NoError(t, err)
	assert.NotNil(t, db)
	assert.Equal(t, "customer-123", db.OwnerID)
}

func TestService_CreateDatabase_QuotaExceeded(t *testing.T) {
	store := newMockStore()
	config := DefaultServiceConfig()
	config.MaxDatabasesPerUser = 2
	svc := NewService(store, config, nil)

	// Create 2 databases
	for i := 0; i < 2; i++ {
		_, err := svc.CreateDatabase(context.Background(), "user-123", false, CreateRequest{
			DisplayName: "DB " + string(rune('A'+i)),
		})
		require.NoError(t, err)
	}

	// Third should fail
	_, err := svc.CreateDatabase(context.Background(), "user-123", false, CreateRequest{
		DisplayName: "DB C",
	})
	assert.ErrorIs(t, err, ErrQuotaExceeded)
}

func TestService_CreateDatabase_AdminBypassQuota(t *testing.T) {
	store := newMockStore()
	config := DefaultServiceConfig()
	config.MaxDatabasesPerUser = 1
	svc := NewService(store, config, nil)

	// Create 1 database as user
	_, err := svc.CreateDatabase(context.Background(), "user-123", false, CreateRequest{
		DisplayName: "DB A",
	})
	require.NoError(t, err)

	// Admin can create more for the same user
	_, err = svc.CreateDatabase(context.Background(), "admin-user", true, CreateRequest{
		DisplayName: "DB B",
		OwnerID:     "user-123",
	})
	assert.NoError(t, err)
}

func TestService_CreateDatabase_ReservedSlug(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)

	slug := "admin"
	_, err := svc.CreateDatabase(context.Background(), "user-123", false, CreateRequest{
		Slug:        &slug,
		DisplayName: "My Admin",
	})
	assert.ErrorIs(t, err, ErrReservedSlug)
}

func TestService_CreateDatabase_AdminCanUseReservedSlug(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)

	slug := "default"
	db, err := svc.CreateDatabase(context.Background(), "admin-user", true, CreateRequest{
		Slug:        &slug,
		DisplayName: "Default Database",
	})
	assert.NoError(t, err)
	assert.Equal(t, &slug, db.Slug)
}

func TestService_CreateDatabase_MissingDisplayName(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)

	_, err := svc.CreateDatabase(context.Background(), "user-123", false, CreateRequest{})
	assert.ErrorIs(t, err, ErrDisplayNameRequired)
}

func TestService_ListDatabases(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)

	// Create databases for two users
	_, err := svc.CreateDatabase(context.Background(), "user-1", false, CreateRequest{
		DisplayName: "User 1 DB",
	})
	require.NoError(t, err)

	_, err = svc.CreateDatabase(context.Background(), "user-2", false, CreateRequest{
		DisplayName: "User 2 DB",
	})
	require.NoError(t, err)

	// Non-admin can only see their own
	result, err := svc.ListDatabases(context.Background(), "user-1", false, ListOptions{})
	assert.NoError(t, err)
	assert.Equal(t, 1, result.Total)
	assert.Len(t, result.Databases, 1)
	assert.Equal(t, "User 1 DB", result.Databases[0].DisplayName)
	assert.NotNil(t, result.Quota)

	// Admin can see all
	result, err = svc.ListDatabases(context.Background(), "admin", true, ListOptions{})
	assert.NoError(t, err)
	assert.Equal(t, 2, result.Total)
	assert.Nil(t, result.Quota)
}

func TestService_GetDatabase(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)

	slug := "my-app"
	created, err := svc.CreateDatabase(context.Background(), "user-123", false, CreateRequest{
		Slug:        &slug,
		DisplayName: "My App",
	})
	require.NoError(t, err)

	// Get by slug
	db, err := svc.GetDatabase(context.Background(), "user-123", false, "my-app")
	assert.NoError(t, err)
	assert.Equal(t, created.ID, db.ID)

	// Get by ID
	db, err = svc.GetDatabase(context.Background(), "user-123", false, "id:"+created.ID)
	assert.NoError(t, err)
	assert.Equal(t, created.ID, db.ID)
}

func TestService_GetDatabase_NotOwner(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)

	slug := "my-app"
	_, err := svc.CreateDatabase(context.Background(), "user-1", false, CreateRequest{
		Slug:        &slug,
		DisplayName: "My App",
	})
	require.NoError(t, err)

	// Other user cannot access
	_, err = svc.GetDatabase(context.Background(), "user-2", false, "my-app")
	assert.ErrorIs(t, err, ErrNotOwner)

	// Admin can access
	db, err := svc.GetDatabase(context.Background(), "admin", true, "my-app")
	assert.NoError(t, err)
	assert.Equal(t, "user-1", db.OwnerID)
}

func TestService_UpdateDatabase(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)

	created, err := svc.CreateDatabase(context.Background(), "user-123", false, CreateRequest{
		DisplayName: "Original Name",
	})
	require.NoError(t, err)

	// Update display name
	newName := "Updated Name"
	updated, err := svc.UpdateDatabase(context.Background(), "user-123", false, "id:"+created.ID, UpdateRequest{
		DisplayName: &newName,
	})
	assert.NoError(t, err)
	assert.Equal(t, "Updated Name", updated.DisplayName)
}

func TestService_UpdateDatabase_SetSlug(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)

	created, err := svc.CreateDatabase(context.Background(), "user-123", false, CreateRequest{
		DisplayName: "My App",
	})
	require.NoError(t, err)
	assert.Nil(t, created.Slug)

	// Set slug
	slug := "my-app"
	updated, err := svc.UpdateDatabase(context.Background(), "user-123", false, "id:"+created.ID, UpdateRequest{
		Slug: &slug,
	})
	assert.NoError(t, err)
	assert.Equal(t, &slug, updated.Slug)
}

func TestService_UpdateDatabase_SlugImmutable(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)

	slug := "my-app"
	created, err := svc.CreateDatabase(context.Background(), "user-123", false, CreateRequest{
		Slug:        &slug,
		DisplayName: "My App",
	})
	require.NoError(t, err)

	// Try to change slug
	newSlug := "new-slug"
	_, err = svc.UpdateDatabase(context.Background(), "user-123", false, "id:"+created.ID, UpdateRequest{
		Slug: &newSlug,
	})
	assert.ErrorIs(t, err, ErrSlugImmutable)
}

func TestService_DeleteDatabase(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)

	slug := "my-app"
	created, err := svc.CreateDatabase(context.Background(), "user-123", false, CreateRequest{
		Slug:        &slug,
		DisplayName: "My App",
	})
	require.NoError(t, err)

	// Delete
	err = svc.DeleteDatabase(context.Background(), "user-123", false, "my-app")
	assert.NoError(t, err)

	// Verify status is deleting
	db, _ := store.Get(context.Background(), created.ID)
	assert.Equal(t, StatusDeleting, db.Status)
}

func TestService_DeleteDatabase_Protected(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)

	// Create a protected database (admin creating default)
	slug := "default"
	_, err := svc.CreateDatabase(context.Background(), "admin", true, CreateRequest{
		Slug:        &slug,
		DisplayName: "Default Database",
	})
	require.NoError(t, err)

	// Try to delete
	err = svc.DeleteDatabase(context.Background(), "admin", true, "default")
	assert.ErrorIs(t, err, ErrProtectedDatabase)
}

func TestService_ResolveDatabase(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)

	slug := "my-app"
	created, err := svc.CreateDatabase(context.Background(), "user-123", false, CreateRequest{
		Slug:        &slug,
		DisplayName: "My App",
	})
	require.NoError(t, err)

	// Resolve active database
	db, err := svc.ResolveDatabase(context.Background(), "my-app")
	assert.NoError(t, err)
	assert.Equal(t, created.ID, db.ID)
}

func TestService_ResolveDatabase_Suspended(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)

	slug := "my-app"
	created, err := svc.CreateDatabase(context.Background(), "user-123", false, CreateRequest{
		Slug:        &slug,
		DisplayName: "My App",
	})
	require.NoError(t, err)

	// Suspend the database (admin action)
	status := StatusSuspended
	_, err = svc.UpdateDatabase(context.Background(), "admin", true, "id:"+created.ID, UpdateRequest{
		Status: &status,
	})
	require.NoError(t, err)

	// Resolve should fail
	_, err = svc.ResolveDatabase(context.Background(), "my-app")
	assert.ErrorIs(t, err, ErrDatabaseSuspended)
}

func TestService_ResolveDatabase_Deleting(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)

	slug := "my-app"
	_, err := svc.CreateDatabase(context.Background(), "user-123", false, CreateRequest{
		Slug:        &slug,
		DisplayName: "My App",
	})
	require.NoError(t, err)

	// Delete the database
	err = svc.DeleteDatabase(context.Background(), "user-123", false, "my-app")
	require.NoError(t, err)

	// Resolve should fail
	_, err = svc.ResolveDatabase(context.Background(), "my-app")
	assert.ErrorIs(t, err, ErrDatabaseDeleting)
}

func TestService_ValidateDatabase(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)

	slug := "my-app"
	_, err := svc.CreateDatabase(context.Background(), "user-123", false, CreateRequest{
		Slug:        &slug,
		DisplayName: "My App",
	})
	require.NoError(t, err)

	// Validate active database
	err = svc.ValidateDatabase(context.Background(), "my-app")
	assert.NoError(t, err)

	// Validate non-existent database
	err = svc.ValidateDatabase(context.Background(), "nonexistent")
	assert.ErrorIs(t, err, ErrDatabaseNotFound)
}

func TestService_UpdateDatabase_AdminSetSlug_InvalidSlugLength(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)

	// Create a database without slug
	created, err := svc.CreateDatabase(context.Background(), "admin", true, CreateRequest{
		DisplayName: "My App",
	})
	require.NoError(t, err)
	assert.Nil(t, created.Slug)

	// Admin tries to set an invalid slug (too short)
	slug := "ab" // Only 2 characters, minimum is 3
	_, err = svc.UpdateDatabase(context.Background(), "admin", true, "id:"+created.ID, UpdateRequest{
		Slug: &slug,
	})
	assert.ErrorIs(t, err, ErrInvalidSlugLength)
}

func TestService_UpdateDatabase_AdminSetSlug_InvalidSlugFormat(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)

	// Create a database without slug
	created, err := svc.CreateDatabase(context.Background(), "admin", true, CreateRequest{
		DisplayName: "My App",
	})
	require.NoError(t, err)
	assert.Nil(t, created.Slug)

	// Admin tries to set an invalid slug (invalid format)
	slug := "Invalid_Slug!" // Contains uppercase, underscore, and special char
	_, err = svc.UpdateDatabase(context.Background(), "admin", true, "id:"+created.ID, UpdateRequest{
		Slug: &slug,
	})
	assert.ErrorIs(t, err, ErrInvalidSlugFormat)
}

func TestService_UpdateDatabase_AdminSetSlug_Valid(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)

	// Create a database without slug
	created, err := svc.CreateDatabase(context.Background(), "admin", true, CreateRequest{
		DisplayName: "My App",
	})
	require.NoError(t, err)
	assert.Nil(t, created.Slug)

	// Admin sets a valid slug (including reserved ones)
	slug := "admin" // Reserved slug, but admin can use it
	updated, err := svc.UpdateDatabase(context.Background(), "admin", true, "id:"+created.ID, UpdateRequest{
		Slug: &slug,
	})
	assert.NoError(t, err)
	assert.Equal(t, &slug, updated.Slug)
}

func TestService_UpdateDatabase_NotFound(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)

	newName := "Updated Name"
	_, err := svc.UpdateDatabase(context.Background(), "user-123", false, "nonexistent", UpdateRequest{
		DisplayName: &newName,
	})
	assert.ErrorIs(t, err, ErrDatabaseNotFound)
}

func TestService_UpdateDatabase_NotOwner(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)

	// User-1 creates database
	slug := "my-app"
	created, err := svc.CreateDatabase(context.Background(), "user-1", false, CreateRequest{
		Slug:        &slug,
		DisplayName: "My App",
	})
	require.NoError(t, err)

	// User-2 tries to update
	newName := "Hacked Name"
	_, err = svc.UpdateDatabase(context.Background(), "user-2", false, "id:"+created.ID, UpdateRequest{
		DisplayName: &newName,
	})
	assert.ErrorIs(t, err, ErrNotOwner)
}

func TestService_UpdateDatabase_NonAdminInvalidSlug(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)

	// Create a database without slug
	created, err := svc.CreateDatabase(context.Background(), "user-123", false, CreateRequest{
		DisplayName: "My App",
	})
	require.NoError(t, err)
	assert.Nil(t, created.Slug)

	// Non-admin tries to set an invalid slug (too short)
	slug := "ab"
	_, err = svc.UpdateDatabase(context.Background(), "user-123", false, "id:"+created.ID, UpdateRequest{
		Slug: &slug,
	})
	assert.ErrorIs(t, err, ErrInvalidSlugLength)
}

func TestService_UpdateDatabase_NonAdminReservedSlug(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)

	// Create a database without slug
	created, err := svc.CreateDatabase(context.Background(), "user-123", false, CreateRequest{
		DisplayName: "My App",
	})
	require.NoError(t, err)

	// Non-admin tries to set a reserved slug
	slug := "admin"
	_, err = svc.UpdateDatabase(context.Background(), "user-123", false, "id:"+created.ID, UpdateRequest{
		Slug: &slug,
	})
	assert.ErrorIs(t, err, ErrReservedSlug)
}

func TestService_UpdateDatabase_AdminInvalidStatus(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)

	created, err := svc.CreateDatabase(context.Background(), "user-123", false, CreateRequest{
		DisplayName: "My App",
	})
	require.NoError(t, err)

	// Admin tries to set an invalid status
	invalidStatus := DatabaseStatus("invalid")
	_, err = svc.UpdateDatabase(context.Background(), "admin", true, "id:"+created.ID, UpdateRequest{
		Status: &invalidStatus,
	})
	assert.ErrorIs(t, err, ErrInvalidStatus)
}

func TestService_UpdateDatabase_AdminSetSettings(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)

	created, err := svc.CreateDatabase(context.Background(), "user-123", false, CreateRequest{
		DisplayName: "My App",
	})
	require.NoError(t, err)

	// Admin updates settings
	settings := &DatabaseSettings{
		MaxDocuments:    1000,
		MaxStorageBytes: 1024 * 1024 * 100, // 100MB
	}
	updated, err := svc.UpdateDatabase(context.Background(), "admin", true, "id:"+created.ID, UpdateRequest{
		Settings: settings,
	})
	assert.NoError(t, err)
	assert.Equal(t, int64(1000), updated.MaxDocuments)
	assert.Equal(t, int64(1024*1024*100), updated.MaxStorageBytes)
}

func TestService_UpdateDatabase_UpdateDescription(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)

	created, err := svc.CreateDatabase(context.Background(), "user-123", false, CreateRequest{
		DisplayName: "My App",
	})
	require.NoError(t, err)
	assert.Nil(t, created.Description)

	// Update description
	desc := "This is my application database"
	updated, err := svc.UpdateDatabase(context.Background(), "user-123", false, "id:"+created.ID, UpdateRequest{
		Description: &desc,
	})
	assert.NoError(t, err)
	assert.NotNil(t, updated.Description)
	assert.Equal(t, desc, *updated.Description)
}

func TestService_DeleteDatabase_NotFound(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)

	err := svc.DeleteDatabase(context.Background(), "user-123", false, "nonexistent")
	assert.ErrorIs(t, err, ErrDatabaseNotFound)
}

func TestService_DeleteDatabase_NotOwner(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)

	// User-1 creates database
	slug := "my-app"
	_, err := svc.CreateDatabase(context.Background(), "user-1", false, CreateRequest{
		Slug:        &slug,
		DisplayName: "My App",
	})
	require.NoError(t, err)

	// User-2 tries to delete
	err = svc.DeleteDatabase(context.Background(), "user-2", false, "my-app")
	assert.ErrorIs(t, err, ErrNotOwner)
}

func TestService_DeleteDatabase_AdminCanDeleteOthersDatabase(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)

	// User creates database
	slug := "user-app"
	created, err := svc.CreateDatabase(context.Background(), "user-1", false, CreateRequest{
		Slug:        &slug,
		DisplayName: "User App",
	})
	require.NoError(t, err)

	// Admin can delete it
	err = svc.DeleteDatabase(context.Background(), "admin", true, "user-app")
	assert.NoError(t, err)

	// Verify status is deleting
	db, _ := store.Get(context.Background(), created.ID)
	assert.Equal(t, StatusDeleting, db.Status)
}

func TestService_CreateDatabase_InvalidSlugFormat(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)

	slug := "InvalidSlug!" // Contains uppercase and special char
	_, err := svc.CreateDatabase(context.Background(), "user-123", false, CreateRequest{
		Slug:        &slug,
		DisplayName: "My App",
	})
	assert.ErrorIs(t, err, ErrInvalidSlugFormat)
}

func TestService_CreateDatabase_AdminInvalidSlugFormat(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)

	slug := "Invalid_Slug!" // Contains uppercase, underscore, and special char
	_, err := svc.CreateDatabase(context.Background(), "admin", true, CreateRequest{
		Slug:        &slug,
		DisplayName: "My App",
	})
	assert.ErrorIs(t, err, ErrInvalidSlugFormat)
}

func TestService_CreateDatabase_AdminWithSettings(t *testing.T) {
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)

	settings := &DatabaseSettings{
		MaxDocuments:    5000,
		MaxStorageBytes: 1024 * 1024 * 500, // 500MB
	}
	db, err := svc.CreateDatabase(context.Background(), "admin", true, CreateRequest{
		DisplayName: "Admin DB",
		Settings:    settings,
	})

	assert.NoError(t, err)
	assert.Equal(t, int64(5000), db.MaxDocuments)
	assert.Equal(t, int64(1024*1024*500), db.MaxStorageBytes)
}

func TestService_ListDatabases_AdminNoQuota(t *testing.T) {
	store := newMockStore()
	config := DefaultServiceConfig()
	config.MaxDatabasesPerUser = 0 // Unlimited
	svc := NewService(store, config, nil)

	_, err := svc.CreateDatabase(context.Background(), "user-1", false, CreateRequest{
		DisplayName: "User 1 DB",
	})
	require.NoError(t, err)

	// Non-admin with unlimited quota should not have quota info
	result, err := svc.ListDatabases(context.Background(), "user-1", false, ListOptions{})
	assert.NoError(t, err)
	assert.Nil(t, result.Quota)
}
