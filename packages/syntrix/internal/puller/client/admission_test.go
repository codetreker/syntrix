package client

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	pb "github.com/codetreker/syntrix/api/gen/puller/v1"
	"github.com/codetreker/syntrix/internal/puller/config"
	"github.com/codetreker/syntrix/internal/puller/events"
	pullergrpc "github.com/codetreker/syntrix/internal/puller/grpc"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type admissionStream struct {
	grpc.ClientStream
	ctx     context.Context
	frames  chan *pb.PullerEvent
	failure chan error
}

func (s *admissionStream) Recv() (*pb.PullerEvent, error) {
	select {
	case frame := <-s.frames:
		return frame, nil
	case err := <-s.failure:
		return nil, err
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}

func admissionClient(t *testing.T, ctx context.Context) (*Client, *admissionStream) {
	t.Helper()
	stream := &admissionStream{ctx: ctx, frames: make(chan *pb.PullerEvent), failure: make(chan error, 1)}
	client, err := NewWithConfig("localhost:1", nil, ClientConfig{InitialBackoff: time.Hour})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	client.client = &mockPullerServiceClient{subscribeFunc: func(_ context.Context, req *pb.SubscribeRequest, _ ...grpc.CallOption) (pb.PullerService_SubscribeClient, error) {
		require.True(t, req.ReportAdmissionReady)
		require.False(t, req.RequireReady)
		return stream, nil
	}}
	return client, stream
}

func TestClientAdmissionOrdersControlsAndClearsTransportLossImmediately(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, stream := admissionClient(t, ctx)
	ch, state := client.SubscribeStatus(ctx, "test", "")
	stream.frames <- &pb.PullerEvent{ChangeEvent: &pb.ChangeEvent{EventId: "1-1-event", OpType: "insert"}, Progress: "business-progress"}
	stream.frames <- &pb.PullerEvent{Ready: true, AdmissionState: events.AdmissionAdmitted, AdmissionGeneration: 1}
	first := <-ch
	require.NotNil(t, first.Change)
	ready := <-ch
	require.Empty(t, ready.Progress)
	require.False(t, state.Snapshot().Ready)
	state.Observe(ready)
	require.True(t, state.Snapshot().Ready)
	stream.frames <- &pb.PullerEvent{AdmissionState: events.AdmissionRecovering, AdmissionGeneration: 2, AdmissionReason: "source_unavailable"}
	require.Eventually(t, func() bool { return !state.Snapshot().Ready }, time.Second, time.Millisecond)
	state.Observe(ready)
	require.False(t, state.Snapshot().Ready)
	<-ch
	stream.frames <- &pb.PullerEvent{Ready: true, AdmissionState: events.AdmissionAdmitted, AdmissionGeneration: 2}
	next := <-ch
	state.Observe(next)
	require.True(t, state.Snapshot().Ready)
	stream.failure <- status.Error(codes.Unavailable, "lost")
	require.Eventually(t, func() bool { return !state.Snapshot().Ready }, time.Second, time.Millisecond)
	state.Observe(next)
	require.False(t, state.Snapshot().Ready)
}

func TestClientAdmissionExpiresWhileBoundedDispatchIsBlocked(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	client, stream := admissionClient(t, ctx)
	ch, state := client.SubscribeStatus(ctx, "blocked", "")
	stream.frames <- &pb.PullerEvent{Ready: true, AdmissionState: events.AdmissionAdmitted, AdmissionGeneration: 1}
	state.Observe(<-ch)
	require.True(t, state.Snapshot().Ready)
	// Fill the actual bounded dispatch queue, leaving Recv's dispatcher blocked
	// before the next refresh rather than synthesizing status expiry.
	for i := 0; i < 1001; i++ {
		select {
		case stream.frames <- &pb.PullerEvent{ChangeEvent: &pb.ChangeEvent{EventId: "event", OpType: "insert"}}:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	changed := state.Snapshot().Changed
	select {
	case <-changed:
	case <-ctx.Done():
		t.Fatal("blocked dispatch preserved stale admission")
	}
	require.False(t, state.Snapshot().Ready)
	for i := 0; i < 1001; i++ {
		select {
		case frame := <-ch:
			require.NotNil(t, frame.Change)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	stream.frames <- &pb.PullerEvent{Ready: true, AdmissionState: events.AdmissionAdmitted, AdmissionGeneration: 1}
	ready := <-ch
	require.False(t, state.Snapshot().Ready)
	state.Observe(ready)
	require.True(t, state.Snapshot().Ready)
}

func TestClientRejectsMalformedAdmissionAndUnsupportedSource(t *testing.T) {
	for _, kind := range []string{"mixed", "progress", "generation", "state", "unsupported"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			client, stream := admissionClient(t, ctx)
			ch, state := client.SubscribeStatus(ctx, "invalid", "")
			frame := &pb.PullerEvent{Ready: true, AdmissionState: events.AdmissionAdmitted, AdmissionGeneration: 1}
			switch kind {
			case "mixed":
				frame.ChangeEvent = &pb.ChangeEvent{}
			case "progress":
				frame.Progress = "not-neutral"
			case "generation":
				frame.AdmissionGeneration = 0
			case "state":
				frame.AdmissionState = "unknown"
			case "unsupported":
				stream.failure <- status.Error(codes.Unimplemented, "unsupported")
			}
			if kind != "unsupported" {
				stream.frames <- frame
			}
			select {
			case got := <-ch:
				require.Error(t, got.Error)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			require.True(t, state.Snapshot().Terminal)
			require.False(t, state.Snapshot().Ready)
		})
	}
}

func TestClientAdmissionPreservesTerminalFailuresAndCallerCancellation(t *testing.T) {
	for _, phase := range []string{"admission", "receive"} {
		for _, code := range []codes.Code{codes.Canceled, codes.InvalidArgument, codes.FailedPrecondition, codes.Unimplemented, codes.Internal} {
			t.Run(phase+"/"+code.String(), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				failure := status.Error(code, "subscription rejected")
				client, err := New("localhost:1", nil)
				require.NoError(t, err)
				defer client.Close()
				calls := 0
				client.client = &mockPullerServiceClient{subscribeFunc: func(context.Context, *pb.SubscribeRequest, ...grpc.CallOption) (pb.PullerService_SubscribeClient, error) {
					calls++
					if code == codes.Canceled {
						cancel()
					}
					if phase == "admission" {
						return nil, failure
					}
					return &mockSubscribeClient{failAfter: 0, failError: failure}, nil
				}}
				out := make(chan *events.PullerEvent, 1)
				admission := events.NewSubscriptionStatus(ctx)
				client.subscribeLoop(ctx, "terminal", "", false, out, nil, false, admission)
				require.Equal(t, 1, calls)
				require.True(t, admission.Snapshot().Terminal)
				require.False(t, admission.Snapshot().Ready)
				if code != codes.Canceled {
					evt := <-out
					require.ErrorIs(t, evt.Error, failure)
					require.False(t, evt.Retryable)
				}
				_, open := <-out
				require.False(t, open)
			})
		}
	}
}

type admissionRestartSource struct {
	codecEventSource
	replay  []*events.StoreChangeEvent
	resumes chan string
}

func (s *admissionRestartSource) Readiness(context.Context) error { return nil }

func (s *admissionRestartSource) Replay(ctx context.Context, after map[string]string, _ bool) (events.Iterator, error) {
	select {
	case s.resumes <- after["backend"]:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &admissionReplayIterator{events: s.replay}, nil
}

type admissionReplayIterator struct {
	events  []*events.StoreChangeEvent
	current *events.StoreChangeEvent
}

func (i *admissionReplayIterator) Next() bool {
	if len(i.events) == 0 {
		return false
	}
	i.current, i.events = i.events[0], i.events[1:]
	return true
}

func (i *admissionReplayIterator) Event() *events.StoreChangeEvent { return i.current }
func (i *admissionReplayIterator) Err() error                      { return nil }
func (i *admissionReplayIterator) Close() error                    { return nil }

func TestClientAdmissionRecoversAfterServerShutdownAndRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	start := func(address string, source *admissionRestartSource) (*pullergrpc.Server, string, func()) {
		listener, err := net.Listen("tcp", address)
		require.NoError(t, err)
		transport := grpc.NewServer()
		service := pullergrpc.NewServer(config.GRPCConfig{}, source, nil)
		pb.RegisterPullerServiceServer(transport, service)
		service.Init()
		done := make(chan error, 1)
		go func() { done <- transport.Serve(listener) }()
		var once sync.Once
		stop := func() {
			once.Do(func() {
				service.Shutdown()
				transport.Stop()
				select {
				case err := <-done:
					require.NoError(t, err)
				case <-time.After(time.Second):
					t.Fatal("gRPC server did not stop")
				}
			})
		}
		t.Cleanup(stop)
		return service, listener.Addr().String(), stop
	}
	firstSource := &admissionRestartSource{}
	firstServer, address, stopFirst := start("127.0.0.1:0", firstSource)
	reconnecting := make(chan error, 32)
	client, err := NewWithConfig(address, nil, ClientConfig{
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     10 * time.Millisecond,
		OnStateChange: func(state ConnectionState, err error) {
			if state == StateReconnecting {
				select {
				case reconnecting <- err:
				case <-ctx.Done():
				}
			}
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	ch, admission := client.SubscribeStatus(ctx, "restart", "")
	receive := func() *events.PullerEvent {
		select {
		case evt, open := <-ch:
			require.True(t, open, "restart closed ordinary ingestion")
			require.NotNil(t, evt)
			require.NoError(t, evt.Error)
			return evt
		case <-ctx.Done():
			t.Fatal(ctx.Err())
			return nil
		}
	}
	receiveChange := func() *events.StoreChangeEvent {
		for {
			evt := receive()
			if evt.Change != nil {
				return evt.Change
			}
			admission.Observe(evt)
		}
	}
	var firstReady *events.PullerEvent
	for firstReady == nil {
		if evt := receive(); evt.Ready {
			firstReady = evt
		}
	}
	admission.Observe(firstReady)
	require.True(t, admission.Snapshot().Ready)
	before := &events.StoreChangeEvent{Backend: "backend", EventID: "1-0-before", OpType: events.StoreOperationInsert, ClusterTime: events.ClusterTime{T: 1}}
	require.NoError(t, firstSource.handler(ctx, "backend", before))
	require.Equal(t, before.EventID, receiveChange().EventID)

	firstServer.Shutdown()
	select {
	case err := <-reconnecting:
		require.Equal(t, codes.Canceled, status.Code(err))
	case <-ctx.Done():
		t.Fatal("server shutdown did not enter retry")
	}
	require.NoError(t, ctx.Err())
	require.False(t, admission.Snapshot().Ready)
	require.False(t, admission.Snapshot().Terminal)
	admission.Observe(firstReady)
	require.False(t, admission.Snapshot().Ready)
	stopFirst()

	missed := &events.StoreChangeEvent{Backend: "backend", EventID: "2-0-missed", OpType: events.StoreOperationInsert, ClusterTime: events.ClusterTime{T: 2}}
	secondSource := &admissionRestartSource{replay: []*events.StoreChangeEvent{missed}, resumes: make(chan string, 1)}
	_, _, _ = start(address, secondSource)
	var replayed bool
	for {
		evt := receive()
		if evt.Change != nil {
			require.Equal(t, missed.EventID, evt.Change.EventID)
			replayed = true
		}
		if evt.Ready {
			require.True(t, replayed, "re-admission overtook replay")
			require.Greater(t, evt.TransportGeneration, firstReady.TransportGeneration)
			require.False(t, admission.Snapshot().Ready)
			admission.Observe(firstReady)
			require.False(t, admission.Snapshot().Ready)
			admission.Observe(evt)
			break
		}
	}
	select {
	case after := <-secondSource.resumes:
		require.Equal(t, before.EventID, after)
	case <-ctx.Done():
		t.Fatal("restart did not resume from delivered progress")
	}
	require.True(t, admission.Snapshot().Ready)
	require.False(t, admission.Snapshot().Terminal)
	after := &events.StoreChangeEvent{Backend: "backend", EventID: "3-0-after", OpType: events.StoreOperationInsert, ClusterTime: events.ClusterTime{T: 3}}
	require.NoError(t, secondSource.handler(ctx, "backend", after))
	require.Equal(t, after.EventID, receiveChange().EventID)
	cancel()
	for {
		select {
		case _, open := <-ch:
			if !open {
				require.True(t, admission.Snapshot().Terminal)
				require.False(t, admission.Snapshot().Ready)
				return
			}
		case <-time.After(time.Second):
			t.Fatal("caller cancellation did not stop subscription")
		}
	}
}
