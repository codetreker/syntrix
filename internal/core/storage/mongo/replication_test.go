package mongo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/event"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
)

func requireReplicationCode(t *testing.T, err error, code types.ReplicationErrorCode) {
	t.Helper()
	var typed *types.ReplicationError
	require.ErrorAs(t, err, &typed)
	require.Equal(t, code, typed.Code)
}

func testReplicationCheckpoint(t *testing.T) replicationCheckpoint {
	t.Helper()
	cluster, err := bson.Marshal(bson.D{{Key: "$clusterTime", Value: bson.D{{Key: "clusterTime", Value: primitive.Timestamp{T: 10, I: 2}}, {Key: "signature", Value: bson.D{{Key: "hash", Value: primitive.Binary{Data: make([]byte, 20)}}, {Key: "keyId", Value: int64(0)}}}}}})
	require.NoError(t, err)
	return replicationCheckpoint{Version: 1, Source: watchSource{Database: "physical", Collection: "docs", UUID: strings.Repeat("a", 32)}, Database: "tenant", Collection: "users", Phase: types.ReplicationScan, Start: primitive.Timestamp{T: 10, I: 1}, OperationTime: primitive.Timestamp{T: 10, I: 1}, ClusterTime: cluster}
}

func TestReplicationCheckpointValidation(t *testing.T) {
	cp := testReplicationCheckpoint(t)
	position, err := cp.position()
	require.NoError(t, err)
	decoded, err := decodeReplicationCheckpoint(position)
	require.NoError(t, err)
	require.Equal(t, cp, decoded)
	for _, mutate := range []func(*replicationCheckpoint){
		func(c *replicationCheckpoint) { c.AfterID = "a/b" },
		func(c *replicationCheckpoint) { c.OperationTime = primitive.Timestamp{T: 9} },
		func(c *replicationCheckpoint) { c.Source.UUID = "bad" },
		func(c *replicationCheckpoint) { c.ClusterTime = bson.Raw{1} },
		func(c *replicationCheckpoint) { c.Phase = types.ReplicationChanges; c.AfterID = "alice" },
	} {
		bad := cp
		mutate(&bad)
		raw, marshalErr := json.Marshal(bad)
		require.NoError(t, marshalErr)
		_, decodeErr := decodeReplicationCheckpoint(types.ReplicationPosition{Phase: bad.Phase, Opaque: base64.RawURLEncoding.EncodeToString(raw)})
		require.Error(t, decodeErr)
	}
	raw, err := base64.RawURLEncoding.DecodeString(position.Opaque)
	require.NoError(t, err)
	position.Opaque = base64.RawURLEncoding.EncodeToString(append(raw, ' '))
	_, err = decodeReplicationCheckpoint(position)
	require.Error(t, err)
}

func TestReplicationMutationIdentityAndPhysicalCleanup(t *testing.T) {
	cp := testReplicationCheckpoint(t)
	token := bson.D{{Key: "_data", Value: "token"}}
	event := bson.D{{Key: "_id", Value: token}, {Key: "operationType", Value: "delete"}, {Key: "documentKey", Value: bson.D{{Key: "_id", Value: "unrecoverable"}}}}
	raw, err := bson.Marshal(event)
	require.NoError(t, err)
	mutation, err := parseReplicationMutation(raw, cp)
	require.NoError(t, err)
	require.Nil(t, mutation.identity)
	event[1].Value = "update"
	raw, err = bson.Marshal(event)
	require.NoError(t, err)
	_, err = parseReplicationMutation(raw, cp)
	requireReplicationCode(t, err, types.ReplicationIdentityUnavailable)
	doc := types.NewStoredDoc("tenant", "users", "alice", nil)
	doc.Deleted = true
	event[2].Value = bson.D{{Key: "_id", Value: doc.Id}}
	event = append(event, bson.E{Key: "fullDocumentBeforeChange", Value: doc})
	raw, err = bson.Marshal(event)
	require.NoError(t, err)
	mutation, err = parseReplicationMutation(raw, cp)
	require.NoError(t, err)
	require.Equal(t, "users/alice", mutation.identity.Fullpath)
	event[1].Value = "drop"
	raw, err = bson.Marshal(event)
	require.NoError(t, err)
	_, err = parseReplicationMutation(raw, cp)
	requireReplicationCode(t, err, types.ReplicationSourceMismatch)
}

