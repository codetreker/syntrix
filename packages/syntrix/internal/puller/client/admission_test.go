package client

import (
	"context"
	"testing"
	"time"

	pb "github.com/codetreker/syntrix/api/gen/puller/v1"
	"github.com/codetreker/syntrix/internal/puller/events"
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
