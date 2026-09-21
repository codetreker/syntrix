package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/database"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/gateway/config"
	"github.com/syntrixbase/syntrix/internal/puller/events"
	"github.com/syntrixbase/syntrix/internal/streamer"
	"github.com/syntrixbase/syntrix/pkg/model"
)

func replicaStateFixture(t *testing.T) (*replicaClient, *replicaSubscription, *replicaPendingPage) {
	t.Helper()
	budget := newReplicaBudget(config.DefaultReplicaConfig())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client := &replicaClient{server: &Server{replicaBudget: budget}, ctx: ctx, cancel: cancel, authenticated: true, authGen: 1,
		subs: make(map[string]*replicaSubscription), seen: make(map[string]struct{}), control: make(chan *replicaOutbound, 32)}
	subCtx, subCancel := context.WithCancel(ctx)
	releaseSource, ok := budget.trySubscription(128)
	require.True(t, ok)
	sub := &replicaSubscription{id: "source", gen: 1, ctx: subCtx, cancel: subCancel, registered: true, registering: true, sourceBytes: 128, releaseSource: releaseSource}
	client.subs[sub.id] = sub
	client.sourceBytes = 128
	client.sourceCount = 1
	client.pages = 1
	pageCtx, pageCancel := context.WithCancel(subCtx)
	releaseRead, ok := budget.tryRead()
	require.True(t, ok)
	releaseBytes, ok := budget.tryPage(replicaDataFrameBytes)
	require.True(t, ok)
	page := &replicaPendingPage{sub: sub, id: "read", ctx: pageCtx, cancel: pageCancel, releaseRead: releaseRead, releaseBytes: releaseBytes, bufferHeld: true}
	sub.page = page
	t.Cleanup(func() { releaseRead(); releaseBytes(); releaseSource(); pageCancel(); subCancel() })
	return client, sub, page
}

func TestReplicaRetirementKeepsExecutingAndBufferReservations(t *testing.T) {
	c, sub, page := replicaStateFixture(t)
	c.mu.Lock()
	c.retireSubLocked(sub)
	c.mu.Unlock()
	require.Equal(t, 1, c.pages)
	require.ErrorIs(t, page.ctx.Err(), context.Canceled)
	snapshot := c.server.replicaBudget.snapshot()
	require.EqualValues(t, 1, snapshot.Reads)
	require.EqualValues(t, replicaDataFrameBytes, snapshot.PageBytes)
	require.EqualValues(t, 1, snapshot.Subscriptions)
	c.mu.Lock()
	page.workDone = true
	page.releaseRead()
	page.releaseRead = nil
	c.releasePageLocked(page)
	sub.registering = false
	c.finishSourceLocked(sub)
	c.mu.Unlock()
	snapshot = c.server.replicaBudget.snapshot()
	require.Zero(t, snapshot.Reads)
	require.Zero(t, snapshot.Subscriptions)
	require.EqualValues(t, replicaDataFrameBytes, snapshot.PageBytes)
	c.discard(&replicaOutbound{page: page, data: []byte("retired")})
	require.Zero(t, c.server.replicaBudget.snapshot().PageBytes)
	require.Zero(t, c.pages)
	c.discard(&replicaOutbound{page: page})
	require.Zero(t, c.pages)
}