func drainReplication(t *testing.T, ctx context.Context, source types.ReplicationSource, position types.ReplicationPosition, limit int, mirror map[string]*types.ReplicationState) types.ReplicationPosition {
	t.Helper()
	for i := 0; i < 100; i++ {
		var page types.ReplicationPage
		var err error
		budget := types.ReplicationBudget{Limit: limit}
		if position.Phase == types.ReplicationScan {
			page, err = source.ReadBootstrapPage(ctx, "tenant", "users", position, budget)
		} else {
			page, err = source.ReadChangesPage(ctx, "tenant", "users", position, budget)
		}
		require.NoError(t, err)
		for _, frame := range page.Frames {
			if frame.State != nil {
				mirror[frame.State.ID] = frame.State
			}
		}
		position = page.End
		if page.CaughtUp {
			return position
		}
	}
	t.Fatal("replication did not reach watermark")
	return position
}

func TestMongoReplicationBootstrapRacesAndPortableCursor(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", 0)
	source := store.(types.ReplicationSource)
	for _, id := range []string{"b", "d", "f"} {
		doc := types.NewStoredDoc("tenant", "users", id, map[string]interface{}{"n": int64(9007199254740993)})
		doc.UpdatedAt = 100
		require.NoError(t, store.Create(ctx, "tenant", doc))
	}
	position, err := source.BeginBootstrap(ctx, "tenant", "users", types.ReplicationBudget{})
	require.NoError(t, err)
	page, err := source.ReadBootstrapPage(ctx, "tenant", "users", position, types.ReplicationBudget{Limit: 1})
	require.NoError(t, err)
	require.Len(t, page.Frames, 1)
	require.Equal(t, "b", page.Frames[0].State.ID)
	mirror := map[string]*types.ReplicationState{"b": page.Frames[0].State}
	require.NoError(t, store.Create(ctx, "tenant", types.NewStoredDoc("tenant", "users", "a", map[string]interface{}{"n": int64(9007199254740993)})))
	require.NoError(t, store.Update(ctx, "tenant", "users/b", map[string]interface{}{"changed": true}, nil))
	require.NoError(t, store.Delete(ctx, "tenant", "users/d", nil))
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(testMongoURI))
	require.NoError(t, err)
	defer client.Disconnect(context.Background())
	other := NewDocumentStore(client, client.Database(env.DBName), "docs", "sys", 0).(types.ReplicationSource)
	position = drainReplication(t, ctx, other, page.End, 2, mirror)
	require.Equal(t, true, mirror["b"].Document.Data["changed"])
	require.True(t, mirror["d"].Deleted)
	require.Equal(t, int64(9007199254740993), mirror["a"].Document.Data["n"])
	require.NoError(t, store.Create(ctx, "tenant", types.NewStoredDoc("tenant", "users", "d", map[string]interface{}{"recreated": true})))
	position = drainReplication(t, ctx, other, position, 1, mirror)
	require.False(t, mirror["d"].Deleted)
	require.Equal(t, true, mirror["d"].Document.Data["recreated"])
	_, err = other.ReadChangesPage(ctx, "other", "users", position, types.ReplicationBudget{})
	requireReplicationCode(t, err, types.ReplicationScopeMismatch)
	require.NoError(t, env.DB.Collection("docs").Drop(ctx))
	require.NoError(t, env.DB.CreateCollection(ctx, "docs"))
	_, err = other.ReadChangesPage(ctx, "tenant", "users", position, types.ReplicationBudget{})
	requireReplicationCode(t, err, types.ReplicationSourceMismatch)
}

