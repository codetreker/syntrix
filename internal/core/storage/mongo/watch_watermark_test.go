package mongo

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
)

func TestWatchTargetPersistsAcrossFilteredPages(t *testing.T) {
	cp := watchBinding()
	cp.Target = watchToken(t, "05")
	after, err := cp.encode(watchToken(t, "01"))
	require.NoError(t, err)
	for _, prefix := range []string{"02", "03", "04"} {
		native := &stubChangeStream{token: watchToken(t, prefix)}
		store := nativeWatch(t, native)
		store.readWatchTarget = func(context.Context, *mongo.Collection, watchCheckpoint) (bson.Raw, error) {
			t.Fatal("unfinished target must be reused")
			return nil, nil
		}
		stream, err := store.Watch(context.Background(), "tenant", "users", after, types.WatchOptions{})
		require.NoError(t, err)
		require.Equal(t, after, stream.InitialCheckpoint())
		frame, err := stream.Next(context.Background())
		require.NoError(t, err)
		require.False(t, frame.CaughtUp, "an empty native batch may still precede the target")
		require.Nil(t, frame.Event)
		decoded, err := decodeWatchCheckpoint(frame.Checkpoint)
		require.NoError(t, err)
		require.Equal(t, cp.Target, decoded.Target)
		after = frame.Checkpoint
		require.NoError(t, stream.Close())
	}
	change := watchChange(t, "insert")
	change.ID = watchToken(t, "05")
	native := &stubChangeStream{token: watchToken(t, "05"), events: []bson.Raw{mustBSON(t, change)}}
	store := nativeWatch(t, native)
	stream, err := store.Watch(context.Background(), "tenant", "users", after, types.WatchOptions{})
	require.NoError(t, err)
	frame, err := stream.Next(context.Background())
	require.NoError(t, err)
	require.False(t, frame.CaughtUp)
	decoded, err := decodeWatchCheckpoint(frame.Checkpoint)
	require.NoError(t, err)
	require.Equal(t, cp.Target, decoded.Target, "a full data page must retain the target until a proof frame")
	require.NoError(t, stream.Close())
	stream, err = store.Watch(context.Background(), "tenant", "users", frame.Checkpoint, types.WatchOptions{})
	require.NoError(t, err)
	frame, err = stream.Next(context.Background())
	require.NoError(t, err)
	require.True(t, frame.CaughtUp)
	decoded, err = decodeWatchCheckpoint(frame.Checkpoint)
	require.NoError(t, err)
	require.Empty(t, decoded.Target)
	change.ID = watchToken(t, "06")
	native.token = change.ID
	native.events = []bson.Raw{mustBSON(t, change)}
	frame, err = stream.Next(context.Background())
	require.NoError(t, err)
	decoded, err = decodeWatchCheckpoint(frame.Checkpoint)
	require.NoError(t, err)
	require.Empty(t, decoded.Target, "completed targets must not reappear on later live events")
	require.NoError(t, stream.Close())
	store.readWatchTarget = func(context.Context, *mongo.Collection, watchCheckpoint) (bson.Raw, error) {
		return watchToken(t, "07"), nil
	}
	stream, err = store.Watch(context.Background(), "tenant", "users", frame.Checkpoint, types.WatchOptions{})
	require.NoError(t, err)
	defer stream.Close()
	frame, err = stream.Next(context.Background())
	require.NoError(t, err)
	require.False(t, frame.CaughtUp, "an independent drain captures fresh committed work")
}

