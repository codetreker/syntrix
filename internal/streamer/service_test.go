package streamer

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pb "github.com/syntrixbase/syntrix/api/gen/streamer/v1"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/puller"
	"github.com/syntrixbase/syntrix/internal/puller/events"
	"github.com/syntrixbase/syntrix/pkg/model"
)

var testLogger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

func init() {
	slog.SetDefault(testLogger)
}

// testStoredDoc creates a StoredDoc with all required fields for testing.
// Note: StoredDoc.Id is MongoDB's _id (database:hash format), NOT the user document ID.
// The user document ID comes from Fullpath (collection/docID) and should also be in Data["id"].
func testStoredDoc(collection, docID, database string, data map[string]interface{}) *storage.StoredDoc {
	// Ensure data has the user document ID
	if data == nil {
		data = make(map[string]interface{})
	}
	data["id"] = docID

	return &storage.StoredDoc{
		Id:         "_id_" + docID, // MongoDB _id (not the user document ID)
		Database:   database,
		Collection: collection,
		Fullpath:   collection + "/" + docID, // User document ID is extracted from here
		Data:       data,
	}
}

// testSyntrixEvent creates a SyntrixChangeEvent for testing.
// This is the business-layer event that ProcessEvent expects.
func testSyntrixEvent(eventID, database, collection, docID string, eventType events.EventType, data map[string]interface{}) events.SyntrixChangeEvent {
	return events.SyntrixChangeEvent{
		Id:        eventID,
		Database:  database,
		Type:      eventType,
		Document:  testStoredDoc(collection, docID, database, data),
		Timestamp: time.Now().UnixMilli(),
	}
}

func TestNewService(t *testing.T) {
	s, err := NewService(ServerConfig{}, nil)
	require.NoError(t, err)
	require.NotNil(t, s)
}

func getInternalService(s StreamerServer) *streamerService {
	return s.(*streamerService)
}

func TestService_Stream(t *testing.T) {
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)

	stream, err := s.Stream(context.Background())
	require.NoError(t, err)
	require.NotNil(t, stream)

	err = stream.Close()
	require.NoError(t, err)
}

func TestService_Stream_Subscribe(t *testing.T) {
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)

	stream, err := s.Stream(context.Background())
	require.NoError(t, err)
	defer stream.Close()

	subID, err := stream.Subscribe(context.Background(), "database1", "users", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, subID.ID)
}

func TestService_Stream_SubscribeWithFilters(t *testing.T) {
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)

	stream, err := s.Stream(context.Background())
	require.NoError(t, err)
	defer stream.Close()

	subID, err := stream.Subscribe(context.Background(), "database1", "users", []model.Filter{
		{Field: "status", Op: model.OpEq, Value: "active"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, subID.ID)
}

func TestService_ProcessEvent_NoSubscriptions(t *testing.T) {
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)
	internal := getInternalService(s)

	err = internal.ProcessEvent(testSyntrixEvent("evt1", "database1", "users", "doc1", events.EventCreate, nil))
	require.NoError(t, err)
}

func TestService_ProcessEvent_WithSubscription(t *testing.T) {
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)
	internal := getInternalService(s)

	stream, err := s.Stream(context.Background())
	require.NoError(t, err)
	defer stream.Close()

	subID, err := stream.Subscribe(context.Background(), "database1", "users", nil)
	require.NoError(t, err)

	err = internal.ProcessEvent(testSyntrixEvent("evt1", "database1", "users", "doc1", events.EventCreate, map[string]interface{}{"name": "Alice"}))
	require.NoError(t, err)

	delivery, err := stream.Recv()
	require.NoError(t, err)
	require.NotNil(t, delivery)
	assert.Equal(t, []string{subID.ID}, delivery.SubscriptionIDs)
	assert.Equal(t, "evt1", delivery.Event.EventID)
	assert.Equal(t, "database1", delivery.Event.Database)
	assert.Equal(t, "users", delivery.Event.Collection)
	assert.Equal(t, "doc1", delivery.Event.DocumentID)
	assert.Equal(t, OperationInsert, delivery.Event.Operation)
}

