package realtime

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/database"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	storagetypes "github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/gateway/config"
	"github.com/syntrixbase/syntrix/internal/gateway/replication"
	"github.com/syntrixbase/syntrix/internal/streamer"
	"github.com/syntrixbase/syntrix/pkg/model"
)

func replicaFailureClient(t *testing.T) *replicaClient {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err == nil {
			accepted <- conn
		}
	}))
	peer, _, err := websocket.DefaultDialer.Dial(strings.Replace(httpServer.URL, "http", "ws", 1), nil)
	require.NoError(t, err)
	var socket *websocket.Conn
	select {
	case socket = <-accepted:
	case <-time.After(time.Second):
		t.Fatal("socket upgrade did not finish")
	}
	ctx, cancel := context.WithCancel(context.Background())
	server := NewServer(&MockQueryService{}, &mockStreamerStream{}, "documents", &replicaIntegrationAuth{}, config.RealtimeConfig{Replica: config.DefaultReplicaConfig()})
	server.SetDatabaseService(&replicaIntegrationDatabase{identity: replicaIntegrationIdentity})
	server.hub.SetStream(&mockStreamerStream{})
	c := &replicaClient{server: server, conn: socket, ctx: ctx, cancel: cancel, authenticated: true, authGen: 1,
		principal: replication.Principal{Subject: "alice"}, database: "app", subs: make(map[string]*replicaSubscription), seen: make(map[string]struct{}),
		control: make(chan *replicaOutbound, 32), data: make(chan *replicaOutbound, 4)}
	t.Cleanup(func() { c.close(); c.wg.Wait(); server.hub.SetStream(nil); _ = peer.Close(); httpServer.Close() })
	return c
}

func replicaFailureFrame(t *testing.T, c *replicaClient) BaseMessage {
	t.Helper()
	select {
	case frame := <-c.control:
		var message BaseMessage
		require.NoError(t, json.Unmarshal(frame.data, &message))
		return message
	case <-time.After(time.Second):
		t.Fatal("control reply did not arrive")
		return BaseMessage{}
	}
}
func replicaFailureCode(t *testing.T, c *replicaClient, expected string) replicaErrorPayload {
	t.Helper()
	frame := replicaFailureFrame(t, c)
	require.Equal(t, TypeError, frame.Type)
	var payload replicaErrorPayload
	require.NoError(t, json.Unmarshal(frame.Payload, &payload))
	require.Equal(t, expected, payload.Code)
	return payload
}

func fillReplicaAuthorizationQueue(t *testing.T, budget *replicaBudget) {
	t.Helper()
	budget.mu.Lock()
	budget.cfg.AuthConcurrency = 1
	budget.cfg.PendingRegistrations = 1
	budget.mu.Unlock()
	release, err := budget.acquireAuth(context.Background())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		granted, err := budget.acquireAuth(ctx)
		if err == nil {
			granted()
		}
	}()
	require.Eventually(t, func() bool { return budget.snapshot().AuthWaiting == 1 }, time.Second, time.Millisecond)
	t.Cleanup(func() {
		cancel()
		release()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("authorization waiter did not exit")
		}
	})
}

func TestReplicaFrameEncodingDoesNotPublishUnsupportedPayload(t *testing.T) {
	encoded, err := encodeReplicaFrame("request", TypeReplicaPage, map[string]any{"unsupported": math.NaN()})
	require.Error(t, err)
	require.Nil(t, encoded)
}

func TestReplicaAuthorizationOverloadPreservesDirtyNotificationForRetry(t *testing.T) {
	c, sub, _ := replicaStateFixture(t)
	sub.identity = replicaIntegrationIdentity
	fillReplicaAuthorizationQueue(t, c.server.replicaBudget)
	releasePending, ok := c.server.replicaBudget.tryPending()
	require.True(t, ok)
	sub.flushing = true
	sub.dirty = false
	c.wg.Add(1)
	c.flushChanged(sub, "app", replication.Principal{Subject: "alice"}, releasePending)
	errorReply := replicaFailureCode(t, c, "REPLICATION_SOURCE_BUSY")
	require.Positive(t, errorReply.RetryAfter)
	require.True(t, sub.dirty)
	require.False(t, sub.flushing)
	require.False(t, sub.retired)
	require.Zero(t, c.server.replicaBudget.snapshot().Pending)
}