func TestMongoReplicationPrefixesAndPhysicalCleanup(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", 0)
	source := store.(types.ReplicationSource)
	position, err := source.BeginBootstrap(ctx, "tenant", "users", types.ReplicationBudget{})
	require.NoError(t, err)
	mirror := make(map[string]*types.ReplicationState)
	position = drainReplication(t, ctx, source, position, 10, mirror)
	for _, id := range []string{"a", "b", "c"} {
		require.NoError(t, store.Create(ctx, "tenant", types.NewStoredDoc("tenant", "users", id, nil)))
	}
	page, err := source.ReadChangesPage(ctx, "tenant", "users", position, types.ReplicationBudget{Limit: 3})
	require.NoError(t, err)
	require.Len(t, page.Frames, 3)
	// A transport accepting only the first state resumes every unreturned state.
	position = page.Frames[0].After
	mirror[page.Frames[0].State.ID] = page.Frames[0].State
	position = drainReplication(t, ctx, source, position, 1, mirror)
	require.Len(t, mirror, 3)
	require.NoError(t, store.Delete(ctx, "tenant", "users/a", nil))
	position = drainReplication(t, ctx, source, position, 10, mirror)
	require.True(t, mirror["a"].Deleted)
	_, err = env.DB.Collection("docs").DeleteOne(ctx, bson.M{"_id": types.CalculateDatabase("tenant", "users/a")})
	require.NoError(t, err)
	page, err = source.ReadChangesPage(ctx, "tenant", "users", position, types.ReplicationBudget{})
	require.NoError(t, err)
	for _, frame := range page.Frames {
		require.Nil(t, frame.State)
	}
	require.True(t, page.CaughtUp)
	_, err = source.ReadChangesPage(ctx, "tenant", "users", page.End, types.ReplicationBudget{SoftDeadline: time.Now().Add(-time.Second)})
	require.Error(t, err)
}

func TestMongoReplicationGrowingBatchRetainsCompletedPrefix(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", 0).(*documentStore)
	position, err := store.BeginBootstrap(ctx, "tenant", "users", types.ReplicationBudget{})
	require.NoError(t, err)
	cp, err := decodeReplicationCheckpoint(position)
	require.NoError(t, err)
	cp.Phase = types.ReplicationChanges
	position, err = cp.position()
	require.NoError(t, err)
	events := make([]bson.Raw, 0, 3)
	collection, err := env.DB.Collection("docs").Clone(options.Collection().SetWriteConcern(writeconcern.Majority()))
	require.NoError(t, err)
	for i, id := range []string{"a", "b", "c"} {
		doc := types.NewStoredDoc("tenant", "users", id, nil)
		event := changeStreamEvent{ID: watchToken(t, id), OperationType: "update", FullDocument: &doc, ClusterTime: cp.Start}
		event.DocumentKey.ID = doc.Id
		raw, encodeErr := bson.Marshal(event)
		require.NoError(t, encodeErr)
		events = append(events, raw)
		if i > 0 {
			doc.Data = map[string]interface{}{"grown": strings.Repeat("x", 4000)}
		}
		_, insertErr := collection.InsertOne(ctx, doc)
		require.NoError(t, insertErr)
	}
	store.openStream = func(_ context.Context, _ *mongo.Collection, _ mongo.Pipeline, opts *options.ChangeStreamOptions) (changeStream, error) {
		start := 0
		if opts.ResumeAfter != nil {
			token := opts.ResumeAfter.(bson.Raw)
			switch token.Lookup("_data").StringValue() {
			case "a":
				start = 1
			case "b":
				start = 2
			case "c":
				start = 3
			}
		}
		return &stubChangeStream{events: append([]bson.Raw(nil), events[start:]...), token: watchToken(t, "c")}, nil
	}
	for _, expected := range []string{"a", "b", "c"} {
		page, pageErr := store.ReadChangesPage(ctx, "tenant", "users", position, types.ReplicationBudget{Limit: 3, MaxSourceBytes: 6000})
		require.NoError(t, pageErr)
		require.Len(t, page.Frames, 1)
		require.Equal(t, expected, page.Frames[0].State.ID)
		require.LessOrEqual(t, page.Usage.SourceBytes, int64(6000))
		position = page.End
	}
}