func TestService_ProcessEvent_MultipleStreams(t *testing.T) {
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)
	internal := getInternalService(s)

	stream1, _ := s.Stream(context.Background())
	stream2, _ := s.Stream(context.Background())
	defer stream1.Close()
	defer stream2.Close()

	subID1, _ := stream1.Subscribe(context.Background(), "database1", "users", nil)
	subID2, _ := stream2.Subscribe(context.Background(), "database1", "users", nil)

	internal.ProcessEvent(testSyntrixEvent("evt1", "database1", "users", "doc1", events.EventCreate, map[string]interface{}{"name": "Alice"}))

	msg1, err := stream1.Recv()
	require.NoError(t, err)
	assert.Equal(t, []string{subID1.ID}, msg1.SubscriptionIDs)

	msg2, err := stream2.Recv()
	require.NoError(t, err)
	assert.Equal(t, []string{subID2.ID}, msg2.SubscriptionIDs)
}

func TestService_Stop(t *testing.T) {
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)

	stream, _ := s.Stream(context.Background())
	require.NotNil(t, stream)
	status := stream.Status()
	require.False(t, status.Terminal)

	err = s.Stop(context.Background())
	require.NoError(t, err)
	select {
	case <-status.Changed:
	case <-time.After(time.Second):
		t.Fatal("service stop did not invalidate stream ownership")
	}
	require.True(t, stream.Status().Terminal)
	_, err = s.Stream(context.Background())
	require.ErrorIs(t, err, context.Canceled)
}

func TestService_Stream_CanceledAdmission(t *testing.T) {
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stream, err := s.Stream(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, stream)
	require.Empty(t, getInternalService(s).streams)
}

func TestService_SubscribeWithManager(t *testing.T) {
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)
	internal := getInternalService(s)

	stream, _ := s.Stream(context.Background())
	defer stream.Close()

	_, err = stream.Subscribe(context.Background(), "database1", "users", nil)
	require.NoError(t, err)

	resp, err := internal.manager.Subscribe("test-gw", &pb.SubscribeRequest{
		SubscriptionId: "sub2",
		Database:       "database1",
		Collection:     "users",
	})
	require.NoError(t, err)
	require.True(t, resp.Success)
}

func TestService_Start(t *testing.T) {
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)

	err = s.Start(context.Background())
	require.NoError(t, err)
}

func TestService_Stream_Unsubscribe(t *testing.T) {
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)

	stream, err := s.Stream(context.Background())
	require.NoError(t, err)
	defer stream.Close()

	subID, err := stream.Subscribe(context.Background(), "database1", "users", nil)
	require.NoError(t, err)

	err = stream.Unsubscribe(subID.ID)
	require.NoError(t, err)
}

func TestService_Stream_ContextCancel(t *testing.T) {
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := s.Stream(ctx)
	require.NoError(t, err)

	cancel()
	time.Sleep(10 * time.Millisecond)

	_, err = stream.Subscribe(context.Background(), "database1", "users", nil)
	require.Error(t, err)
}

func TestService_Recv_ContextCanceled(t *testing.T) {
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := s.Stream(ctx)
	require.NoError(t, err)

	cancel()
	time.Sleep(10 * time.Millisecond)
	_, err = stream.Recv()
	require.Error(t, err)
}

func TestService_ProcessEventJSON(t *testing.T) {
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)
	internal := getInternalService(s)

	stream, err := s.Stream(context.Background())
	require.NoError(t, err)
	defer stream.Close()

	subID, err := stream.Subscribe(context.Background(), "database1", "users", nil)
	require.NoError(t, err)

	// ProcessEventJSON expects PullerEvent format (wrapper with change_event and progress)
	jsonData := []byte(`{
		"change_event": {
			"eventId": "evt1",
			"database": "database1",
			"mgoColl": "users",
			"mgoDocId": "doc1",
			"opType": "insert",
			"fullDoc": {
				"id": "_id_doc1",
				"database": "database1",
				"collection": "users",
				"fullpath": "users/doc1",
				"data": {"id": "doc1", "name": "Alice"}
			}
		},
		"progress": "progress-1"
	}`)

	err = internal.ProcessEventJSON(jsonData)
	require.NoError(t, err)

	delivery, err := stream.Recv()
	require.NoError(t, err)
	require.NotNil(t, delivery)
	assert.Contains(t, delivery.SubscriptionIDs, subID.ID)
}

func TestService_ProcessEventJSON_InvalidJSON(t *testing.T) {
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)
	internal := getInternalService(s)

	err = internal.ProcessEventJSON([]byte("invalid json"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to unmarshal event")
}

func TestService_ProcessEventJSON_TransformError(t *testing.T) {
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)
	internal := getInternalService(s)

	// Event with unknown operation type will cause Transform to return ErrUnknownOpType
	jsonData := []byte(`{
		"change_event": {
			"eventId": "evt1",
			"database": "database1",
			"mgoColl": "users",
			"mgoDocId": "doc1",
			"opType": "unknown_operation"
		},
		"progress": "progress-1"
	}`)

	err = internal.ProcessEventJSON(jsonData)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to transform event")
}

