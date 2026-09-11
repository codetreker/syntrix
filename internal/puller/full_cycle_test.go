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
	storagemongo "github.com/syntrixbase/syntrix/internal/core/storage/mongo"
	"github.com/syntrixbase/syntrix/internal/puller/config"
	"github.com/syntrixbase/syntrix/internal/puller/core"
	"github.com/syntrixbase/syntrix/internal/puller/events"
	pullergrpc "github.com/syntrixbase/syntrix/internal/puller/grpc"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// testGRPCServer wraps a gRPC server with the Puller service for testing.
type testGRPCServer struct {
	grpcServer   *grpc.Server
	pullerServer *pullergrpc.Server
	listener     net.Listener
	port         int
}

// newTestGRPCServer creates a test gRPC server with the Puller service registered.
func newTestGRPCServer(t *testing.T, pullerCore *core.Puller, logger *slog.Logger) *testGRPCServer {
	port := getFreePort(t)
	lis, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", port))
	require.NoError(t, err)

	grpcCfg := config.GRPCConfig{
		MaxConnections: 10,
	}
	pullerServer := pullergrpc.NewServer(grpcCfg, pullerCore, logger)

	grpcServer := grpc.NewServer()
	pullerv1.RegisterPullerServiceServer(grpcServer, pullerServer)

	// Initialize the puller server event handler
	pullerServer.Init()

	// Start serving in background
	go grpcServer.Serve(lis)

	return &testGRPCServer{
		grpcServer:   grpcServer,
		pullerServer: pullerServer,
		listener:     lis,
		port:         port,
	}
}

func (s *testGRPCServer) Stop() {
	s.pullerServer.Shutdown()
	s.grpcServer.GracefulStop()
}

func setupIntegrationEnv(t *testing.T) (*mongo.Collection, *core.Puller, *pullergrpc.Server, pullerv1.PullerServiceClient, func()) {
	// Setup MongoDB connection

	// 1. Setup MongoDB
	mongoURI := os.Getenv("MONGO_URI")
	if mongoURI == "" {
		mongoURI = "mongodb://localhost:27017"
	}
	dbName := strings.ReplaceAll(t.Name(), "/", "_") + "_" + fmt.Sprintf("%d", time.Now().UnixNano())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(mongoURI))
	require.NoError(t, err)

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
			BatchSize:     10,
			BatchInterval: 10 * time.Millisecond,
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
	pullerCore.SetRetryDelay(10 * time.Millisecond)
	pullerCore.SetBackpressureSlowDownDelay(10 * time.Millisecond)
	pullerCore.SetBackpressurePauseDelay(50 * time.Millisecond)

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

	// 3. Start gRPC Server
	grpcTestServer := newTestGRPCServer(t, pullerCore, logger)
	grpcServer := grpcTestServer.pullerServer

	// 4. Connect gRPC Client
	conn, err := grpc.NewClient(fmt.Sprintf("localhost:%d", grpcTestServer.port), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)

	grpcClient := pullerv1.NewPullerServiceClient(conn)

	cleanup := func() {
		conn.Close()
		grpcTestServer.Stop()
		pullerCore.Stop(context.Background())
		_ = client.Database(dbName).Drop(context.Background())
		_ = client.Disconnect(context.Background())
	}

	return coll, pullerCore, grpcServer, grpcClient, cleanup
}