func TestReplicaACKWaitsForWorkerAndBufferReleaseAndDuplicateIsIdempotent(t *testing.T) {
	c, sub, page := replicaStateFixture(t)
	page.sent = true
	payload, _ := json.Marshal(replicaAckPayload{SubID: sub.id, RequestID: page.id})
	message := BaseMessage{ID: page.id, Type: TypeReplicaAck, Payload: payload}
	c.acknowledge(message, 1)
	require.Nil(t, sub.page)
	require.Equal(t, 1, c.pages)
	require.True(t, page.acked)
	require.EqualValues(t, replicaDataFrameBytes, c.server.replicaBudget.snapshot().PageBytes)
	c.acknowledge(message, 1)
	require.Empty(t, c.control)
	c.mu.Lock()
	page.workDone = true
	page.releaseRead()
	page.releaseRead = nil
	page.bufferHeld = false
	c.releasePageLocked(page)
	c.mu.Unlock()
	require.Zero(t, c.server.replicaBudget.snapshot().PageBytes)
	require.Zero(t, c.pages)
	message.ID = "unknown"
	message.Payload = []byte(`{"subId":"source","requestId":"unknown"}`)
	c.acknowledge(message, 1)
	frame := <-c.control
	var decoded BaseMessage
	require.NoError(t, json.Unmarshal(frame.data, &decoded))
	var failure replicaErrorPayload
	require.NoError(t, json.Unmarshal(decoded.Payload, &failure))
	require.Equal(t, "REPLICATION_PROTOCOL_ERROR", failure.Code)
}

func TestReplicaAuthAndSubscriptionOwnerFences(t *testing.T) {
	c, sub, _ := replicaStateFixture(t)
	require.True(t, c.subCurrentLocked(sub))
	c.authGen++
	require.False(t, c.subCurrentLocked(sub))
	c.authGen--
	c.expires = time.Now().Add(-time.Second)
	require.False(t, c.authCurrentLocked(c.authGen))
	c.expires = time.Time{}
	c.subs[sub.id] = &replicaSubscription{id: sub.id}
	require.False(t, c.subCurrentLocked(sub))
	c.subs[sub.id] = sub
	sub.cancel()
	require.False(t, c.subCurrentLocked(sub))
}

func TestReplicaOldDeadlineCannotRetireRenewedOwner(t *testing.T) {
	c, sub, page := replicaStateFixture(t)
	c.authGen = 2
	c.closeIf(func() bool { return c.authGen == 1 })
	require.False(t, c.closed)
	c.authGen = 1
	page.sent = true
	payload, _ := json.Marshal(replicaAckPayload{SubID: sub.id, RequestID: page.id})
	c.acknowledge(BaseMessage{ID: page.id, Payload: payload}, 1)
	newPage := &replicaPendingPage{id: "next", sub: sub}
	sub.page = newPage
	c.expirePage(page)
	require.Same(t, newPage, sub.page)
	require.False(t, sub.retired)
	require.Empty(t, c.control)
}