func TestService_ProcessEventJSON_DeleteIgnored(t *testing.T) {
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)
	internal := getInternalService(s)

	// Hard delete operation should be silently ignored
	jsonData := []byte(`{
		"change_event": {
			"eventId": "evt1",
			"database": "database1",
			"mgoColl": "users",
			"mgoDocId": "doc1",
			"opType": "delete"
		},
		"progress": "progress-1"
	}`)

	err = internal.ProcessEventJSON(jsonData)
	require.NoError(t, err) // Should not error, just silently ignore
}

// TestService_Stream_ClosedOperations tests that operations on a closed stream return errors.
func TestService_Stream_ClosedOperations(t *testing.T) {
	t.Parallel()

	t.Run("Subscribe", func(t *testing.T) {
		s, err := NewService(ServerConfig{}, slog.Default())
		require.NoError(t, err)

		stream, err := s.Stream(context.Background())
		require.NoError(t, err)

		err = stream.Close()
		require.NoError(t, err)

		_, err = stream.Subscribe(context.Background(), "database1", "users", nil)
		require.Error(t, err)
	})

	t.Run("Unsubscribe", func(t *testing.T) {
		s, err := NewService(ServerConfig{}, slog.Default())
		require.NoError(t, err)

		stream, err := s.Stream(context.Background())
		require.NoError(t, err)

		stream.Close()

		err = stream.Unsubscribe("sub1")
		require.Error(t, err)
	})

	t.Run("Recv", func(t *testing.T) {
		s, err := NewService(ServerConfig{}, slog.Default())
		require.NoError(t, err)

		stream, err := s.Stream(context.Background())
		require.NoError(t, err)

		stream.Close()
		time.Sleep(10 * time.Millisecond)

		_, err = stream.Recv()
		require.Error(t, err)
	})
}

func TestService_ProcessEvent_Timeout(t *testing.T) {
	s, err := NewService(ServerConfig{SendTimeout: 10 * time.Millisecond}, slog.Default())
	require.NoError(t, err)
	internal := getInternalService(s)

	stream, err := s.Stream(context.Background())
	require.NoError(t, err)
	defer stream.Close()

	_, err = stream.Subscribe(context.Background(), "database1", "users", nil)
	require.NoError(t, err)

	// Fill up the outgoing channel to cause timeout
	for i := 0; i < 1100; i++ {
		internal.ProcessEvent(testSyntrixEvent("evt1", "database1", "users", "doc1", events.EventCreate, map[string]interface{}{"name": "Alice"}))
	}
	// Just ensure no panic, timeout behavior is timing-dependent
}

func TestService_Unsubscribe_NotFound(t *testing.T) {
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)

	stream, err := s.Stream(context.Background())
	require.NoError(t, err)
	defer stream.Close()

	// Unsubscribe a non-existent subscription - now returns error (direct call to manager)
	err = stream.Unsubscribe("nonexistent-sub-id")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "subscription not found")
}

func TestService_Subscribe_Error(t *testing.T) {
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)

	stream, err := s.Stream(context.Background())
	require.NoError(t, err)
	defer stream.Close()

	// Subscribe without filters - should succeed
	subID, err := stream.Subscribe(context.Background(), "database1", "users", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, subID.ID)
}

func TestService_MultipleSubscriptions(t *testing.T) {
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)
	internal := getInternalService(s)

	stream, err := s.Stream(context.Background())
	require.NoError(t, err)
	defer stream.Close()

	// Create multiple subscriptions
	subID1, err := stream.Subscribe(context.Background(), "database1", "users", nil)
	require.NoError(t, err)

	subID2, err := stream.Subscribe(context.Background(), "database1", "orders", nil)
	require.NoError(t, err)

	assert.NotEqual(t, subID1, subID2)

	// Send an event that matches the first subscription
	err = internal.ProcessEvent(testSyntrixEvent("evt1", "database1", "users", "doc1", events.EventCreate, map[string]interface{}{"name": "Alice"}))
	require.NoError(t, err)

	delivery, err := stream.Recv()
	require.NoError(t, err)
	assert.Contains(t, delivery.SubscriptionIDs, subID1.ID)
}