func TestReplicaCanceledNotificationDoesNotPublishFailure(t *testing.T) {
	c, sub, _ := replicaStateFixture(t)
	sub.cancel()
	c.wg.Add(1)
	release, ok := c.server.replicaBudget.tryPending()
	require.True(t, ok)
	c.flushChanged(sub, "app", replication.Principal{Subject: "alice"}, release)
	require.Empty(t, c.control)
	require.Zero(t, c.server.replicaBudget.snapshot().Pending)
	c.failure("read", sub, "read", 1, context.Canceled)
	require.Empty(t, c.control)
	c.failure("deadline", sub, "deadline", 1, context.DeadlineExceeded)
	replicaFailureCode(t, c, "DEADLINE_EXCEEDED")
}

func TestReplicaRegistrationAdmissionAndDeadlineFailuresReleaseWork(t *testing.T) {
	t.Run("pending capacity", func(t *testing.T) {
		c := replicaFailureClient(t)
		c.server.replicaBudget.cfg.PendingRegistrations = 1
		release, ok := c.server.replicaBudget.tryPending()
		require.True(t, ok)
		defer release()
		payload, err := json.Marshal(map[string]any{"collection": "users", "source": replicaIntegrationSource()})
		require.NoError(t, err)
		c.startSubscribe(BaseMessage{ID: "busy", Type: TypeSubscribe, Payload: payload}, 1)
		require.Positive(t, replicaFailureCode(t, c, "REPLICATION_SOURCE_BUSY").RetryAfter)
		snapshot := c.server.replicaBudget.snapshot()
		require.Zero(t, snapshot.Subscriptions)
		require.Zero(t, snapshot.SourceBytes)
		require.EqualValues(t, 1, snapshot.Pending)
	})
	t.Run("registration deadline", func(t *testing.T) {
		c := replicaFailureClient(t)
		entered := make(chan context.Context, 1)
		c.server.hub.SetStream(&replicaRegistrationFaultStream{mockStreamerStream: &mockStreamerStream{}, register: func(ctx context.Context, _, _ string, _ []model.Filter) (streamer.Registration, error) {
			entered <- ctx
			<-ctx.Done()
			return streamer.Registration{}, ctx.Err()
		}})
		payload, err := json.Marshal(map[string]any{"collection": "users", "source": replicaIntegrationSource()})
		require.NoError(t, err)
		c.startSubscribe(BaseMessage{ID: "expiring", Type: TypeSubscribe, Payload: payload}, 1)
		var backend context.Context
		select {
		case backend = <-entered:
		case <-time.After(time.Second):
			t.Fatal("backend registration did not start")
		}
		c.mu.Lock()
		c.subs["expiring"].registrationTimer.Reset(time.Millisecond)
		c.mu.Unlock()
		replicaFailureCode(t, c, "REPLICATION_TRANSPORT_UNAVAILABLE")
		c.wg.Wait()
		require.ErrorIs(t, backend.Err(), context.Canceled)
		snapshot := c.server.replicaBudget.snapshot()
		require.Zero(t, snapshot.Pending)
		require.Zero(t, snapshot.SourceBytes)
		require.Zero(t, snapshot.Subscriptions)
	})
	for _, tc := range []struct{ name, collection, expected string }{
		{"missing collection", "", "INVALID_REPLICATION_SOURCE"}, {"invalid concrete scope", "users/alice", "BAD_REQUEST"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := replicaFailureClient(t)
			payload, err := json.Marshal(map[string]any{"collection": tc.collection, "source": replicaIntegrationSource()})
			require.NoError(t, err)
			c.startSubscribe(BaseMessage{ID: "invalid", Type: TypeSubscribe, Payload: payload}, 1)
			replicaFailureCode(t, c, tc.expected)
			c.wg.Wait()
			require.Zero(t, c.server.replicaBudget.snapshot().SourceBytes)
		})
	}
	t.Run("malformed envelope", func(t *testing.T) {
		c := replicaFailureClient(t)
		c.startSubscribe(BaseMessage{ID: "invalid", Payload: []byte(`{"ignored":true}`)}, 1)
		replicaFailureCode(t, c, "INVALID_REPLICATION_SOURCE")
	})
	t.Run("authority denial", func(t *testing.T) {
		c := replicaFailureClient(t)
		c.principal.Subject = "stranger"
		payload, _ := json.Marshal(map[string]any{"collection": "users", "source": replicaIntegrationSource()})
		c.startSubscribe(BaseMessage{ID: "denied", Payload: payload}, 1)
		replicaFailureCode(t, c, "FORBIDDEN")
		c.wg.Wait()
		require.Zero(t, c.server.replicaBudget.snapshot().SourceBytes)
	})
}

