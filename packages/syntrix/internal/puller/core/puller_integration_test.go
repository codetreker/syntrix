package core

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/puller/config"
	"github.com/codetreker/syntrix/internal/puller/events"
	"github.com/codetreker/syntrix/internal/puller/flowcontrol"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func TestPuller_Recovery_Restart(t *testing.T) {
	// Setup
	tmpDir, err := os.MkdirTemp("", "puller-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := config.Config{
		Buffer: config.BufferConfig{
			Path: tmpDir,
		},
		Cleaner: config.CleanerConfig{
			Interval: 1 * time.Hour,
		},
	}
	p := New(cfg, slog.Default())
	p.retryDelay = 1 * time.Millisecond

	// Mock watchFunc
	var callCount int32
	p.watchFunc = func(ctx context.Context, backend *Backend, logger *slog.Logger) error {
		count := atomic.AddInt32(&callCount, 1)
		logger.Info("Mock watchFunc called", "count", count)
		if count <= 2 {
			return errors.New("simulated error")
		}
		// Block until context cancelled to simulate running
		<-ctx.Done()
		return nil
	}

	// Add a backend with dummy mongo client
	client, err := mongo.Connect(context.Background(), options.Client().ApplyURI("mongodb://localhost:27017"))
	if err != nil {
		// Even if Connect fails (it shouldn't for just creating the struct), we might have issues.
		// But usually Connect returns a client.
		t.Logf("mongo.Connect error (ignored): %v", err)
	}

	err = p.AddBackend("test-backend", client, "test-db", config.PullerBackendConfig{})
	if err != nil {
		t.Fatalf("AddBackend failed: %v", err)
	}

	// Start Puller
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start in a goroutine
	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Wait for recovery loop
	// We expect:
	// 1. watchFunc called -> returns error
	// 2. Recovery handler logs error and decides to restart (or reconnect)
	// 3. watchFunc called again -> returns error
	// 4. ...
	// 5. watchFunc called again -> blocks (success)

	// Wait until callCount >= 3
	start := time.Now()
	for atomic.LoadInt32(&callCount) < 3 {
		if time.Since(start) > 2*time.Second {
			t.Fatalf("Timeout waiting for recovery. Call count: %d", atomic.LoadInt32(&callCount))
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Success
}

func TestPuller_Backpressure(t *testing.T) {
	// Setup
	cfg := config.Config{}
	p := New(cfg, slog.Default())
	p.backpressureSlowDownDelay = 10 * time.Millisecond
	p.backpressurePauseDelay = 50 * time.Millisecond

	// Configure backpressure with 1ms threshold
	bpOpts := flowcontrol.BackpressureOptions{
		SlowThreshold:     1 * time.Millisecond,
		CriticalThreshold: 10 * time.Millisecond,
	}
	bp := flowcontrol.NewBackpressureMonitor(bpOpts)

	backend := &Backend{
		name:         "test-backend",
		backpressure: bp,
	}

	// Test SlowDown
	// Set handler that sleeps for 5ms (trigger SlowDown)
	p.SetEventHandler(func(ctx context.Context, backendName string, event *events.StoreChangeEvent) error {
		time.Sleep(5 * time.Millisecond)
		return nil
	})

	// Measure time
	start := time.Now()
	p.invokeHandlerWithBackpressure(context.Background(), backend, &events.StoreChangeEvent{})
	duration := time.Since(start)

	// Expected: 5ms (handler) + 10ms (sleep) = ~15ms
	if duration < 10*time.Millisecond {
		t.Errorf("Expected slowdown sleep, got duration: %v", duration)
	}

	// Test Pause
	// Set handler that sleeps for 15ms (trigger Pause)
	p.SetEventHandler(func(ctx context.Context, backendName string, event *events.StoreChangeEvent) error {
		time.Sleep(15 * time.Millisecond)
		return nil
	})

	start = time.Now()
	p.invokeHandlerWithBackpressure(context.Background(), backend, &events.StoreChangeEvent{})
	duration = time.Since(start)

	// Expected: 15ms (handler) + 50ms (sleep) = ~65ms
	if duration < 50*time.Millisecond {
		t.Errorf("Expected pause sleep, got duration: %v", duration)
	}
}
