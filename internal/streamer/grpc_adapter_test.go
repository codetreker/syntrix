package streamer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pb "github.com/syntrixbase/syntrix/api/gen/streamer/v1"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/puller/events"
	"google.golang.org/grpc/metadata"
)

// --- Mock gRPC Stream for testing GRPCStream ---

// mockBidiStream implements grpc.BidiStreamingServer for testing.
type mockBidiStream struct {
	ctx      context.Context
	recvMsgs []*pb.GatewayMessage
	recvIdx  int
	sentMsgs []*pb.StreamerMessage
	recvErr  error
	sendErr  error
	mu       sync.Mutex
}

func (m *mockBidiStream) Send(msg *pb.StreamerMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sendErr != nil {
		return m.sendErr
	}
	m.sentMsgs = append(m.sentMsgs, msg)
	return nil
}

func (m *mockBidiStream) Recv() (*pb.GatewayMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.recvErr != nil {
		return nil, m.recvErr
	}
	if m.recvIdx >= len(m.recvMsgs) {
		// Block until context is done
		m.mu.Unlock()
		<-m.ctx.Done()
		m.mu.Lock()
		return nil, m.ctx.Err()
	}
	msg := m.recvMsgs[m.recvIdx]
	m.recvIdx++
	return msg, nil
}

func (m *mockBidiStream) Context() context.Context     { return m.ctx }
func (m *mockBidiStream) SetHeader(metadata.MD) error  { return nil }
func (m *mockBidiStream) SendHeader(metadata.MD) error { return nil }
func (m *mockBidiStream) SetTrailer(metadata.MD)       {}
func (m *mockBidiStream) SendMsg(interface{}) error    { return nil }
func (m *mockBidiStream) RecvMsg(interface{}) error    { return nil }

type concurrentSendStream struct {
	*mockBidiStream
	active     atomic.Int32
	overlapped atomic.Bool
	entered    chan struct{}
	release    chan struct{}
}

func (m *concurrentSendStream) Send(msg *pb.StreamerMessage) error {
	if m.active.Add(1) != 1 {
		m.overlapped.Store(true)
	}
	defer m.active.Add(-1)
	m.entered <- struct{}{}
	<-m.release
	return m.mockBidiStream.Send(msg)
}

func TestGRPCAdapter_SerializesDataAndControl(t *testing.T) {
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	wire := &concurrentSendStream{
		mockBidiStream: &mockBidiStream{ctx: ctx},
		entered:        make(chan struct{}, 3),
		release:        make(chan struct{}),
	}
	adapter := newGRPCStreamAdapter(ctx, "gateway", wire, getInternalService(s))
	defer adapter.localStream.Close()
	done := make(chan error, 3)
	go func() {
		done <- adapter.send(&pb.StreamerMessage{Payload: &pb.StreamerMessage_Delivery{Delivery: &pb.EventDelivery{}}})
	}()
	select {
	case <-wire.entered:
	case <-ctx.Done():
		t.Fatal("data send did not start")
	}
	go func() {
		done <- adapter.handleProtoMessage(&pb.GatewayMessage{Payload: &pb.GatewayMessage_Heartbeat{Heartbeat: &pb.Heartbeat{Timestamp: 1}}})
	}()
	go func() {
		done <- adapter.handleProtoMessage(&pb.GatewayMessage{Payload: &pb.GatewayMessage_Subscribe{Subscribe: &pb.SubscribeRequest{Database: "db", Collection: "items"}}})
	}()
	select {
	case <-wire.entered:
		t.Error("control send overlapped the blocked data send")
	case <-time.After(20 * time.Millisecond):
	}
	close(wire.release)
	for range 3 {
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-ctx.Done():
			t.Fatal("send did not drain")
		}
	}
	require.False(t, wire.overlapped.Load())
	require.Len(t, wire.sentMsgs, 3)
}

