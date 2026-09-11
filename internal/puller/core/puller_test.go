package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/puller/buffer"
	"github.com/syntrixbase/syntrix/internal/puller/config"
	"github.com/syntrixbase/syntrix/internal/puller/cursor"
	"github.com/syntrixbase/syntrix/internal/puller/events"
	"github.com/syntrixbase/syntrix/internal/puller/normalizer"
	"github.com/syntrixbase/syntrix/internal/puller/recovery"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func init() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})))
}

func TestNew(t *testing.T) {
	t.Parallel()
	cfg := config.Config{}

	// Test with nil logger
	p := New(cfg, nil)
	if p == nil {
		t.Fatal("New() returned nil")
	}
	if p.backends == nil {
		t.Error("backends map should be initialized")
	}

	// Test with provided logger
	logger := slog.Default()
	p2 := New(cfg, logger)
	if p2 == nil {
		t.Fatal("New() with logger returned nil")
	}
}

func TestPuller_SetEventHandler(t *testing.T) {
	t.Parallel()
	p := New(config.Config{}, nil)

	called := false
	handler := func(ctx context.Context, backendName string, event *events.StoreChangeEvent) error {
		called = true
		return nil
	}

	p.SetEventHandler(handler)

	if p.eventHandler == nil {
		t.Error("eventHandler should be set")
	}

	// Verify handler can be called
	_ = p.eventHandler(context.Background(), "test", &events.StoreChangeEvent{})
	if !called {
		t.Error("eventHandler should have been called")
	}
}

func TestPuller_BackendNames_Empty(t *testing.T) {
	t.Parallel()
	p := New(config.Config{}, nil)

	names := p.BackendNames()
	if len(names) != 0 {
		t.Errorf("BackendNames() = %v, want empty", names)
	}
}

func TestPuller_Start_NoBackends(t *testing.T) {
	t.Parallel()
	p := New(config.Config{}, nil)

	err := p.Start(context.Background())
	if err == nil {
		t.Error("Start() should fail with no backends")
	}
	if err.Error() != "no backends configured" {
		t.Errorf("Start() error = %v, want 'no backends configured'", err)
	}
}

