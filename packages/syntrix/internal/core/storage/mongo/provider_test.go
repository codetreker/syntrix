package mongo

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func TestNewProvider_InvalidURI(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Invalid URI format
	p, err := NewProvider(ctx, "invalid-uri", "testdb")
	assert.Error(t, err)
	assert.Nil(t, p)
}

func TestNewProvider_ConnectionFailure(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Valid URI format but likely unreachable port
	// Using a short connect timeout to fail fast
	// Added serverSelectionTimeoutMS to fail fast on Ping
	uri := "mongodb://localhost:27999/?connectTimeoutMS=100&serverSelectionTimeoutMS=100"
	p, err := NewProvider(ctx, uri, "testdb")
	assert.Error(t, err)
	assert.Nil(t, p)
}

func TestProvider_Getters(t *testing.T) {
	t.Parallel()
	// Manually construct Provider since we are in the same package
	mockClient := &mongo.Client{}
	dbName := "test_db"
	p := &Provider{
		client: mockClient,
		dbName: dbName,
	}

	assert.Equal(t, mockClient, p.Client())
	assert.Equal(t, dbName, p.DatabaseName())
}

func TestProvider_Close(t *testing.T) {
	t.Parallel()
	// We need a real client to call Disconnect without panic (or at least a client created via Connect)
	// Even if not connected, Disconnect should be safe to call on a client returned by Connect.

	ctx := context.Background()
	clientOpts := options.Client().ApplyURI("mongodb://localhost:27017")
	client, _ := mongo.Connect(ctx, clientOpts)
	// We don't care if Connect succeeded in connecting, just that we have a client object.

	p := &Provider{
		client: client,
		dbName: "test_db",
	}

	err := p.Close(ctx)
	// Disconnect might return error if not connected, or nil.
	// We just want to ensure it calls client.Disconnect and doesn't panic.
	// Actually Disconnect is safe to call.
	_ = err
}

func TestNewProvider_Success(t *testing.T) {
	t.Parallel()
	// This test requires a running MongoDB.
	// We try to connect to the test URI defined in backend_test.go

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Try to connect first to see if Mongo is available
	clientOpts := options.Client().ApplyURI(testMongoURI)
	client, err := mongo.Connect(ctx, clientOpts)
	if err != nil {
		t.Fatal("MongoDB not available")
	}
	if err := client.Ping(ctx, nil); err != nil {
		t.Fatal("MongoDB not available")
	}
	client.Disconnect(ctx)

	// Generate unique DB name
	safeName := strings.ReplaceAll(t.Name(), "/", "_")
	safeName = strings.ReplaceAll(safeName, "\\", "_")
	if len(safeName) > 20 {
		safeName = safeName[len(safeName)-20:]
	}
	dbName := fmt.Sprintf("test_mongo_prov_%s_%d", safeName, time.Now().UnixNano()%100000)

	// Now test NewProvider
	p, err := NewProvider(ctx, testMongoURI, dbName)
	assert.NoError(t, err)
	assert.NotNil(t, p)

	if p != nil {
		assert.NotNil(t, p.Client())
		assert.Equal(t, dbName, p.DatabaseName())
		p.Close(ctx)
	}
}