func TestGRPCAdapter_RetiredRegistrationRejected(t *testing.T) {
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)
	service := getInternalService(s)
	wire := &mockBidiStream{ctx: context.Background()}
	adapter := newGRPCStreamAdapter(context.Background(), "retired", wire, service)
	defer adapter.localStream.Close()
	require.NoError(t, s.Stop(context.Background()))
	err = adapter.handleProtoMessage(&pb.GatewayMessage{Payload: &pb.GatewayMessage_Subscribe{Subscribe: &pb.SubscribeRequest{SubscriptionId: "late", Database: "db", Collection: "items"}}})
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, wire.sentMsgs)
	require.Error(t, service.manager.Unsubscribe("late"))
}

// --- GRPCStream Tests ---

func TestGRPCStream_ContextCanceled(t *testing.T) {
	t.Parallel()
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)
	internal := getInternalService(s)

	ctx, cancel := context.WithCancel(context.Background())
	mockStream := &mockBidiStream{ctx: ctx}

	done := make(chan error, 1)
	go func() {
		done <- internal.GRPCStream(mockStream)
	}()

	// Cancel context
	time.Sleep(10 * time.Millisecond)
	cancel()

	err = <-done
	assert.ErrorIs(t, err, context.Canceled)
}

func TestGRPCStream_RecvError(t *testing.T) {
	t.Parallel()
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)
	internal := getInternalService(s)

	ctx := context.Background()
	mockStream := &mockBidiStream{
		ctx:     ctx,
		recvErr: io.EOF,
	}

	err = internal.GRPCStream(mockStream)
	assert.ErrorIs(t, err, io.EOF)
}

func TestGRPCStream_HeartbeatMessage(t *testing.T) {
	t.Parallel()
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)
	internal := getInternalService(s)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockStream := &mockBidiStream{
		ctx: ctx,
		recvMsgs: []*pb.GatewayMessage{
			{
				Payload: &pb.GatewayMessage_Heartbeat{
					Heartbeat: &pb.Heartbeat{
						Timestamp: 12345,
					},
				},
			},
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- internal.GRPCStream(mockStream)
	}()

	// Give time for heartbeat to be processed and ack sent
	time.Sleep(50 * time.Millisecond)
	cancel()

	<-done

	// Should have sent a heartbeat ack
	mockStream.mu.Lock()
	defer mockStream.mu.Unlock()
	require.GreaterOrEqual(t, len(mockStream.sentMsgs), 1)
	ack := mockStream.sentMsgs[0].GetHeartbeatAck()
	require.NotNil(t, ack)
	assert.Equal(t, int64(12345), ack.Timestamp)
}

func TestGRPCStream_SendError(t *testing.T) {
	t.Parallel()
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)
	internal := getInternalService(s)

	ctx := context.Background()
	mockStream := &mockBidiStream{
		ctx:     ctx,
		sendErr: errors.New("send failed"),
		recvMsgs: []*pb.GatewayMessage{
			{
				Payload: &pb.GatewayMessage_Heartbeat{
					Heartbeat: &pb.Heartbeat{Timestamp: 1},
				},
			},
		},
	}

	err = internal.GRPCStream(mockStream)
	assert.Error(t, err)
}

func TestGRPCStream_ServiceStopped(t *testing.T) {
	t.Parallel()
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)
	internal := getInternalService(s)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mockStream := &mockBidiStream{ctx: ctx}

	done := make(chan error, 1)
	go func() {
		done <- internal.GRPCStream(mockStream)
	}()

	// Stop the service
	time.Sleep(10 * time.Millisecond)
	internal.Stop(context.Background())

	err = <-done
	assert.Error(t, err)
}

func TestGRPCStream_UnsubscribeMessage(t *testing.T) {
	t.Parallel()
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)
	internal := getInternalService(s)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockStream := &mockBidiStream{
		ctx: ctx,
		recvMsgs: []*pb.GatewayMessage{
			{
				Payload: &pb.GatewayMessage_Unsubscribe{
					Unsubscribe: &pb.UnsubscribeRequest{
						SubscriptionId: "nonexistent-sub",
					},
				},
			},
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- internal.GRPCStream(mockStream)
	}()

	// Wait for message to be processed
	time.Sleep(30 * time.Millisecond)
	cancel()
	<-done
}