func TestMongoReplicationMissingIdentityAndFilteredProgress(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", 0).(*documentStore)
	position, err := store.BeginBootstrap(ctx, "tenant", "users", types.ReplicationBudget{})
	require.NoError(t, err)
	cp, err := decodeReplicationCheckpoint(position)
	require.NoError(t, err)
	cp.Phase = types.ReplicationChanges
	position, err = cp.position()
	require.NoError(t, err)
	doc := types.NewStoredDoc("tenant", "users", "absent", nil)
	event := changeStreamEvent{ID: watchToken(t, "one"), OperationType: "update", FullDocument: &doc, ClusterTime: cp.Start}
	event.DocumentKey.ID = doc.Id
	var native *stubChangeStream
	store.openStream = func(context.Context, *mongo.Collection, mongo.Pipeline, *options.ChangeStreamOptions) (changeStream, error) {
		return native, nil
	}
	setEvent := func() {
		raw, encodeErr := bson.Marshal(event)
		require.NoError(t, encodeErr)
		native = &stubChangeStream{events: []bson.Raw{raw}, token: watchToken(t, "end")}
	}
	setEvent()
	page, err := store.ReadChangesPage(ctx, "tenant", "users", position, types.ReplicationBudget{Limit: 1})
	require.NoError(t, err)
	require.Len(t, page.Frames, 1)
	require.True(t, page.Frames[0].State.Deleted)
	require.Nil(t, page.Frames[0].State.Document)
	require.Equal(t, int32(1), native.closeCalls.Load())
	event.FullDocument = nil
	setEvent()
	page, err = store.ReadChangesPage(ctx, "tenant", "users", position, types.ReplicationBudget{})
	requireReplicationCode(t, err, types.ReplicationIdentityUnavailable)
	require.Empty(t, page.End.Opaque)
	doc = types.NewStoredDoc("tenant", "other", "ignored", nil)
	event.FullDocument = &doc
	event.DocumentKey.ID = doc.Id
	setEvent()
	page, err = store.ReadChangesPage(ctx, "tenant", "users", position, types.ReplicationBudget{MaxFrames: 1})
	require.NoError(t, err)
	require.Len(t, page.Frames, 1)
	require.Nil(t, page.Frames[0].State)
	require.False(t, page.CaughtUp)
	require.NotEqual(t, position, page.End)
	event.OperationType = "delete"
	event.FullDocument = nil
	setEvent()
	page, err = store.ReadChangesPage(ctx, "tenant", "users", position, types.ReplicationBudget{})
	require.NoError(t, err)
	require.True(t, page.CaughtUp)
}

type replicationHistoryProbeStream struct {
	changeStream
	reads int
}

func (s *replicationHistoryProbeStream) TryNext(ctx context.Context) bool {
	s.reads++
	return s.changeStream.TryNext(ctx)
}

func TestMongoReplicationHistoryProbeExecutesSourceRead(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", 0).(*documentStore)
	position, err := store.BeginBootstrap(ctx, "tenant", "users", types.ReplicationBudget{})
	require.NoError(t, err)
	native := &replicationHistoryProbeStream{changeStream: &stubChangeStream{err: mongo.CommandError{Code: 286, Message: "history unavailable"}}}
	store.openStream = func(context.Context, *mongo.Collection, mongo.Pipeline, *options.ChangeStreamOptions) (changeStream, error) {
		return native, nil
	}
	page, err := store.ReadBootstrapPage(ctx, "tenant", "users", position, types.ReplicationBudget{})
	requireReplicationCode(t, err, types.ReplicationHistoryUnavailable)
	require.Empty(t, page.End.Opaque)
	require.Equal(t, 1, native.reads)
}

