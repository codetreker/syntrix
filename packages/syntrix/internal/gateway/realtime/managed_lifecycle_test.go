package realtime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	gatewayconfig "github.com/codetreker/syntrix/internal/gateway/config"
	"github.com/codetreker/syntrix/internal/identity"
	"github.com/codetreker/syntrix/internal/streamer"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

type streamServiceFunc func(context.Context) (streamer.Stream, error)

func (f streamServiceFunc) Stream(ctx context.Context) (streamer.Stream, error) { return f(ctx) }

type readyStreamService struct {
	stream          streamer.Stream
	mu              sync.Mutex
	ready, terminal error
}

func (s *readyStreamService) Stream(context.Context) (streamer.Stream, error) { return s.stream, nil }
func (s *readyStreamService) Readiness(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ready
}
func (s *readyStreamService) TerminalError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminal
}

func TestRealtimeReadinessTracksActiveReceiverAndDependencyRecovery(t *testing.T) {
	stream := newSupervisedTestStream()
	dependency := &readyStreamService{stream: stream, ready: errors.New("not admitted")}
	s := NewServer(&stubQuery{}, dependency, "", nil, gatewayconfig.RealtimeConfig{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.Error(t, s.Readiness(ctx))
	require.NoError(t, s.StartBackgroundTasks(ctx))
	require.Error(t, s.Readiness(ctx))
	dependency.mu.Lock()
	dependency.ready = nil
	dependency.mu.Unlock()
	require.Eventually(t, func() bool { return s.Readiness(ctx) == nil }, time.Second, time.Millisecond)
	stream.transition(streamer.StateReconnecting, 2)
	require.Error(t, s.Readiness(ctx))
	stream.transition(streamer.StateConnected, 3)
	require.NoError(t, s.Readiness(ctx))
	fatal := errors.New("terminal ingestion failure")
	dependency.mu.Lock()
	dependency.terminal = fatal
	dependency.mu.Unlock()
	require.ErrorIs(t, s.TerminalError(), fatal)
	stream.fail <- fatal
	require.Eventually(t, func() bool { s.lifecycleMu.Lock(); defer s.lifecycleMu.Unlock(); return errors.Is(s.terminal, fatal) }, time.Second, time.Millisecond)
	require.ErrorIs(t, s.TerminalError(), fatal)
	require.NoError(t, s.Shutdown(ctx))
	require.Error(t, s.Readiness(ctx))
	require.Error(t, s.StartBackgroundTasks(ctx))
	w := httptest.NewRecorder()
	s.HandleWS(w, httptest.NewRequest("GET", "/realtime/ws", nil))
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	w = httptest.NewRecorder()
	s.HandleSSE(w, httptest.NewRequest("GET", "/realtime/sse", nil))
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestRealtimeFailedSetupCanRetryAndCanceledSetupOpensNoTasks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream := newSupervisedTestStream()
	calls := 0
	s := NewServer(nil, streamServiceFunc(func(context.Context) (streamer.Stream, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("temporary")
		}
		return stream, nil
	}), "", nil, gatewayconfig.RealtimeConfig{})
	require.Error(t, s.StartBackgroundTasks(ctx))
	require.NoError(t, s.StartBackgroundTasks(ctx))
	require.NoError(t, s.Shutdown(ctx))
	require.NoError(t, s.Shutdown(ctx))
	require.Equal(t, 2, calls)

	entered := make(chan struct{})
	late := newSupervisedTestStream()
	s = NewServer(nil, streamServiceFunc(func(ctx context.Context) (streamer.Stream, error) { close(entered); <-ctx.Done(); return late, nil }), "", nil, gatewayconfig.RealtimeConfig{})
	result := make(chan error, 1)
	go func() { result <- s.StartBackgroundTasks(ctx) }()
	<-entered
	require.NoError(t, s.Shutdown(ctx))
	require.ErrorIs(t, <-result, context.Canceled)
	require.EqualValues(t, 1, late.closed.Load())
	require.Nil(t, s.hub.stream)
	require.Error(t, s.Readiness(ctx))
	require.NoError(t, NewServer(nil, nil, "", nil, gatewayconfig.RealtimeConfig{}).Shutdown(ctx))
}

func TestRealtimeShutdownWaitsForReceiveAndOwnedCleanup(t *testing.T) {
	stream := newSupervisedTestStream()
	s := NewServer(nil, streamServiceFunc(func(context.Context) (streamer.Stream, error) { return stream, nil }), "", nil, gatewayconfig.RealtimeConfig{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, s.StartBackgroundTasks(ctx))
	entered, finish := make(chan struct{}), make(chan struct{})
	stream.unsubscribe = func(string) error { close(entered); <-finish; return nil }
	s.hub.cleanupReplicaRegistration(s.hub.streamOwner, "owned")
	<-entered
	short, stop := context.WithTimeout(ctx, 10*time.Millisecond)
	defer stop()
	require.ErrorIs(t, s.Shutdown(short), context.DeadlineExceeded)
	require.False(t, s.hub.startCleanup(func() { t.Error("cleanup admitted after shutdown") }))
	close(finish)
	require.NoError(t, s.Shutdown(ctx))
	require.EqualValues(t, 1, stream.closed.Load())
	canceled, abort := context.WithCancel(ctx)
	abort()
	require.ErrorIs(t, s.Readiness(canceled), context.Canceled)
}

func TestRealtimeEveryTransportRejectsPortSchemeAndOpaqueOrigin(t *testing.T) {
	s := NewServer(nil, nil, "", nil, gatewayconfig.RealtimeConfig{AllowedOrigins: []string{"https://app.example:8443"}})
	for _, path := range []string{"/realtime/ws", "/realtime/ws?mode=replica", "/realtime/sse"} {
		for _, value := range []string{"http://api.example:3000", "https://api.example:8080", "https://app.example:8444", "null", "http://user:password@api.example:8080"} {
			r := httptest.NewRequest("GET", "http://api.example:8080"+path, nil)
			r.Header.Set("Origin", value)
			w := httptest.NewRecorder()
			if path == "/realtime/sse" {
				s.HandleSSE(w, r)
			} else {
				s.HandleWS(w, r)
			}
			require.Equal(t, http.StatusForbidden, w.Code, path+" "+value)
		}
	}
}

func TestRealtimeFailedStartupNeverUpgradesUnownedSocket(t *testing.T) {
	s := NewServer(nil, &mockStreamerError{}, "", nil, gatewayconfig.RealtimeConfig{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.Error(t, s.StartBackgroundTasks(ctx))
	endpoint := httptest.NewServer(http.HandlerFunc(s.HandleWS))
	defer endpoint.Close()
	_, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(endpoint.URL, "http"), nil)
	require.Error(t, err)
	require.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
	require.NoError(t, response.Body.Close())
	require.NoError(t, s.Shutdown(ctx))
}

func TestRealtimeFailedStartupRejectsAuthenticatedSSE(t *testing.T) {
	s := NewServer(nil, &mockStreamerError{}, "", &mockAuthService{}, gatewayconfig.RealtimeConfig{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.Error(t, s.StartBackgroundTasks(ctx))
	r := httptest.NewRequest("GET", "http://example.com/realtime/sse", nil)
	r.Header.Set("Authorization", "Bearer good")
	r.Header.Set("Origin", "http://example.com")
	w := httptest.NewRecorder()
	s.HandleSSE(w, r)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.NoError(t, s.Shutdown(ctx))
}

func TestRealtimeConfiguredExternalOriginAcceptedByBothWebSocketModes(t *testing.T) {
	for _, mode := range []string{"", ReplicaDataMode} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			stream := newSupervisedTestStream()
			s := NewServer(nil, streamServiceFunc(func(context.Context) (streamer.Stream, error) { return stream, nil }), "", nil, gatewayconfig.RealtimeConfig{AllowedOrigins: []string{"https://app.example:8443"}})
			s.SetDatabaseService(&replicaIntegrationDatabase{identity: replicaIntegrationIdentity})
			require.NoError(t, s.StartBackgroundTasks(ctx))
			require.Eventually(t, func() bool { return s.Readiness(ctx) == nil }, time.Second, time.Millisecond)
			require.NoError(t, s.TerminalError())
			endpoint := httptest.NewServer(http.HandlerFunc(s.HandleWS))
			defer endpoint.Close()
			url := "ws" + strings.TrimPrefix(endpoint.URL, "http") + "/realtime/ws"
			if mode != "" {
				url += "?mode=" + mode
			}
			peer, _, err := websocket.DefaultDialer.Dial(url, http.Header{"Origin": []string{"https://app.example:8443"}})
			require.NoError(t, err)
			defer peer.Close()
			require.NoError(t, s.Shutdown(ctx))
			require.NoError(t, peer.SetReadDeadline(time.Now().Add(time.Second)))
			_, _, err = peer.ReadMessage()
			require.Error(t, err)
		})
	}
}

func TestRealtimeRejectsInvalidConfigurationBeforeBackendEffects(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, cfg := range []gatewayconfig.RealtimeConfig{
		{Replica: gatewayconfig.ReplicaConfig{Connections: -1}},
		{AllowedOrigins: []string{"*"}},
	} {
		s := NewServer(nil, streamServiceFunc(func(context.Context) (streamer.Stream, error) {
			t.Fatal("invalid configuration opened backend")
			return nil, nil
		}), "", nil, cfg)
		require.Error(t, s.StartBackgroundTasks(ctx))
	}
	s := NewServer(nil, nil, "", nil, gatewayconfig.RealtimeConfig{Replica: gatewayconfig.ReplicaConfig{Connections: -1}})
	w := httptest.NewRecorder()
	s.HandleWS(w, httptest.NewRequest("GET", "/realtime/ws?mode="+ReplicaDataMode, nil))
	require.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestRealtimeShutdownWaitsForAdmittedAuthentication(t *testing.T) {
	entered, finish := make(chan struct{}), make(chan struct{})
	verifier, err := identity.NewVerifier(func(string) (*identity.Claims, error) { close(entered); <-finish; return nil, identity.ErrInvalidToken })
	require.NoError(t, err)
	s := NewServer(nil, nil, "", verifier, gatewayconfig.RealtimeConfig{})
	r := httptest.NewRequest("GET", "http://example.com/realtime/sse", nil)
	r.Header.Set("Authorization", "Bearer test")
	r.Header.Set("Origin", "http://example.com")
	finished := make(chan struct{})
	go func() { defer close(finished); s.HandleSSE(httptest.NewRecorder(), r) }()
	<-entered
	short, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, s.Shutdown(short), context.DeadlineExceeded)
	close(finish)
	<-finished
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	require.NoError(t, s.Shutdown(ctx))
}