// TestGRPCAdapter_OutgoingDelivery tests the delivery sending through gRPC.
func TestGRPCAdapter_OutgoingDelivery(t *testing.T) {
	t.Parallel()
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)
	internal := getInternalService(s)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Subscribe first, then receive messages to trigger outgoing delivery
	mockStream := &mockBidiStream{
		ctx: ctx,
		recvMsgs: []*pb.GatewayMessage{
			{
				Payload: &pb.GatewayMessage_Subscribe{
					Subscribe: &pb.SubscribeRequest{
						SubscriptionId: "sub-grpc-1",
						Database:       "database1",
						Collection:     "users",
					},
				},
			},
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- internal.GRPCStream(mockStream)
	}()

	// Wait for subscription to be processed
	time.Sleep(50 * time.Millisecond)

	// Process an event that matches the subscription
	err = internal.ProcessEvent(events.SyntrixChangeEvent{
		Id:       "evt-grpc-1",
		Database: "database1",
		Type:     events.EventCreate,
		Document: &storage.StoredDoc{
			Id:         "_id_doc1",
			Database:   "database1",
			Collection: "users",
			Fullpath:   "users/doc1",
			Data:       map[string]interface{}{"id": "doc1", "name": "Alice"},
		},
	})
	require.NoError(t, err)

	// Wait for event to be delivered
	time.Sleep(50 * time.Millisecond)

	// Verify the event was sent
	mockStream.mu.Lock()
	defer mockStream.mu.Unlock()
	found := false
	for _, msg := range mockStream.sentMsgs {
		if delivery := msg.GetDelivery(); delivery != nil {
			if delivery.Event.Collection == "users" {
				found = true
				break
			}
		}
	}
	assert.True(t, found, "expected delivery to be sent")

	cancel()
	<-done
}

// TestGRPCAdapter_OutgoingSendError tests error handling when sending fails.
func TestGRPCAdapter_OutgoingSendError(t *testing.T) {
	t.Parallel()
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)
	internal := getInternalService(s)

	ctx := context.Background()

	mockStream := &mockBidiStream{
		ctx: ctx,
		recvMsgs: []*pb.GatewayMessage{
			{
				Payload: &pb.GatewayMessage_Subscribe{
					Subscribe: &pb.SubscribeRequest{
						SubscriptionId: "sub-send-err",
						Database:       "database1",
						Collection:     "products",
					},
				},
			},
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- internal.GRPCStream(mockStream)
	}()

	// Wait for subscription to be processed
	time.Sleep(50 * time.Millisecond)

	// Now set send error to trigger failure when delivering
	mockStream.mu.Lock()
	mockStream.sendErr = errors.New("send delivery failed")
	mockStream.mu.Unlock()

	// Process an event to trigger delivery send
	err = internal.ProcessEvent(events.SyntrixChangeEvent{
		Id:       "evt-send-err",
		Database: "database1",
		Type:     events.EventCreate,
		Document: &storage.StoredDoc{
			Id:         "_id_doc1",
			Database:   "database1",
			Collection: "products",
			Fullpath:   "products/doc1",
			Data: map[string]interface{}{
				"id":   "doc1",
				"name": "Product",
			},
		},
	})
	require.NoError(t, err)

	// The adapter should exit with an error
	select {
	case err := <-done:
		assert.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("expected error from send failure")
	}
}

// TestGRPCAdapter_SubscribeAutoGenerateID tests that subscription ID is auto-generated if empty.
func TestGRPCAdapter_SubscribeAutoGenerateID(t *testing.T) {
	t.Parallel()
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)
	internal := getInternalService(s)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockStream := &mockBidiStream{
		ctx: ctx,
		recvMsgs: []*pb.GatewayMessage{
			{
				Payload: &pb.GatewayMessage_Subscribe{
					Subscribe: &pb.SubscribeRequest{
						SubscriptionId: "", // Empty - should be auto-generated
						Database:       "database1",
						Collection:     "users",
					},
				},
			},
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- internal.GRPCStream(mockStream)
	}()

	// Wait for subscription to be processed
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	// Verify response was sent with a generated subscription ID
	mockStream.mu.Lock()
	defer mockStream.mu.Unlock()
	require.GreaterOrEqual(t, len(mockStream.sentMsgs), 1)
	resp := mockStream.sentMsgs[0].GetSubscribeResponse()
	require.NotNil(t, resp)
	assert.NotEmpty(t, resp.SubscriptionId)
	assert.True(t, resp.Success)
}