func TestWatchTargetTopologyAndFailures(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	for _, scenario := range []string{"replica", "mongos", "bootstrap", "read failure", "hello failure", "open failure", "missing token", "missing ordered key", "close failure"} {
		mt.Run(scenario, func(mt *mtest.T) {
			clock := watchScanBinding(mt.T)
			cp := watchBinding()
			if scenario == "bootstrap" {
				cp = clock
			} else {
				response := mtest.CreateCursorResponse(0, mt.DB.Name()+".docs", mtest.FirstBatch)
				response = append(response, bson.E{Key: "operationTime", Value: *clock.Start}, bson.E{Key: "$clusterTime", Value: clock.ClusterTime.Lookup("$clusterTime").Document()})
				if scenario == "read failure" {
					response = mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 13, Message: "denied"})
				}
				mt.AddMockResponses(response)
			}
			hello := bson.D{{Key: "ok", Value: 1}}
			if scenario == "mongos" {
				hello = append(hello, bson.E{Key: "msg", Value: "isdbgrid"})
			}
			if scenario == "hello failure" {
				hello = mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 13, Message: "denied"})
			}
			mt.AddMockResponses(hello)
			native := &stubChangeStream{token: watchToken(mt.T, "05")}
			if scenario == "missing token" {
				native.token = nil
			}
			if scenario == "missing ordered key" {
				native.token = mustBSON(mt.T, bson.M{"value": 1})
			}
			if scenario == "close failure" {
				native.closeErr = errors.New("close failed")
			}
			store := &documentStore{client: mt.Client, db: mt.DB}
			store.openStream = func(_ context.Context, _ *mongo.Collection, _ mongo.Pipeline, opts *options.ChangeStreamOptions) (changeStream, error) {
				expected := *clock.Start
				if scenario == "mongos" {
					expected.I++
				}
				require.Equal(mt, &expected, opts.StartAtOperationTime)
				require.EqualValues(mt, 0, *opts.BatchSize)
				if scenario == "open failure" {
					return nil, errors.New("open failed")
				}
				return native, nil
			}
			target, err := store.watchTarget(context.Background(), mt.DB.Collection("docs"), cp)
			if scenario == "replica" || scenario == "mongos" || scenario == "bootstrap" {
				require.NoError(mt, err)
				require.Equal(mt, native.token, target)
			} else {
				require.Error(mt, err)
			}
			if scenario != "read failure" && scenario != "hello failure" && scenario != "open failure" {
				require.EqualValues(mt, 1, native.closeCalls.Load())
				require.True(mt, native.closeBound.Load())
			}
		})
	}
	client, err := mongo.NewClient()
	require.NoError(t, err)
	store := &documentStore{client: client, db: client.Database("test")}
	_, err = store.watchTarget(context.Background(), store.db.Collection("docs"), watchScanBinding(t))
	require.ErrorIs(t, err, mongo.ErrClientDisconnected)
}

func TestWatchTargetCheckpointValidation(t *testing.T) {
	cp := watchBinding()
	for _, invalid := range []bson.Raw{{1, 2}, mustBSON(t, bson.M{"value": 1}), mustBSON(t, bson.M{"_data": ""})} {
		cp.Target = invalid
		_, err := cp.encode(watchToken(t, "01"))
		require.Error(t, err)
	}
	for _, tc := range []struct{ from, to primitive.Timestamp }{{primitive.Timestamp{T: 1, I: 1}, primitive.Timestamp{T: 1, I: 2}}, {primitive.Timestamp{T: 1, I: ^uint32(0)}, primitive.Timestamp{T: 2}}} {
		got, err := nextWatchTimestamp(tc.from)
		require.NoError(t, err)
		require.Equal(t, tc.to, got)
	}
	_, err := nextWatchTimestamp(primitive.Timestamp{T: ^uint32(0), I: ^uint32(0)})
	require.Error(t, err)
}

