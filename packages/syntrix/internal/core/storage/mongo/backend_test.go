package mongo

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/storage/types"
	"github.com/codetreker/syntrix/pkg/model"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testMongoURI = "mongodb://localhost:27017"
	testDBName   = "syntrix_test"
)

var (
	globalTestClient     *mongo.Client
	globalTestClientOnce sync.Once
)

func getGlobalTestClient(t *testing.T) *mongo.Client {
	globalTestClientOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		clientOpts := options.Client().ApplyURI(testMongoURI)
		client, err := mongo.Connect(ctx, clientOpts)
		require.NoError(t, err)
		err = client.Ping(ctx, nil)
		require.NoError(t, err)
		globalTestClient = client
	})
	return globalTestClient
}

type testDocumentStore struct {
	*documentStore
}

func (s *testDocumentStore) Close(ctx context.Context) error {
	// Do not close the shared client
	return nil
}

func setupTestBackend(t *testing.T) types.DocumentStore {
	env := setupTestEnv(t)

	dStore := &documentStore{
		client:              env.Client,
		db:                  env.DB,
		dataCollection:      "documents",
		sysCollection:       "sys",
		softDeleteRetention: 0,
	}

	// Recreate indexes
	ctx := context.Background()
	require.NoError(t, dStore.EnsureIndexes(ctx))

	return &testDocumentStore{documentStore: dStore}
}

