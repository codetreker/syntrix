package mongo

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/mongo"
)

type TestEnv struct {
	Client *mongo.Client
	DBName string
	DB     *mongo.Database
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
	dbName := fmt.Sprintf("test_mongo_%s_%d", safeName, time.Now().UnixNano()%100000)

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