// TestGRPCAdapter_Subscribe_ManagerError tests handleProtoMessage when manager.Subscribe returns error.
func TestGRPCAdapter_Subscribe_ManagerError(t *testing.T) {
	t.Parallel()
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)
	internal := getInternalService(s)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create two subscriptions with the same ID to trigger an error on the second one
	mockStream := &mockBidiStream{
		ctx: ctx,
		recvMsgs: []*pb.GatewayMessage{
			{
				Payload: &pb.GatewayMessage_Subscribe{
					Subscribe: &pb.SubscribeRequest{
						SubscriptionId: "dup-id",
						Database:       "database1",
						Collection:     "users",
					},
				},
			},
			{
				Payload: &pb.GatewayMessage_Subscribe{
					Subscribe: &pb.SubscribeRequest{
						SubscriptionId: "dup-id", // Same ID - should cause error
						Database:       "database1",
						Collection:     "users",
					},
				},
			},
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- internal.GRPCStream(mockStream)
	}()

	// Wait for both subscriptions to be processed
	time.Sleep(80 * time.Millisecond)
	cancel()
	<-done

	// Verify both responses were sent
	mockStream.mu.Lock()
	defer mockStream.mu.Unlock()

	require.GreaterOrEqual(t, len(mockStream.sentMsgs), 2)

	// First should succeed
	resp1 := mockStream.sentMsgs[0].GetSubscribeResponse()
	require.NotNil(t, resp1)
	assert.True(t, resp1.Success)

	// Second should fail (duplicate ID)
	resp2 := mockStream.sentMsgs[1].GetSubscribeResponse()
	require.NotNil(t, resp2)
	assert.False(t, resp2.Success)
	assert.NotEmpty(t, resp2.Error)
}

// --- NewGRPCServer and grpcServerAdapter Tests ---

func TestNewGRPCServer(t *testing.T) {
	t.Parallel()
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)

	grpcServer, err := NewGRPCServer(s)
	require.NoError(t, err)
	assert.NotNil(t, grpcServer)
}

func TestNewGRPCServer_ErrorOnNonStreamerService(t *testing.T) {
	t.Parallel()
	// Create a mock that implements StreamerServer but is not *streamerService
	mockServer := &mockStreamerServer{}

	grpcServer, err := NewGRPCServer(mockServer)
	assert.Error(t, err)
	assert.Nil(t, grpcServer)
	assert.Contains(t, err.Error(), "requires a *streamerService instance")
}

// mockStreamerServer is a mock StreamerServer for testing NewGRPCServer panic.
type mockStreamerServer struct{}

func (m *mockStreamerServer) Stream(ctx context.Context) (Stream, error) {
	return nil, nil
}

func (m *mockStreamerServer) Start(ctx context.Context) error {
	return nil
}

func (m *mockStreamerServer) Stop(ctx context.Context) error {
	return nil
}

func TestGRPCServerAdapter_Stream(t *testing.T) {
	t.Parallel()
	s, err := NewService(ServerConfig{}, slog.Default())
	require.NoError(t, err)

	grpcServer, err := NewGRPCServer(s)
	require.NoError(t, err)
	require.NotNil(t, grpcServer)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockStream := &mockBidiStream{ctx: ctx}

	done := make(chan error, 1)
	go func() {
		done <- grpcServer.Stream(mockStream)
	}()

	// Cancel to stop the stream
	time.Sleep(20 * time.Millisecond)
	cancel()

	err = <-done
	assert.ErrorIs(t, err, context.Canceled)
}