func TestService_RecvNoMatch(t *testing.T) {
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)
	internal := getInternalService(s)

	stream, err := s.Stream(context.Background())
	require.NoError(t, err)
	defer stream.Close()

	// Subscribe to a different collection - should not match
	_, err = stream.Subscribe(context.Background(), "database1", "orders", nil)
	require.NoError(t, err)

	// Send event to users collection (not orders)
	err = internal.ProcessEvent(testSyntrixEvent("evt1", "database1", "users", "doc1", events.EventCreate, map[string]interface{}{"name": "Alice"}))
	require.NoError(t, err)

	// Should timeout since no match
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		stream.Recv()
		close(done)
	}()

	select {
	case <-ctx.Done():
		// Expected - timeout means no match
	case <-done:
		t.Error("Unexpected delivery")
	}
}

func TestService_Close_CancelsContext(t *testing.T) {
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)

	stream, err := s.Stream(context.Background())
	require.NoError(t, err)

	// Subscribe first
	_, err = stream.Subscribe(context.Background(), "database1", "users", nil)
	require.NoError(t, err)

	// Close should work
	err = stream.Close()
	require.NoError(t, err)

	// Subsequent operations should fail
	_, err = stream.Subscribe(context.Background(), "database2", "orders", nil)
	require.Error(t, err)
}

func TestService_ProcessEvent_DeleteOperation(t *testing.T) {
	t.Parallel()
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)
	internal := getInternalService(s)

	stream, err := s.Stream(context.Background())
	require.NoError(t, err)
	defer stream.Close()

	_, err = stream.Subscribe(context.Background(), "database1", "users", nil)
	require.NoError(t, err)

	// Delete event: SyntrixChangeEvent with EventDelete type
	// Note: Delete events without document are skipped by ProcessEvent (nil check)
	// So we test with a document that has Deleted=true
	deleteEvent := testSyntrixEvent("evt1", "database1", "users", "doc1", events.EventDelete, nil)
	deleteEvent.Document.Deleted = true
	err = internal.ProcessEvent(deleteEvent)
	require.NoError(t, err)

	// Event should be delivered
	delivery, err := stream.Recv()
	require.NoError(t, err)
	assert.Equal(t, OperationDelete, delivery.Event.Operation)
}

// --- Puller Integration Tests ---

// testPullerService is a mock puller.Service for testing Start() and consumePullerEvents.
type testPullerService struct {
	events      chan *puller.Event
	subscribeOK bool
	err         error
}

func newTestPullerService() *testPullerService {
	return &testPullerService{
		events:      make(chan *puller.Event, 10),
		subscribeOK: true,
	}
}

func (m *testPullerService) Subscribe(ctx context.Context, consumerID string, after string) <-chan *puller.Event {
	if m.err != nil {
		return nil
	}
	return m.events
}

func TestStart_WithMockPuller(t *testing.T) {
	t.Parallel()
	mockPuller := newTestPullerService()

	s, err := NewService(ServerConfig{}, slog.Default(), WithPullerClient(mockPuller))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = s.Start(ctx)
	require.NoError(t, err)

	// Give a moment for the goroutine to start
	time.Sleep(10 * time.Millisecond)
}

// Note: TestStart_SubscribeError was removed because with the new auto-reconnect design,
// Subscribe no longer returns an error. It returns a channel and handles reconnection internally.

func TestStart_StandaloneMode(t *testing.T) {
	t.Parallel()
	// No puller configured - standalone mode
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)

	ctx := context.Background()
	err = s.Start(ctx)
	require.NoError(t, err)
}

func TestStart_WithPullerAddr(t *testing.T) {
	t.Parallel()
	// Test that Start creates a puller client from address.
	// gRPC connection is lazy, so NewClient won't fail even if server doesn't exist.
	s, err := NewService(ServerConfig{
		PullerAddr: "localhost:50051", // Server may not exist, but gRPC is lazy
	}, slog.Default())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start should succeed because gRPC connection is lazy
	err = s.Start(ctx)
	require.NoError(t, err)

	// Give time for background goroutine to start
	time.Sleep(10 * time.Millisecond)
}

func TestConsumePullerEvents_ProcessEvent(t *testing.T) {
	t.Parallel()
	mockPuller := newTestPullerService()

	s, err := NewService(ServerConfig{}, slog.Default(), WithPullerClient(mockPuller))
	require.NoError(t, err)

	// Start the service
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = s.Start(ctx)
	require.NoError(t, err)

	// Send an event through the mock puller
	testEvent := &puller.Event{
		Change: &events.StoreChangeEvent{
			EventID:  "evt-1",
			MgoColl:  "test-topic",
			MgoDocID: "doc-1",
			OpType:   events.StoreOperationInsert,
		},
		Progress: "progress-1",
	}
	mockPuller.events <- testEvent

	// Give time for the event to be processed
	time.Sleep(30 * time.Millisecond)
}

