package grpc

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	pullerv1 "github.com/syntrixbase/syntrix/api/gen/puller/v1"
	"github.com/syntrixbase/syntrix/internal/puller/config"
	"github.com/syntrixbase/syntrix/internal/puller/events"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestNewServer_AdmissionLimit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ configured, effective int }{{0, 100}, {-1, 100}, {2, 2}} {
		t.Run(fmt.Sprint(tc.configured), func(t *testing.T) {
			srv := NewServer(config.GRPCConfig{MaxConnections: tc.configured}, &mockEventSource{}, nil)
			t.Cleanup(srv.cancel)
			require.Equal(t, tc.effective, srv.cfg.MaxConnections)
		})
	}
}

func TestServer_AdmissionConcurrentLimit(t *testing.T) {
	t.Parallel()
	for _, label := range []string{"shared-consumer", ""} {
		t.Run(fmt.Sprintf("consumer=%q", label), func(t *testing.T) {
			const limit, requests = 3, 24
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			entered := make(chan struct{}, requests)
			source := &controllableEventSource{replayFunc: func(ctx context.Context, _ map[string]string, _ bool) (events.Iterator, error) {
				entered <- struct{}{}
				<-ctx.Done()
				return &mockIterator{}, nil
			}}
			srv := NewServer(config.GRPCConfig{MaxConnections: limit, ChannelSize: 1}, source, nil)
			t.Cleanup(srv.cancel)
			start := make(chan struct{})
			results := make(chan error, requests)
			for range requests {
				go func() {
					<-start
					results <- srv.Subscribe(&pullerv1.SubscribeRequest{
						ConsumerId: label, After: makeProgressMarker("backend", "1-1-event"),
					}, &mockSubscribeServer{ctx: ctx})
				}()
			}
			close(start)
			accepted, rejected := 0, 0
			for range requests {
				select {
				case <-entered:
					accepted++
				case err := <-results:
					require.Equal(t, codes.ResourceExhausted, status.Code(err))
					rejected++
				case <-ctx.Done():
					t.Fatal("concurrent admission did not finish")
				}
			}
			require.Equal(t, limit, accepted, "rejected requests must not start replay")
			require.Equal(t, requests-limit, rejected)
			require.Equal(t, limit, srv.SubscriberCount())
			cancel()
			for range accepted {
				select {
				case err := <-results:
					require.NoError(t, err)
				case <-time.After(time.Second):
					t.Fatal("canceled subscription did not release its slot")
				}
			}
			assertAdmissionSlotReusable(t, srv)
		})
	}
}

func assertAdmissionSlotReusable(t *testing.T, srv *Server) {
	t.Helper()
	require.Zero(t, srv.SubscriberCount())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- srv.Subscribe(&pullerv1.SubscribeRequest{ConsumerId: "replacement"}, &mockSubscribeServer{ctx: ctx})
	}()
	require.Eventually(t, func() bool { return srv.SubscriberCount() == 1 }, time.Second, time.Millisecond)
	cancel()
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("replacement subscription did not exit")
	}
	require.Zero(t, srv.SubscriberCount())
}

func TestServer_AdmissionValidationAndCancellation(t *testing.T) {
	t.Parallel()
	srv := NewServer(config.GRPCConfig{MaxConnections: 1}, &mockEventSource{}, nil)
	t.Cleanup(srv.cancel)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- srv.Subscribe(&pullerv1.SubscribeRequest{}, &mockSubscribeServer{ctx: ctx})
	}()
	require.Eventually(t, func() bool { return srv.SubscriberCount() == 1 }, time.Second, time.Millisecond)
	err := srv.Subscribe(&pullerv1.SubscribeRequest{After: "!invalid!"}, &mockSubscribeServer{ctx: ctx})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	canceledCtx, stop := context.WithCancel(ctx)
	stop()
	require.NoError(t, srv.Subscribe(&pullerv1.SubscribeRequest{}, &mockSubscribeServer{ctx: canceledCtx}))
	require.Equal(t, 1, srv.SubscriberCount())
	cancel()
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("subscription did not exit")
	}
	require.Zero(t, srv.SubscriberCount())
}