func TestReplicaSessionControlAdmissionAndSourceImmutability(t *testing.T) {
	var calls atomic.Int32
	env := newReplicaIntegrationEnv(t, func(_ context.Context, _ string, request storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
		calls.Add(1)
		return replicaIntegrationPage(request, "head")
	}, func(cfg *config.RealtimeConfig) { cfg.Replica.Subscriptions = 1 })
	peer := env.dial(t)
	source := replicaIntegrationSource()
	peer.send(t, TypeSubscribe, "unauthenticated", map[string]any{"collection": "users", "source": source})
	replicaIntegrationError(t, peer.receive(t, TypeError), "UNAUTHORIZED", "", "")
	peer.send(t, TypeAuth, "wrong-mode", map[string]any{"token": "owner", "database": "app", "mode": "other"})
	replicaIntegrationError(t, peer.receive(t, TypeError), "BAD_REQUEST", "", "")
	peer.send(t, TypeAuth, "bad-token", map[string]any{"token": "invalid", "database": "app", "mode": ReplicaDataMode})
	replicaIntegrationError(t, peer.receive(t, TypeError), "UNAUTHORIZED", "", "")
	peer.authenticate(t, "owner")
	peer.send(t, TypeSubscribe, "invalid-source", map[string]any{"collection": "users", "source": map[string]any{"version": 2, "filters": []any{}}})
	replicaIntegrationError(t, peer.receive(t, TypeError), "INVALID_REPLICATION_SOURCE", "", "")
	peer.send(t, TypeSubscribe, "only", map[string]any{"collection": "users", "source": source})
	peer.receive(t, TypeSubscribeAck)
	peer.send(t, TypeSubscribe, "only", map[string]any{"collection": "users", "source": source})
	replicaIntegrationError(t, peer.receive(t, TypeError), "REPLICATION_PROTOCOL_ERROR", "only", "")
	request := map[string]any{"collection": "users", "source": source, "checkpoint": nil, "limit": 100}
	peer.send(t, TypeReplicaRead, "wrong-identity", map[string]any{"subId": "only", "requestId": "wrong-identity", "request": request, "expectedDatabaseIdentity": "fedcba9876543210"})
	replicaIntegrationError(t, peer.receive(t, TypeError), "DATABASE_IDENTITY_MISMATCH", "only", "wrong-identity")
	peer.send(t, TypeReplicaRead, "wrong-hash", map[string]any{"subId": "only", "requestId": "wrong-hash", "request": request, "expectedSourceHash": strings.Repeat("b", 64)})
	replicaIntegrationError(t, peer.receive(t, TypeError), "INVALID_REPLICATION_SOURCE", "only", "wrong-hash")
	peer.read(t, "only", "changed-source", map[string]any{"version": 1, "filters": []any{}}, nil)
	replicaIntegrationError(t, peer.receive(t, TypeError), "INVALID_REPLICATION_SOURCE", "only", "changed-source")
	require.Zero(t, calls.Load())
	peer.read(t, "only", "valid", source, nil)
	peer.receive(t, TypeReplicaPage)
	peer.send(t, TypeReplicaAck, "not-current", map[string]any{"subId": "only", "requestId": "not-current"})
	replicaIntegrationError(t, peer.receive(t, TypeError), "REPLICATION_PROTOCOL_ERROR", "only", "not-current")
	peer.send(t, TypeReplicaAck, "valid", map[string]any{"subId": "only", "requestId": "valid"})
	peer.send(t, TypeUnsubscribe, "only", map[string]any{"subId": "only"})
	peer.receive(t, TypeUnsubscribeAck)
	peer.send(t, TypeUnsubscribe, "only", map[string]any{"subId": "only"})
	peer.receive(t, TypeUnsubscribeAck)
	peer.send(t, TypeSubscribe, "new-attempt", map[string]any{"collection": "users", "source": source})
	replicaIntegrationError(t, peer.receive(t, TypeError), "REPLICATION_TRANSPORT_BUSY", "new-attempt", "")
	select {
	case <-peer.done:
	case <-time.After(time.Second):
		t.Fatal("bounded registration history did not retire connection")
	}
	require.Eventually(t, func() bool {
		s := env.server.replicaBudget.snapshot()
		return s.Connections == 0 && s.Subscriptions == 0 && s.Reads == 0 && s.PageBytes == 0 && s.SourceBytes == 0
	}, time.Second, time.Millisecond)
}

func TestReplicaSourceDefinitionCapacityRejectsBeforeBackendRegistration(t *testing.T) {
	for _, global := range []bool{false, true} {
		t.Run(map[bool]string{false: "connection", true: "gateway"}[global], func(t *testing.T) {
			env := newReplicaIntegrationEnv(t, func(context.Context, string, storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
				panic("rejected registration cannot call Query")
			}, func(cfg *config.RealtimeConfig) {
				if global {
					cfg.Replica.SourceBytes = 1
				} else {
					cfg.Replica.SourceBytesPerConnection = 1
				}
			})
			peer := env.dial(t)
			peer.authenticate(t, "owner")
			peer.send(t, TypeSubscribe, "source", map[string]any{"collection": "users", "source": replicaIntegrationSource()})
			replicaIntegrationError(t, peer.receive(t, TypeError), "REPLICATION_TRANSPORT_BUSY", "source", "")
			snapshot := env.server.replicaBudget.snapshot()
			require.Zero(t, snapshot.SourceBytes)
			require.Zero(t, snapshot.Subscriptions)
		})
	}
}