func TestMongoReplicationSameTransactionTokens(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", 0)
	source := store.(types.ReplicationSource)
	position, err := source.BeginBootstrap(ctx, "tenant", "users", types.ReplicationBudget{})
	require.NoError(t, err)
	position = drainReplication(t, ctx, source, position, 10, make(map[string]*types.ReplicationState))
	session, err := env.Client.StartSession()
	require.NoError(t, err)
	defer session.EndSession(ctx)
	_, err = session.WithTransaction(ctx, func(sctx mongo.SessionContext) (interface{}, error) {
		for _, id := range []string{"a", "b", "c"} {
			if createErr := store.Create(sctx, "tenant", types.NewStoredDoc("tenant", "users", id, nil)); createErr != nil {
				return nil, createErr
			}
		}
		return nil, nil
	})
	require.NoError(t, err)
	var last bson.Raw
	for _, id := range []string{"a", "b", "c"} {
		page, pageErr := source.ReadChangesPage(ctx, "tenant", "users", position, types.ReplicationBudget{Limit: 1})
		require.NoError(t, pageErr)
		require.Len(t, page.Frames, 1)
		require.Equal(t, id, page.Frames[0].State.ID)
		cp, decodeErr := decodeReplicationCheckpoint(page.End)
		require.NoError(t, decodeErr)
		require.NotEqual(t, last, cp.Token)
		last = cp.Token
		position = page.End
	}
}

func TestReplicationNativeErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		native   int32
		expected types.ReplicationErrorCode
	}{
		{9, types.ReplicationInvalidCursor}, {260, types.ReplicationInvalidCursor},
		{40647, types.ReplicationInvalidCursor}, {40648, types.ReplicationInvalidCursor},
		{280, types.ReplicationHistoryUnavailable}, {286, types.ReplicationHistoryUnavailable},
	} {
		t.Run(fmt.Sprint(tc.native), func(t *testing.T) {
			cause := &mongo.CommandError{Code: tc.native, Message: "native token failure"}
			err := replicationStreamClassify("tenant", "users", cause)
			requireReplicationCode(t, err, tc.expected)
			require.True(t, errors.Is(err, cause))
		})
	}
	err := replicationClassify("tenant", "users", mongo.CommandError{Code: 9, Message: "ordinary find rejected"})
	requireReplicationCode(t, err, types.ReplicationUnavailable)
}

func TestMongoReplicationMalformedNativeCursor(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", 0).(*documentStore)
	position, err := store.BeginBootstrap(ctx, "tenant", "users", types.ReplicationBudget{})
	require.NoError(t, err)
	position = drainReplication(t, ctx, store, position, 10, make(map[string]*types.ReplicationState))
	cp, err := decodeReplicationCheckpoint(position)
	require.NoError(t, err)
	data := cp.Token.Lookup("_data").StringValue()
	for _, token := range []bson.D{
		{{Key: "_data", Value: int32(1)}},
		{{Key: "_data", Value: "zz"}},
		{{Key: "_data", Value: data}, {Key: "_typeBits", Value: "invalid"}},
	} {
		cp.Token, err = bson.Marshal(token)
		require.NoError(t, err)
		malformed, encodeErr := cp.position()
		require.NoError(t, encodeErr)
		page, readErr := store.ReadChangesPage(ctx, "tenant", "users", malformed, types.ReplicationBudget{})
		requireReplicationCode(t, readErr, types.ReplicationInvalidCursor)
		require.Empty(t, page.End.Opaque)
	}
}

func TestMongoReplicationNativeFailuresOnOpenAndRead(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", 0).(*documentStore)
	position, err := store.BeginBootstrap(ctx, "tenant", "users", types.ReplicationBudget{})
	require.NoError(t, err)
	cp, err := decodeReplicationCheckpoint(position)
	require.NoError(t, err)
	cp.Phase = types.ReplicationChanges
	changes, err := cp.position()
	require.NoError(t, err)
	for _, code := range []int32{9, 280} {
		for _, opening := range []bool{true, false} {
			cause := &mongo.CommandError{Code: code, Message: "replay failure"}
			store.openStream = func(context.Context, *mongo.Collection, mongo.Pipeline, *options.ChangeStreamOptions) (changeStream, error) {
				if opening {
					return nil, cause
				}
				return &stubChangeStream{err: cause}, nil
			}
			expected := types.ReplicationInvalidCursor
			if code == 280 {
				expected = types.ReplicationHistoryUnavailable
			}
			page, readErr := store.ReadChangesPage(ctx, "tenant", "users", changes, types.ReplicationBudget{})
			requireReplicationCode(t, readErr, expected)
			require.Empty(t, page.End.Opaque)
			require.True(t, errors.Is(readErr, cause))
			page, readErr = store.ReadBootstrapPage(ctx, "tenant", "users", position, types.ReplicationBudget{})
			requireReplicationCode(t, readErr, expected)
			require.Empty(t, page.End.Opaque)
			require.True(t, errors.Is(readErr, cause))
		}
	}
}

