package realtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/database"
	"github.com/syntrixbase/syntrix/internal/core/identity"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	storagetypes "github.com/syntrixbase/syntrix/internal/core/storage/types"
	gatewayconfig "github.com/syntrixbase/syntrix/internal/gateway/config"
	"github.com/syntrixbase/syntrix/internal/puller/events"
	"github.com/syntrixbase/syntrix/internal/query"
	querycore "github.com/syntrixbase/syntrix/internal/query/core"
	"github.com/syntrixbase/syntrix/internal/query/wire"
	"github.com/syntrixbase/syntrix/internal/streamer"
	"github.com/syntrixbase/syntrix/pkg/model"
)

const replicaIntegrationIdentity = "0123456789abcdef"

type replicaIntegrationAuth struct{ mockAuthService }

func (*replicaIntegrationAuth) ValidateToken(token string) (*identity.Claims, error) {
	claims := &identity.Claims{UserID: "alice", RegisteredClaims: jwt.RegisteredClaims{
		Subject: "alice", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}}
	switch token {
	case "owner":
	case "slug-grant":
		claims.UserID, claims.Subject, claims.DBAdmin = "bob", "bob", []string{"app"}
	case "id-grant":
		claims.UserID, claims.Subject, claims.DBAdmin = "bob", "bob", []string{replicaIntegrationIdentity}
	case "stranger":
		claims.UserID, claims.Subject = "charlie", "charlie"
	default:
		if !strings.HasPrefix(token, "expires:") {
			return nil, identity.ErrInvalidToken
		}
		expires, err := time.Parse(time.RFC3339Nano, strings.TrimPrefix(token, "expires:"))
		if err != nil {
			return nil, identity.ErrInvalidToken
		}
		claims.ExpiresAt = jwt.NewNumericDate(expires)
	}
	return claims, nil
}

type replicaIntegrationDatabase struct {
	database.Service
	mu         sync.Mutex
	identity   string
	namespaces []string
	cached     atomic.Int32
}

func (d *replicaIntegrationDatabase) ResolveDatabase(context.Context, string) (*database.Database, error) {
	d.cached.Add(1)
	return nil, errors.New("replica admission must use authoritative database resolution")
}

func (d *replicaIntegrationDatabase) ResolveDatabaseAuthoritative(ctx context.Context, namespace string) (*database.Database, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.namespaces = append(d.namespaces, namespace)
	if namespace != "app" {
		return nil, database.ErrDatabaseNotFound
	}
	slug := "app"
	return &database.Database{ID: d.identity, Slug: &slug, OwnerID: "alice", Status: database.StatusActive}, nil
}

func (d *replicaIntegrationDatabase) replace() {
	d.mu.Lock()
	d.identity = "fedcba9876543210"
	d.mu.Unlock()
}

type replicaIntegrationQuery struct {
	query.Service
	pull func(context.Context, string, storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error)
}

func (q *replicaIntegrationQuery) Pull(ctx context.Context, namespace string, request storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
	return q.pull(ctx, namespace, request)
}

type replicaIntegrationFrame struct {
	message BaseMessage
	bytes   int
}

type replicaIntegrationPeer struct {
	conn   *websocket.Conn
	frames chan replicaIntegrationFrame
	done   chan struct{}
	err    error
}

func (p *replicaIntegrationPeer) send(t *testing.T, typ, id string, payload any) {
	t.Helper()
	encoded, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NoError(t, p.conn.SetWriteDeadline(time.Now().Add(3*time.Second)))
	require.NoError(t, p.conn.WriteJSON(BaseMessage{Type: typ, ID: id, Payload: encoded}))
}

func (p *replicaIntegrationPeer) receive(t *testing.T, typ string) replicaIntegrationFrame {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case frame := <-p.frames:
			if frame.message.Type == TypeHeartbeat || (frame.message.Type == "replica_changed" && typ != "replica_changed") {
				continue
			}
			require.Equal(t, typ, frame.message.Type, "payload=%s", frame.message.Payload)
			return frame
		case <-p.done:
			select {
			case frame := <-p.frames:
				require.Equal(t, typ, frame.message.Type, "payload=%s", frame.message.Payload)
				return frame
			default:
				t.Fatalf("WebSocket closed before %s: %v", typ, p.err)
			}
		case <-deadline.C:
			t.Fatalf("Timed out waiting for %s", typ)
		}
	}
}

