package streamer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	pb "github.com/syntrixbase/syntrix/api/gen/streamer/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestRemoteStream_RegistrationAndDelivery(t *testing.T) {
	_, stream, _, wire := openContractStream(t, nil, nil)
	initial := stream.Status()
	require.Equal(t, StateConnected, initial.State)
	require.Equal(t, uint64(1), initial.Generation)
	require.False(t, initial.Terminal)
	registration := registerContract(t, stream, wire)
	require.NotEmpty(t, registration.ID)
	require.Equal(t, initial.Generation, registration.Generation)
	wire.incoming <- streamReceive{message: &pb.StreamerMessage{Payload: &pb.StreamerMessage_HeartbeatAck{HeartbeatAck: &pb.HeartbeatAck{}}}}
	ackRegistration(wire, "unknown", true)
	wire.incoming <- streamReceive{message: &pb.StreamerMessage{Payload: &pb.StreamerMessage_Delivery{Delivery: &pb.EventDelivery{SubscriptionIds: []string{registration.ID}, Event: &pb.StreamerEvent{EventId: "event", Database: "db", Collection: "items", Operation: pb.OperationType_OPERATION_TYPE_INSERT}}}}}
	delivery, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, "event", delivery.Event.EventID)
	require.Equal(t, []string{registration.ID}, delivery.SubscriptionIDs)
	require.NoError(t, stream.Unsubscribe(registration.ID))
	require.Equal(t, registration.ID, takeWithin(t, wire.sent).GetUnsubscribe().SubscriptionId)
}

func TestRemoteStream_RestorationWaitsForACK(t *testing.T) {
	_, stream, factory, first := openContractStream(t, nil, nil)
	registration := registerContract(t, stream, first)
	before := stream.Status()
	first.incoming <- streamReceive{err: io.EOF}
	second := takeWithin(t, factory.attempts)
	request := takeWithin(t, second.sent).GetSubscribe()
	require.NotNil(t, request)
	require.Equal(t, registration.ID, request.SubscriptionId)
	status := stream.Status()
	require.NotEqual(t, StateConnected, status.State)
	require.Greater(t, status.Generation, before.Generation)
	require.False(t, status.Terminal)
	select {
	case <-before.Changed:
	default:
		t.Fatal("generation retirement did not notify observers")
	}
	ackRegistration(second, registration.ID, true)
	status = waitStreamStatus(t, stream, func(s StreamStatus) bool { return s.State == StateConnected })
	require.Greater(t, status.Generation, registration.Generation)
	require.NoError(t, stream.Unsubscribe(registration.ID))
	require.Equal(t, registration.ID, takeWithin(t, second.sent).GetUnsubscribe().SubscriptionId)
}

func TestRemoteStream_DropsBufferedRetiredGeneration(t *testing.T) {
	_, stream, factory, first := openContractStream(t, nil, nil)
	registration := registerContract(t, stream, first)
	delivery := func(id string) streamReceive {
		return streamReceive{message: &pb.StreamerMessage{Payload: &pb.StreamerMessage_Delivery{Delivery: &pb.EventDelivery{SubscriptionIds: []string{registration.ID}, Event: &pb.StreamerEvent{EventId: id, Database: "db", Collection: "items"}}}}}
	}
	first.incoming <- delivery("retired-generation-event")
	first.incoming <- streamReceive{err: io.EOF}
	second := takeWithin(t, factory.attempts)
	takeWithin(t, second.sent)
	ackRegistration(second, registration.ID, true)
	waitStreamStatus(t, stream, func(s StreamStatus) bool { return s.State == StateConnected })
	second.incoming <- delivery("current-generation-event")
	event, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, "current-generation-event", event.Event.EventID)
}

func TestRemoteStream_RestorationRejectionRetriesWithSameID(t *testing.T) {
	_, stream, factory, first := openContractStream(t, nil, nil)
	registration := registerContract(t, stream, first)
	first.incoming <- streamReceive{err: io.EOF}
	second := takeWithin(t, factory.attempts)
	request := takeWithin(t, second.sent).GetSubscribe()
	require.Equal(t, registration.ID, request.SubscriptionId)
	ackRegistration(second, registration.ID, false)
	third := takeWithin(t, factory.attempts)
	request = takeWithin(t, third.sent).GetSubscribe()
	require.Equal(t, registration.ID, request.SubscriptionId)
	require.NotEqual(t, StateConnected, stream.Status().State)
	ackRegistration(third, registration.ID, true)
	waitStreamStatus(t, stream, func(s StreamStatus) bool { return s.State == StateConnected })
}