func TestReplicaRetiredQueryTailsKeepFourConnectionCreditsUntilExit(t *testing.T) {
	started := make(chan context.Context, 4)
	gate := make(chan struct{})
	var release sync.Once
	var calls atomic.Int32
	env := newReplicaIntegrationEnv(t, func(ctx context.Context, _ string, request storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
		if calls.Add(1) <= 4 {
			started <- ctx
			<-gate
		}
		return replicaIntegrationPage(request, "head")
	})
	t.Cleanup(func() { release.Do(func() { close(gate) }) })
	peer := env.dial(t)
	peer.authenticate(t, "owner")
	source := replicaIntegrationSource()
	for index := 0; index < 4; index++ {
		id := fmt.Sprintf("old-%d", index)
		peer.subscribe(t, id, source)
		peer.read(t, id, fmt.Sprintf("read-%d", index), source, nil)
	}
	contexts := make([]context.Context, 0, 4)
	for index := 0; index < 4; index++ {
		select {
		case ctx := <-started:
			contexts = append(contexts, ctx)
		case <-time.After(time.Second):
			t.Fatal("four source reads were not admitted")
		}
	}
	peer.authenticate(t, "owner")
	for _, ctx := range contexts {
		require.ErrorIs(t, ctx.Err(), context.Canceled)
	}
	peer.subscribe(t, "new-source", source)
	peer.read(t, "new-source", "too-early", source, nil)
	replicaIntegrationError(t, peer.receive(t, TypeError), "REPLICATION_TRANSPORT_BUSY", "new-source", "too-early")
	require.EqualValues(t, 4, calls.Load())
	require.EqualValues(t, 4, env.server.replicaBudget.snapshot().Reads)
	release.Do(func() { close(gate) })
	require.Eventually(t, func() bool { s := env.server.replicaBudget.snapshot(); return s.Reads == 0 && s.PageBytes == 0 }, time.Second, time.Millisecond)
	peer.read(t, "new-source", "after-exit", source, nil)
	decodeReplicaIntegrationPage(t, peer.receive(t, TypeReplicaPage), "new-source", "after-exit")
	require.EqualValues(t, 5, calls.Load())
	peer.send(t, TypeReplicaAck, "after-exit", map[string]any{"subId": "new-source", "requestId": "after-exit"})
}

type replicaRegistrationFaultStream struct {
	*mockStreamerStream
	register func(context.Context, string, string, []model.Filter) (streamer.Registration, error)
}

func (s *replicaRegistrationFaultStream) Subscribe(ctx context.Context, namespace, collection string, filters []model.Filter) (streamer.Registration, error) {
	return s.register(ctx, namespace, collection, filters)
}
func replicaRegistrationFaultEnv(t *testing.T, register func(context.Context, string, string, []model.Filter) (streamer.Registration, error)) *replicaIntegrationEnv {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	stream := &replicaRegistrationFaultStream{mockStreamerStream: &mockStreamerStream{}, register: register}
	db := &replicaIntegrationDatabase{identity: replicaIntegrationIdentity}
	server := NewServer(&MockQueryService{}, stream, "documents", &replicaIntegrationAuth{}, config.RealtimeConfig{AllowDevOrigin: true, Replica: config.DefaultReplicaConfig()})
	server.SetDatabaseService(db)
	server.hub.SetStream(stream)
	go server.hub.Run(ctx)
	env := &replicaIntegrationEnv{server: server, database: db, ctx: ctx, cancel: cancel, http: httptest.NewServer(http.HandlerFunc(server.HandleWS))}
	t.Cleanup(func() { cancel(); env.http.Close() })
	return env
}