func TestMongoBackend_CRUD(t *testing.T) {
	backend := setupTestBackend(t)
	defer backend.Close(context.Background())

	ctx := context.Background()
	collection := "users"
	docID := "testuser"
	docPath := collection + "/" + docID
	database := "default"

	// 1. Create
	doc := types.NewStoredDoc(database, collection, docID, map[string]interface{}{
		"name": "Test User",
		"age":  30,
	})
	err := backend.Create(ctx, database, doc)
	require.NoError(t, err)

	// 2. Get
	fetchedDoc, err := backend.Get(ctx, database, collection+"/"+docID)
	require.NoError(t, err)
	assert.Equal(t, doc.Id, fetchedDoc.Id)
	assert.Equal(t, doc.Collection, fetchedDoc.Collection)
	assert.Equal(t, types.CalculateCollectionHash(collection), fetchedDoc.CollectionHash)
	assert.Equal(t, "Test User", fetchedDoc.Data["name"])
	assert.Equal(t, int64(1), fetchedDoc.Version)

	// 3. Update (Success)
	newData := map[string]interface{}{
		"name": "Updated User",
		"age":  31,
	}

	filters := model.Filters{
		{Field: "version", Op: model.OpEq, Value: fetchedDoc.Version},
	}

	err = backend.Update(ctx, database, docPath, newData, filters)
	require.NoError(t, err)

	// Verify Update
	fetchedDoc, err = backend.Get(ctx, database, docPath)
	require.NoError(t, err)
	assert.Equal(t, "Updated User", fetchedDoc.Data["name"])
	assert.Equal(t, int64(2), fetchedDoc.Version)

	// 4. Update (CAS Failure)
	oldVersion := int64(1)
	filters = model.Filters{
		{Field: "version", Op: model.OpEq, Value: oldVersion},
	}
	err = backend.Update(ctx, database, docPath, newData, filters)
	assert.ErrorIs(t, err, model.ErrPreconditionFailed)

	// 5. Delete
	err = backend.Delete(ctx, database, docPath, nil)
	require.NoError(t, err)

	// Verify Delete
	_, err = backend.Get(ctx, database, docPath)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

func TestMongoBackend_Update_IfMatch(t *testing.T) {
	backend := setupTestBackend(t)
	defer backend.Close(context.Background())

	ctx := context.Background()
	collection := "users"
	docID := "ifmatch"
	docPath := collection + "/" + docID
	database := "default"

	// 1. Create
	doc := types.NewStoredDoc(database, collection, docID, map[string]interface{}{
		"status": "active",
		"score":  100,
	})
	err := backend.Create(ctx, database, doc)
	require.NoError(t, err)

	// 2. Update with matching filter (status == active)
	newData := map[string]interface{}{
		"status": "inactive",
		"score":  100,
	}
	filters := model.Filters{
		{Field: "status", Op: model.OpEq, Value: "active"},
	}
	err = backend.Update(ctx, database, docPath, newData, filters)
	require.NoError(t, err)

	// Verify Update
	fetchedDoc, err := backend.Get(ctx, database, docPath)
	require.NoError(t, err)
	assert.Equal(t, "inactive", fetchedDoc.Data["status"])

	// 3. Update with non-matching filter (score > 200)
	newData2 := map[string]interface{}{
		"status": "banned",
		"score":  100,
	}
	filters2 := model.Filters{
		{Field: "score", Op: model.OpGt, Value: 200},
	}
	err = backend.Update(ctx, database, docPath, newData2, filters2)
	assert.ErrorIs(t, err, model.ErrPreconditionFailed)

	// Verify No Update
	fetchedDoc, err = backend.Get(ctx, database, docPath)
	require.NoError(t, err)
	assert.Equal(t, "inactive", fetchedDoc.Data["status"])
}

func TestMongoBackend_FilterOperators_OnUpdatePatchDelete(t *testing.T) {
	backend := setupTestBackend(t)
	defer backend.Close(context.Background())

	ctx := context.Background()
	collection := "users"
	docID := "filter-ops"
	path := collection + "/" + docID
	database := "default"

	seed := types.NewStoredDoc(database, collection, docID, map[string]interface{}{
		"age":  int64(30),
		"tags": []string{"a", "b"},
	})
	require.NoError(t, backend.Create(ctx, database, seed))

	// Update guarded by numeric GT
	gtFilter := model.Filters{{Field: "age", Op: model.OpGt, Value: int64(25)}}
	require.NoError(t, backend.Update(ctx, database, path, map[string]interface{}{
		"age":  int64(31),
		"tags": []string{"a", "b"},
	}, gtFilter))

	updated, err := backend.Get(ctx, database, path)
	require.NoError(t, err)
	assert.EqualValues(t, 31, updated.Data["age"])

	// Update blocked by LT (should conflict)
	ltFilter := model.Filters{{Field: "age", Op: model.OpLt, Value: int64(20)}}
	err = backend.Update(ctx, database, path, map[string]interface{}{"age": 40}, ltFilter)
	assert.ErrorIs(t, err, model.ErrPreconditionFailed)

	// Patch using IN
	inFilter := model.Filters{{Field: "tags", Op: model.OpIn, Value: []string{"a", "c"}}}
	require.NoError(t, backend.Patch(ctx, database, path, map[string]interface{}{"city": "NY"}, inFilter))

	afterPatch, err := backend.Get(ctx, database, path)
	require.NoError(t, err)
	require.EqualValues(t, int64(3), afterPatch.Version)
	assert.Equal(t, "NY", afterPatch.Data["city"])

	// Delete guarded by GTE version
	gteFilter := model.Filters{{Field: "version", Op: model.OpGte, Value: afterPatch.Version}}
	require.NoError(t, backend.Delete(ctx, database, path, gteFilter))

	_, err = backend.Get(ctx, database, path)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

func TestMongoBackend_CreateDuplicate(t *testing.T) {
	backend := setupTestBackend(t)
	defer backend.Close(context.Background())

	ctx := context.Background()
	database := "default"
	doc := types.NewStoredDoc(database, "users", "dup", nil)

	err := backend.Create(ctx, database, doc)
	require.NoError(t, err)

	err = backend.Create(ctx, database, doc)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "document already exists") // Checking error message as we didn't define a specific error var for this yet
}

func TestMongoBackend_GetNotFound(t *testing.T) {
	backend := setupTestBackend(t)
	defer backend.Close(context.Background())

	ctx := context.Background()
	database := "default"
	_, err := backend.Get(ctx, database, "non/existent")
	assert.ErrorIs(t, err, model.ErrNotFound)
}

func TestMongoBackend_IndexesIncludeCollectionHash(t *testing.T) {
	backend := setupTestBackend(t)
	defer backend.Close(context.Background())

	ctx := context.Background()

	var dStore *documentStore
	if ds, ok := backend.(*documentStore); ok {
		dStore = ds
	} else if tds, ok := backend.(*testDocumentStore); ok {
		dStore = tds.documentStore
	}
	require.NotNil(t, dStore, "backend should be *documentStore or *testDocumentStore")

	coll := dStore.getCollection("")
	cur, err := coll.Indexes().List(ctx)
	require.NoError(t, err)
	defer cur.Close(ctx)

	found := false
	for cur.Next(ctx) {
		var idx bson.M
		require.NoError(t, cur.Decode(&idx))
		if key, ok := idx["key"].(bson.M); ok {
			if _, exists := key["collection_hash"]; exists {
				found = true
				break
			}
		}
	}

	assert.True(t, found, "collection_hash index should exist")
}

func TestMongoBackend_Patch(t *testing.T) {
	backend := setupTestBackend(t)
	defer backend.Close(context.Background())

	ctx := context.Background()

	collection := "users"
	docID := "patchuser"
	docPath := collection + "/" + docID
	database := "default"

	// Create initial document
	doc := types.NewStoredDoc(database, collection, docID, map[string]interface{}{
		"name": "Original Name",
		"info": map[string]interface{}{
			"age":  30,
			"city": "New York",
		},
	})
	err := backend.Create(ctx, database, doc)
	require.NoError(t, err)

	// Patch top-level field
	patchData := map[string]interface{}{
		"name": "Patched Name",
	}
	err = backend.Patch(ctx, database, docPath, patchData, model.Filters{})
	require.NoError(t, err)

	fetched, err := backend.Get(ctx, database, docPath)
	require.NoError(t, err)
	assert.Equal(t, "Patched Name", fetched.Data["name"])

	// Ensure other fields are preserved
	info, ok := fetched.Data["info"].(map[string]interface{})
	require.True(t, ok)
	assert.EqualValues(t, 30, info["age"])

	// Patch nested field using dot notation in key
	patchData2 := map[string]interface{}{
		"info.age": 31,
	}
	err = backend.Patch(ctx, database, docPath, patchData2, model.Filters{})
	require.NoError(t, err)

	fetched2, err := backend.Get(ctx, database, docPath)
	require.NoError(t, err)
	info2, ok := fetched2.Data["info"].(map[string]interface{})
	require.True(t, ok)
	assert.EqualValues(t, 31, info2["age"])
	assert.Equal(t, "New York", info2["city"])
}

func TestMongoBackend_Patch_WithFilter(t *testing.T) {
	backend := setupTestBackend(t)
	defer backend.Close(context.Background())

	ctx := context.Background()
	collection := "users"
	docID := "patch-precond"
	path := collection + "/" + docID
	database := "default"

	base := types.NewStoredDoc(database, collection, docID, map[string]interface{}{"name": "One"})
	require.NoError(t, backend.Create(ctx, database, base))

	wrong := model.Filters{{Field: "version", Op: model.OpEq, Value: int64(0)}}
	err := backend.Patch(ctx, database, path, map[string]interface{}{"name": "Wrong"}, wrong)
	assert.ErrorIs(t, err, model.ErrPreconditionFailed)

	current, err := backend.Get(ctx, database, path)
	require.NoError(t, err)
	assert.Equal(t, int64(1), current.Version)
	assert.Equal(t, "One", current.Data["name"])

	match := model.Filters{{Field: "version", Op: model.OpEq, Value: current.Version}}
	require.NoError(t, backend.Patch(ctx, database, path, map[string]interface{}{"name": "Two"}, match))

	updated, err := backend.Get(ctx, database, path)
	require.NoError(t, err)
	assert.Equal(t, "Two", updated.Data["name"])
	assert.Equal(t, int64(2), updated.Version)
}

func TestMongoBackend_Delete_WithFilter(t *testing.T) {
	backend := setupTestBackend(t)
	defer backend.Close(context.Background())

	ctx := context.Background()
	collection := "users"
	docID := "delete-precond"
	path := collection + "/" + docID
	database := "default"

	doc := types.NewStoredDoc(database, collection, docID, map[string]interface{}{"name": "ToDelete"})
	require.NoError(t, backend.Create(ctx, database, doc))

	wrong := model.Filters{{Field: "version", Op: model.OpEq, Value: int64(0)}}
	err := backend.Delete(ctx, database, path, wrong)
	assert.ErrorIs(t, err, model.ErrPreconditionFailed)

	stillThere, err := backend.Get(ctx, database, path)
	require.NoError(t, err)
	assert.Equal(t, "ToDelete", stillThere.Data["name"])

	match := model.Filters{{Field: "version", Op: model.OpEq, Value: stillThere.Version}}
	require.NoError(t, backend.Delete(ctx, database, path, match))

	_, err = backend.Get(ctx, database, path)
	assert.ErrorIs(t, err, model.ErrNotFound)
}
