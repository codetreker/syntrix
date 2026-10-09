package core

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/puller/config"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var (
	testMongoURI = "mongodb://localhost:27017"
	globalClient *mongo.Client
	clientOnce   sync.Once
)

func init() {
	if uri := os.Getenv("MONGODB_URI"); uri != "" {
		testMongoURI = uri
	}
}

func getGlobalTestClient(t *testing.T) *mongo.Client {
	clientOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		client, err := mongo.Connect(ctx, options.Client().ApplyURI(testMongoURI))
		if err != nil {
			t.Fatalf("Failed to connect to MongoDB: %v", err)
			return
		}

		if err := client.Ping(ctx, nil); err != nil {
			t.Fatalf("Failed to ping MongoDB: %v", err)
			return
		}

		globalClient = client
	})

	if globalClient == nil {
		t.Fatal("MongoDB client not initialized")
	}

	return globalClient
}

type TestEnv struct {
	Client *mongo.Client
	DBName string
	DB     *mongo.Database
}

func newTestConfig(t *testing.T) config.Config {
	t.Helper()

	cfg := config.DefaultConfig()
	cfg.Buffer.Path = t.TempDir()
	return cfg
}

func setupTestEnv(t *testing.T) *TestEnv {
	t.Parallel()

	client := getGlobalTestClient(t)

	// Generate unique DB name
	safeName := strings.ReplaceAll(t.Name(), "/", "_")
	safeName = strings.ReplaceAll(safeName, "\\", "_")
	if len(safeName) > 20 {
		safeName = safeName[len(safeName)-20:]
	}
	dbName := fmt.Sprintf("test_puller_core_%s_%d", safeName, time.Now().UnixNano()%100000)

	// Cleanup function
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.Database(dbName).Drop(ctx)
	})

	return &TestEnv{
		Client: client,
		DBName: dbName,
		DB:     client.Database(dbName),
	}
}
