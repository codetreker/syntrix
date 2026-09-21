package realtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/gateway/config"
)

func TestReplicaEntryRejectsBeforeUpgradeAndReturnsCapacity(t *testing.T) {
	for _, name := range []string{"query token", "invalid configuration", "missing database", "missing budget", "connection capacity", "plain HTTP"} {
		t.Run(name, func(t *testing.T) {
			cfg := config.RealtimeConfig{Replica: config.DefaultReplicaConfig()}
			cfg.Replica.Connections = 1
			server := NewServer(nil, nil, "documents", &replicaIntegrationAuth{}, cfg)
			server.SetDatabaseService(&replicaIntegrationDatabase{identity: replicaIntegrationIdentity})
			path, expected := "/realtime/ws?mode=replica-data", http.StatusServiceUnavailable
			switch name {
			case "query token":
				path += "&access_token=forbidden"
				expected = http.StatusUnauthorized
			case "invalid configuration":
				server.cfg.Replica.ReadConcurrency = -1
				expected = http.StatusInternalServerError
			case "missing database":
				server.SetDatabaseService(nil)
			case "missing budget":
				server.replicaBudget = nil
			case "connection capacity":
				release, ok := server.replicaBudget.tryConnection()
				require.True(t, ok)
				defer release()
			case "plain HTTP":
				expected = http.StatusBadRequest
			}
			response := httptest.NewRecorder()
			server.serveReplica(response, httptest.NewRequest(http.MethodGet, path, nil))
			require.Equal(t, expected, response.Code)
			if name == "connection capacity" {
				require.Equal(t, "1", response.Header().Get("Retry-After"))
				require.EqualValues(t, 1, server.replicaBudget.snapshot().Connections)
			} else if server.replicaBudget != nil {
				require.Zero(t, server.replicaBudget.snapshot().Connections)
			}
		})
	}
}

func TestReplicaEntryOriginAndUnavailableBackendReleaseUpgradedOwner(t *testing.T) {
	for _, origin := range []string{"https://untrusted.example", ""} {
		t.Run(origin, func(t *testing.T) {
			server := NewServer(nil, nil, "documents", &replicaIntegrationAuth{}, config.RealtimeConfig{Replica: config.DefaultReplicaConfig()})
			server.SetDatabaseService(&replicaIntegrationDatabase{identity: replicaIntegrationIdentity})
			httpServer := httptest.NewServer(http.HandlerFunc(server.serveReplica))
			defer httpServer.Close()
			headers := http.Header{}
			if origin != "" {
				headers.Set("Origin", origin)
			}
			peer, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http"), headers)
			if origin != "" {
				require.Error(t, err)
				require.Equal(t, http.StatusForbidden, response.StatusCode)
				response.Body.Close()
			} else {
				require.NoError(t, err)
				defer peer.Close()
				require.NoError(t, peer.SetReadDeadline(time.Now().Add(time.Second)))
				_, _, err = peer.ReadMessage()
				require.Error(t, err, "an upgraded connection cannot outlive absent backend ownership")
			}
			require.Eventually(t, func() bool { return server.replicaBudget.snapshot().Connections == 0 }, time.Second, time.Millisecond)
		})
	}
}

func TestReplicaReadPumpRejectsMalformedOrBinaryFramesAndHandlesPong(t *testing.T) {
	for _, kind := range []string{"malformed text", "binary", "pong then auth"} {
		t.Run(kind, func(t *testing.T) {
			env := newReplicaIntegrationEnv(t, func(context.Context, string, storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
				t.Error("control frames must not start a Query")
				return nil, context.Canceled
			})
			peer := env.dial(t)
			if kind == "pong then auth" {
				require.NoError(t, peer.conn.WriteControl(websocket.PongMessage, []byte("alive"), time.Now().Add(time.Second)))
				peer.authenticate(t, "owner")
				return
			}
			messageType, payload := websocket.TextMessage, []byte(`{"type":`)
			if kind == "binary" {
				messageType = websocket.BinaryMessage
				payload = []byte(`{"id":"auth","type":"auth","payload":{"token":"owner","database":"app","mode":"replica-data"}}`)
			}
			require.NoError(t, peer.conn.WriteMessage(messageType, payload))
			replicaIntegrationError(t, peer.receive(t, TypeError), "REPLICATION_PROTOCOL_ERROR", "", "")
			select {
			case <-peer.done:
			case <-time.After(time.Second):
				t.Fatal("fatal protocol response did not close its owner")
			}
			require.Eventually(t, func() bool { return env.server.replicaBudget.snapshot().Connections == 0 }, time.Second, time.Millisecond)
		})
	}
}