func TestRemoteStream_SuccessfulRegistrationOutlivesCaller(t *testing.T) {
	_, stream, factory, first := openContractStream(t, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := stream.Subscribe(ctx, "db", "items", nil); done <- err }()
	request := takeWithin(t, first.sent).GetSubscribe()
	ackRegistration(first, request.SubscriptionId, true)
	require.NoError(t, takeWithin(t, done))
	cancel()
	first.incoming <- streamReceive{err: io.EOF}
	second := takeWithin(t, factory.attempts)
	restored := takeWithin(t, second.sent).GetSubscribe()
	require.Equal(t, request.SubscriptionId, restored.SubscriptionId)
	ackRegistration(second, restored.SubscriptionId, true)
	waitStreamStatus(t, stream, func(s StreamStatus) bool { return s.State == StateConnected })
}

func TestRemoteStream_CanceledRegistrationDoesNotRestore(t *testing.T) {
	_, stream, factory, first := openContractStream(t, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := stream.Subscribe(ctx, "db", "items", nil); done <- err }()
	request := takeWithin(t, first.sent).GetSubscribe()
	require.NotNil(t, request)
	cancel()
	require.ErrorIs(t, takeWithin(t, done), context.Canceled)
	cleanup := takeWithin(t, first.sent).GetUnsubscribe()
	require.NotNil(t, cleanup)
	require.Equal(t, request.SubscriptionId, cleanup.SubscriptionId)
	ackRegistration(first, request.SubscriptionId, true)
	first.incoming <- streamReceive{err: io.EOF}
	second := takeWithin(t, factory.attempts)
	waitStreamStatus(t, stream, func(s StreamStatus) bool { return s.State == StateConnected })
	select {
	case msg := <-second.sent:
		t.Fatalf("canceled registration restored: %v", msg)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestRemoteStream_UnsubscribeReleasesPendingRestoration(t *testing.T) {
	_, stream, factory, first := openContractStream(t, nil, nil)
	a := registerContract(t, stream, first)
	b := registerContract(t, stream, first)
	first.incoming <- streamReceive{err: io.EOF}
	second := takeWithin(t, factory.attempts)
	restoring := takeWithin(t, second.sent).GetSubscribe()
	require.NotNil(t, restoring)
	remaining := a.ID
	if restoring.SubscriptionId == a.ID {
		remaining = b.ID
	}
	require.NoError(t, stream.Unsubscribe(restoring.SubscriptionId))
	var next *pb.SubscribeRequest
	var removed string
	for next == nil || removed == "" {
		message := takeWithin(t, second.sent)
		if request := message.GetSubscribe(); request != nil {
			next = request
		}
		if request := message.GetUnsubscribe(); request != nil {
			removed = request.SubscriptionId
		}
	}
	require.Equal(t, restoring.SubscriptionId, removed)
	require.Equal(t, remaining, next.SubscriptionId)
	ackRegistration(second, remaining, true)
	waitStreamStatus(t, stream, func(s StreamStatus) bool { return s.State == StateConnected })
}

func TestRemoteStream_ForeignLateACKCannotCompleteNewRegistration(t *testing.T) {
	_, stream, factory, first := openContractStream(t, nil, nil)
	oldDone := make(chan error, 1)
	go func() { _, err := stream.Subscribe(context.Background(), "db", "old", nil); oldDone <- err }()
	oldRequest := takeWithin(t, first.sent).GetSubscribe()
	first.incoming <- streamReceive{err: io.EOF}
	require.Error(t, takeWithin(t, oldDone))
	second := takeWithin(t, factory.attempts)
	waitStreamStatus(t, stream, func(s StreamStatus) bool { return s.State == StateConnected })
	type result struct {
		registration Registration
		err          error
	}
	newDone := make(chan result, 1)
	go func() { r, err := stream.Subscribe(context.Background(), "db", "new", nil); newDone <- result{r, err} }()
	newRequest := takeWithin(t, second.sent).GetSubscribe()
	require.NotEqual(t, oldRequest.SubscriptionId, newRequest.SubscriptionId)
	ackRegistration(second, oldRequest.SubscriptionId, true)
	select {
	case <-newDone:
		t.Fatal("foreign ACK completed new registration")
	case <-time.After(20 * time.Millisecond):
	}
	ackRegistration(second, newRequest.SubscriptionId, true)
	value := takeWithin(t, newDone)
	require.NoError(t, value.err)
	require.Equal(t, newRequest.SubscriptionId, value.registration.ID)
	require.Equal(t, stream.Status().Generation, value.registration.Generation)
}

func TestRemoteStream_RegistrationFailureAndCanceledAdmission(t *testing.T) {
	_, stream, _, wire := openContractStream(t, nil, nil)
	done := make(chan error, 1)
	go func() { _, err := stream.Subscribe(context.Background(), "db", "items", nil); done <- err }()
	request := takeWithin(t, wire.sent).GetSubscribe()
	ackRegistration(wire, request.SubscriptionId, false)
	require.ErrorContains(t, takeWithin(t, done), "rejected")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := stream.Subscribe(ctx, "db", "items", nil)
	require.ErrorIs(t, err, context.Canceled)
	select {
	case msg := <-wire.sent:
		t.Fatalf("canceled caller dispatched %v", msg)
	default:
	}
}

func TestRemoteStream_PendingRegistrationBound(t *testing.T) {
	_, stream, _, wire := openContractStream(t, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 256)
	for range 256 {
		go func() { _, err := stream.Subscribe(ctx, "db", "items", nil); done <- err }()
	}
	for range 256 {
		require.NotNil(t, takeWithin(t, wire.sent).GetSubscribe())
	}
	admission, cancelAdmission := context.WithTimeout(context.Background(), time.Second)
	defer cancelAdmission()
	_, err := stream.Subscribe(admission, "db", "items", nil)
	require.Error(t, err)
	require.NotErrorIs(t, err, context.DeadlineExceeded)
	require.NoError(t, stream.Close())
	cancel()
	for range 256 {
		require.Error(t, takeWithin(t, done))
	}
}

func TestRemoteStream_CloseInterruptsBlockedIO(t *testing.T) {
	gate := make(chan struct{})
	_, stream, _, wire := openContractStream(t, func(_ int, w *contractWire) error { w.sendGate = gate; return nil }, nil)
	subscribeDone := make(chan error, 1)
	recvDone := make(chan error, 1)
	go func() { _, err := stream.Subscribe(context.Background(), "db", "items", nil); subscribeDone <- err }()
	takeWithin(t, wire.sent)
	go func() { _, err := stream.Recv(); recvDone <- err }()
	closeDone := make(chan error, 1)
	go func() { closeDone <- stream.Close() }()
	require.NoError(t, takeWithin(t, closeDone))
	require.Error(t, takeWithin(t, subscribeDone))
	require.Error(t, takeWithin(t, recvDone))
	require.ErrorIs(t, wire.ctx.Err(), context.Canceled)
	require.True(t, stream.Status().Terminal)
	require.NoError(t, stream.Close())
	_, err := stream.Subscribe(context.Background(), "db", "items", nil)
	require.Error(t, err)
	require.Error(t, stream.Unsubscribe("old"))
}

func TestRemoteStream_CallerCancellationInterruptsBlockedSendAndReleasesRegistration(t *testing.T) {
	gate := make(chan struct{})
	_, stream, factory, first := openContractStream(t, func(attempt int, wire *contractWire) error {
		if attempt == 1 {
			wire.sendGate = gate
		}
		return nil
	}, nil)
	initial := stream.Status()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := stream.Subscribe(ctx, "db", "items", nil); done <- err }()
	canceled := takeWithin(t, first.sent).GetSubscribe()
	require.NotNil(t, canceled)
	cancel()
	require.ErrorIs(t, takeWithin(t, done), context.Canceled)
	second := takeWithin(t, factory.attempts)
	current := waitStreamStatus(t, stream, func(status StreamStatus) bool { return status.State == StateConnected })
	require.Greater(t, current.Generation, initial.Generation)
	require.False(t, current.Terminal)
	require.ErrorIs(t, first.ctx.Err(), context.Canceled)
	select {
	case request := <-second.sent:
		t.Fatalf("canceled registration restored: %v", request)
	default:
	}
	owner := stream.(*remoteStream)
	owner.mu.Lock()
	pending, active := len(owner.pending), len(owner.active)
	owner.mu.Unlock()
	require.Zero(t, pending)
	require.Zero(t, active)
	registration := registerContract(t, stream, second)
	require.NotEqual(t, canceled.SubscriptionId, registration.ID)
	require.Equal(t, current.Generation, registration.Generation)
}

func TestRemoteStream_IndividualCloseAndClientClose(t *testing.T) {
	client, first, factory, _ := openContractStream(t, nil, nil)
	second, err := client.Stream(context.Background())
	require.NoError(t, err)
	defer second.Close()
	secondWire := takeWithin(t, factory.attempts)
	require.NoError(t, first.Close())
	require.True(t, first.Status().Terminal)
	require.False(t, second.Status().Terminal)
	require.NoError(t, client.Close())
	waitStreamStatus(t, second, func(s StreamStatus) bool { return s.Terminal })
	require.ErrorIs(t, secondWire.ctx.Err(), context.Canceled)
	_, err = client.Stream(context.Background())
	require.Error(t, err)
}

func TestRemoteStream_RetryLimit(t *testing.T) {
	failure := errors.New("backend offline")
	_, stream, factory, wire := openContractStream(t, func(n int, _ *contractWire) error {
		if n > 1 {
			return failure
		}
		return nil
	}, func(cfg *ClientConfig) { cfg.MaxRetries = 2 })
	wire.incoming <- streamReceive{err: io.EOF}
	status := waitStreamStatus(t, stream, func(s StreamStatus) bool { return s.Terminal })
	require.Equal(t, StateDisconnected, status.State)
	require.Error(t, status.Err)
	require.Equal(t, int32(3), factory.calls.Load())
	_, err := stream.Recv()
	require.Error(t, err)
}

func TestRemoteStream_RestorationFailureCountsTowardRetryLimit(t *testing.T) {
	_, stream, factory, first := openContractStream(t, nil, func(cfg *ClientConfig) { cfg.MaxRetries = 2 })
	registration := registerContract(t, stream, first)
	first.incoming <- streamReceive{err: io.EOF}
	for range 2 {
		wire := takeWithin(t, factory.attempts)
		request := takeWithin(t, wire.sent).GetSubscribe()
		require.Equal(t, registration.ID, request.SubscriptionId)
		ackRegistration(wire, registration.ID, false)
	}
	waitStreamStatus(t, stream, func(s StreamStatus) bool { return s.Terminal })
	require.Equal(t, int32(3), factory.calls.Load())
}

func TestRemoteStream_HeartbeatAndSendsSerialized(t *testing.T) {
	gate := make(chan struct{})
	_, stream, _, wire := openContractStream(t, func(_ int, w *contractWire) error { w.sendGate = gate; return nil }, func(cfg *ClientConfig) { cfg.HeartbeatInterval = 5 * time.Millisecond })
	done := make(chan error, 1)
	go func() { _, err := stream.Subscribe(context.Background(), "db", "items", nil); done <- err }()
	first := takeWithin(t, wire.sent)
	require.NotNil(t, first.GetSubscribe())
	time.Sleep(20 * time.Millisecond)
	require.False(t, wire.overlap.Load())
	close(gate)
	heartbeat := takeWithin(t, wire.sent)
	require.NotNil(t, heartbeat.GetHeartbeat())
	require.False(t, wire.overlap.Load())
	ackRegistration(wire, first.GetSubscribe().SubscriptionId, true)
	require.NoError(t, takeWithin(t, done))
}

func TestRemoteStream_SendFailureRetiresGeneration(t *testing.T) {
	failure := errors.New("wire send failed")
	_, stream, factory, first := openContractStream(t, func(n int, w *contractWire) error {
		if n == 1 {
			w.sendErr = failure
		}
		return nil
	}, nil)
	original := stream.Status().Generation
	_, err := stream.Subscribe(context.Background(), "db", "items", nil)
	require.Error(t, err)
	require.NotNil(t, takeWithin(t, first.sent).GetSubscribe())
	second := takeWithin(t, factory.attempts)
	status := waitStreamStatus(t, stream, func(s StreamStatus) bool { return s.State == StateConnected && s.Generation > original })
	require.False(t, status.Terminal)
	registerContract(t, stream, second)
}

func TestRemoteStream_ActivityTimeoutReplacesAttempt(t *testing.T) {
	_, stream, factory, first := openContractStream(t, nil, func(cfg *ClientConfig) { cfg.ActivityTimeout = 20 * time.Millisecond })
	original := stream.Status().Generation
	second := takeWithin(t, factory.attempts)
	require.NotSame(t, first, second)
	require.ErrorIs(t, first.ctx.Err(), context.Canceled)
	waitStreamStatus(t, stream, func(s StreamStatus) bool { return s.Generation > original })
}

func TestRemoteStream_StateCallbackCanCloseStream(t *testing.T) {
	states := make(chan ConnectionState, 8)
	closed := make(chan error, 1)
	var stream Stream
	_, stream, _, wire := openContractStream(t, nil, func(cfg *ClientConfig) {
		cfg.InitialBackoff = 50 * time.Millisecond
		cfg.OnStateChange = func(state ConnectionState, _ error) {
			states <- state
			if state == StateReconnecting {
				closed <- stream.Close()
			}
		}
	})
	wire.incoming <- streamReceive{err: io.EOF}
	require.NoError(t, takeWithin(t, closed))
	for takeWithin(t, states) != StateDisconnected {
	}
	require.True(t, stream.Status().Terminal)
}

type wireRegistration struct {
	id       string
	allowACK chan struct{}
}
type restorationServer struct {
	pb.UnimplementedStreamerServiceServer
	registrations chan wireRegistration
	disconnect    chan struct{}
	attempts      atomic.Int32
	exits         chan int32
}

func (s *restorationServer) Stream(wire grpc.BidiStreamingServer[pb.GatewayMessage, pb.StreamerMessage]) error {
	attempt := s.attempts.Add(1)
	defer func() { s.exits <- attempt }()
	for {
		message, err := wire.Recv()
		if err != nil {
			return err
		}
		request := message.GetSubscribe()
		if request == nil {
			continue
		}
		registration := wireRegistration{id: request.SubscriptionId, allowACK: make(chan struct{})}
		select {
		case s.registrations <- registration:
		case <-wire.Context().Done():
			return wire.Context().Err()
		}
		select {
		case <-registration.allowACK:
		case <-wire.Context().Done():
			return wire.Context().Err()
		}
		if err := wire.Send(&pb.StreamerMessage{Payload: &pb.StreamerMessage_SubscribeResponse{SubscribeResponse: &pb.SubscribeResponse{SubscriptionId: registration.id, Success: true}}}); err != nil {
			return err
		}
		if attempt == 1 {
			select {
			case <-s.disconnect:
				return status.Error(codes.Unavailable, "retire transport")
			case <-wire.Context().Done():
				return wire.Context().Err()
			}
		}
	}
}

func TestRemoteStream_RealGRPCRestorationAndCancellation(t *testing.T) {
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	backend := &restorationServer{registrations: make(chan wireRegistration, 2), disconnect: make(chan struct{}), exits: make(chan int32, 2)}
	pb.RegisterStreamerServiceServer(server, backend)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	connection, err := grpc.NewClient("passthrough:///streamer-test", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	config := DefaultClientConfig()
	config.InitialBackoff = time.Millisecond
	config.MaxBackoff = time.Millisecond
	client := &streamerClient{ctx: ctx, cancel: cancel, conn: connection, client: pb.NewStreamerServiceClient(connection), config: config, logger: slog.Default()}
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	stream, err := client.Stream(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stream.Close()) })
	type registrationResult struct {
		registration Registration
		err          error
	}
	done := make(chan registrationResult, 1)
	go func() { r, err := stream.Subscribe(ctx, "db", "items", nil); done <- registrationResult{r, err} }()
	first := takeWithin(t, backend.registrations)
	close(first.allowACK)
	result := takeWithin(t, done)
	require.NoError(t, result.err)
	require.Equal(t, first.id, result.registration.ID)
	close(backend.disconnect)
	require.Equal(t, int32(1), takeWithin(t, backend.exits))
	second := takeWithin(t, backend.registrations)
	require.Equal(t, first.id, second.id)
	require.NotEqual(t, StateConnected, stream.Status().State)
	close(second.allowACK)
	waitStreamStatus(t, stream, func(s StreamStatus) bool { return s.State == StateConnected })
	require.NoError(t, stream.Close())
	require.Equal(t, int32(2), takeWithin(t, backend.exits))
	require.True(t, stream.Status().Terminal)
}
