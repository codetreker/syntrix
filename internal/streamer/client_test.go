package streamer

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	pb "github.com/syntrixbase/syntrix/api/gen/streamer/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type streamReceive struct {
	message *pb.StreamerMessage
	err     error
}
type contractWire struct {
	ctx      context.Context
	sent     chan *pb.GatewayMessage
	incoming chan streamReceive
	sendGate <-chan struct{}
	sendErr  error
	active   atomic.Int32
	overlap  atomic.Bool
}

func (w *contractWire) Send(msg *pb.GatewayMessage) error {
	if w.active.Add(1) != 1 {
		w.overlap.Store(true)
	}
	defer w.active.Add(-1)
	select {
	case w.sent <- msg:
	case <-w.ctx.Done():
		return w.ctx.Err()
	}
	if w.sendGate != nil {
		select {
		case <-w.sendGate:
		case <-w.ctx.Done():
			return w.ctx.Err()
		}
	}
	if err := w.ctx.Err(); err != nil {
		return err
	}
	return w.sendErr
}
func (w *contractWire) Recv() (*pb.StreamerMessage, error) {
	select {
	case value := <-w.incoming:
		return value.message, value.err
	case <-w.ctx.Done():
		return nil, w.ctx.Err()
	}
}
func (w *contractWire) CloseSend() error             { return nil }
func (w *contractWire) Header() (metadata.MD, error) { return nil, nil }
func (w *contractWire) Trailer() metadata.MD         { return nil }
func (w *contractWire) Context() context.Context     { return w.ctx }
func (w *contractWire) SendMsg(any) error            { return nil }
func (w *contractWire) RecvMsg(any) error            { return nil }

type contractService struct {
	pb.StreamerServiceClient
	attempts       chan *contractWire
	calls          atomic.Int32
	configure      func(int, *contractWire) error
	returnCanceled bool
}

func (f *contractService) Stream(ctx context.Context, _ ...grpc.CallOption) (pb.StreamerService_StreamClient, error) {
	number := int(f.calls.Add(1))
	w := &contractWire{ctx: ctx, sent: make(chan *pb.GatewayMessage, 512), incoming: make(chan streamReceive, 512)}
	if f.configure != nil {
		if err := f.configure(number, w); err != nil {
			return nil, err
		}
	}
	if f.returnCanceled {
		return w, nil
	}
	select {
	case f.attempts <- w:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return w, nil
}
func openContractStream(t *testing.T, configure func(int, *contractWire) error, tune func(*ClientConfig)) (*streamerClient, Stream, *contractService, *contractWire) {
	t.Helper()
	cfg := DefaultClientConfig()
	cfg.InitialBackoff = time.Millisecond
	cfg.MaxBackoff = 2 * time.Millisecond
	cfg.HeartbeatInterval = time.Hour
	cfg.ActivityTimeout = time.Hour
	cfg.MaxRetries = 2
	if tune != nil {
		tune(&cfg)
	}
	ctx, cancel := context.WithCancel(context.Background())
	factory := &contractService{attempts: make(chan *contractWire, 16), configure: configure}
	client := &streamerClient{ctx: ctx, cancel: cancel, config: cfg, logger: slog.Default(), client: factory}
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	owner, stop := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(stop)
	stream, err := client.Stream(owner)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stream.Close()) })
	return client, stream, factory, takeWithin(t, factory.attempts)
}
func takeWithin[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for contract event")
		var zero T
		return zero
	}
}
func waitStreamStatus(t *testing.T, stream Stream, predicate func(StreamStatus) bool) StreamStatus {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		status := stream.Status()
		if predicate(status) {
			return status
		}
		select {
		case <-status.Changed:
		case <-timer.C:
			t.Fatalf("status did not reach expected state: %+v", status)
		}
	}
}
func ackRegistration(w *contractWire, id string, success bool) {
	w.incoming <- streamReceive{message: &pb.StreamerMessage{Payload: &pb.StreamerMessage_SubscribeResponse{SubscribeResponse: &pb.SubscribeResponse{SubscriptionId: id, Success: success, Error: "rejected"}}}}
}
func registerContract(t *testing.T, stream Stream, w *contractWire) Registration {
	t.Helper()
	type result struct {
		registration Registration
		err          error
	}
	done := make(chan result, 1)
	go func() { r, e := stream.Subscribe(context.Background(), "db", "items", nil); done <- result{r, e} }()
	request := takeWithin(t, w.sent).GetSubscribe()
	require.NotNil(t, request)
	ackRegistration(w, request.SubscriptionId, true)
	resultValue := takeWithin(t, done)
	require.NoError(t, resultValue.err)
	return resultValue.registration
}
func TestDefaultClientConfig(t *testing.T) {
	cfg := DefaultClientConfig()
	require.Equal(t, "localhost:50052", cfg.StreamerAddr)
	require.Positive(t, cfg.InitialBackoff)
	require.Positive(t, cfg.MaxBackoff)
	require.Positive(t, cfg.HeartbeatInterval)
	require.Positive(t, cfg.ActivityTimeout)
}
func TestNewClient_DefaultsAndClose(t *testing.T) {
	service, err := NewClient(ClientConfig{StreamerAddr: "localhost:50052"}, nil)
	require.NoError(t, err)
	client := service.(*streamerClient)
	require.Equal(t, time.Second, client.config.InitialBackoff)
	require.Equal(t, 30*time.Second, client.config.MaxBackoff)
	require.Equal(t, 2.0, client.config.BackoffMultiplier)
	require.Equal(t, 30*time.Second, client.config.HeartbeatInterval)
	require.Equal(t, 90*time.Second, client.config.ActivityTimeout)
	require.NoError(t, client.Close())
	require.ErrorIs(t, client.ctx.Err(), context.Canceled)
}
func TestStreamerClient_StreamFactoryFailure(t *testing.T) {
	expected := errors.New("dial failed")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &streamerClient{ctx: ctx, cancel: cancel, config: DefaultClientConfig(), logger: slog.Default(), client: &contractService{configure: func(int, *contractWire) error { return expected }}}
	_, err := client.Stream(context.Background())
	require.ErrorIs(t, err, expected)
}

func TestStreamerClient_CanceledDuringStreamCreation(t *testing.T) {
	for _, target := range []string{"caller", "client"} {
		t.Run(target, func(t *testing.T) {
			owner, cancelOwner := context.WithCancel(context.Background())
			defer cancelOwner()
			caller, cancelCaller := context.WithCancel(context.Background())
			defer cancelCaller()
			factory := &contractService{returnCanceled: true, configure: func(int, *contractWire) error {
				if target == "caller" {
					cancelCaller()
				} else {
					cancelOwner()
				}
				return nil
			}}
			client := &streamerClient{ctx: owner, cancel: cancelOwner, config: DefaultClientConfig(), logger: slog.Default(), client: factory}
			stream, err := client.Stream(caller)
			if stream != nil {
				defer stream.Close()
			}
			require.ErrorIs(t, err, context.Canceled)
			require.Nil(t, stream)
		})
	}
}
func TestConnectionState_String(t *testing.T) {
	for state, want := range map[ConnectionState]string{StateDisconnected: "disconnected", StateConnecting: "connecting", StateConnected: "connected", StateReconnecting: "reconnecting", ConnectionState(99): "unknown"} {
		require.Equal(t, want, state.String())
	}
}
