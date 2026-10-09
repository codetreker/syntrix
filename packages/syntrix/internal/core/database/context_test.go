package database

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContext_WithDatabase(t *testing.T) {
	slug := "test-db"
	db := &Database{
		ID:          "abc123",
		Slug:        &slug,
		DisplayName: "Test Database",
		OwnerID:     "user-123",
		Status:      StatusActive,
	}

	ctx := context.Background()
	newCtx := WithDatabase(ctx, db)

	// Verify that context has database
	assert.NotEqual(t, ctx, newCtx)

	// Retrieve database from context
	retrievedDB, ok := FromContext(newCtx)
	require.True(t, ok)
	assert.Equal(t, db.ID, retrievedDB.ID)
	assert.Equal(t, db.DisplayName, retrievedDB.DisplayName)
}

func TestContext_FromContext(t *testing.T) {
	t.Run("database present", func(t *testing.T) {
		slug := "test-db"
		db := &Database{
			ID:   "abc123",
			Slug: &slug,
		}

		ctx := WithDatabase(context.Background(), db)
		retrievedDB, ok := FromContext(ctx)

		assert.True(t, ok)
		assert.NotNil(t, retrievedDB)
		assert.Equal(t, "abc123", retrievedDB.ID)
	})

	t.Run("database not present", func(t *testing.T) {
		ctx := context.Background()
		retrievedDB, ok := FromContext(ctx)

		assert.False(t, ok)
		assert.Nil(t, retrievedDB)
	})

	t.Run("wrong type in context", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), contextKeyDatabase, "not a database")
		retrievedDB, ok := FromContext(ctx)

		assert.False(t, ok)
		assert.Nil(t, retrievedDB)
	})
}

func TestContext_MustFromContext(t *testing.T) {
	t.Run("database present", func(t *testing.T) {
		slug := "test-db"
		db := &Database{
			ID:   "abc123",
			Slug: &slug,
		}

		ctx := WithDatabase(context.Background(), db)
		retrievedDB := MustFromContext(ctx)

		assert.NotNil(t, retrievedDB)
		assert.Equal(t, "abc123", retrievedDB.ID)
	})

	t.Run("database not present - panics", func(t *testing.T) {
		ctx := context.Background()

		assert.Panics(t, func() {
			MustFromContext(ctx)
		})
	})
}