func TestConsumePullerEvents_ContextDone(t *testing.T) {
	t.Parallel()
	mockPuller := newTestPullerService()

	s, err := NewService(ServerConfig{}, slog.Default(), WithPullerClient(mockPuller))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())

	err = s.Start(ctx)
	require.NoError(t, err)

	// Cancel context to trigger consumePullerEvents exit
	cancel()
	time.Sleep(20 * time.Millisecond)
}

func TestConsumePullerEvents_ChannelClose(t *testing.T) {
	t.Parallel()
	mockPuller := newTestPullerService()

	s, err := NewService(ServerConfig{}, slog.Default(), WithPullerClient(mockPuller))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = s.Start(ctx)
	require.NoError(t, err)

	// Close the channel to trigger the "channel closed" case
	close(mockPuller.events)
	time.Sleep(20 * time.Millisecond)
}

func TestConsumePullerEvents_NilChange(t *testing.T) {
	t.Parallel()
	mockPuller := newTestPullerService()

	s, err := NewService(ServerConfig{}, slog.Default(), WithPullerClient(mockPuller))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = s.Start(ctx)
	require.NoError(t, err)

	// Send an event with nil Change (should be skipped)
	mockPuller.events <- &puller.Event{
		Change:   nil,
		Progress: "progress-2",
	}

	time.Sleep(20 * time.Millisecond)
}

func TestConsumePullerEvents_ServiceStopped(t *testing.T) {
	t.Parallel()
	mockPuller := newTestPullerService()

	s, err := NewService(ServerConfig{}, slog.Default(), WithPullerClient(mockPuller))
	require.NoError(t, err)

	ctx := context.Background()
	err = s.Start(ctx)
	require.NoError(t, err)

	// Stop the service to trigger the s.ctx.Done() case
	s.Stop(context.Background())
	time.Sleep(20 * time.Millisecond)
}

// Note: TestStart_WithPullerAddr_Error and TestStart_WithValidPullerAddr_NoServer were removed
// because with the new auto-reconnect design, Subscribe no longer returns an immediate error.
// The client will keep trying to reconnect in the background.

func TestService_Subscribe_FilterCompileError(t *testing.T) {
	t.Parallel()
	// Test subscribe with invalid filter that causes compilation failure
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)

	stream, err := s.Stream(context.Background())
	require.NoError(t, err)
	defer stream.Close()

	// Subscribe with an invalid operator that will fail filter compilation
	_, err = stream.Subscribe(context.Background(), "database1", "users", []model.Filter{
		{Field: "status", Op: model.FilterOp("invalid_operator"), Value: "active"},
	})

	// Should fail because the operator is invalid
	require.Error(t, err)
	assert.Contains(t, err.Error(), "subscribe failed")
}

func TestService_Subscribe_ManagerReturnsError(t *testing.T) {
	t.Parallel()
	// Test subscribe error handling when manager returns non-nil error
	// This tests the err != nil branch in subscribe()
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)
	internal := getInternalService(s)

	// Test by calling subscribe directly with an invalid gateway (empty gatewayID)
	// The manager should handle this gracefully
	_, err = internal.subscribe("test-gateway", "database1", "users", []model.Filter{
		{Field: "status", Op: model.FilterOp("invalid_op"), Value: "test"},
	})

	require.Error(t, err)
}

func TestService_ConsumePullerEvents_ServiceContextDone(t *testing.T) {
	t.Parallel()
	// Test that consumePullerEvents stops when service context is done
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)
	internal := getInternalService(s)

	// Create a mock puller event channel
	eventChan := make(chan *puller.Event)

	// Start consumePullerEvents in a goroutine
	consumeCtx := context.Background()
	done := make(chan struct{})
	go func() {
		internal.consumePullerEvents(consumeCtx, eventChan)
		close(done)
	}()

	// Wait briefly then stop the service (which cancels internal context)
	time.Sleep(50 * time.Millisecond)
	internal.cancel()

	// consumePullerEvents should exit via s.ctx.Done()
	select {
	case <-done:
		// Good
	case <-time.After(2 * time.Second):
		t.Fatal("consumePullerEvents did not exit after service context cancel")
	}

	close(eventChan)
}