func TestReplicaBackendRegistrationFailureIsSafeAndReturnsReservations(t *testing.T) {
	const privateFailure = "backend-private-token-and-details"
	var attempted atomic.Int32
	env := replicaRegistrationFaultEnv(t, func(context.Context, string, string, []model.Filter) (streamer.Registration, error) {
		attempted.Add(1)
		return streamer.Registration{}, errors.New(privateFailure)
	})
	peer := env.dial(t)
	peer.authenticate(t, "owner")
	peer.send(t, TypeSubscribe, "registration", map[string]any{"collection": "users", "source": replicaIntegrationSource()})
	frame := peer.receive(t, TypeError)
	replicaIntegrationError(t, frame, "REPLICATION_TRANSPORT_UNAVAILABLE", "registration", "")
	require.NotContains(t, string(frame.message.Payload), privateFailure)
	require.EqualValues(t, 1, attempted.Load())
	require.Eventually(t, func() bool {
		s := env.server.replicaBudget.snapshot()
		return s.Subscriptions == 0 && s.SourceBytes == 0 && s.Pending == 0 && s.AuthRunning == 0
	}, time.Second, time.Millisecond)
	peer.send(t, TypeUnsubscribe, "registration", map[string]any{"subId": "registration"})
	peer.receive(t, TypeUnsubscribeAck)
}

func TestReplicaCanceledRegistrationKeepsReservationsUntilBackendReturns(t *testing.T) {
	entered := make(chan struct{})
	canceled := make(chan struct{})
	gate := make(chan struct{})
	var release sync.Once
	var attempts atomic.Int32
	env := replicaRegistrationFaultEnv(t, func(ctx context.Context, _ string, _ string, _ []model.Filter) (streamer.Registration, error) {
		if attempts.Add(1) == 1 {
			close(entered)
			<-ctx.Done()
			close(canceled)
			<-gate
			return streamer.Registration{}, ctx.Err()
		}
		return streamer.Registration{ID: "fresh-backend-registration", Generation: 1}, nil
	})
	t.Cleanup(func() { release.Do(func() { close(gate) }) })
	peer := env.dial(t)
	peer.authenticate(t, "owner")
	peer.send(t, TypeSubscribe, "pending", map[string]any{"collection": "users", "source": replicaIntegrationSource()})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("backend registration did not start")
	}
	peer.send(t, TypeUnsubscribe, "pending", map[string]any{"subId": "pending"})
	peer.receive(t, TypeUnsubscribeAck)
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("unsubscribe did not cancel backend registration")
	}
	snapshot := env.server.replicaBudget.snapshot()
	require.EqualValues(t, 1, snapshot.Pending)
	require.EqualValues(t, 1, snapshot.Subscriptions)
	require.Positive(t, snapshot.SourceBytes)
	release.Do(func() { close(gate) })
	require.Eventually(t, func() bool {
		s := env.server.replicaBudget.snapshot()
		return s.Pending == 0 && s.Subscriptions == 0 && s.SourceBytes == 0
	}, time.Second, time.Millisecond)
	peer.subscribe(t, "fresh", replicaIntegrationSource())
	require.EqualValues(t, 2, attempts.Load())
}

type replicaRevocableDatabase struct {
	database.Service
	base   *replicaIntegrationDatabase
	denied atomic.Bool
}

func (d *replicaRevocableDatabase) ResolveDatabaseAuthoritative(ctx context.Context, namespace string) (*database.Database, error) {
	db, err := d.base.ResolveDatabaseAuthoritative(ctx, namespace)
	if err == nil && d.denied.Load() {
		db.OwnerID = "new-owner"
	}
	return db, err
}

func TestReplicaChangedReauthorizesIdentityAndPermissionBeforeAnyActivityFrame(t *testing.T) {
	for _, change := range []string{"identity", "permission"} {
		t.Run(change, func(t *testing.T) {
			var queries atomic.Int32
			env := newReplicaIntegrationEnv(t, func(context.Context, string, storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
				queries.Add(1)
				return nil, errors.New("notification cannot execute Query")
			})
			authority := &replicaRevocableDatabase{base: env.database}
			env.server.SetDatabaseService(authority)
			peer := env.dial(t)
			peer.authenticate(t, "owner")
			peer.subscribe(t, "source", replicaIntegrationSource())
			code := "FORBIDDEN"
			if change == "identity" {
				env.database.replace()
				code = "DATABASE_IDENTITY_MISMATCH"
			} else {
				authority.denied.Store(true)
			}
			document := storage.NewStoredDoc("app", "users", "alice", map[string]any{"active": false})
			require.NoError(t, env.streamer.(streamer.EventProcessor).ProcessEvent(events.SyntrixChangeEvent{Id: "wake", Database: "app", Type: events.EventUpdate, Document: &document}))
			replicaIntegrationError(t, peer.receive(t, TypeError), code, "source", "")
			require.Zero(t, queries.Load())
			require.Eventually(t, func() bool {
				s := env.server.replicaBudget.snapshot()
				return s.Subscriptions == 0 && s.SourceBytes == 0 && s.Pending == 0 && s.AuthRunning == 0
			}, time.Second, time.Millisecond)
			peer.send(t, TypeUnsubscribe, "source", map[string]any{"subId": "source"})
			peer.receive(t, TypeUnsubscribeAck)
		})
	}
}