func TestWatchTargetFailureReleasesResumedSession(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	for _, scenario := range []string{"target read failure", "invalid target"} {
		mt.Run(scenario, func(mt *mtest.T) {
			cp := watchScanBinding(mt.T)
			after, err := cp.marshal()
			require.NoError(mt, err)
			store := &documentStore{client: mt.Client, db: mt.DB, dataCollection: "docs", sysCollection: "sys"}
			store.readSource = func(context.Context, *mongo.Collection, bool) (watchSource, error) { return cp.Source, nil }
			var observed context.Context
			var session mongo.Session
			cause := &mongo.CommandError{Code: 13, Message: "private source details"}
			store.readWatchTarget = func(ctx context.Context, _ *mongo.Collection, _ watchCheckpoint) (bson.Raw, error) {
				observed, session = ctx, mongo.SessionFromContext(ctx)
				require.NotNil(mt, session)
				require.Equal(mt, cp.Start, session.OperationTime())
				if scenario == "target read failure" {
					return nil, cause
				}
				return mustBSON(mt.T, bson.M{"value": 1}), nil
			}
			store.openStream = func(context.Context, *mongo.Collection, mongo.Pipeline, *options.ChangeStreamOptions) (changeStream, error) {
				mt.Fatal("target failure must prevent opening the data stream")
				return nil, nil
			}
			stream, err := store.Watch(context.Background(), "tenant", "users", after, types.WatchOptions{})
			require.Nil(mt, stream)
			if scenario == "target read failure" {
				requireWatchCode(mt.T, err, types.WatchPermissionDenied)
				require.ErrorIs(mt, err, cause)
			} else {
				requireWatchCode(mt.T, err, types.WatchInvalidCheckpoint)
			}
			require.NotContains(mt, err.Error(), "private source details")
			require.ErrorIs(mt, observed.Err(), context.Canceled)
			require.Error(mt, session.AdvanceOperationTime(cp.Start), "the resumed causal session must be ended")
		})
	}
}

func TestMongoWatchIdleWatermarkNeedsNoNewWrite(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", 0)
	first, err := store.Watch(ctx, "tenant", "users", "", types.WatchOptions{StartMode: types.WatchStartForScan, MaxAwaitTime: 10 * time.Millisecond})
	require.NoError(t, err)
	initial := first.InitialCheckpoint()
	cp, err := decodeWatchCheckpoint(initial)
	require.NoError(t, err)
	require.Empty(t, cp.Target)
	require.NoError(t, first.Close())
	for i := 0; i < 2; i++ {
		stream, err := store.Watch(ctx, "tenant", "users", initial, types.WatchOptions{MaxAwaitTime: 10 * time.Millisecond})
		require.NoError(t, err)
		for {
			frame, err := stream.Next(ctx)
			require.NoError(t, err)
			if frame.CaughtUp {
				initial = frame.Checkpoint
				break
			}
		}
		require.NoError(t, stream.Close())
	}
}

func TestMongoWatchFilteredBacklogPrecedesCaughtUp(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", 0)
	first, err := store.Watch(ctx, "tenant", "users", "", types.WatchOptions{StartMode: types.WatchStartForScan})
	require.NoError(t, err)
	after := first.InitialCheckpoint()
	require.NoError(t, first.Close())
	collection := env.DB.Collection("docs", options.Collection().SetWriteConcern(writeconcern.Majority()))
	backlog := make([]interface{}, 2000)
	for i := range backlog {
		backlog[i] = types.NewStoredDoc("unrelated", "users", fmt.Sprintf("doc-%04d", i), nil)
	}
	_, err = collection.InsertMany(ctx, backlog)
	require.NoError(t, err)
	_, err = collection.InsertOne(ctx, types.NewStoredDoc("tenant", "users", "final", nil))
	require.NoError(t, err)
	stream, err := store.Watch(ctx, "tenant", "users", after, types.WatchOptions{MaxAwaitTime: time.Millisecond})
	require.NoError(t, err)
	defer stream.Close()
	seenFinal := false
	for {
		frame, err := stream.Next(ctx)
		require.NoError(t, err)
		if frame.Event != nil {
			require.Equal(t, "final", frame.Event.DocumentID)
			seenFinal = true
		}
		if frame.CaughtUp {
			require.True(t, seenFinal, "quiescent source watermark must cover the relevant change after the filtered backlog")
			break
		}
	}
}