func (p *replicaIntegrationPeer) authenticate(t *testing.T, token string) {
	t.Helper()
	p.send(t, TypeAuth, "auth-attempt", map[string]any{"token": token, "database": "app", "mode": "replica-data"})
	frame := p.receive(t, TypeAuthAck)
	require.Equal(t, "auth-attempt", frame.message.ID)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(frame.message.Payload, &payload))
	require.Equal(t, "replica-data", payload["mode"])
}

func (p *replicaIntegrationPeer) subscribe(t *testing.T, id string, source map[string]any) {
	t.Helper()
	p.send(t, TypeSubscribe, id, map[string]any{"collection": "users", "source": source, "expectedDatabaseIdentity": replicaIntegrationIdentity})
	frame := p.receive(t, TypeSubscribeAck)
	require.Equal(t, id, frame.message.ID)
	var payload struct {
		SubID            string `json:"subId"`
		DatabaseIdentity string `json:"databaseIdentity"`
	}
	require.NoError(t, json.Unmarshal(frame.message.Payload, &payload))
	require.Equal(t, id, payload.SubID)
	require.Equal(t, replicaIntegrationIdentity, payload.DatabaseIdentity)
}

func (p *replicaIntegrationPeer) read(t *testing.T, subID, requestID string, source map[string]any, checkpoint any) {
	t.Helper()
	request := map[string]any{"collection": "users", "source": source}
	if source["limit"] != nil {
		request["requestId"] = requestID
	} else {
		request["checkpoint"], request["limit"] = checkpoint, 100
	}
	p.send(t, "replica_read", requestID, map[string]any{"subId": subID, "requestId": requestID,
		"request": request, "expectedDatabaseIdentity": replicaIntegrationIdentity})
}

type replicaIntegrationEnv struct {
	server   *Server
	streamer streamer.StreamerServer
	database *replicaIntegrationDatabase
	http     *httptest.Server
	ctx      context.Context
	cancel   context.CancelFunc
	sockets  chan net.Conn
}