func TestPuller_Stop_NotStarted(t *testing.T) {
	t.Parallel()
	p := New(config.Config{}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Should not panic or error when stopping without starting
	err := p.Stop(ctx)
	if err != nil {
		t.Errorf("Stop() error = %v, want nil", err)
	}
}

func TestPuller_Subscribe(t *testing.T) {
	t.Parallel()
	p := New(config.Config{}, nil)

	ctx := context.Background()
	ch := p.Subscribe(ctx, "consumer-1", "")
	if ch == nil {
		t.Error("Subscribe() returned nil channel")
	}
}

func TestPuller_Subscribe_SendsEvents(t *testing.T) {
	t.Parallel()
	p := New(config.Config{}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := p.Subscribe(ctx, "consumer-1", "")

	// Send an event through the handler
	evt := &events.StoreChangeEvent{
		EventID: "test-event-1",
		MgoColl: "test",
	}

	p.subs.Broadcast(evt)

	// Verify event was received
	select {
	case received := <-ch:
		if received.Change.EventID != evt.EventID {
			t.Errorf("received event ID = %v, want %v", received.Change.EventID, evt.EventID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("timeout waiting for event")
	}
}

func TestPuller_Subscribe_SameLabel(t *testing.T) {
	for _, label := range []string{"same", ""} {
		for _, canceled := range []int{0, 1} {
			t.Run(fmt.Sprintf("label=%s/cancel=%d", label, canceled), func(t *testing.T) {
				p := New(config.Config{}, nil)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				t.Cleanup(cancel)
				var channels [2]<-chan *events.PullerEvent
				var cancels [2]context.CancelFunc
				for i := range channels {
					subCtx, subCancel := context.WithCancel(ctx)
					cancels[i] = subCancel
					channels[i] = p.Subscribe(subCtx, label, "")
				}
				t.Cleanup(func() {
					for _, stop := range cancels {
						stop()
					}
					for _, ch := range channels {
					drain:
						for {
							select {
							case _, ok := <-ch:
								if !ok {
									break drain
								}
							case <-ctx.Done():
								t.Error("subscription did not terminate")
								return
							}
						}
					}
				})
				require.Equal(t, 2, p.subs.Count())

				first := &events.StoreChangeEvent{
					Backend: "db1", EventID: "first", ClusterTime: events.ClusterTime{T: 1, I: 1},
				}
				p.subs.Broadcast(first)
				for _, ch := range channels {
					select {
					case got, ok := <-ch:
						require.True(t, ok, "same-label subscription closed")
						require.Same(t, first, got.Change)
					case <-ctx.Done():
						t.Fatal("subscription did not receive first event")
					}
				}

				cancels[canceled]()
				select {
				case _, ok := <-channels[canceled]:
					require.False(t, ok, "expected canceled subscription to close")
				case <-ctx.Done():
					t.Fatal("canceled subscription did not close")
				}
				require.Equal(t, 1, p.subs.Count(), "channel closure must follow subscription removal")

				second := &events.StoreChangeEvent{
					Backend: "db1", EventID: "second", ClusterTime: events.ClusterTime{T: 1, I: 2},
				}
				p.subs.Broadcast(second)
				select {
				case got, ok := <-channels[1-canceled]:
					require.True(t, ok, "other subscription was closed by cancellation")
					require.Same(t, second, got.Change)
				case <-ctx.Done():
					t.Fatal("remaining subscription did not receive second event")
				}
			})
		}
	}
}

func TestPuller_Subscribe_NonBlocking(t *testing.T) {
	t.Parallel()
	p := New(config.Config{}, nil)

	ctx := context.Background()
	_ = p.Subscribe(ctx, "consumer-1", "")

	// When channel is not full, handler should return nil (event sent)
	evt := &events.StoreChangeEvent{EventID: "test"}
	p.subs.Broadcast(evt)
}

func TestPuller_Subscribe_ChannelFull(t *testing.T) {
	t.Parallel()
	p := New(config.Config{}, nil)

	ctx := context.Background()
	ch := p.Subscribe(ctx, "consumer-1", "")

	// Fill the channel (buffer size is 1000)
	for i := 0; i < 1000; i++ {
		evt := &events.StoreChangeEvent{EventID: "test"}
		p.subs.Broadcast(evt)
	}

	// The channel should be full now, sending more should not block
	evt := &events.StoreChangeEvent{EventID: "overflow"}
	p.subs.Broadcast(evt)

	// Drain channel to avoid leaks
	for len(ch) > 0 {
		<-ch
	}
}

func TestPuller_SetDelayOverrides(t *testing.T) {
	p := New(config.Config{}, nil)

	p.SetRetryDelay(5 * time.Second)
	p.SetBackpressureSlowDownDelay(7 * time.Millisecond)
	p.SetBackpressurePauseDelay(9 * time.Millisecond)

	if p.retryDelay != 5*time.Second {
		t.Fatalf("retryDelay = %v, want %v", p.retryDelay, 5*time.Second)
	}
	if p.backpressureSlowDownDelay != 7*time.Millisecond {
		t.Fatalf("backpressureSlowDownDelay = %v, want %v", p.backpressureSlowDownDelay, 7*time.Millisecond)
	}
	if p.backpressurePauseDelay != 9*time.Millisecond {
		t.Fatalf("backpressurePauseDelay = %v, want %v", p.backpressurePauseDelay, 9*time.Millisecond)
	}
}

func TestPuller_runBackend_RecoveryActions(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Buffer.BatchInterval = 5 * time.Millisecond
	p := New(cfg, nil)

	client, err := mongo.NewClient(options.Client().ApplyURI("mongodb://localhost:27017"))
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}

	backendCfg := config.PullerBackendConfig{Name: "primary"}
	if err := p.AddBackend("primary", client, "testdb", backendCfg); err != nil {
		t.Fatalf("AddBackend error: %v", err)
	}

	backend := p.backends["primary"]
	backend.recoveryHandler = recovery.NewHandler(recovery.HandlerOptions{Checkpoint: backend.buffer, MaxConsecutiveErrors: 2})
	p.retryDelay = 1 * time.Millisecond

	errs := []error{
		nil,
		errors.New("connection reset by peer"),
		errors.New("unexpected failure"),
	}
	var calls atomic.Int32
	p.watchFunc = func(ctx context.Context, backend *Backend, logger *slog.Logger) error {
		idx := int(calls.Add(1) - 1)
		if idx >= len(errs) {
			return errors.New("stop")
		}
		return errs[idx]
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p.wg.Add(1)
	go p.runBackend(ctx, "primary", backend)

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runBackend did not return in time")
	}

	if calls.Load() < int32(len(errs)) {
		t.Fatalf("watchFunc called %d times, want at least %d", calls.Load(), len(errs))
	}
}

func TestPuller_watchChangeStream_WithResumeToken(t *testing.T) {
	env := setupTestEnv(t)
	cfg := newTestConfig(t)
	p := New(cfg, nil)
	defer p.Stop(context.Background())

	backendCfg := config.PullerBackendConfig{Name: "backend1"}
	if err := p.AddBackend("backend1", env.Client, env.DBName, backendCfg); err != nil {
		t.Fatalf("AddBackend error: %v", err)
	}
	backend := p.backends["backend1"]

	if err := backend.buffer.SaveCheckpoint(bson.Raw{0x05, 0x00, 0x00, 0x00, 0x00}); err != nil {
		t.Fatalf("SaveCheckpoint error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	if err := p.watchChangeStream(ctx, backend, p.logger); err == nil {
		t.Fatal("expected watchChangeStream to return error for invalid resume token")
	}
}

func TestPuller_watchChangeStream_FromBeginning(t *testing.T) {
	env := setupTestEnv(t)
	cfg := newTestConfig(t)
	cfg.Bootstrap.Mode = "from_beginning"
	p := New(cfg, nil)
	defer p.Stop(context.Background())

	backendCfg := config.PullerBackendConfig{Name: "backend1"}
	if err := p.AddBackend("backend1", env.Client, env.DBName, backendCfg); err != nil {
		t.Fatalf("AddBackend error: %v", err)
	}
	backend := p.backends["backend1"]

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	err := p.watchChangeStream(ctx, backend, p.logger)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Logf("watchChangeStream returned error: %v", err)
	}
}

func TestPuller_watchChangeStream_ProcessesEvent(t *testing.T) {
	env := setupTestEnv(t)
	cfg := newTestConfig(t)
	p := New(cfg, nil)

	backendCfg := config.PullerBackendConfig{Name: "backend1", Collections: []string{"users"}}
	require.NoError(t, p.AddBackend("backend1", env.Client, env.DBName, backendCfg))
	backend := p.backends["backend1"]

	var handled atomic.Int32
	p.SetEventHandler(func(ctx context.Context, backendName string, evt *events.StoreChangeEvent) error {
		handled.Add(1)
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- p.watchChangeStream(ctx, backend, p.logger) }()

	// Wait briefly for stream to open
	time.Sleep(150 * time.Millisecond)

	_, err := backend.db.Collection("users").InsertOne(ctx, storage.NewStoredDoc(env.DBName, "users", "test-user", map[string]any{"u": "v"}))
	require.NoError(t, err)

	assert.Eventually(t, func() bool {
		return handled.Load() > 0
	}, 3*time.Second, 20*time.Millisecond, "expected event handler to fire")

	cancel()

	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("watchChangeStream error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("watchChangeStream did not return")
	}

	require.NoError(t, backend.buffer.Close())
}

func TestPuller_watchChangeStream_LoadErrorDoesNotOpenStream(t *testing.T) {
	cfg := newTestConfig(t)
	p := New(cfg, nil)

	backendCfg := config.PullerBackendConfig{Name: "backend1"}
	client, err := mongo.NewClient(options.Client().ApplyURI(testMongoURI))
	require.NoError(t, err)
	require.NoError(t, p.AddBackend("backend1", client, "testdb", backendCfg))
	backend := p.backends["backend1"]
	token := bson.Raw{5, 0, 0, 0, 0}
	require.NoError(t, backend.buffer.SaveCheckpoint(token))
	require.NoError(t, backend.buffer.Close())
	var opened int
	p.openStream = func(context.Context, *mongo.Database, mongo.Pipeline, *options.ChangeStreamOptions) (changeStream, error) {
		opened++
		return &fakeChangeStream{}, nil
	}

	err = p.watchChangeStream(context.Background(), backend, p.logger)
	require.ErrorIs(t, err, errCaptureFailed)
	assert.ErrorContains(t, err, "failed to load checkpoint")
	assert.Zero(t, opened)
	reopened, err := buffer.NewForBackend(cfg.Buffer.Path, "backend1", nil)
	require.NoError(t, err)
	defer reopened.Close()
	actual, err := reopened.LoadCheckpoint()
	require.NoError(t, err)
	assert.Equal(t, token, actual)
}

func TestPuller_watchChangeStream_StreamErr(t *testing.T) {
	env := setupTestEnv(t)
	cfg := newTestConfig(t)
	p := New(cfg, nil)
	defer p.Stop(context.Background())

	backendCfg := config.PullerBackendConfig{Name: "backend1"}
	require.NoError(t, p.AddBackend("backend1", env.Client, env.DBName, backendCfg))
	backend := p.backends["backend1"]

	fakeStream := &fakeChangeStream{err: errors.New("stream error")}
	p.openStream = func(ctx context.Context, db *mongo.Database, pipeline mongo.Pipeline, opts *options.ChangeStreamOptions) (changeStream, error) {
		return fakeStream, nil
	}

	err := p.watchChangeStream(context.Background(), backend, p.logger)
	require.Error(t, err)
	assert.True(t, fakeStream.closed.Load(), "expected stream.Close to be called")
}

func TestPuller_runBackend_ProcessingFailureStopsBeforeNextEvent(t *testing.T) {
	decodeErr := errors.New("decode failure")
	for _, failure := range []string{"decode", "normalize", "write"} {
		t.Run(failure, func(t *testing.T) {
			cfg := newTestConfig(t)
			p := New(cfg, nil)
			p.retryDelay = time.Millisecond
			client, err := mongo.NewClient(options.Client().ApplyURI(testMongoURI))
			require.NoError(t, err)
			require.NoError(t, p.AddBackend("backend1", client, "testdb", config.PullerBackendConfig{Name: "backend1"}))
			backend := p.backends["backend1"]
			raw := normalizer.RawEvent{
				OperationType: "insert", DocumentKey: bson.M{"_id": "first"},
				ResumeToken: bson.Raw{5, 0, 0, 0, 0},
			}
			stream := &captureTestStream{raw: []normalizer.RawEvent{raw, raw}}
			switch failure {
			case "decode":
				stream.decodeErr = decodeErr
			case "normalize":
				stream.raw[0].OperationType = "unknown"
			case "write":
				stream.beforeDecode = func() { require.NoError(t, backend.buffer.Close()) }
			}
			var opened, published int
			p.openStream = func(context.Context, *mongo.Database, mongo.Pipeline, *options.ChangeStreamOptions) (changeStream, error) {
				opened++
				return stream, nil
			}
			p.SetEventHandler(func(context.Context, string, *events.StoreChangeEvent) error {
				published++
				return nil
			})
			var captureErr error
			p.watchFunc = func(ctx context.Context, backend *Backend, logger *slog.Logger) error {
				captureErr = p.watchChangeStream(ctx, backend, logger)
				return captureErr
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			p.wg.Add(1)
			p.runBackend(ctx, "backend1", backend)
			require.NoError(t, ctx.Err(), "backend must stop without waiting for cancellation")
			require.ErrorIs(t, captureErr, errCaptureFailed)
			assert.ErrorContains(t, captureErr, "failed to "+failure)
			if failure == "decode" {
				assert.ErrorIs(t, captureErr, decodeErr)
			}
			assert.Equal(t, 1, opened, "processing failures must not reopen from_now")
			assert.Equal(t, 1, stream.nextCalls, "the second event must remain unread")
			assert.Zero(t, published)
			assert.True(t, stream.closed.Load())
			reopened, err := buffer.NewForBackend(cfg.Buffer.Path, "backend1", nil)
			require.NoError(t, err)
			defer reopened.Close()
			token, err := reopened.LoadCheckpoint()
			require.NoError(t, err)
			assert.Nil(t, token)
		})
	}
}

func TestPuller_runBackend_NativeErrorsKeepDurableCheckpoint(t *testing.T) {
	for _, stage := range []string{"open", "read"} {
		for _, reconnect := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/reconnect=%t", stage, reconnect), func(t *testing.T) {
				cfg := newTestConfig(t)
				p := New(cfg, nil)
				p.retryDelay = time.Millisecond
				client, err := mongo.NewClient(options.Client().ApplyURI(testMongoURI))
				require.NoError(t, err)
				require.NoError(t, p.AddBackend("backend1", client, "testdb", config.PullerBackendConfig{Name: "backend1"}))
				backend := p.backends["backend1"]
				token := bson.Raw{5, 0, 0, 0, 0}
				require.NoError(t, backend.buffer.SaveCheckpoint(token))
				historyErr := &mongo.CommandError{Code: 286, Name: "ChangeStreamHistoryLost", Message: "source history is unavailable"}
				var opened int
				p.openStream = func(_ context.Context, _ *mongo.Database, _ mongo.Pipeline, opts *options.ChangeStreamOptions) (changeStream, error) {
					opened++
					assert.Equal(t, token, opts.ResumeAfter)
					assert.Nil(t, opts.StartAtOperationTime)
					var streamErr error = historyErr
					if reconnect && opened == 1 {
						streamErr = errors.New("connection reset by peer")
					}
					if stage == "open" {
						return nil, streamErr
					}
					return &fakeChangeStream{err: streamErr}, nil
				}
				var captureErr error
				p.watchFunc = func(ctx context.Context, backend *Backend, logger *slog.Logger) error {
					captureErr = p.watchChangeStream(ctx, backend, logger)
					return captureErr
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				p.wg.Add(1)
				p.runBackend(ctx, "backend1", backend)
				require.NoError(t, ctx.Err())
				assert.ErrorIs(t, captureErr, historyErr)
				wantOpened := 1
				if reconnect {
					wantOpened = 2
				}
				assert.Equal(t, wantOpened, opened)
				reopened, err := buffer.NewForBackend(cfg.Buffer.Path, "backend1", nil)
				require.NoError(t, err)
				defer reopened.Close()
				actual, err := reopened.LoadCheckpoint()
				require.NoError(t, err)
				assert.Equal(t, token, actual, "history failure must retain the native token")
			})
		}
	}
}

func TestPuller_watchChangeStream_CancellationPreventsAdmission(t *testing.T) {
	cfg := newTestConfig(t)
	p := New(cfg, nil)
	client, err := mongo.NewClient(options.Client().ApplyURI(testMongoURI))
	require.NoError(t, err)
	require.NoError(t, p.AddBackend("backend1", client, "testdb", config.PullerBackendConfig{Name: "backend1"}))
	backend := p.backends["backend1"]
	defer backend.buffer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	raw := normalizer.RawEvent{
		OperationType: "insert", DocumentKey: bson.M{"_id": "first"},
		ResumeToken: bson.Raw{5, 0, 0, 0, 0},
	}
	stream := &captureTestStream{
		raw: []normalizer.RawEvent{raw, raw}, beforeDecode: cancel,
	}
	p.openStream = func(context.Context, *mongo.Database, mongo.Pipeline, *options.ChangeStreamOptions) (changeStream, error) {
		return stream, nil
	}
	var published int
	p.SetEventHandler(func(context.Context, string, *events.StoreChangeEvent) error {
		published++
		return nil
	})

	err = p.watchChangeStream(ctx, backend, p.logger)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, err, errCaptureFailed)
	assert.Zero(t, published)
	assert.Equal(t, 1, stream.nextCalls, "the second event must remain unread")
	assert.True(t, stream.closed.Load())
	require.NoError(t, backend.buffer.Close())
	reopened, err := buffer.NewForBackend(cfg.Buffer.Path, "backend1", nil)
	require.NoError(t, err)
	defer reopened.Close()
	token, err := reopened.LoadCheckpoint()
	require.NoError(t, err)
	assert.Nil(t, token, "the canceled event must not be admitted or committed")
}

func TestPuller_watchChangeStream_PublishesBeforeBatchFlush(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Buffer.BatchSize = 100
	cfg.Buffer.BatchInterval = time.Hour
	p := New(cfg, nil)
	client, err := mongo.NewClient(options.Client().ApplyURI(testMongoURI))
	require.NoError(t, err)
	require.NoError(t, p.AddBackend("backend1", client, "testdb", config.PullerBackendConfig{Name: "backend1"}))
	backend := p.backends["backend1"]
	defer backend.buffer.Close()
	token := bson.Raw{5, 0, 0, 0, 0}
	stream := &captureTestStream{raw: []normalizer.RawEvent{{
		OperationType: "insert", DocumentKey: bson.M{"_id": "first"}, ResumeToken: token,
	}}}
	p.openStream = func(context.Context, *mongo.Database, mongo.Pipeline, *options.ChangeStreamOptions) (changeStream, error) {
		return stream, nil
	}
	var published atomic.Int32
	p.SetEventHandler(func(context.Context, string, *events.StoreChangeEvent) error {
		published.Add(1)
		return nil
	})
	done := make(chan error, 1)
	go func() { done <- p.watchChangeStream(context.Background(), backend, p.logger) }()
	select {
	case err := <-done:
		require.ErrorContains(t, err, "change stream closed")
	case <-time.After(time.Second):
		t.Fatal("live publication waited for a batch flush")
	}
	assert.Equal(t, int32(1), published.Load())
	actual, err := backend.buffer.LoadCheckpoint()
	require.NoError(t, err)
	assert.Nil(t, actual, "the event must still be pending when it is published")
	require.NoError(t, backend.buffer.Close())
	reopened, err := buffer.NewForBackend(cfg.Buffer.Path, "backend1", nil)
	require.NoError(t, err)
	defer reopened.Close()
	actual, err = reopened.LoadCheckpoint()
	require.NoError(t, err)
	assert.Equal(t, token, actual)
}

func TestBuildWatchPipeline_NoFilter(t *testing.T) {
	t.Parallel()
	p := New(config.Config{}, nil)

	cfg := config.PullerBackendConfig{}
	pipeline := p.buildWatchPipeline(cfg)

	if pipeline != nil {
		t.Error("buildWatchPipeline() should return nil for no filter")
	}
}

func TestBuildWatchPipeline_Collections(t *testing.T) {
	t.Parallel()
	p := New(config.Config{}, nil)

	cfg := config.PullerBackendConfig{
		Collections: []string{"users", "orders"},
	}
	pipeline := p.buildWatchPipeline(cfg)

	if pipeline == nil {
		t.Fatal("buildWatchPipeline() returned nil for include filter")
	}
	if len(pipeline) != 1 {
		t.Errorf("pipeline length = %d, want 1", len(pipeline))
	}
}

func TestPuller_AddBackend(t *testing.T) {
	env := setupTestEnv(t)
	cfg := newTestConfig(t)
	p := New(cfg, nil)
	defer p.Stop(context.Background())

	backendCfg := config.PullerBackendConfig{
		Collections: []string{"users"},
	}

	// Test AddBackend
	err := p.AddBackend("backend1", env.Client, env.DBName, backendCfg)
	if err != nil {
		t.Fatalf("AddBackend failed: %v", err)
	}

	if len(p.backends) != 1 {
		t.Errorf("Expected 1 backend, got %d", len(p.backends))
	}

	// Test duplicate backend
	err = p.AddBackend("backend1", env.Client, env.DBName, backendCfg)
	if err == nil {
		t.Error("Expected error for duplicate backend")
	}
}

func TestPuller_AddBackend_InvalidMaxSizeIsGraceful(t *testing.T) {
	env := setupTestEnv(t)
	cfg := newTestConfig(t)
	cfg.Buffer.MaxSize = "not-a-size"
	p := New(cfg, nil)
	defer p.Stop(context.Background())

	err := p.AddBackend("backend1", env.Client, env.DBName, config.PullerBackendConfig{Collections: []string{"users"}})
	require.NoError(t, err)
}

func TestPuller_StartStop(t *testing.T) {
	env := setupTestEnv(t)
	cfg := newTestConfig(t)
	p := New(cfg, nil)

	backendCfg := config.PullerBackendConfig{
		Collections: []string{"users"},
	}

	err := p.AddBackend("backend1", env.Client, env.DBName, backendCfg)
	if err != nil {
		t.Fatalf("AddBackend failed: %v", err)
	}

	// Start
	ctx := context.Background()
	err = p.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Wait a bit for goroutines to start
	time.Sleep(100 * time.Millisecond)

	// Stop
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = p.Stop(stopCtx)
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
}

func TestPuller_Stop_TimesOutWhenWorkersHang(t *testing.T) {
	p := New(newTestConfig(t), nil)
	buf, err := buffer.New(buffer.Options{Path: t.TempDir()})
	require.NoError(t, err)
	p.backends["backend1"] = &Backend{buffer: buf}
	defer p.Stop(context.Background())

	// Simulate a worker that never finishes.
	p.wg.Add(1)
	defer p.wg.Done()
	p.cancel = func() {}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err = p.Stop(ctx)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	_, err = buf.LoadCheckpoint()
	assert.NoError(t, err, "a live worker retains ownership of its buffer after a stop timeout")
}

func TestPuller_StopPreservesBufferCloseError(t *testing.T) {
	p := New(newTestConfig(t), nil)
	buf, err := buffer.New(buffer.Options{Path: t.TempDir()})
	require.NoError(t, err)
	p.backends["backend1"] = &Backend{buffer: buf}
	iter, err := buf.ScanFrom("")
	require.NoError(t, err)
	defer iter.Close()
	closeErr := buf.Close()
	require.ErrorContains(t, closeErr, "leaked iterators")
	assert.ErrorIs(t, p.Stop(context.Background()), closeErr)
	assert.ErrorIs(t, p.Stop(context.Background()), closeErr)
}

func TestPuller_Replay_IteratorErrorClosesExisting(t *testing.T) {
	env := setupTestEnv(t)
	cfg := newTestConfig(t)
	p := New(cfg, nil)
	defer p.Stop(context.Background())

	require.NoError(t, p.AddBackend("backend1", env.Client, env.DBName, config.PullerBackendConfig{Collections: []string{"users"}}))
	require.NoError(t, p.AddBackend("backend2", env.Client, env.DBName, config.PullerBackendConfig{Collections: []string{"users"}}))

	backend2 := p.backends["backend2"]
	require.NoError(t, backend2.buffer.Close())

	_, err := p.Replay(context.Background(), nil, false)
	require.Error(t, err)
}

type fakeChangeStream struct {
	err    error
	closed atomic.Bool
}

func (f *fakeChangeStream) TryNext(context.Context) bool { return false }
func (f *fakeChangeStream) ID() int64                    { return 0 }
func (f *fakeChangeStream) ResumeToken() bson.Raw        { return nil }

func (f *fakeChangeStream) Decode(any) error { return nil }

func (f *fakeChangeStream) Err() error { return f.err }

func (f *fakeChangeStream) Close(context.Context) error {
	f.closed.Store(true)
	return nil
}

type captureTestStream struct {
	fakeChangeStream
	raw          []normalizer.RawEvent
	nextCalls    int
	decodeErr    error
	beforeDecode func()
}

func (s *captureTestStream) TryNext(context.Context) bool {
	if s.nextCalls == len(s.raw) {
		return false
	}
	s.nextCalls++
	return true
}

func (s *captureTestStream) Decode(dst any) error {
	if s.beforeDecode != nil {
		s.beforeDecode()
	}
	if s.decodeErr != nil {
		return s.decodeErr
	}
	*dst.(*normalizer.RawEvent) = s.raw[s.nextCalls-1]
	return nil
}

func TestPuller_Subscribe_WithAfter(t *testing.T) {
	t.Parallel()
	p := New(config.Config{}, nil)

	pm := cursor.NewProgressMarker()
	pm.SetPosition("backend1", "pos1")
	after := pm.Encode()

	ctx := context.Background()
	ch := p.Subscribe(ctx, "consumer-1", after)
	require.NotNil(t, ch)
}

func TestPuller_Subscribe_WithInvalidAfter(t *testing.T) {
	t.Parallel()
	p := New(config.Config{}, nil)

	ctx := context.Background()
	// Should not fail, just ignore invalid token
	ch := p.Subscribe(ctx, "consumer-1", "invalid-token")
	require.NotNil(t, ch)
}
