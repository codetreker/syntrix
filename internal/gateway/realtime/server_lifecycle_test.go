package realtime

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	gatewayconfig "github.com/syntrixbase/syntrix/internal/gateway/config"
	"github.com/syntrixbase/syntrix/internal/streamer"
	"github.com/syntrixbase/syntrix/pkg/model"
)

type supervisedTestStream struct {
	*lifecycleStream
	stop chan struct{}
	fail chan error
	once sync.Once
}

func newSupervisedTestStream() *supervisedTestStream {
	return &supervisedTestStream{lifecycleStream: newLifecycleStream(), stop: make(chan struct{}), fail: make(chan error, 1)}
}
func (s *supervisedTestStream) Recv() (*streamer.EventDelivery, error) {
	select {
	case <-s.stop:
		return nil, io.EOF
	case err := <-s.fail:
		return nil, err
	}
}
func (s *supervisedTestStream) Close() error {
	s.once.Do(func() {
		s.closed.Add(1)
		s.mu.Lock()
		close(s.changed)
		s.changed = make(chan struct{})
		s.status = streamer.StreamStatus{State: streamer.StateDisconnected, Generation: 2, Terminal: true, Changed: s.changed}
		s.mu.Unlock()
		close(s.stop)
	})
	return nil
}

type sequenceStreamService struct {
	streamer.Service
	first, second streamer.Stream
	calls         atomic.Int32
}

func (s *sequenceStreamService) Stream(ctx context.Context) (streamer.Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch s.calls.Add(1) {
	case 1:
		return s.first, nil
	case 2:
		return s.second, nil
	default:
		return nil, errors.New("unexpected stream creation")
	}
}

func TestServerReplacesTerminalStreamAndClosesAllOldClients(t *testing.T) {
	first, second := newSupervisedTestStream(), newSupervisedTestStream()
	service := &sequenceStreamService{first: first, second: second}
	server := NewServer(&stubQuery{}, service, "", nil, gatewayconfig.RealtimeConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, server.StartBackgroundTasks(ctx))
	ordinary := &Client{hub: server.hub, send: make(chan BaseMessage, 1), subscriptions: make(map[string]Subscription), streamerSubIDs: make(map[string]hubRegistration)}
	require.True(t, server.hub.Register(ordinary))
	var retired atomic.Int32
	_, ok := server.hub.RegisterReplicaConnection(func() { retired.Add(1) })
	require.True(t, ok)
	first.fail <- errors.New("backend receive ended")
	require.Eventually(t, func() bool { return retired.Load() == 1 }, time.Second, time.Millisecond)
	select {
	case <-ordinary.outboundDone():
	case <-time.After(time.Second):
		t.Fatal("ordinary connection stayed alive after backend replacement")
	}
	require.Eventually(t, func() bool {
		server.hub.streamMu.Lock()
		defer server.hub.streamMu.Unlock()
		return server.hub.stream == second
	}, 3*time.Second, time.Millisecond)
	release, err := server.hub.SubscribeReplica(ctx, "app", "users", func() {}, func(error) {})
	require.NoError(t, err)
	release()
	cancel()
	require.Eventually(t, func() bool { return second.closed.Load() == 1 }, time.Second, time.Millisecond)
	require.EqualValues(t, 2, service.calls.Load())
}

func TestRetiredLegacyWebSocketDiscardsQueuedFrames(t *testing.T) {
	hub := NewHub()
	hub.SetStream(newLifecycleStream())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go hub.Run(ctx)
	ready, begin, done := make(chan *Client, 1), make(chan struct{}), make(chan struct{})
	var startOnce sync.Once
	start := func() { startOnce.Do(func() { close(begin) }) }
	t.Cleanup(start)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		client := &Client{hub: hub, conn: conn, send: make(chan BaseMessage, 2)}
		if !hub.Register(client) {
			_ = conn.Close()
			return
		}
		client.send <- BaseMessage{Type: TypeHeartbeat}
		ready <- client
		go func() { defer close(done); <-begin; client.writePump() }()
	}))
	t.Cleanup(server.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	client := <-ready
	require.Eventually(t, func() bool { hub.mu.RLock(); defer hub.mu.RUnlock(); return hub.clients[client] }, time.Second, time.Millisecond)
	hub.SetStream(newLifecycleStream())
	start()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
	_, _, err = conn.ReadMessage()
	require.Error(t, err)
	var timeout net.Error
	if errors.As(err, &timeout) {
		require.False(t, timeout.Timeout(), "retirement must close the socket")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("retired writer did not exit")
	}
}

type blockedSSEWriter struct {
	header               http.Header
	entered, interrupted chan struct{}
	enterOnce, stopOnce  sync.Once
}

func (w *blockedSSEWriter) Header() http.Header { return w.header }
func (*blockedSSEWriter) WriteHeader(int)       {}
func (*blockedSSEWriter) Flush()                {}
func (w *blockedSSEWriter) SetWriteDeadline(deadline time.Time) error {
	if !deadline.After(time.Now()) {
		w.stopOnce.Do(func() { close(w.interrupted) })
	}
	return nil
}
func (w *blockedSSEWriter) Write(data []byte) (int, error) {
	if strings.HasPrefix(string(data), "data:") {
		w.enterOnce.Do(func() { close(w.entered) })
		<-w.interrupted
		return 0, context.DeadlineExceeded
	}
	return len(data), nil
}

func TestRetiredLegacySSEInterruptsBlockedWrite(t *testing.T) {
	hub := NewHub()
	hub.SetStream(newLifecycleStream())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go hub.Run(ctx)
	writer := &blockedSSEWriter{header: make(http.Header), entered: make(chan struct{}), interrupted: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		request := httptest.NewRequest(http.MethodGet, "/realtime/sse?database=app&collection=users", nil).WithContext(ctx)
		ServeSSE(hub, &stubQuery{}, nil, gatewayconfig.RealtimeConfig{}, writer, request)
	}()
	var subID string
	require.Eventually(t, func() bool {
		hub.subscriptionsMu.RLock()
		defer hub.subscriptionsMu.RUnlock()
		for id := range hub.subscriptions {
			subID = id
			return true
		}
		return false
	}, time.Second, time.Millisecond)
	hub.BroadcastDelivery(&streamer.EventDelivery{SubscriptionIDs: []string{subID}, Event: &streamer.Event{
		Database: "app", Collection: "users", DocumentID: "alice", Operation: streamer.OperationUpdate,
		Document: model.Document{"id": "alice"},
	}})
	select {
	case <-writer.entered:
	case <-time.After(time.Second):
		t.Fatal("SSE did not enter its network write")
	}
	hub.SetStream(newLifecycleStream())
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SSE write was not interrupted by retirement")
	}
}

func TestReplicaModeSelectionRejectsAmbiguousModeBeforeUpgrade(t *testing.T) {
	server := NewServer(&stubQuery{}, nil, "", nil, gatewayconfig.RealtimeConfig{})
	for _, path := range []string{"/?mode=", "/?mode=other", "/?mode=&mode=replica-data", "/?mode=replica-data&mode=replica-data"} {
		response := httptest.NewRecorder()
		server.HandleWS(response, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusBadRequest, response.Code)
	}
}