func TestReplicaCurrentACKExpiryRetiresSourceAndReturnsAllQuiescentReservations(t *testing.T) {
	c, sub, page := replicaStateFixture(t)
	sub.registering = false
	page.sent = true
	page.workDone = true
	page.bufferHeld = false
	page.releaseRead()
	page.releaseRead = nil
	var released int
	sub.releaseBackend = func() { released++ }
	c.expirePage(page)
	require.True(t, sub.retired)
	require.Nil(t, sub.page)
	require.ErrorIs(t, page.ctx.Err(), context.Canceled)
	require.Equal(t, 1, released)
	require.Zero(t, c.pages)
	snapshot := c.server.replicaBudget.snapshot()
	require.Zero(t, snapshot.Reads)
	require.Zero(t, snapshot.PageBytes)
	require.Zero(t, snapshot.Subscriptions)
	require.Zero(t, snapshot.SourceBytes)
	frame := <-c.control
	var message BaseMessage
	require.NoError(t, json.Unmarshal(frame.data, &message))
	var failure replicaErrorPayload
	require.NoError(t, json.Unmarshal(message.Payload, &failure))
	require.Equal(t, "read", message.ID)
	require.Equal(t, "read", failure.RequestID)
	require.Equal(t, "source", failure.SubID)
	require.Equal(t, "REPLICATION_TRANSPORT_UNAVAILABLE", failure.Code)
	c.expirePage(page)
	require.Equal(t, 1, released)
	require.Empty(t, c.control)
}

func TestReplicaFullControlQueueClosesActualSocketAndCancelsOwner(t *testing.T) {
	connections := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err == nil {
			connections <- conn
		}
	}))
	defer server.Close()
	peer, _, err := websocket.DefaultDialer.Dial(strings.Replace(server.URL, "http", "ws", 1), nil)
	require.NoError(t, err)
	defer peer.Close()
	var socket *websocket.Conn
	select {
	case socket = <-connections:
	case <-time.After(time.Second):
		t.Fatal("socket was not upgraded")
	}
	defer socket.Close()
	c, sub, page := replicaStateFixture(t)
	c.conn = socket
	c.control = make(chan *replicaOutbound, 1)
	sub.registering = false
	page.sent = true
	page.workDone = true
	page.bufferHeld = false
	page.releaseRead()
	page.releaseRead = nil
	require.True(t, c.sendControl(&replicaOutbound{gen: 1, data: []byte(`{"type":"heartbeat"}`)}))
	require.False(t, c.sendControl(&replicaOutbound{gen: 1, data: []byte(`{"type":"heartbeat"}`)}))
	require.True(t, c.closed)
	require.ErrorIs(t, c.ctx.Err(), context.Canceled)
	require.True(t, sub.retired)
	require.NoError(t, peer.SetReadDeadline(time.Now().Add(time.Second)))
	_, _, err = peer.ReadMessage()
	require.Error(t, err)
	snapshot := c.server.replicaBudget.snapshot()
	require.Zero(t, snapshot.PageBytes)
	require.Zero(t, snapshot.Subscriptions)
	require.Zero(t, snapshot.Reads)
}