func newReplicaIntegrationEnv(t *testing.T, pull func(context.Context, string, storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error), configure ...func(*gatewayconfig.RealtimeConfig)) *replicaIntegrationEnv {
	t.Helper()
	local, err := streamer.NewService(streamer.ServerConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	db := &replicaIntegrationDatabase{identity: replicaIntegrationIdentity}
	cfg := gatewayconfig.RealtimeConfig{AllowDevOrigin: true, Replica: gatewayconfig.DefaultReplicaConfig()}
	for _, apply := range configure {
		apply(&cfg)
	}
	server := NewServer(&replicaIntegrationQuery{pull: pull}, local, "documents", &replicaIntegrationAuth{}, cfg)
	server.SetDatabaseService(db)
	require.NoError(t, server.StartBackgroundTasks(ctx))
	env := &replicaIntegrationEnv{server: server, streamer: local, database: db, ctx: ctx, cancel: cancel, sockets: make(chan net.Conn, 16)}
	env.http = httptest.NewUnstartedServer(http.HandlerFunc(server.HandleWS))
	env.http.Config.ConnState = func(conn net.Conn, state http.ConnState) {
		if state == http.StateNew {
			env.sockets <- conn
		}
	}
	env.http.Start()
	t.Cleanup(func() {
		cancel()
		env.http.Close()
		shutdown, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		require.NoError(t, local.Stop(shutdown))
	})
	return env
}

func (e *replicaIntegrationEnv) dial(t *testing.T) *replicaIntegrationPeer {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(strings.Replace(e.http.URL, "http", "ws", 1)+"?mode=replica-data", nil)
	require.NoError(t, err)
	peer := &replicaIntegrationPeer{conn: conn, frames: make(chan replicaIntegrationFrame, 32), done: make(chan struct{})}
	go func() {
		defer close(peer.done)
		for {
			_, encoded, err := conn.ReadMessage()
			if err != nil {
				peer.err = err
				return
			}
			var message BaseMessage
			if err := json.Unmarshal(encoded, &message); err != nil {
				peer.err = err
				return
			}
			select {
			case peer.frames <- replicaIntegrationFrame{message: message, bytes: len(encoded)}:
			case <-e.ctx.Done():
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = conn.Close()
		select {
		case <-peer.done:
		case <-time.After(3 * time.Second):
			t.Error("WebSocket reader did not stop")
		}
	})
	return peer
}

func replicaIntegrationSource() map[string]any {
	return map[string]any{"version": 1, "filters": []any{map[string]any{"field": "active", "op": "==", "value": map[string]any{"type": "bool", "value": true}}}}
}

func replicaIntegrationPage(request storage.ReplicationPullRequest, checkpoint string, events ...storagetypes.ReplicationEvent) (*storage.ReplicationPullResponse, error) {
	hash, err := querycore.ReplicationSourceHash(request)
	if err != nil {
		return nil, err
	}
	cursor, err := replicaIntegrationCursor(request, hash, "live", checkpoint)
	if err != nil {
		return nil, err
	}
	return &storage.ReplicationPullResponse{ProtocolVersion: 1, Mode: "events", DatabaseIdentity: request.DatabaseIdentity,
		SourceHash: hash, GenerationID: "01234567-89ab-4def-8123-456789abcdef", Phase: "live", Checkpoint: cursor,
		CaughtUp: true, BootstrapComplete: true, Events: events}, nil
}

func replicaIntegrationCursor(request storage.ReplicationPullRequest, hash, phase, position string) (string, error) {
	// Query admission validates its canonical v4 envelope even when the backing
	// Query is a strict stub; the nested Store position remains opaque.
	encoded, err := json.Marshal(struct {
		Version          int    `json:"version"`
		Database         string `json:"database"`
		DatabaseIdentity string `json:"databaseIdentity"`
		Collection       string `json:"collection"`
		Phase            string `json:"phase"`
		Position         string `json:"position"`
		SourceHash       string `json:"sourceHash"`
		GenerationID     string `json:"generationId"`
	}{4, "app", request.DatabaseIdentity, request.Collection, phase, position, hash, "01234567-89ab-4def-8123-456789abcdef"})
	return base64.RawURLEncoding.EncodeToString(encoded), err
}

func decodeReplicaIntegrationPage(t *testing.T, frame replicaIntegrationFrame, subID, requestID string) map[string]json.RawMessage {
	t.Helper()
	require.Equal(t, requestID, frame.message.ID)
	var envelope struct {
		SubID     string                     `json:"subId"`
		RequestID string                     `json:"requestId"`
		Page      map[string]json.RawMessage `json:"page"`
	}
	require.NoError(t, json.Unmarshal(frame.message.Payload, &envelope))
	require.Equal(t, subID, envelope.SubID)
	require.Equal(t, requestID, envelope.RequestID)
	return envelope.Page
}

func TestReplicaWSIntegrationTypedSourceAndWholeCollectionWake(t *testing.T) {
	var calls atomic.Int32
	checkpoint := "opaque:" + strings.Repeat("x", 70<<10)
	requests := make(chan storage.ReplicationPullRequest, 4)
	env := newReplicaIntegrationEnv(t, func(_ context.Context, namespace string, request storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
		if namespace != "app" || request.DatabaseIdentity != replicaIntegrationIdentity {
			return nil, fmt.Errorf("unexpected Query scope %q/%q", namespace, request.DatabaseIdentity)
		}
		requests <- request
		sequence := calls.Add(1)
		if sequence == 1 {
			page, err := replicaIntegrationPage(request, checkpoint)
			if err != nil {
				return nil, err
			}
			page.Phase, page.CaughtUp, page.BootstrapComplete = "scan", false, false
			page.Checkpoint, err = replicaIntegrationCursor(request, page.SourceHash, "scan", checkpoint)
			return page, err
		}
		if sequence == 2 {
			return replicaIntegrationPage(request, "typed-head", storagetypes.ReplicationEvent{Type: storagetypes.ReplicationUpsert,
				Document: model.Document{"id": "alice", "collection": "users", "version": int64(9), "createdAt": int64(1), "updatedAt": int64(9), "active": true, "counter": int64(9007199254740993), "fraction": float64(1)}},
				storagetypes.ReplicationEvent{Type: storagetypes.ReplicationDelete, ID: "alice"},
				storagetypes.ReplicationEvent{Type: storagetypes.ReplicationUpsert, Document: model.Document{"id": "alice", "collection": "users", "version": int64(1), "createdAt": int64(10), "updatedAt": int64(10), "active": true, "counter": int64(9007199254740994)}})
		}
		return replicaIntegrationPage(request, "opaque-second", storagetypes.ReplicationEvent{Type: storagetypes.ReplicationLeave, ID: "alice"})
	})
	peer := env.dial(t)
	peer.authenticate(t, "owner")
	source := replicaIntegrationSource()
	peer.subscribe(t, "matching", source)
	require.Zero(t, calls.Load(), "subscription registration must not prefetch a source page")
	peer.read(t, "matching", "read-progress", source, nil)
	page := decodeReplicaIntegrationPage(t, peer.receive(t, "replica_page"), "matching", "read-progress")
	require.JSONEq(t, `[]`, string(page["events"]))
	require.JSONEq(t, `false`, string(page["caughtUp"]))
	require.JSONEq(t, `false`, string(page["bootstrapComplete"]))
	var saved string
	require.NoError(t, json.Unmarshal(page["checkpoint"], &saved))
	initialRequest := <-requests
	initialHash, err := querycore.ReplicationSourceHash(initialRequest)
	require.NoError(t, err)
	expectedCursor, err := replicaIntegrationCursor(initialRequest, initialHash, "scan", checkpoint)
	require.NoError(t, err)
	require.Equal(t, expectedCursor, saved)
	require.Greater(t, len(saved), 64<<10)
	peer.send(t, "replica_ack", "read-progress", map[string]any{"subId": "matching", "requestId": "read-progress"})
	// The next legal cursor exceeds the ordinary realtime 64 KiB frame limit.
	peer.read(t, "matching", "read-one", source, saved)
	page = decodeReplicaIntegrationPage(t, peer.receive(t, "replica_page"), "matching", "read-one")
	var entries []struct {
		Type     string          `json:"type"`
		ID       string          `json:"id"`
		Document json.RawMessage `json:"document"`
	}
	require.NoError(t, json.Unmarshal(page["events"], &entries))
	require.Len(t, entries, 3)
	require.Equal(t, []string{"upsert", "delete", "upsert"}, []string{entries[0].Type, entries[1].Type, entries[2].Type})
	first, err := model.DecodeTypedValue(entries[0].Document)
	require.NoError(t, err)
	require.Equal(t, int64(9007199254740993), first.(map[string]any)["counter"])
	require.IsType(t, float64(0), first.(map[string]any)["fraction"])
	require.Empty(t, entries[1].Document)
	last, err := model.DecodeTypedValue(entries[2].Document)
	require.NoError(t, err)
	require.Equal(t, int64(1), last.(map[string]any)["version"])
	var typedCursor string
	require.NoError(t, json.Unmarshal(page["checkpoint"], &typedCursor))
	require.NotEmpty(t, typedCursor)
	require.NotEqual(t, saved, typedCursor)
	peer.send(t, "replica_ack", "read-one", map[string]any{"subId": "matching", "requestId": "read-one"})
	document := storage.NewStoredDoc("app", "users", "alice", map[string]any{"active": false})
	require.NoError(t, env.streamer.(streamer.EventProcessor).ProcessEvent(events.SyntrixChangeEvent{Id: "changed", Database: "app", Type: events.EventUpdate, Document: &document}))
	changed := peer.receive(t, "replica_changed")
	require.Contains(t, string(changed.message.Payload), `"matching"`)
	require.Equal(t, int32(2), calls.Load(), "changed wakes a read without executing an unsolicited Query")
	peer.read(t, "matching", "read-two", source, typedCursor)
	page = decodeReplicaIntegrationPage(t, peer.receive(t, "replica_page"), "matching", "read-two")
	require.JSONEq(t, `[{"type":"leave","id":"alice"}]`, string(page["events"]))
	require.Equal(t, saved, (<-requests).Checkpoint)
	require.Equal(t, typedCursor, (<-requests).Checkpoint)
	require.Zero(t, env.database.cached.Load())
}

func TestReplicaWSIntegrationLargeTypedWindowAndEmptyReplacement(t *testing.T) {
	var calls atomic.Int32
	env := newReplicaIntegrationEnv(t, func(_ context.Context, _ string, request storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
		hash, err := querycore.ReplicationSourceHash(request)
		if err != nil {
			return nil, err
		}
		complete := true
		page := &storage.ReplicationPullResponse{ProtocolVersion: 1, Mode: "replace", DatabaseIdentity: request.DatabaseIdentity,
			SourceHash: hash, GenerationID: "01234567-89ab-4def-8123-456789abcdef", RequestID: request.RequestID, Complete: &complete,
			EffectiveOrder: []model.Order{{Field: "score", Direction: "asc"}, {Field: "id", Direction: "asc"}}, Documents: []model.Document{}}
		if calls.Add(1) == 1 {
			for i := 0; i < 6; i++ {
				page.Documents = append(page.Documents, model.Document{"id": fmt.Sprintf("large-%d", i), "collection": "users",
					"version": int64(9007199254740993), "createdAt": int64(1), "updatedAt": int64(2), "active": true,
					"score": int64(i), "payload": strings.Repeat("x", 900<<10)})
			}
		}
		return page, nil
	})
	peer := env.dial(t)
	peer.authenticate(t, "slug-grant")
	source := replicaIntegrationSource()
	source["limit"] = 6
	source["orderBy"] = []model.Order{{Field: "score", Direction: "asc"}}
	peer.subscribe(t, "window", source)
	peer.read(t, "window", "window-one", source, nil)
	frame := peer.receive(t, "replica_page")
	require.Greater(t, frame.bytes, 4<<20)
	require.LessOrEqual(t, frame.bytes, wire.MaxPageBytes+1024)
	page := decodeReplicaIntegrationPage(t, frame, "window", "window-one")
	var documents []json.RawMessage
	require.NoError(t, json.Unmarshal(page["documents"], &documents))
	require.Len(t, documents, 6)
	value, err := model.DecodeTypedValue(documents[5])
	require.NoError(t, err)
	require.Equal(t, int64(9007199254740993), value.(map[string]any)["version"])
	require.JSONEq(t, `"window-one"`, string(page["requestId"]))
	peer.send(t, "replica_ack", "window-one", map[string]any{"subId": "window", "requestId": "window-one"})
	peer.read(t, "window", "window-two", source, nil)
	page = decodeReplicaIntegrationPage(t, peer.receive(t, "replica_page"), "window", "window-two")
	require.JSONEq(t, `[]`, string(page["documents"]))
	require.JSONEq(t, `true`, string(page["complete"]))
	require.Equal(t, int32(2), calls.Load())
}

func replicaIntegrationError(t *testing.T, frame replicaIntegrationFrame, code, subID, requestID string) replicaErrorPayload {
	t.Helper()
	var payload replicaErrorPayload
	require.NoError(t, json.Unmarshal(frame.message.Payload, &payload))
	require.Equal(t, code, payload.Code)
	require.Equal(t, subID, payload.SubID)
	require.Equal(t, requestID, payload.RequestID)
	return payload
}

func TestReplicaWSIntegrationFullScopeAndAuthoritativeIdentity(t *testing.T) {
	for _, token := range []string{"owner", "slug-grant", "id-grant", "stranger", "invalid"} {
		t.Run(token, func(t *testing.T) {
			var calls atomic.Int32
			env := newReplicaIntegrationEnv(t, func(_ context.Context, _ string, request storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
				calls.Add(1)
				return replicaIntegrationPage(request, "unexpected")
			})
			peer := env.dial(t)
			if token == "stranger" || token == "invalid" {
				peer.send(t, TypeAuth, "denied-auth", map[string]any{"token": token, "database": "app", "mode": "replica-data"})
				code := "FORBIDDEN"
				if token == "invalid" {
					code = "UNAUTHORIZED"
				}
				frame := peer.receive(t, TypeError)
				require.Equal(t, "denied-auth", frame.message.ID)
				replicaIntegrationError(t, frame, code, "", "")
			} else {
				peer.authenticate(t, token)
				source := replicaIntegrationSource()
				peer.subscribe(t, "bound", source)
				env.database.replace()
				peer.read(t, "bound", "must-not-read-new-database", source, nil)
				frame := peer.receive(t, TypeError)
				code := "DATABASE_IDENTITY_MISMATCH"
				if token == "id-grant" {
					// An old object-ID grant does not authorize discovery of its replacement.
					code = "FORBIDDEN"
				}
				replicaIntegrationError(t, frame, code, "bound", "must-not-read-new-database")
			}
			require.Zero(t, calls.Load())
			require.Zero(t, env.database.cached.Load())
		})
	}
}

func TestReplicaWSIntegrationAckOrdersReadsAndDuplicateAckIsIdempotent(t *testing.T) {
	var calls atomic.Int32
	env := newReplicaIntegrationEnv(t, func(_ context.Context, _ string, request storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
		return replicaIntegrationPage(request, fmt.Sprintf("cursor-%d", calls.Add(1)))
	})
	peer := env.dial(t)
	peer.authenticate(t, "owner")
	source := replicaIntegrationSource()
	peer.subscribe(t, "ordered", source)
	peer.read(t, "ordered", "first", source, nil)
	first := decodeReplicaIntegrationPage(t, peer.receive(t, "replica_page"), "ordered", "first")
	var checkpoint string
	require.NoError(t, json.Unmarshal(first["checkpoint"], &checkpoint))
	peer.read(t, "ordered", "premature", source, checkpoint)
	replicaIntegrationError(t, peer.receive(t, TypeError), "REPLICATION_PROTOCOL_ERROR", "ordered", "premature")
	require.Equal(t, int32(1), calls.Load())
	peer.send(t, "replica_ack", "first", map[string]any{"subId": "ordered", "requestId": "first"})
	peer.send(t, "replica_ack", "first", map[string]any{"subId": "ordered", "requestId": "first"})
	peer.read(t, "ordered", "second", source, checkpoint)
	page := decodeReplicaIntegrationPage(t, peer.receive(t, "replica_page"), "ordered", "second")
	require.NotEqual(t, string(first["checkpoint"]), string(page["checkpoint"]))
	require.Equal(t, int32(2), calls.Load())
}

func TestReplicaWSIntegrationPressureRetainsCanceledQueryPermit(t *testing.T) {
	entered := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseQuery := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseQuery()
	var calls atomic.Int32
	env := newReplicaIntegrationEnv(t, func(ctx context.Context, _ string, request storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-ctx.Done()
			close(canceled)
			<-release
		}
		return replicaIntegrationPage(request, "safe-current")
	}, func(cfg *gatewayconfig.RealtimeConfig) {
		cfg.Replica.ReadConcurrency = 1
		cfg.Replica.PageCreditsPerConnection = 1
	})
	first, second := env.dial(t), env.dial(t)
	first.authenticate(t, "owner")
	second.authenticate(t, "id-grant")
	source := replicaIntegrationSource()
	first.subscribe(t, "held", source)
	first.subscribe(t, "other", source)
	second.subscribe(t, "waiting", source)
	first.read(t, "held", "held-read", source, nil)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first Query did not start")
	}
	first.read(t, "other", "connection-overload", source, nil)
	transport := replicaIntegrationError(t, first.receive(t, TypeError), "REPLICATION_TRANSPORT_BUSY", "other", "connection-overload")
	require.Zero(t, transport.RetryAfter)
	second.read(t, "waiting", "source-overload", source, nil)
	busy := replicaIntegrationError(t, second.receive(t, TypeError), "REPLICATION_SOURCE_BUSY", "waiting", "source-overload")
	require.Greater(t, busy.RetryAfter, 0)
	require.Equal(t, int32(1), calls.Load())
	first.send(t, TypeUnsubscribe, "held", map[string]any{"subId": "held"})
	ack := first.receive(t, TypeUnsubscribeAck)
	require.Equal(t, "held", ack.message.ID)
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("unsubscribe did not cancel the Query context")
	}
	require.EqualValues(t, 1, env.server.replicaBudget.snapshot().Reads)
	first.read(t, "other", "retired-tail-credit", source, nil)
	replicaIntegrationError(t, first.receive(t, TypeError), "REPLICATION_TRANSPORT_BUSY", "other", "retired-tail-credit")
	second.read(t, "waiting", "tail-still-running", source, nil)
	replicaIntegrationError(t, second.receive(t, TypeError), "REPLICATION_SOURCE_BUSY", "waiting", "tail-still-running")
	require.Equal(t, int32(1), calls.Load())
	releaseQuery()
	require.Eventually(t, func() bool { return env.server.replicaBudget.snapshot().Reads == 0 }, 3*time.Second, time.Millisecond)
	second.read(t, "waiting", "admitted-after-drain", source, nil)
	decodeReplicaIntegrationPage(t, second.receive(t, "replica_page"), "waiting", "admitted-after-drain")
	require.Equal(t, int32(2), calls.Load())
	select {
	case frame := <-first.frames:
		require.NotEqual(t, "replica_page", frame.message.Type, "retired Query result escaped its owner")
	default:
	}
	second.send(t, "replica_ack", "admitted-after-drain", map[string]any{"subId": "waiting", "requestId": "admitted-after-drain"})
	require.Eventually(t, func() bool { return env.server.replicaBudget.snapshot().PageBytes == 0 }, 3*time.Second, time.Millisecond)
}

