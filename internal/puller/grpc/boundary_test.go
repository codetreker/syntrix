package grpc

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	pullerv1 "github.com/syntrixbase/syntrix/api/gen/puller/v1"
	"github.com/syntrixbase/syntrix/internal/puller/config"
	"github.com/syntrixbase/syntrix/internal/puller/core"
	"github.com/syntrixbase/syntrix/internal/puller/cursor"
	"github.com/syntrixbase/syntrix/internal/puller/events"
	ggrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type testBoundarySource struct {
	mockEventSource
	validated      func() error
	replay         []*events.StoreChangeEvent
	replayIterator events.Iterator
	bootstrapError error
}

func (s *testBoundarySource) BootstrapBoundary(context.Context) (string, error) {
	return "boundary", s.bootstrapError
}

func TestBoundaryRPCsDistinguishUnavailableCaptureFromInvalidHistory(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		failure error
		code    codes.Code
	}{
		{"ready", nil, codes.OK},
		{"expired", errors.New("retained history expired"), codes.FailedPrecondition},
		{"reconnecting", fmt.Errorf("backend source: %w", core.ErrCaptureUnavailable), codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			source := &testBoundarySource{bootstrapError: tc.failure, validated: func() error { return tc.failure }}
			server := NewServer(config.GRPCConfig{}, source, nil)
			bootstrap, err := server.BootstrapBoundary(ctx, &pullerv1.BootstrapBoundaryRequest{})
			require.Equal(t, tc.code, status.Code(err))
			validated, err := server.ValidateBoundary(ctx, &pullerv1.ValidateBoundaryRequest{Progress: "opaque-applied-position"})
			require.Equal(t, tc.code, status.Code(err))
			if tc.failure == nil {
				require.Equal(t, "boundary", bootstrap.Progress)
				require.Equal(t, "opaque-applied-position", validated.Progress)
			} else {
				require.Nil(t, bootstrap)
				require.Nil(t, validated)
				stream := &boundaryStream{ctx: ctx, cancel: cancel}
				err := server.Subscribe(&pullerv1.SubscribeRequest{After: cursor.NewProgressMarker().Encode(), RequireReady: true}, stream)
				require.Equal(t, tc.code, status.Code(err))
				require.Empty(t, stream.sent)
				require.Zero(t, server.SubscriberCount())
			}
		})
	}
}

func TestBoundaryProtocolRejectsSourceWithoutVerifiedReplay(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	server := NewServer(config.GRPCConfig{}, &mockEventSource{}, nil)
	bootstrap, err := server.BootstrapBoundary(ctx, &pullerv1.BootstrapBoundaryRequest{})
	require.Equal(t, codes.Unimplemented, status.Code(err))
	require.Nil(t, bootstrap)
	validated, err := server.ValidateBoundary(ctx, &pullerv1.ValidateBoundaryRequest{Progress: "opaque"})
	require.Equal(t, codes.Unimplemented, status.Code(err))
	require.Nil(t, validated)
	stream := &boundaryStream{ctx: ctx, cancel: cancel}
	err = server.Subscribe(&pullerv1.SubscribeRequest{RequireReady: true}, stream)
	require.Equal(t, codes.Unimplemented, status.Code(err))
	require.Empty(t, stream.sent)
	require.Zero(t, server.SubscriberCount())
}

func TestVerifiedSubscriptionRetiresWhenCaptureHealthFails(t *testing.T) {
	t.Parallel()
	for _, temporary := range []bool{false, true} {
		t.Run(fmt.Sprint(temporary), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			checks := 0
			failure := errors.New("buffer lineage changed")
			wantCode := codes.FailedPrecondition
			if temporary {
				failure = fmt.Errorf("backend offline: %w", core.ErrCaptureUnavailable)
				wantCode = codes.Unavailable
			}
			source := &testBoundarySource{validated: func() error {
				checks++
				if checks > 1 {
					return failure
				}
				return nil
			}}
			server := NewServer(config.GRPCConfig{HeartbeatInterval: time.Millisecond}, source, nil)
			var sent []*pullerv1.PullerEvent
			stream := &mockSubscribeServer{ctx: ctx, sendFunc: func(evt *pullerv1.PullerEvent) error { sent = append(sent, evt); return nil }}
			err := server.Subscribe(&pullerv1.SubscribeRequest{After: cursor.NewProgressMarker().Encode(), RequireReady: true}, stream)
			require.Equal(t, wantCode, status.Code(err))
			require.Equal(t, 2, checks)
			require.Len(t, sent, 1)
			require.True(t, sent[0].Ready)
			require.Zero(t, server.SubscriberCount())
		})
	}
}
func (s *testBoundarySource) ValidateBoundary(context.Context, string) error { return s.validated() }
func (s *testBoundarySource) ReplayBoundary(context.Context, string, bool) (events.Iterator, error) {
	if s.replayIterator != nil {
		return s.replayIterator, nil
	}
	return &controllableIterator{events: s.replay}, nil
}

type boundaryStream struct {
	ggrpc.ServerStream
	ctx    context.Context
	sent   []*pullerv1.PullerEvent
	cancel context.CancelFunc
}

func (s *boundaryStream) Context() context.Context { return s.ctx }
func (s *boundaryStream) Send(evt *pullerv1.PullerEvent) error {
	s.sent = append(s.sent, evt)
	if evt.Ready {
		s.cancel()
	}
	return nil
}

