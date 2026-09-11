package grpc

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	pullerv1 "github.com/syntrixbase/syntrix/api/gen/puller/v1"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/puller/config"
	"github.com/syntrixbase/syntrix/internal/puller/core"
	"github.com/syntrixbase/syntrix/internal/puller/cursor"
	"github.com/syntrixbase/syntrix/internal/puller/events"
)

// mockEventSource implements the EventSource interface for testing
type mockEventSource struct {
	handler func(ctx context.Context, backendName string, event *events.StoreChangeEvent) error
}

func (m *mockEventSource) SetEventHandler(handler func(ctx context.Context, backendName string, event *events.StoreChangeEvent) error) {
	m.handler = handler
}

func (m *mockEventSource) EmitEvent(ctx context.Context, backendName string, event *events.StoreChangeEvent) error {
	if m.handler != nil {
		return m.handler(ctx, backendName, event)
	}
	return nil
}

func (m *mockEventSource) Replay(ctx context.Context, after map[string]string, coalesce bool) (events.Iterator, error) {
	return &mockIterator{}, nil
}

type mockIterator struct{}

func (m *mockIterator) Next() bool                      { return false }
func (m *mockIterator) Event() *events.StoreChangeEvent { return nil }
func (m *mockIterator) Err() error                      { return nil }
func (m *mockIterator) Close() error                    { return nil }

func TestNewServer(t *testing.T) {
	t.Parallel()
	cfg := config.GRPCConfig{
		MaxConnections: 100,
	}

	source := &mockEventSource{}

	// Test with nil logger
	server := NewServer(cfg, source, nil)
	if server == nil {
		t.Fatal("NewServer() returned nil")
	}
	if server.subs == nil {
		t.Error("subscriber manager should be initialized")
	}
	if server.eventChan == nil {
		t.Error("event channel should be initialized")
	}

	// Test with provided logger
	logger := slog.Default()
	server2 := NewServer(cfg, source, logger)
	if server2 == nil {
		t.Fatal("NewServer() with logger returned nil")
	}
}

func TestServer_SubscriberCount_WithAdd(t *testing.T) {
	t.Parallel()
	cfg := config.GRPCConfig{}
	source := &mockEventSource{}
	server := NewServer(cfg, source, nil)

	if server.SubscriberCount() != 0 {
		t.Errorf("SubscriberCount() = %d, want 0", server.SubscriberCount())
	}

	// Add a subscriber
	sub := testSubscriber(t, "consumer-1", nil, false, 100)
	server.subs.Add(sub)

	if server.SubscriberCount() != 1 {
		t.Errorf("SubscriberCount() = %d, want 1", server.SubscriberCount())
	}
}