func TestPuller_FullCycle_DataIntegrity(t *testing.T) {
	coll, _, _, client, cleanup := setupIntegrationEnv(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Subscribe first
	stream, err := client.Subscribe(ctx, &pullerv1.SubscribeRequest{
		ConsumerId: "integrity-consumer",
	})
	require.NoError(t, err)

	// Insert 50 documents
	count := 50
	for i := 0; i < count; i++ {
		doc := storage.NewStoredDoc(coll.Database().Name(), coll.Name(), fmt.Sprintf("doc-%d", i), map[string]any{"val": i, "exact": int64(9007199254740991) + int64(i)})
		doc.Data["id"] = "business-id"
		_, err := coll.InsertOne(ctx, doc)
		require.NoError(t, err)
	}

	// Verify 50 events
	for i := 0; i < count; i++ {
		evt, err := stream.Recv()
		require.NoError(t, err)
		assert.Equal(t, "insert", evt.ChangeEvent.OpType)
		assert.Equal(t, storage.CalculateDatabase(coll.Database().Name(), coll.Name()+"/"+fmt.Sprintf("doc-%d", i)), evt.ChangeEvent.MgoDocId)
		doc, err := events.UnmarshalDocument(evt.ChangeEvent.FullDoc)
		require.NoError(t, err)
		require.Equal(t, coll.Name()+"/"+fmt.Sprintf("doc-%d", i), doc.Fullpath)
		require.Equal(t, "business-id", doc.Data["id"])
		require.Equal(t, int64(9007199254740991)+int64(i), doc.Data["exact"])
	}

	store := storagemongo.NewDocumentStore(coll.Database().Client(), coll.Database(), coll.Name(), coll.Name(), 24*time.Hour)
	path := coll.Name() + "/doc-0"
	require.NoError(t, store.Delete(ctx, coll.Database().Name(), path, nil))
	deleted, err := stream.Recv()
	require.NoError(t, err)
	doc, err := events.UnmarshalDocument(deleted.ChangeEvent.FullDoc)
	require.NoError(t, err)
	require.True(t, doc.Deleted)
	require.Equal(t, path, doc.Fullpath)
	require.Empty(t, doc.Data)
	delta, err := events.UnmarshalUpdateDescription(deleted.ChangeEvent.UpdateDesc)
	require.NoError(t, err)
	var stored struct {
		Expiry primitive.DateTime `bson:"sys_expires_at"`
	}
	require.NoError(t, coll.FindOne(ctx, bson.M{"_id": doc.Id}).Decode(&stored))
	require.Equal(t, int64(stored.Expiry), delta.UpdatedFields["sys_expires_at"])
}

func TestPuller_FullCycle_Resilience(t *testing.T) {
	// Custom setup to allow restarting puller while keeping DB/Buffer
	mongoURI := os.Getenv("MONGO_URI")
	if mongoURI == "" {
		mongoURI = "mongodb://localhost:27017"
	}
	dbName := strings.ReplaceAll(t.Name(), "/", "_") + "_" + fmt.Sprintf("%d", time.Now().UnixNano())
	collName := "test_collection"
	tmpDir := t.TempDir()
	bufferPath := filepath.Join(tmpDir, "buffer")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mongoClient, err := mongo.Connect(ctx, options.Client().ApplyURI(mongoURI))
	require.NoError(t, err)
	defer func() {
		_ = mongoClient.Database(dbName).Drop(context.Background())
		_ = mongoClient.Disconnect(context.Background())
	}()

	err = mongoClient.Database(dbName).CreateCollection(ctx, collName)
	require.NoError(t, err)
	coll := mongoClient.Database(dbName).Collection(collName)

	// Helper to start puller
	startPuller := func() (*core.Puller, *testGRPCServer, pullerv1.PullerServiceClient, *grpc.ClientConn) {
		cfg := config.Config{
			Buffer: config.BufferConfig{
				Path:          bufferPath,
				BatchSize:     10,
				BatchInterval: 10 * time.Millisecond,
				QueueSize:     100,
				MaxSize:       "10MB",
			},
			Cleaner: config.CleanerConfig{
				Retention: 1 * time.Hour,
				Interval:  1 * time.Minute,
			},
			Bootstrap: config.BootstrapConfig{
				Mode: "from_now", // Should use checkpoint on restart
			},
		}
		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
		p := core.New(cfg, logger)

		backendCfg := config.PullerBackendConfig{
			Name:        "backend1",
			Collections: []string{collName},
		}
		err := p.AddBackend("backend1", mongoClient, dbName, backendCfg)
		require.NoError(t, err)
		err = p.Start(ctx)
		require.NoError(t, err)

		grpcTestServer := newTestGRPCServer(t, p, logger)

		conn, err := grpc.NewClient(fmt.Sprintf("localhost:%d", grpcTestServer.port), grpc.WithTransportCredentials(insecure.NewCredentials()))
		require.NoError(t, err)
		c := pullerv1.NewPullerServiceClient(conn)

		return p, grpcTestServer, c, conn
	}

	// 1. Start Puller
	p1, s1, c1, conn1 := startPuller()

	// 2. Subscribe and consume some events
	stream1, err := c1.Subscribe(ctx, &pullerv1.SubscribeRequest{ConsumerId: "resilience-1"})
	require.NoError(t, err)

	// Insert 10 docs
	for i := 0; i < 10; i++ {
		_, err := coll.InsertOne(ctx, storage.NewStoredDoc(coll.Database().Name(), coll.Name(), fmt.Sprintf("doc-%d", i), nil))
		require.NoError(t, err)
	}

	// Consume 5
	var lastToken string
	for i := 0; i < 5; i++ {
		evt, err := stream1.Recv()
		require.NoError(t, err)
		assert.Equal(t, storage.CalculateDatabase(coll.Database().Name(), coll.Name()+"/"+fmt.Sprintf("doc-%d", i)), evt.ChangeEvent.MgoDocId)
		lastToken = evt.Progress
	}
	t.Logf("[DEBUG] Phase 1: Consumed 5 events, lastToken=%s", lastToken)

	// 3. Stop Puller (Simulate Crash)
	// Note: Some events may still be in transit from MongoDB change stream.
	// The checkpoint saves the last written event's resume token, so on restart,
	// the change stream will resume from that point and recover any missing events.
	conn1.Close()
	s1.Stop()
	p1.Stop(ctx)

	// 4. Restart Puller
	// The puller will resume the change stream from the checkpoint and
	// fetch any events that were in transit when we stopped.
	p2, s2, c2, conn2 := startPuller()
	defer func() {
		conn2.Close()
		s2.Stop()
		p2.Stop(ctx)
	}()

	// 5. Subscribe with last token
	// The subscriber will:
	// 1. Replay events from buffer (events already persisted)
	// 2. Switch to live mode and receive new events (including doc-9 recovered via resume token)
	t.Logf("[DEBUG] Subscribing with lastToken=%s", lastToken)
	stream2, err := c2.Subscribe(ctx, &pullerv1.SubscribeRequest{
		ConsumerId: "resilience-2",
		After:      lastToken,
	})
	require.NoError(t, err)

	// Resume repeats the saved timestamp group before delivering later events.
	boundary, err := stream2.Recv()
	require.NoError(t, err)
	require.NotNil(t, boundary.ChangeEvent)
	require.Equal(t, lastToken, boundary.Progress)
	require.Equal(t, storage.CalculateDatabase(coll.Database().Name(), coll.Name()+"/doc-4"), boundary.ChangeEvent.MgoDocId)

	// Consume the remaining five distinct events.
	for i := 5; i < 10; i++ {
		evt, err := stream2.Recv()
		if err != nil {
			t.Fatalf("[DEBUG] Failed to receive event doc-%d: %v", i, err)
		}
		assert.Equal(t, storage.CalculateDatabase(coll.Database().Name(), coll.Name()+"/"+fmt.Sprintf("doc-%d", i)), evt.ChangeEvent.MgoDocId)
	}
}

func TestPuller_FullCycle_SlowConsumer(t *testing.T) {
	coll, _, _, client, cleanup := setupIntegrationEnv(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Subscribe
	stream, err := client.Subscribe(ctx, &pullerv1.SubscribeRequest{
		ConsumerId: "slow-consumer",
	})
	require.NoError(t, err)

	// Insert 20 docs fast
	for i := 0; i < 20; i++ {
		_, err := coll.InsertOne(ctx, storage.NewStoredDoc(coll.Database().Name(), coll.Name(), fmt.Sprintf("doc-%d", i), nil))
		require.NoError(t, err)
	}

	// Consume slowly
	for i := 0; i < 20; i++ {
		time.Sleep(10 * time.Millisecond) // Simulate processing time
		evt, err := stream.Recv()
		require.NoError(t, err)
		assert.Equal(t, storage.CalculateDatabase(coll.Database().Name(), coll.Name()+"/"+fmt.Sprintf("doc-%d", i)), evt.ChangeEvent.MgoDocId)
	}
}
