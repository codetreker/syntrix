package rest

import (
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/database"
	"github.com/stretchr/testify/assert"
)

func TestToDatabaseResponse(t *testing.T) {
	slug := "test-db"
	desc := "A test database"
	now := time.Now()

	db := &database.Database{
		ID:              "abc123",
		Slug:            &slug,
		DisplayName:     "Test Database",
		Description:     &desc,
		OwnerID:         "user-123",
		CreatedAt:       now,
		UpdatedAt:       now.Add(time.Hour),
		Status:          database.StatusActive,
		MaxDocuments:    1000,
		MaxStorageBytes: 1024 * 1024,
	}

	resp := toDatabaseResponse(db)

	assert.Equal(t, "abc123", resp.ID)
	assert.NotNil(t, resp.Slug)
	assert.Equal(t, "test-db", *resp.Slug)
	assert.Equal(t, "Test Database", resp.DisplayName)
	assert.NotNil(t, resp.Description)
	assert.Equal(t, "A test database", *resp.Description)
	assert.Equal(t, "user-123", resp.OwnerID)
	assert.Equal(t, now, resp.CreatedAt)
	assert.Equal(t, now.Add(time.Hour), resp.UpdatedAt)
	assert.Equal(t, "active", resp.Status)
	assert.NotNil(t, resp.Settings)
	assert.Equal(t, int64(1000), resp.Settings.MaxDocuments)
	assert.Equal(t, int64(1024*1024), resp.Settings.MaxStorageBytes)
}

func TestToDatabaseResponse_NilSlug(t *testing.T) {
	db := &database.Database{
		ID:          "abc123",
		Slug:        nil,
		DisplayName: "Test Database",
		OwnerID:     "user-123",
		Status:      database.StatusActive,
	}

	resp := toDatabaseResponse(db)

	assert.Nil(t, resp.Slug)
	assert.Nil(t, resp.Description)
}

func TestToDatabaseSummary(t *testing.T) {
	slug := "test-db"
	now := time.Now()

	db := &database.Database{
		ID:          "abc123",
		Slug:        &slug,
		DisplayName: "Test Database",
		OwnerID:     "user-123",
		CreatedAt:   now,
		Status:      database.StatusActive,
	}

	summary := toDatabaseSummary(db)

	assert.Equal(t, "abc123", summary.ID)
	assert.NotNil(t, summary.Slug)
	assert.Equal(t, "test-db", *summary.Slug)
	assert.Equal(t, "Test Database", summary.DisplayName)
	assert.Equal(t, "user-123", summary.OwnerID)
	assert.Equal(t, now, summary.CreatedAt)
	assert.Equal(t, "active", summary.Status)
}

func TestCreateDatabaseRequest_ToCreateRequest(t *testing.T) {
	t.Run("basic request", func(t *testing.T) {
		slug := "my-db"
		desc := "My description"
		req := CreateDatabaseRequest{
			Slug:        &slug,
			DisplayName: "My Database",
			Description: &desc,
		}

		result := req.toCreateRequest()

		assert.NotNil(t, result.Slug)
		assert.Equal(t, "my-db", *result.Slug)
		assert.Equal(t, "My Database", result.DisplayName)
		assert.NotNil(t, result.Description)
		assert.Equal(t, "My description", *result.Description)
		assert.Nil(t, result.Settings)
	})

	t.Run("with owner and settings", func(t *testing.T) {
		maxDocs := int64(5000)
		maxStorage := int64(1024 * 1024 * 100)
		req := CreateDatabaseRequest{
			DisplayName: "Admin DB",
			OwnerID:     "other-user",
			Settings: &DatabaseSettingsRequest{
				MaxDocuments:    &maxDocs,
				MaxStorageBytes: &maxStorage,
			},
		}

		result := req.toCreateRequest()

		assert.Equal(t, "Admin DB", result.DisplayName)
		assert.Equal(t, "other-user", result.OwnerID)
		assert.NotNil(t, result.Settings)
		assert.Equal(t, int64(5000), result.Settings.MaxDocuments)
		assert.Equal(t, int64(1024*1024*100), result.Settings.MaxStorageBytes)
	})

	t.Run("partial settings", func(t *testing.T) {
		maxDocs := int64(1000)
		req := CreateDatabaseRequest{
			DisplayName: "Test",
			Settings: &DatabaseSettingsRequest{
				MaxDocuments: &maxDocs,
			},
		}

		result := req.toCreateRequest()

		assert.NotNil(t, result.Settings)
		assert.Equal(t, int64(1000), result.Settings.MaxDocuments)
		assert.Equal(t, int64(0), result.Settings.MaxStorageBytes)
	})
}

func TestUpdateDatabaseRequest_ToUpdateRequest(t *testing.T) {
	t.Run("basic update", func(t *testing.T) {
		displayName := "Updated Name"
		desc := "Updated description"
		req := UpdateDatabaseRequest{
			DisplayName: &displayName,
			Description: &desc,
		}

		result := req.toUpdateRequest()

		assert.NotNil(t, result.DisplayName)
		assert.Equal(t, "Updated Name", *result.DisplayName)
		assert.NotNil(t, result.Description)
		assert.Equal(t, "Updated description", *result.Description)
		assert.Nil(t, result.Status)
		assert.Nil(t, result.Settings)
	})

	t.Run("with status", func(t *testing.T) {
		status := "suspended"
		req := UpdateDatabaseRequest{
			Status: &status,
		}

		result := req.toUpdateRequest()

		assert.NotNil(t, result.Status)
		assert.Equal(t, database.DatabaseStatus("suspended"), *result.Status)
	})

	t.Run("with settings", func(t *testing.T) {
		maxDocs := int64(10000)
		maxStorage := int64(1024 * 1024 * 500)
		req := UpdateDatabaseRequest{
			Settings: &DatabaseSettingsRequest{
				MaxDocuments:    &maxDocs,
				MaxStorageBytes: &maxStorage,
			},
		}

		result := req.toUpdateRequest()

		assert.NotNil(t, result.Settings)
		assert.Equal(t, int64(10000), result.Settings.MaxDocuments)
		assert.Equal(t, int64(1024*1024*500), result.Settings.MaxStorageBytes)
	})

	t.Run("set slug", func(t *testing.T) {
		slug := "new-slug"
		req := UpdateDatabaseRequest{
			Slug: &slug,
		}

		result := req.toUpdateRequest()

		assert.NotNil(t, result.Slug)
		assert.Equal(t, "new-slug", *result.Slug)
	})
}