type replicaAuthorityHook struct {
	database.Service
	resolve func(context.Context, string) (*database.Database, error)
}

func (d *replicaAuthorityHook) ResolveDatabaseAuthoritative(ctx context.Context, namespace string) (*database.Database, error) {
	return d.resolve(ctx, namespace)
}

func TestReplicaReadFailureBoundariesReturnReservations(t *testing.T) {
	for _, scenario := range []string{"authority busy", "canceled", "canceled after authority", "invalid page scope", "typed encoding failure", "envelope budget"} {
		t.Run(scenario, func(t *testing.T) {
			c, sub, page := replicaStateFixture(t)
			c.conn = replicaFailureClient(t).conn
			page.bufferHeld = false
			sub.identity = replicaIntegrationIdentity
			sub.collection = "users"
			source, err := json.Marshal(replicaIntegrationSource())
			require.NoError(t, err)
			request, err := decodeReplicaSource(replicaSubscribePayload{Collection: "users", Source: source}, sub.id)
			require.NoError(t, err)
			request.DatabaseIdentity = sub.identity
			authority := &replicaIntegrationDatabase{identity: replicaIntegrationIdentity}
			c.server.database = authority
			c.server.queryService = &replicaIntegrationQuery{pull: func(_ context.Context, _ string, req storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
				response, err := replicaIntegrationPage(req, "head")
				if err != nil {
					return nil, err
				}
				switch scenario {
				case "invalid page scope":
					response.DatabaseIdentity = "fedcba9876543210"
				case "typed encoding failure":
					response.Events = []storagetypes.ReplicationEvent{{Type: storagetypes.ReplicationUpsert, Document: model.Document{"id": "alice", "collection": "users", "version": int64(1), "createdAt": int64(1), "updatedAt": int64(1), "value": math.NaN()}}}
				}
				return response, nil
			}}
			expected := "INTERNAL_ERROR"
			switch scenario {
			case "authority busy":
				fillReplicaAuthorizationQueue(t, c.server.replicaBudget)
				expected = "REPLICATION_SOURCE_BUSY"
			case "canceled":
				page.cancel()
				expected = ""
			case "canceled after authority":
				ctx, cancel := context.WithCancelCause(sub.ctx)
				page.ctx = ctx
				page.cancel = func() { cancel(context.Canceled) }
				c.server.database = &replicaAuthorityHook{resolve: func(ctx context.Context, namespace string) (*database.Database, error) {
					db, err := authority.ResolveDatabaseAuthoritative(ctx, namespace)
					cancel(context.Canceled)
					return db, err
				}}
				expected = ""
			case "envelope budget":
				page.id = strings.Repeat("<", replicaIDBytes)
				expected = ""
			}
			c.wg.Add(1)
			c.readPage(page, "app", replication.Principal{Subject: "alice"}, request)
			if expected != "" {
				replicaFailureCode(t, c, expected)
			} else {
				require.Empty(t, c.control)
			}
			if scenario == "envelope budget" {
				require.True(t, c.closed)
				require.Error(t, c.conn.WriteMessage(websocket.TextMessage, []byte(`{}`)))
			}
			require.Nil(t, sub.page)
			require.Zero(t, c.pages)
			snapshot := c.server.replicaBudget.snapshot()
			require.Zero(t, snapshot.Reads)
			require.Zero(t, snapshot.PageBytes)
		})
	}
}
