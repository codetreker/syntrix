package mongo

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func TestDocumentProvider(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	provider, err := NewDocumentProvider(ctx, testMongoURI, env.DBName, "docs", "sys", 0)
	if err != nil {
		t.Fatalf("MongoDB not available: %v", err)
	}
	require.NoError(t, err)

	assert.NotNil(t, provider.Document())

	err = provider.Close(ctx)
	assert.NoError(t, err)
}

func TestDocumentProvider_EnsureIndexesError(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a conflicting index to cause EnsureIndexes to fail
	// EnsureIndexes tries to create {database: 1, collection_hash: 1} with unique=false
	// We create it with unique=true
	coll := env.DB.Collection("docs")
	_, err := coll.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "database", Value: 1}, {Key: "collection_hash", Value: 1}},
		Options: options.Index().SetUnique(true),
	})
	require.NoError(t, err)

	// Now try to create provider, it should fail at EnsureIndexes
	_, err = NewDocumentProvider(ctx, testMongoURI, env.DBName, "docs", "sys", 0)
	assert.Error(t, err)
	// Error message depends on mongo version but usually contains "Index with name ... already exists with different options"
}

func TestDocumentProvider_InvalidURI(t *testing.T) {
	ctx := context.Background()
	// Invalid URI scheme
	_, err := NewDocumentProvider(ctx, "invalid-scheme://localhost", "db", "docs", "sys", 0)
	assert.Error(t, err)
}

func TestDocumentProvider_Error(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	// Invalid URI
	_, err := NewDocumentProvider(ctx, "mongodb://invalid-host:27017", "db", "docs", "sys", 0)
	assert.Error(t, err)
}