func TestReplicaWSIntegrationTerminalStreamerRetiresInflightPage(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	env := newReplicaIntegrationEnv(t, func(ctx context.Context, _ string, request storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		return replicaIntegrationPage(request, "late-retired-backend")
	})
	peer := env.dial(t)
	peer.authenticate(t, "owner")
	source := replicaIntegrationSource()
	peer.subscribe(t, "backend-owned", source)
	peer.read(t, "backend-owned", "old-generation-read", source, nil)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("Query did not start before backend retirement")
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, env.streamer.Stop(shutdown))
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("retiring the actual Stream did not cancel its Query")
	}
	select {
	case <-peer.done:
	case <-time.After(5 * time.Second):
		t.Fatal("terminal Stream left the old WebSocket connected")
	}
	for len(peer.frames) > 0 {
		frame := <-peer.frames
		require.NotEqual(t, "replica_page", frame.message.Type, "old Stream published its late Query result")
	}
	require.Eventually(t, func() bool {
		usage := env.server.replicaBudget.snapshot()
		return usage.Reads == 0 && usage.Connections == 0 && usage.Subscriptions == 0 && usage.Pending == 0 && usage.PageBytes == 0 && usage.SourceBytes == 0
	}, 3*time.Second, time.Millisecond)
}

func TestReplicaWSIntegrationSocketCloseInterruptsBlockedLargePage(t *testing.T) {
	env := newReplicaIntegrationEnv(t, func(_ context.Context, _ string, request storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
		hash, err := querycore.ReplicationSourceHash(request)
		if err != nil {
			return nil, err
		}
		complete := true
		return &storage.ReplicationPullResponse{ProtocolVersion: 1, Mode: "replace", DatabaseIdentity: request.DatabaseIdentity,
			SourceHash: hash, GenerationID: "01234567-89ab-4def-8123-456789abcdef", RequestID: request.RequestID, Complete: &complete,
			EffectiveOrder: []model.Order{{Field: "id", Direction: "asc"}}, Documents: []model.Document{{"id": "large", "collection": "users",
				"version": int64(1), "createdAt": int64(1), "updatedAt": int64(1), "active": true, "payload": strings.Repeat("x", 6<<20)}}}, nil
	})
	conn, _, err := websocket.DefaultDialer.Dial(strings.Replace(env.http.URL, "http", "ws", 1)+"?mode=replica-data", nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	serverSocket := <-env.sockets
	require.NoError(t, serverSocket.(*net.TCPConn).SetWriteBuffer(1024))
	require.NoError(t, conn.UnderlyingConn().(*net.TCPConn).SetReadBuffer(1024))
	peer := &replicaIntegrationPeer{conn: conn}
	peer.send(t, TypeAuth, "slow-auth", map[string]any{"token": "owner", "database": "app", "mode": "replica-data"})
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	var frame BaseMessage
	require.NoError(t, conn.ReadJSON(&frame))
	require.Equal(t, TypeAuthAck, frame.Type)
	source := replicaIntegrationSource()
	source["limit"] = 1
	peer.send(t, TypeSubscribe, "slow", map[string]any{"collection": "users", "source": source})
	require.NoError(t, conn.ReadJSON(&frame))
	require.Equal(t, TypeSubscribeAck, frame.Type)
	peer.read(t, "slow", "slow-read", source, nil)
	kind, reader, err := conn.NextReader()
	require.NoError(t, err)
	require.Equal(t, websocket.TextMessage, kind)
	prefix := make([]byte, 80)
	_, err = io.ReadFull(reader, prefix)
	require.NoError(t, err)
	require.Contains(t, string(prefix), `"type":"replica_page"`)
	// A six-MiB message cannot finish in these bounded TCP buffers while the
	// client consumes only its header. Closing must interrupt the active write.
	require.Greater(t, env.server.replicaBudget.snapshot().PageBytes, int64(0))
	require.NoError(t, conn.Close())
	require.Eventually(t, func() bool {
		usage := env.server.replicaBudget.snapshot()
		return usage.Connections == 0 && usage.Reads == 0 && usage.PageBytes == 0 && usage.Subscriptions == 0 && usage.SourceBytes == 0 && usage.Pending == 0
	}, 3*time.Second, time.Millisecond)
}

func TestReplicaWSIntegrationReauthenticationDiscardsOldQueryCompletion(t *testing.T) {
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var calls atomic.Int32
	env := newReplicaIntegrationEnv(t, func(ctx context.Context, _ string, request storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
		sequence := calls.Add(1)
		if sequence == 1 {
			close(started)
			<-ctx.Done()
			close(canceled)
			<-release
		}
		return replicaIntegrationPage(request, fmt.Sprintf("generation-position-%d", sequence))
	})
	peer := env.dial(t)
	peer.authenticate(t, "owner")
	source := replicaIntegrationSource()
	peer.subscribe(t, "old-auth", source)
	peer.read(t, "old-auth", "old-read", source, nil)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("old authenticated read did not start")
	}
	peer.send(t, TypeAuth, "replacement-auth", map[string]any{"token": "slug-grant", "database": "app", "mode": "replica-data"})
	require.Equal(t, "replacement-auth", peer.receive(t, TypeAuthAck).message.ID)
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("reauthentication did not cancel the old Query context")
	}
	peer.subscribe(t, "new-auth", source)
	peer.read(t, "new-auth", "new-read", source, nil)
	page := decodeReplicaIntegrationPage(t, peer.receive(t, "replica_page"), "new-auth", "new-read")
	var checkpoint string
	require.NoError(t, json.Unmarshal(page["checkpoint"], &checkpoint))
	peer.send(t, "replica_ack", "new-read", map[string]any{"subId": "new-auth", "requestId": "new-read"})
	unblock()
	require.Eventually(t, func() bool {
		usage := env.server.replicaBudget.snapshot()
		return usage.Reads == 0 && usage.PageBytes == 0
	}, 3*time.Second, time.Millisecond)
	// This fresh response also forms a TCP-order barrier behind any obsolete
	// frame incorrectly emitted when the old Query finally returned.
	peer.read(t, "new-auth", "after-old-drain", source, checkpoint)
	decodeReplicaIntegrationPage(t, peer.receive(t, "replica_page"), "new-auth", "after-old-drain")
	require.Equal(t, int32(3), calls.Load())
}

