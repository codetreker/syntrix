package mongo

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func TestNewDocumentStore(t *testing.T) {
	env := setupTestEnv(t)

	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", 0)
	assert.NotNil(t, store)
}

func TestRevocationStore_Close(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	store := NewRevocationStore(env.DB, "")
	err := store.Close(ctx)
	assert.NoError(t, err)
}

func TestDocumentStore_Close(t *testing.T) {
	// Create a dedicated client for this test to avoid closing the global one
	ctx := context.Background()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(testMongoURI))
	if err != nil {
		t.Fatalf("Failed to connect to MongoDB: %v", err)
	}
	defer client.Disconnect(ctx)

	db := client.Database("test_close")
	store := NewDocumentStore(client, db, "docs", "sys", 0)
	err = store.Close(ctx)
	assert.NoError(t, err)

	// Test with nil client
	storeNil := NewDocumentStore(nil, db, "docs", "sys", 0)
	err = storeNil.Close(ctx)
	assert.NoError(t, err)
}

func TestDocumentStore_GetCollection(t *testing.T) {
	env := setupTestEnv(t)
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", 0)
	ds, ok := store.(*documentStore)
	assert.True(t, ok)

	// Test data collection
	coll := ds.getCollection("users/1")
	assert.Equal(t, "docs", coll.Name())

	// Test sys collection
	coll = ds.getCollection("sys/config")
	assert.Equal(t, "sys", coll.Name())

	coll = ds.getCollection("sys")
	assert.Equal(t, "sys", coll.Name())
}
