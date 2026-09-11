package puller

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pullerv1 "github.com/syntrixbase/syntrix/api/gen/puller/v1"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/puller/config"
	"github.com/syntrixbase/syntrix/internal/puller/core"
	pullergrpc "github.com/syntrixbase/syntrix/internal/puller/grpc"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestPuller_GRPC_Integration(t *testing.T) {
	// 1. Setup MongoDB
	mongoURI := os.Getenv("MONGO_URI")
	if mongoURI == "" {
		mongoURI = "mongodb://localhost:27017"
	}
	dbName := strings.ReplaceAll(t.Name(), "/", "_") + "_" + fmt.Sprintf("%d", time.Now().UnixNano())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(mongoURI))
	require.NoError(t, err)
	defer func() {
		_ = client.Database(dbName).Drop(ctx)
		_ = client.Disconnect(context.Background())
	}()

	// Create collection
	collName := "test_collection"
	err = client.Database(dbName).CreateCollection(ctx, collName)
	require.NoError(t, err)
	coll := client.Database(dbName).Collection(collName)

	// 2. Configure Puller
	tmpDir := t.TempDir()
	cfg := config.Config{
		Buffer: config.BufferConfig{
			Path:          filepath.Join(tmpDir, "buffer"),
			BatchSize:     1, // Ensure immediate flush for testing
			BatchInterval: 1 * time.Millisecond,
			QueueSize:     100,
			MaxSize:       "10MB",
		},
		Cleaner: config.CleanerConfig{
			Retention: 1 * time.Hour,
			Interval:  1 * time.Minute,
		},
		Bootstrap: config.BootstrapConfig{
			Mode: "from_now",
		},
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	pullerCore := core.New(cfg, logger)

	// Add backend
	backendCfg := config.PullerBackendConfig{
		Name:        "backend1",
		Collections: []string{collName},
	}
	err = pullerCore.AddBackend("backend1", client, dbName, backendCfg)
	require.NoError(t, err)

	// Start Puller Core
	err = pullerCore.Start(context.Background())
	require.NoError(t, err)
	defer pullerCore.Stop(context.Background())

	// 3. Start gRPC Server
	grpcPort := getFreePort(t)
	lis, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", grpcPort))
	require.NoError(t, err)

	grpcCfg := config.GRPCConfig{
		MaxConnections: 10,
	}
	pullerServer := pullergrpc.NewServer(grpcCfg, pullerCore, logger)

	grpcServer := grpc.NewServer()
	pullerv1.RegisterPullerServiceServer(grpcServer, pullerServer)
	pullerServer.Init()

	go grpcServer.Serve(lis)
	defer func() {
		pullerServer.Shutdown()
		grpcServer.GracefulStop()
	}()

	// 4. Connect gRPC Client
	conn, err := grpc.NewClient(fmt.Sprintf("localhost:%d", grpcPort), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer conn.Close()

	grpcClient := pullerv1.NewPullerServiceClient(conn)

	// 5. Test Live Streaming
	t.Run("LiveStreaming", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()

		stream, err := grpcClient.Subscribe(ctx, &pullerv1.SubscribeRequest{
			ConsumerId: "consumer-1",
		})
		require.NoError(t, err)

		// Insert document
		_, err = coll.InsertOne(ctx, storage.NewStoredDoc(dbName, collName, "doc1", map[string]any{"val": 1}))
		require.NoError(t, err)

		// Receive event
		evt, err := stream.Recv()
		require.NoError(t, err)
		assert.Equal(t, "insert", evt.ChangeEvent.OpType)
		assert.Equal(t, storage.CalculateDatabase(dbName, collName+"/doc1"), evt.ChangeEvent.MgoDocId)

		// Verify progress marker
		assert.NotEmpty(t, evt.Progress)
	})

	// 6. Test Replay
	t.Run("Replay", func(t *testing.T) {
		// Wait for doc1 to be fully buffered
		time.Sleep(100 * time.Millisecond)

		// Insert another document
		_, err = coll.InsertOne(ctx, storage.NewStoredDoc(dbName, collName, "doc2", map[string]any{"val": 2}))
		require.NoError(t, err)

		// Wait for doc2 to be buffered
		time.Sleep(100 * time.Millisecond)

		replayCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()

		// Subscribe from beginning (empty after)
		stream, err := grpcClient.Subscribe(replayCtx, &pullerv1.SubscribeRequest{
			ConsumerId: "consumer-2",
			After:      "e30", // From beginning of buffer (empty JSON object)
		})
		require.NoError(t, err)

		// Should receive doc1
		evt1, err := stream.Recv()
		require.NoError(t, err)
		assert.Equal(t, storage.CalculateDatabase(dbName, collName+"/doc1"), evt1.ChangeEvent.MgoDocId)

		// Should receive doc2
		evt2, err := stream.Recv()
		require.NoError(t, err)
		assert.Equal(t, storage.CalculateDatabase(dbName, collName+"/doc2"), evt2.ChangeEvent.MgoDocId)
	})
}

func getFreePort(t *testing.T) int {
	addr, err := net.ResolveTCPAddr("tcp", "localhost:0")
	require.NoError(t, err)
	l, err := net.ListenTCP("tcp", addr)
	require.NoError(t, err)
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
