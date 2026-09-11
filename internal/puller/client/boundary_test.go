package client

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	pullerv1 "github.com/syntrixbase/syntrix/api/gen/puller/v1"
	"github.com/syntrixbase/syntrix/internal/puller/events"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type boundaryRPCClient struct {
	pullerv1.PullerServiceClient
	bootstrap func(context.Context) (*pullerv1.BoundaryResponse, error)
	validate  func(context.Context, string) (*pullerv1.BoundaryResponse, error)
}

func (c *boundaryRPCClient) BootstrapBoundary(ctx context.Context, _ *pullerv1.BootstrapBoundaryRequest, _ ...grpc.CallOption) (*pullerv1.BoundaryResponse, error) {
	return c.bootstrap(ctx)
}

func (c *boundaryRPCClient) ValidateBoundary(ctx context.Context, req *pullerv1.ValidateBoundaryRequest, _ ...grpc.CallOption) (*pullerv1.BoundaryResponse, error) {
	return c.validate(ctx, req.Progress)
}

func TestBoundaryClientPreservesOpaqueProgressAndRPCFailures(t *testing.T) {
	t.Parallel()
	const progress = "opaque-boundary-with-empty-backend"
	for _, rpcErr := range []error{nil, status.Error(codes.FailedPrecondition, "buffer lineage changed"), status.Error(codes.Unavailable, "capture reconnecting")} {
		t.Run(status.Code(rpcErr).String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			c := &Client{client: &boundaryRPCClient{
				bootstrap: func(received context.Context) (*pullerv1.BoundaryResponse, error) {
					require.Same(t, ctx, received)
					return &pullerv1.BoundaryResponse{Progress: progress}, rpcErr
				},
				validate: func(received context.Context, marker string) (*pullerv1.BoundaryResponse, error) {
					require.Same(t, ctx, received)
					require.Equal(t, progress, marker)
					return &pullerv1.BoundaryResponse{Progress: marker}, rpcErr
				},
			}}
			got, err := c.BootstrapBoundary(ctx)
			require.ErrorIs(t, err, rpcErr)
			if rpcErr == nil {
				require.Equal(t, progress, got)
			} else {
				require.Empty(t, got)
			}
			require.ErrorIs(t, c.ValidateBoundary(ctx, progress), rpcErr)
		})
	}
}

func TestVerifiedClientReportsOneAttemptAndClassifiesFailure(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"admission", "receive"} {
		for _, tc := range []struct {
			name      string
			err       error
			retryable bool
		}{
			{"unavailable", status.Error(codes.Unavailable, "capture reconnecting"), true},
			{"deadline", status.Error(codes.DeadlineExceeded, "transport timeout"), true},
			{"capacity", status.Error(codes.ResourceExhausted, "admission full"), true},
			{"expired", status.Error(codes.FailedPrecondition, "history expired"), false},
			{"invalid", status.Error(codes.InvalidArgument, "invalid cursor"), false},
			{"unsupported", status.Error(codes.Unimplemented, "no ready protocol"), false},
			{"corrupt", status.Error(codes.Internal, "corrupt replay"), false},
			{"eof", io.EOF, true},
		} {
			t.Run(phase+"/"+tc.name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				calls := 0
				c := &Client{logger: slog.Default(), cfg: DefaultClientConfig(), client: &mockPullerServiceClient{subscribeFunc: func(_ context.Context, req *pullerv1.SubscribeRequest, _ ...grpc.CallOption) (pullerv1.PullerService_SubscribeClient, error) {
					calls++
					require.True(t, req.RequireReady)
					require.Equal(t, "applied-progress", req.After)
					if phase == "admission" {
						return nil, tc.err
					}
					return &mockSubscribeClient{failError: tc.err}, nil
				}}}
				out := make(chan *events.PullerEvent, 1)
				c.subscribeLoop(ctx, "indexer", "applied-progress", false, out, nil, true)
				require.Equal(t, 1, calls)
				failure := <-out
				require.NotNil(t, failure)
				require.ErrorIs(t, failure.Error, tc.err)
				require.Equal(t, tc.retryable, failure.Retryable)
				require.Empty(t, failure.Progress)
				_, open := <-out
				require.False(t, open)
			})
		}
	}
}

func TestVerifiedClientRejectsMalformedControlAndPayloadBeforeReady(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		frame   *pullerv1.PullerEvent
		message string
	}{
		{"mixed-ready", &pullerv1.PullerEvent{Ready: true, Progress: "unapplied", ChangeEvent: &pullerv1.ChangeEvent{}}, "invalid ready frame"},
		{"empty-ready", &pullerv1.PullerEvent{Ready: true}, "no replay boundary"},
		{"corrupt-payload", &pullerv1.PullerEvent{Progress: "unapplied", ChangeEvent: &pullerv1.ChangeEvent{FullDoc: []byte("invalid BSON")}}, "decode puller full document"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			c := &Client{logger: slog.Default(), client: &mockPullerServiceClient{subscribeFunc: func(context.Context, *pullerv1.SubscribeRequest, ...grpc.CallOption) (pullerv1.PullerService_SubscribeClient, error) {
				return &mockSubscribeClient{events: []*pullerv1.PullerEvent{tc.frame}, failAfter: -1}, nil
			}}}
			var received []*events.PullerEvent
			for evt := range c.SubscribeReady(ctx, "indexer", "applied", func(string) { t.Error("malformed frame announced readiness") }) {
				received = append(received, evt)
			}
			require.Len(t, received, 1)
			require.ErrorContains(t, received[0].Error, tc.message)
			require.False(t, received[0].Retryable)
			require.Empty(t, received[0].Progress)
		})
	}
}

func TestVerifiedClientWaitsForReadyFrameAndStopsOnInvalidBoundary(t *testing.T) {
	t.Parallel()
	for _, valid := range []bool{false, true} {
		t.Run(map[bool]string{false: "invalid", true: "ready"}[valid], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			ready := false
			c, err := New("localhost:1", nil)
			require.NoError(t, err)
			defer c.Close()
			c.client = &mockPullerServiceClient{subscribeFunc: func(context.Context, *pullerv1.SubscribeRequest, ...grpc.CallOption) (pullerv1.PullerService_SubscribeClient, error) {
				require.False(t, ready)
				if !valid {
					return &mockSubscribeClient{failAfter: 0, failError: status.Error(codes.FailedPrecondition, "expired")}, nil
				}
				return &mockSubscribeClient{events: []*pullerv1.PullerEvent{{Ready: true, Progress: "complete"}}, failAfter: -1}, nil
			}}
			out := make(chan *events.PullerEvent, 1)
			c.subscribeLoop(ctx, "indexer", "boundary", false, out, func(progress string) { require.Equal(t, "complete", progress); ready = true; cancel() }, true)
			require.Equal(t, valid, ready)
			if valid {
				require.Len(t, out, 1)
				require.True(t, (<-out).Ready)
			} else {
				require.Len(t, out, 1)
				failure := <-out
				require.Error(t, failure.Error)
				require.False(t, failure.Retryable)
			}
		})
	}
}
