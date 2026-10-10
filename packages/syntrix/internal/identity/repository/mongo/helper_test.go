package mongo

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type TestEnv struct {
	Client *mongo.Client
	DB     *mongo.Database
}

func setupTestEnv(t *testing.T) *TestEnv {
	t.Helper()
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI("mongodb://localhost:27017"))
	require.NoError(t, err)
	require.NoError(t, client.Ping(ctx, nil))
	testName := strings.ReplaceAll(t.Name(), "/", "_")
	if len(testName) > 20 {
		testName = testName[len(testName)-20:]
	}
	name := fmt.Sprintf("identity_%s_%d", testName, time.Now().UnixNano())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.Database(name).Drop(ctx)
		_ = client.Disconnect(ctx)
	})
	return &TestEnv{Client: client, DB: client.Database(name)}
}