func TestReplicationIndexSpecification(t *testing.T) {
	keys, err := bson.Marshal(bson.D{{Key: "database", Value: 1}, {Key: "collection", Value: 1}, {Key: "fullpath", Value: 1}})
	require.NoError(t, err)
	valid := replicationIndexSpec{Keys: keys}
	require.True(t, valid.usable())
	for _, mutate := range []func(*replicationIndexSpec){
		func(s *replicationIndexSpec) { s.Sparse = true },
		func(s *replicationIndexSpec) { s.Hidden = true },
		func(s *replicationIndexSpec) { s.Partial = bson.Raw{5, 0, 0, 0, 0} },
		func(s *replicationIndexSpec) { s.Collation, _ = bson.Marshal(bson.M{"locale": "en"}) },
		func(s *replicationIndexSpec) {
			s.Keys, _ = bson.Marshal(bson.D{{Key: "database", Value: 1}, {Key: "fullpath", Value: 1}, {Key: "collection", Value: 1}})
		},
		func(s *replicationIndexSpec) {
			s.Keys, _ = bson.Marshal(bson.D{{Key: "database", Value: 1}, {Key: "collection", Value: 1}, {Key: "fullpath", Value: -1}})
		},
	} {
		spec := valid
		mutate(&spec)
		require.False(t, spec.usable())
	}
}

func TestMongoReplicationBootstrapIndexReadiness(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var creates atomic.Int32
	monitor := &event.CommandMonitor{Started: func(_ context.Context, e *event.CommandStartedEvent) {
		if e.CommandName == "createIndexes" && e.DatabaseName == env.DBName {
			creates.Add(1)
		}
	}}
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(testMongoURI).SetWriteConcern(writeconcern.Majority()).SetMonitor(monitor))
	require.NoError(t, err)
	defer client.Disconnect(context.Background())
	source := NewDocumentStore(client, client.Database(env.DBName), "docs", "sys", 0).(types.ReplicationSource)
	first, err := source.BeginBootstrap(ctx, "tenant", "users", types.ReplicationBudget{})
	require.NoError(t, err)
	require.Equal(t, int32(1), creates.Load())
	_, err = source.BeginBootstrap(ctx, "tenant", "users", types.ReplicationBudget{})
	require.NoError(t, err)
	require.Equal(t, int32(1), creates.Load(), "ready sources must not execute DDL")
	require.NoError(t, env.DB.Collection("docs").Drop(ctx))
	recreated, err := source.BeginBootstrap(ctx, "tenant", "users", types.ReplicationBudget{})
	require.NoError(t, err)
	require.Equal(t, int32(2), creates.Load())
	_, err = source.ReadBootstrapPage(ctx, "tenant", "users", first, types.ReplicationBudget{})
	requireReplicationCode(t, err, types.ReplicationSourceMismatch)
	_, err = source.ReadBootstrapPage(ctx, "tenant", "users", recreated, types.ReplicationBudget{})
	require.NoError(t, err)
	_, err = env.DB.Collection("docs").Indexes().DropOne(ctx, sourceScanIndexName)
	require.NoError(t, err)
	_, err = env.DB.Collection("docs").Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "fullpath", Value: 1}}, Options: options.Index().SetName(sourceScanIndexName)})
	require.NoError(t, err)
	_, err = source.BeginBootstrap(ctx, "tenant", "users", types.ReplicationBudget{})
	requireReplicationCode(t, err, types.ReplicationUnsupported)
}