func TestReplicaWSIntegrationTokenExpiryCancelsLatePage(t *testing.T) {
	started, canceled := make(chan struct{}), make(chan struct{})
	env := newReplicaIntegrationEnv(t, func(ctx context.Context, _ string, request storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		return replicaIntegrationPage(request, "expired-owner-position")
	})
	peer := env.dial(t)
	expires := time.Now().Add(2 * time.Second).Truncate(time.Second).Add(time.Second)
	peer.authenticate(t, "expires:"+expires.Format(time.RFC3339Nano))
	source := replicaIntegrationSource()
	peer.subscribe(t, "expires", source)
	peer.read(t, "expires", "expires-read", source, nil)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("Query did not begin before token expiry")
	}
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("token expiry did not cancel Query work")
	}
	require.False(t, time.Now().Before(expires))
	select {
	case <-peer.done:
	case <-time.After(3 * time.Second):
		t.Fatal("expired token left the replica WebSocket connected")
	}
	for len(peer.frames) > 0 {
		require.NotEqual(t, "replica_page", (<-peer.frames).message.Type)
	}
	require.Eventually(t, func() bool {
		usage := env.server.replicaBudget.snapshot()
		return usage.Reads == 0 && usage.PageBytes == 0 && usage.Connections == 0
	}, 3*time.Second, time.Millisecond)
}
