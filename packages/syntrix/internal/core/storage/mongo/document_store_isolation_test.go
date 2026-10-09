package mongo

import (
	"context"
	"testing"

	"github.com/codetreker/syntrix/internal/core/storage/types"
	"github.com/codetreker/syntrix/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDocumentStore_DatabaseIsolation(t *testing.T) {
	env := setupTestEnv(t)
	store := NewDocumentStore(env.Client, env.DB, "docs_isolation", "sys_isolation", 0)
	ctx := context.Background()

	// Ensure indexes
	if ds, ok := store.(interface{ EnsureIndexes(context.Context) error }); ok {
		require.NoError(t, ds.EnsureIndexes(ctx))
	}

	databaseA := "databaseA"
	databaseB := "databaseB"
	collection := "users"
	docID := "user1"
	path := collection + "/" + docID

	// 1. Create document in Database A
	docA := types.NewStoredDoc(databaseA, collection, docID, map[string]interface{}{
		"name": "User A",
	})
	err := store.Create(ctx, databaseA, docA)
	require.NoError(t, err)

	// 2. Verify Database A can read it
	retrievedA, err := store.Get(ctx, databaseA, path)
	require.NoError(t, err)
	assert.Equal(t, "User A", retrievedA.Data["name"])

	// 3. Verify Database B CANNOT read it
	_, err = store.Get(ctx, databaseB, path)
	assert.ErrorIs(t, err, model.ErrNotFound, "Database B should not see Database A's document")

	// 4. Verify Database B CANNOT update it
	err = store.Update(ctx, databaseB, path, map[string]interface{}{"name": "Hacked"}, nil)
	assert.ErrorIs(t, err, model.ErrNotFound, "Database B should not be able to update Database A's document")

	// 5. Verify Database B CANNOT delete it
	err = store.Delete(ctx, databaseB, path, nil)
	assert.ErrorIs(t, err, model.ErrNotFound, "Database B should not be able to delete Database A's document")

	// 6. Create document with SAME path in Database B
	docB := types.NewStoredDoc(databaseB, collection, docID, map[string]interface{}{
		"name": "User B",
	})
	err = store.Create(ctx, databaseB, docB)
	require.NoError(t, err)

	// 7. Verify both exist and are different
	retrievedA, err = store.Get(ctx, databaseA, path)
	require.NoError(t, err)
	assert.Equal(t, "User A", retrievedA.Data["name"])

	retrievedB, err := store.Get(ctx, databaseB, path)
	require.NoError(t, err)
	assert.Equal(t, "User B", retrievedB.Data["name"])

	// 8. Verify IDs are different
	assert.NotEqual(t, retrievedA.Id, retrievedB.Id)
	assert.Contains(t, retrievedA.Id, databaseA+":")
	assert.Contains(t, retrievedB.Id, databaseB+":")
}

func TestDocumentStore_DatabaseGeneration(t *testing.T) {
	t.Parallel()
	database := "mydatabase"
	collection := "some"
	docid := "path"
	path := collection + "/" + docid

	// Calculate expected ID
	expectedID := types.CalculateDatabase(database, path)

	// Create doc
	doc := types.NewStoredDoc(database, collection, docid, nil)

	assert.Equal(t, expectedID, doc.Id)
	assert.Equal(t, database, doc.Database)
}
