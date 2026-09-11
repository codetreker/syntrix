package core

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/puller/buffer"
	"github.com/syntrixbase/syntrix/internal/puller/config"
	"github.com/syntrixbase/syntrix/internal/puller/events"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func TestPuller_WatchAndCheckpoint(t *testing.T) {
	env := setupTestEnv(t)

	// Configure puller with checkpoint settings
	cfg := newTestConfig(t)

	p := New(cfg, nil)

	backendCfg := config.PullerBackendConfig{
		Collections: []string{"users"},
	}

	err := p.AddBackend("backend1", env.Client, env.DBName, backendCfg)
	if err != nil {
		t.Fatalf("AddBackend failed: %v", err)
	}

	// Subscribe to events
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := p.Subscribe(ctx, "consumer-1", "")

	// Start puller
	err = p.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Wait for change stream to be established
	time.Sleep(500 * time.Millisecond)

	// Insert a document to trigger an event
	coll := env.DB.Collection("users")
	_, err = coll.InsertOne(ctx, storage.NewStoredDoc(env.DBName, "users", "test-user", map[string]any{"username": "testuser", "email": "test@example.com"}))
	if err != nil {
		t.Fatalf("InsertOne failed: %v", err)
	}

	// Wait for event
	select {
	case evt := <-ch:
		if evt.Change.MgoColl != "users" {
			t.Errorf("Expected collection 'users', got '%s'", evt.Change.MgoColl)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for event")
	}

	// Wait for checkpoint to be saved (async)
	// Since we set Interval to 100ms, it should save shortly
	time.Sleep(200 * time.Millisecond)

	// Stop puller to force final checkpoint
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	err = p.Stop(stopCtx)
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	// Verify checkpoint exists
	checkpointBuf, err := buffer.NewForBackend(cfg.Buffer.Path, "backend1", nil)
	if err != nil {
		t.Fatalf("NewForBackend failed: %v", err)
	}
	defer checkpointBuf.Close()

	ckpt, err := checkpointBuf.LoadCheckpoint()
	if err != nil {
		t.Fatalf("LoadCheckpoint failed: %v", err)
	}
	if ckpt == nil {
		t.Fatal("Expected checkpoint to be saved")
	}
}

func TestPuller_BackendNames_NonEmpty(t *testing.T) {
	env := setupTestEnv(t)
	cfg := newTestConfig(t)
	p := New(cfg, nil)
	defer p.Stop(context.Background())

	backendCfg := config.PullerBackendConfig{
		Collections: []string{"users"},
	}

	_ = p.AddBackend("backend1", env.Client, env.DBName, backendCfg)
	_ = p.AddBackend("backend2", env.Client, env.DBName, backendCfg)

	names := p.BackendNames()
	if len(names) != 2 {
		t.Errorf("Expected 2 backend names, got %d", len(names))
	}
}

func TestPuller_ResumeFromCheckpoint(t *testing.T) {
	env := setupTestEnv(t)

	// We need a valid resume token. Run a short session to generate one.
	cfg := newTestConfig(t)
	p := New(cfg, nil)
	backendCfg := config.PullerBackendConfig{Collections: []string{"users"}}
	_ = p.AddBackend("backend1", env.Client, env.DBName, backendCfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	eventsCh := p.Subscribe(ctx, "c1", "")
	_ = p.Start(ctx)

	// Wait for change stream to be established
	time.Sleep(100 * time.Millisecond)

	// Generate event
	coll := env.DB.Collection("users")
	_, _ = coll.InsertOne(ctx, storage.NewStoredDoc(env.DBName, "users", "test-user", map[string]any{"a": 1}))

	// Wait for event to ensure it was processed
	select {
	case <-eventsCh:
		// Event received
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for event")
	}

	p.Stop(ctx)

	// Verify checkpoint exists
	checkpointBuf, err := buffer.NewForBackend(cfg.Buffer.Path, "backend1", nil)
	if err != nil {
		t.Fatalf("NewForBackend failed: %v", err)
	}
	ckpt, err := checkpointBuf.LoadCheckpoint()
	_ = checkpointBuf.Close()
	if err != nil {
		t.Fatalf("LoadCheckpoint failed: %v", err)
	}
	if ckpt == nil {
		t.Fatal("Failed to create initial checkpoint")
	}

	// 2. Start new puller instance, it should resume
	p2 := New(cfg, nil)
	if err := p2.AddBackend("backend1", env.Client, env.DBName, backendCfg); err != nil {
		t.Fatalf("AddBackend failed: %v", err)
	}

	// We can't easily verify "resuming" without mocking logger or checking internal state.
	// But running it ensures the code path is executed.
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	_ = p2.Subscribe(ctx2, "c1", "")
	err = p2.Start(ctx2)
	if err != nil {
		t.Fatalf("Failed to restart puller: %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	p2.Stop(ctx2)
}

func TestPuller_EventHandlerError(t *testing.T) {
	env := setupTestEnv(t)
	cfg := newTestConfig(t)
	p := New(cfg, nil)
	backendCfg := config.PullerBackendConfig{Collections: []string{"users"}}
	_ = p.AddBackend("backend1", env.Client, env.DBName, backendCfg)

	// Set a handler that returns an error
	p.SetEventHandler(func(ctx context.Context, backendName string, event *events.StoreChangeEvent) error {
		return fmt.Errorf("simulated handler error")
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = p.Start(ctx)

	// Wait for change stream to be established
	time.Sleep(100 * time.Millisecond)

	// Generate event
	coll := env.DB.Collection("users")
	_, _ = coll.InsertOne(ctx, storage.NewStoredDoc(env.DBName, "users", "test-user", map[string]any{"a": 1}))

	// Wait a bit, should not crash
	time.Sleep(100 * time.Millisecond)
	p.Stop(ctx)
}

func TestPuller_ChangeStreamError_Reconnect(t *testing.T) {
	t.Parallel()
	// Create a dedicated client to avoid affecting global shared client
	ctx := context.Background()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI("mongodb://localhost:27017"))
	if err != nil {
		t.Fatal("Failed to connect to MongoDB")
	}
	// We don't defer disconnect here because we want to disconnect manually during test
	// But we should ensure it's cleaned up if test fails before disconnect
	defer func() {
		if client != nil {
			_ = client.Disconnect(context.Background())
		}
	}()

	dbName := fmt.Sprintf("test_puller_reconnect_%d", time.Now().UnixNano())
	defer func() {
		// Reconnect to drop db? No, client is closed.
		// We can use a fresh client to drop it, or just ignore it (it's a test db)
		// Ideally we should clean up.
		cleanClient, _ := mongo.Connect(context.Background(), options.Client().ApplyURI("mongodb://localhost:27017"))
		if cleanClient != nil {
			_ = cleanClient.Database(dbName).Drop(context.Background())
			_ = cleanClient.Disconnect(context.Background())
		}
	}()

	cfg := newTestConfig(t)
	p := New(cfg, nil)
	p.retryDelay = 10 * time.Millisecond
	backendCfg := config.PullerBackendConfig{Collections: []string{"users"}}
	_ = p.AddBackend("backend1", client, dbName, backendCfg)

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = p.Start(runCtx)

	// Wait for it to start
	time.Sleep(100 * time.Millisecond)

	// Force close the client to trigger change stream error
	_ = client.Disconnect(runCtx)

	// Wait for reconnection attempt (it will fail since client is closed, but should loop)
	time.Sleep(200 * time.Millisecond)

	// Stop should still work (or timeout)
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer stopCancel()
	_ = p.Stop(stopCtx)
}

func TestPuller_WatchChangeStream_LoadError(t *testing.T) {
	cfg := newTestConfig(t)
	p := New(cfg, nil)
	defer p.Stop(context.Background())
	backendCfg := config.PullerBackendConfig{Collections: []string{"users"}}
	client, err := mongo.NewClient(options.Client().ApplyURI(testMongoURI))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.AddBackend("backend1", client, "testdb", backendCfg); err != nil {
		t.Fatal(err)
	}

	// Close buffer to force LoadCheckpoint error
	_ = p.backends["backend1"].buffer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.openStream = func(context.Context, *mongo.Database, mongo.Pipeline, *options.ChangeStreamOptions) (changeStream, error) {
		t.Error("checkpoint read failure opened a fresh change stream")
		return &fakeChangeStream{}, nil
	}

	err = p.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("checkpoint read failure retried instead of stopping the backend")
	}
}

func TestPuller_WatchChangeStream_WatchError(t *testing.T) {
	env := setupTestEnv(t)
	cfg := newTestConfig(t)
	p := New(cfg, nil)
	defer p.Stop(context.Background())
	backendCfg := config.PullerBackendConfig{Collections: []string{"users"}}
	_ = p.AddBackend("backend1", env.Client, env.DBName, backendCfg)

	// Close client to force Watch error
	// We use a separate client to avoid breaking other tests
	ctx := context.Background()
	client, _ := mongo.Connect(ctx, options.Client().ApplyURI("mongodb://localhost:27017"))
	defer client.Disconnect(ctx)

	p.backends["backend1"].client = client
	p.backends["backend1"].db = client.Database(env.DBName)

	_ = client.Disconnect(ctx)

	// Call watchChangeStream directly
	backend := p.backends["backend1"]
	err := p.watchChangeStream(ctx, backend, p.logger)
	if err == nil {
		t.Error("Expected error from watchChangeStream with closed client")
	}
}

func TestPuller_WatchChangeStream_Invalidate(t *testing.T) {
	env := setupTestEnv(t)
	cfg := newTestConfig(t)
	p := New(cfg, nil)
	p.retryDelay = 10 * time.Millisecond
	backendCfg := config.PullerBackendConfig{Collections: []string{"users"}}
	_ = p.AddBackend("backend1", env.Client, env.DBName, backendCfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start puller
	go func() {
		_ = p.Start(ctx)
	}()

	time.Sleep(100 * time.Millisecond)

	// Drop collection to trigger invalidate event
	// This causes:
	// 1. 'invalidate' event received
	// 2. Normalize fails (OperationType 'invalidate' is not valid) -> Covers Normalize error
	// 3. Stream closes
	// 4. Loop exits -> Covers final return
	err := env.DB.Collection("users").Drop(ctx)
	if err != nil {
		t.Fatalf("Failed to drop collection: %v", err)
	}

	// Wait for processing
	time.Sleep(200 * time.Millisecond)

	// Stop puller
	p.Stop(ctx)
}

func TestPuller_Subscribe_ContextCancel(t *testing.T) {
	env := setupTestEnv(t)
	cfg := newTestConfig(t)
	p := New(cfg, nil)
	backendCfg := config.PullerBackendConfig{Collections: []string{"users"}}
	_ = p.AddBackend("backend1", env.Client, env.DBName, backendCfg)

	ctx, cancel := context.WithCancel(context.Background())

	// Subscribe to events
	ch := p.Subscribe(ctx, "cancel-test", "")

	// Start puller
	go func() {
		_ = p.Start(ctx)
	}()

	time.Sleep(100 * time.Millisecond)

	// Cancel context - should trigger ctx.Done() branch in Subscribe goroutine
	cancel()

	// Channel should close
	select {
	case _, ok := <-ch:
		if ok {
			// May get an event, try again
			select {
			case _, ok := <-ch:
				if ok {
					t.Log("Received extra event")
				}
			case <-time.After(500 * time.Millisecond):
				// OK
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for channel to close after context cancel")
	}

	p.Stop(context.Background())
}

func TestPuller_Subscribe_SubscriberClosed(t *testing.T) {
	env := setupTestEnv(t)
	cfg := newTestConfig(t)
	p := New(cfg, nil)
	backendCfg := config.PullerBackendConfig{Collections: []string{"users"}}
	_ = p.AddBackend("backend1", env.Client, env.DBName, backendCfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Subscribe to events
	ch := p.Subscribe(ctx, "close-test", "")

	// Start puller
	go func() {
		_ = p.Start(ctx)
	}()

	time.Sleep(100 * time.Millisecond)

	// Close all subscribers - should trigger sub.Done() branch
	p.subs.CloseAll()

	// Channel should close
	select {
	case _, ok := <-ch:
		if ok {
			// May get an event, try again
			select {
			case _, ok := <-ch:
				if ok {
					t.Log("Received extra event")
				}
			case <-time.After(500 * time.Millisecond):
				// OK
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for channel to close after subscriber closed")
	}

	p.Stop(context.Background())
}