func TestServer_ConvertEvent(t *testing.T) {
	t.Parallel()
	cfg := config.GRPCConfig{}
	source := &mockEventSource{}
	server := NewServer(cfg, source, nil)

	tests := []struct {
		name    string
		backend string
		event   *events.StoreChangeEvent
		wantErr bool
	}{
		{
			name:    "basic event",
			backend: "backend-1",
			event: &events.StoreChangeEvent{
				EventID:  "evt-123",
				Database: "database-1",
				MgoColl:  "users",
				MgoDocID: "doc-1",
				OpType:   events.StoreOperationInsert,
				ClusterTime: events.ClusterTime{
					T: 100,
					I: 1,
				},
				Timestamp: 1234567890,
			},
			wantErr: false,
		},
		{
			name:    "event with full document",
			backend: "backend-2",
			event: &events.StoreChangeEvent{
				EventID:      "evt-456",
				OpType:       events.StoreOperationUpdate,
				FullDocument: &storage.StoredDoc{Id: storage.CalculateDatabase("database-1", "users/doc-1"), Database: "database-1", Collection: "users", Fullpath: "users/doc-1", Data: map[string]any{"name": "test", "value": 123}},
			},
			wantErr: false,
		},
		{
			name:    "event with update description",
			backend: "backend-3",
			event: &events.StoreChangeEvent{
				EventID: "evt-789",
				OpType:  events.StoreOperationUpdate,
				UpdateDesc: &events.UpdateDescription{
					UpdatedFields: map[string]any{"name": "new"},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := server.convertEvent(tt.backend, tt.event)
			if (err != nil) != tt.wantErr {
				t.Errorf("convertEvent() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil {
				return
			}

			if result.EventId != tt.event.EventID {
				t.Errorf("Id = %v, want %v", result.EventId, tt.event.EventID)
			}
			if result.Database != tt.event.Database {
				t.Errorf("Database = %v, want %v", result.Database, tt.event.Database)
			}
			if result.MgoColl != tt.event.MgoColl {
				t.Errorf("Collection = %v, want %v", result.MgoColl, tt.event.MgoColl)
			}
			if result.MgoDocId != tt.event.MgoDocID {
				t.Errorf("DocumentId = %v, want %v", result.MgoDocId, tt.event.MgoDocID)
			}
			if result.OpType != string(tt.event.OpType) {
				t.Errorf("OperationType = %v, want %v", result.OpType, string(tt.event.OpType))
			}
			if result.Timestamp != tt.event.Timestamp {
				t.Errorf("Timestamp = %v, want %v", result.Timestamp, tt.event.Timestamp)
			}
			if result.ClusterTime.T != tt.event.ClusterTime.T {
				t.Errorf("ClusterTime.T = %v, want %v", result.ClusterTime.T, tt.event.ClusterTime.T)
			}
			if result.ClusterTime.I != tt.event.ClusterTime.I {
				t.Errorf("ClusterTime.I = %v, want %v", result.ClusterTime.I, tt.event.ClusterTime.I)
			}
		})
	}
}

func TestServer_Shutdown_NotInitialized_Noop(t *testing.T) {
	t.Parallel()
	cfg := config.GRPCConfig{}
	source := &mockEventSource{}
	server := NewServer(cfg, source, nil)

	// Shutdown before Init should be safe (noop)
	server.Shutdown()
}

func TestServer_Shutdown_Initialized(t *testing.T) {
	t.Parallel()
	cfg := config.GRPCConfig{}
	source := &mockEventSource{}
	server := NewServer(cfg, source, nil)

	// Init the server
	server.Init()

	// Add some subscribers
	sub1 := testSubscriber(t, "consumer-1", nil, false, 100)
	sub2 := testSubscriber(t, "consumer-2", nil, false, 100)
	server.subs.Add(sub1)
	server.subs.Add(sub2)

	// Shutdown
	server.Shutdown()

	// Verify subscribers were closed
	if server.subs.Count() != 0 {
		t.Errorf("subscriber count = %d, want 0", server.subs.Count())
	}
}

func TestProgressMarker_SetPosition_NilPositions(t *testing.T) {
	t.Parallel()
	pm := &cursor.ProgressMarker{Positions: nil}
	pm.SetPosition("backend-1", "evt-123")

	if pm.Positions == nil {
		t.Error("Positions should be initialized")
	}
	if pm.Positions["backend-1"] != "evt-123" {
		t.Error("Position should be set")
	}
}

func TestProgressMarker_GetPosition_NilPositions(t *testing.T) {
	t.Parallel()
	pm := &cursor.ProgressMarker{Positions: nil}
	pos := pm.GetPosition("backend-1")

	if pos != "" {
		t.Errorf("GetPosition() = %q, want empty string", pos)
	}
}

func TestProgressMarker_Clone_Nil(t *testing.T) {
	t.Parallel()
	var pm *cursor.ProgressMarker
	clone := pm.Clone()

	if clone == nil {
		t.Error("Clone() of nil should return empty marker")
	}
	if clone.Positions == nil {
		t.Error("Cloned marker should have initialized Positions")
	}
}

func TestServer_SendHeartbeat(t *testing.T) {
	t.Parallel()
	cfg := config.GRPCConfig{
		HeartbeatInterval: 100 * time.Millisecond,
	}
	source := &mockEventSource{}
	server := NewServer(cfg, source, nil)

	// Create a mock stream
	sentEvents := make([]*mockSentEvent, 0)
	mockStream := &mockSubscribeStream{
		sentEvents: &sentEvents,
	}

	// Create a subscriber with some progress
	initialProgress := &cursor.ProgressMarker{
		Positions: map[string]string{"backend-1": "100-1-abc"},
	}
	sub := testSubscriber(t, "test-consumer", initialProgress, false, 100)

	// Send heartbeat
	err := server.sendHeartbeat(mockStream, sub)
	if err != nil {
		t.Fatalf("sendHeartbeat() error = %v", err)
	}

	// Verify heartbeat was sent
	if len(sentEvents) != 1 {
		t.Fatalf("Expected 1 sent event, got %d", len(sentEvents))
	}

	// Verify it's a heartbeat (nil ChangeEvent)
	if sentEvents[0].event.ChangeEvent != nil {
		t.Error("Heartbeat should have nil ChangeEvent")
	}

	// Progress should be present since we have positions
	if sentEvents[0].event.Progress == "" {
		t.Error("Heartbeat should have Progress marker when subscriber has positions")
	}
}

func TestServer_SendHeartbeat_EmptyProgress(t *testing.T) {
	t.Parallel()
	cfg := config.GRPCConfig{}
	source := &mockEventSource{}
	server := NewServer(cfg, source, nil)

	// Create a mock stream
	sentEvents := make([]*mockSentEvent, 0)
	mockStream := &mockSubscribeStream{
		sentEvents: &sentEvents,
	}

	// Create a subscriber with no progress (fresh subscriber)
	sub := testSubscriber(t, "test-consumer", nil, false, 100)

	// Send heartbeat
	err := server.sendHeartbeat(mockStream, sub)
	if err != nil {
		t.Fatalf("sendHeartbeat() error = %v", err)
	}

	// Verify heartbeat was sent
	if len(sentEvents) != 1 {
		t.Fatalf("Expected 1 sent event, got %d", len(sentEvents))
	}

	// Verify it's a heartbeat (nil ChangeEvent)
	if sentEvents[0].event.ChangeEvent != nil {
		t.Error("Heartbeat should have nil ChangeEvent")
	}
	// Empty progress is valid for fresh subscribers
}

func TestServer_HeartbeatIntervalDefault(t *testing.T) {
	t.Parallel()
	cfg := config.GRPCConfig{
		HeartbeatInterval: 0, // Not set, should default to 30s
	}
	source := &mockEventSource{}
	server := NewServer(cfg, source, nil)

	// Verify config is stored
	if server.cfg.HeartbeatInterval != 0 {
		t.Error("HeartbeatInterval should be 0 in config (default applied at runtime)")
	}
}

func TestServer_SendHeartbeat_Error(t *testing.T) {
	t.Parallel()
	cfg := config.GRPCConfig{}
	source := &mockEventSource{}
	server := NewServer(cfg, source, slog.Default())

	sub := testSubscriber(t, "test-consumer", nil, false, 100)

	// Use a stream that returns an error on Send
	mockStream := &mockErrorStream{
		err: errors.New("send failed"),
	}

	err := server.sendHeartbeat(mockStream, sub)
	if err == nil {
		t.Fatal("sendHeartbeat() should return error when stream.Send fails")
	}
	if err.Error() != "send failed" {
		t.Errorf("sendHeartbeat() error = %v, want 'send failed'", err)
	}
}

type mockErrorStream struct {
	pullerv1.PullerService_SubscribeServer
	err error
}

func (m *mockErrorStream) Send(evt *pullerv1.PullerEvent) error {
	return m.err
}

func (m *mockErrorStream) Context() context.Context {
	return context.Background()
}

type mockSentEvent struct {
	event *pullerv1.PullerEvent
}

type mockSubscribeStream struct {
	pullerv1.PullerService_SubscribeServer
	sentEvents *[]*mockSentEvent
	ctx        context.Context
}

func (m *mockSubscribeStream) Send(evt *pullerv1.PullerEvent) error {
	*m.sentEvents = append(*m.sentEvents, &mockSentEvent{event: evt})
	return nil
}

func (m *mockSubscribeStream) Context() context.Context {
	if m.ctx != nil {
		return m.ctx
	}
	return context.Background()
}

func testSubscriber(t *testing.T, id string, after *cursor.ProgressMarker, coalesce bool, size int) *core.Subscriber {
	t.Helper()
	sub, err := core.NewSubscriber(id, after, coalesce, size)
	if err != nil {
		t.Fatal(err)
	}
	return sub
}