func replicaEntrySocketPair(t *testing.T) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	connections := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err == nil {
			connections <- conn
		}
	}))
	t.Cleanup(server.Close)
	peer, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = peer.Close() })
	select {
	case socket := <-connections:
		t.Cleanup(func() { _ = socket.Close() })
		return socket, peer
	case <-time.After(time.Second):
		t.Fatal("socket pair did not upgrade")
		return nil, nil
	}
}

func replicaEntryClient(t *testing.T) (*replicaClient, *websocket.Conn) {
	t.Helper()
	socket, peer := replicaEntrySocketPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	client := &replicaClient{ctx: ctx, cancel: cancel, conn: socket, authGen: 2,
		subs: make(map[string]*replicaSubscription), control: make(chan *replicaOutbound, 2), data: make(chan *replicaOutbound, 1)}
	t.Cleanup(client.close)
	return client, peer
}

func TestReplicaControlRejectsStaleClosedAndOversizedFrames(t *testing.T) {
	t.Run("stale generation does not close the current owner", func(t *testing.T) {
		client, _ := replicaEntryClient(t)
		require.False(t, client.sendControl(&replicaOutbound{gen: 1, data: []byte(`{"type":"heartbeat"}`)}))
		require.NoError(t, client.ctx.Err())
		require.Empty(t, client.control)
		require.True(t, client.sendControl(&replicaOutbound{gen: 2, data: []byte(`{"type":"heartbeat"}`)}))
		client.close()
		require.False(t, client.sendControl(&replicaOutbound{gen: 2, data: []byte(`{"type":"heartbeat"}`)}))
	})
	t.Run("oversized control retires the actual socket", func(t *testing.T) {
		client, peer := replicaEntryClient(t)
		require.False(t, client.sendControl(&replicaOutbound{gen: 2, data: make([]byte, replicaEnvelopeBytes+1)}))
		require.ErrorIs(t, client.ctx.Err(), context.Canceled)
		require.Empty(t, client.control)
		require.NoError(t, peer.SetReadDeadline(time.Now().Add(time.Second)))
		_, _, err := peer.ReadMessage()
		require.Error(t, err)
	})
}

func TestReplicaWriterHeartbeatAndPingFailureOwnTheirLifetime(t *testing.T) {
	for _, closedSocket := range []bool{false, true} {
		t.Run(map[bool]string{false: "live heartbeat and ping", true: "closed socket ping fails"}[closedSocket], func(t *testing.T) {
			client, peer := replicaEntryClient(t)
			previousPing, previousHeartbeat := pingPeriod, heartbeatInterval
			pingPeriod, heartbeatInterval = 5*time.Millisecond, 7*time.Millisecond
			if closedSocket {
				heartbeatInterval = time.Hour
				require.NoError(t, client.conn.Close())
			}
			defer func() {
				client.close()
				client.wg.Wait()
				pingPeriod, heartbeatInterval = previousPing, previousHeartbeat
			}()
			pings := make(chan struct{}, 1)
			peer.SetPingHandler(func(string) error {
				select {
				case pings <- struct{}{}:
				default:
				}
				return nil
			})
			client.wg.Add(1)
			go client.writePump()
			if closedSocket {
				select {
				case <-client.ctx.Done():
				case <-time.After(time.Second):
					t.Fatal("failed ping did not retire the writer")
				}
			} else {
				require.NoError(t, peer.SetReadDeadline(time.Now().Add(time.Second)))
				for {
					var frame BaseMessage
					require.NoError(t, peer.ReadJSON(&frame))
					require.Equal(t, TypeHeartbeat, frame.Type)
					if len(pings) > 0 {
						break
					}
				}
			}
		})
	}
}