func TestVerifiedSubscribeRegistersAndReplaysBeforeReady(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	pm := cursor.NewProgressMarker()
	pm.Positions["source"] = ""
	pm.Lineages = map[string]string{"source": "lineage"}
	source := &testBoundarySource{replay: []*events.StoreChangeEvent{{Backend: "source", EventID: "1-1-a", ClusterTime: events.ClusterTime{T: 1, I: 1}}}}
	server := NewServer(config.GRPCConfig{}, source, nil)
	source.validated = func() error { require.Equal(t, 1, server.SubscriberCount()); return nil }
	stream := &boundaryStream{ctx: ctx, cancel: cancel}
	require.NoError(t, server.Subscribe(&pullerv1.SubscribeRequest{After: pm.Encode(), RequireReady: true}, stream))
	require.Len(t, stream.sent, 2)
	require.Equal(t, "1-1-a", stream.sent[0].ChangeEvent.EventId)
	require.True(t, stream.sent[1].Ready)
	require.Nil(t, stream.sent[1].ChangeEvent)
	final, err := cursor.DecodeProgressMarker(stream.sent[1].Progress)
	require.NoError(t, err)
	require.Equal(t, "1-1-a", final.Positions["source"])
	require.Equal(t, "lineage", final.Lineages["source"])
	require.Zero(t, server.SubscriberCount())
}

func TestVerifiedSubscribeRejectsBoundaryWithoutReadyFrame(t *testing.T) {
	t.Parallel()
	source := &testBoundarySource{validated: func() error { return errors.New("expired") }}
	server := NewServer(config.GRPCConfig{}, source, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stream := &boundaryStream{ctx: ctx, cancel: cancel}
	err := server.Subscribe(&pullerv1.SubscribeRequest{RequireReady: true}, stream)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Empty(t, stream.sent)
}

func TestVerifiedSubscribeTreatsCancellationDuringBoundaryValidationAsNormalTermination(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	source := &testBoundarySource{validated: func() error {
		cancel()
		return context.Canceled
	}}
	server := NewServer(config.GRPCConfig{}, source, nil)
	stream := &mockSubscribeServer{ctx: ctx}

	err := server.Subscribe(&pullerv1.SubscribeRequest{
		After:        cursor.NewProgressMarker().Encode(),
		RequireReady: true,
	}, stream)
	require.NoError(t, err)
	require.Zero(t, server.SubscriberCount())
}

type failingBoundaryIterator struct {
	mockIterator
	closed bool
}

func (i *failingBoundaryIterator) Err() error {
	if i.closed {
		return nil
	}
	return errors.New("corrupt buffered event")
}
func (i *failingBoundaryIterator) Close() error { i.closed = true; return nil }

type blockingEmptyBoundaryIterator struct {
	started chan struct{}
	release chan struct{}
}

func (i *blockingEmptyBoundaryIterator) Next() bool {
	close(i.started)
	<-i.release
	return false
}

func (*blockingEmptyBoundaryIterator) Event() *events.StoreChangeEvent { return nil }
func (*blockingEmptyBoundaryIterator) Err() error                      { return nil }
func (*blockingEmptyBoundaryIterator) Close() error                    { return nil }

func TestVerifiedSubscribeRejectsReplayFailureBeforeReady(t *testing.T) {
	t.Parallel()
	source := &testBoundarySource{validated: func() error { return nil }, replayIterator: &failingBoundaryIterator{}}
	server := NewServer(config.GRPCConfig{}, source, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stream := &boundaryStream{ctx: ctx, cancel: cancel}
	pm := cursor.NewProgressMarker()
	pm.Positions["source"] = ""
	err := server.Subscribe(&pullerv1.SubscribeRequest{After: pm.Encode(), RequireReady: true}, stream)
	require.Equal(t, codes.Internal, status.Code(err))
	require.Contains(t, err.Error(), "corrupt buffered event")
	require.Empty(t, stream.sent)
}

func TestVerifiedSubscribeResetsHeartbeatAfterReplay(t *testing.T) {
	t.Parallel()
	interval := 40 * time.Millisecond
	iter := &blockingEmptyBoundaryIterator{started: make(chan struct{}), release: make(chan struct{})}
	source := &testBoundarySource{validated: func() error { return nil }, replayIterator: iter}
	server := NewServer(config.GRPCConfig{HeartbeatInterval: interval}, source, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	type observedFrame struct {
		ready bool
		at    time.Time
	}
	frames := make(chan observedFrame, 2)
	stream := &mockSubscribeServer{ctx: ctx, sendFunc: func(frame *pullerv1.PullerEvent) error {
		frames <- observedFrame{ready: frame.Ready, at: time.Now()}
		if !frame.Ready {
			cancel()
		}
		return nil
	}}
	marker := cursor.NewProgressMarker()
	marker.Positions["source"] = "0-0-start"
	done := make(chan error, 1)
	go func() {
		done <- server.Subscribe(&pullerv1.SubscribeRequest{After: marker.Encode(), RequireReady: true}, stream)
	}()

	select {
	case <-iter.started:
	case <-ctx.Done():
		t.Fatal("replay did not start")
	}
	select {
	case frame := <-frames:
		t.Fatalf("frame sent during replay: ready=%t", frame.ready)
	case <-time.After(3 * interval):
	}
	close(iter.release)

	receiveFrame := func(description string) observedFrame {
		t.Helper()
		select {
		case frame := <-frames:
			return frame
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s", description)
			return observedFrame{}
		}
	}
	ready := receiveFrame("Ready")
	require.True(t, ready.ready)
	heartbeat := receiveFrame("heartbeat")
	require.False(t, heartbeat.ready)
	require.GreaterOrEqual(t, heartbeat.at.Sub(ready.at), interval*3/4)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("subscription did not stop after heartbeat cancellation")
	}
}