func TestServer_AdmissionShutdown(t *testing.T) {
	t.Parallel()
	for range 20 {
		srv := NewServer(config.GRPCConfig{MaxConnections: 1, ChannelSize: 1}, &mockEventSource{}, nil)
		srv.Init()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		start := make(chan struct{})
		result := make(chan error, 1)
		stopped := make(chan struct{})
		go func() {
			<-start
			result <- srv.Subscribe(&pullerv1.SubscribeRequest{}, &mockSubscribeServer{ctx: ctx})
		}()
		go func() {
			<-start
			srv.Shutdown()
			close(stopped)
		}()
		close(start)
		select {
		case err := <-result:
			require.Contains(t, []codes.Code{codes.Canceled, codes.Unavailable}, status.Code(err))
		case <-ctx.Done():
			t.Fatal("subscription survived server shutdown")
		}
		select {
		case <-stopped:
		case <-ctx.Done():
			t.Fatal("server shutdown did not finish")
		}
		err := srv.Subscribe(&pullerv1.SubscribeRequest{}, &mockSubscribeServer{ctx: ctx})
		require.Equal(t, codes.Unavailable, status.Code(err))
		require.Zero(t, srv.SubscriberCount())
		cancel()
	}
}

func TestServer_AdmissionShutdownBeforeInit(t *testing.T) {
	t.Parallel()
	srv := NewServer(config.GRPCConfig{MaxConnections: 1}, &mockEventSource{}, nil)
	t.Cleanup(srv.cancel)
	srv.Shutdown()
	assertAdmissionSlotReusable(t, srv)
}

func TestServer_AdmissionMultipleStreamsOneConnection(t *testing.T) {
	t.Parallel()
	source := &mockEventSource{}
	srv := NewServer(config.GRPCConfig{MaxConnections: 1, ChannelSize: 1, HeartbeatInterval: time.Hour}, source, nil)
	srv.Init()
	t.Cleanup(srv.Shutdown)
	listener := bufconn.Listen(1024 * 1024)
	transport := grpc.NewServer()
	pullerv1.RegisterPullerServiceServer(transport, srv)
	serveResult := make(chan error, 1)
	go func() { serveResult <- transport.Serve(listener) }()
	t.Cleanup(func() {
		transport.Stop()
		require.NoError(t, listener.Close())
		select {
		case err := <-serveResult:
			require.NoError(t, err)
		case <-time.After(time.Second):
			t.Error("gRPC transport did not stop")
		}
	})
	var connections atomic.Int32
	conn, err := grpc.NewClient("passthrough:///admission", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			connections.Add(1)
			return listener.DialContext(ctx)
		}))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	client := pullerv1.NewPullerServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	firstCtx, cancelFirst := context.WithCancel(ctx)
	defer cancelFirst()
	first, err := client.Subscribe(firstCtx, &pullerv1.SubscribeRequest{ConsumerId: "shared"})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return srv.SubscriberCount() == 1 }, time.Second, time.Millisecond)
	require.NoError(t, source.EmitEvent(ctx, "backend", &events.StoreChangeEvent{EventID: "one", ClusterTime: events.ClusterTime{T: 1}}))
	message, err := first.Recv()
	require.NoError(t, err)
	require.Equal(t, "one", message.GetChangeEvent().GetEventId())
	second, err := client.Subscribe(ctx, &pullerv1.SubscribeRequest{ConsumerId: "shared"})
	require.NoError(t, err)
	_, err = second.Recv()
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
	require.Equal(t, 1, srv.SubscriberCount())
	cancelFirst()
	require.Eventually(t, func() bool { return srv.SubscriberCount() == 0 }, time.Second, time.Millisecond)
	replacement, err := client.Subscribe(ctx, &pullerv1.SubscribeRequest{ConsumerId: "shared"})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return srv.SubscriberCount() == 1 }, time.Second, time.Millisecond)
	require.NoError(t, source.EmitEvent(ctx, "backend", &events.StoreChangeEvent{EventID: "two", ClusterTime: events.ClusterTime{T: 2}}))
	message, err = replacement.Recv()
	require.NoError(t, err)
	require.Equal(t, "two", message.GetChangeEvent().GetEventId())
	require.EqualValues(t, 1, connections.Load())
	cancel()
	require.Eventually(t, func() bool { return srv.SubscriberCount() == 0 }, time.Second, time.Millisecond)
}
