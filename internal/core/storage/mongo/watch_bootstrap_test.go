package mongo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func watchScanBinding(t *testing.T) watchCheckpoint {
	t.Helper()
	cp := watchBinding()
	cp.Start = &primitive.Timestamp{T: 100, I: 1}
	cp.ClusterTime = mustBSON(t, bson.D{{Key: "$clusterTime", Value: bson.D{
		{Key: "clusterTime", Value: *cp.Start},
		{Key: "signature", Value: bson.D{{Key: "hash", Value: primitive.Binary{Data: make([]byte, 20)}}, {Key: "keyId", Value: int64(0)}}},
	}}})
	return cp
}

func TestWatchScanCheckpoint(t *testing.T) {
	cp := watchScanBinding(t)
	encoded, err := cp.marshal()
	require.NoError(t, err)
	decoded, err := decodeWatchCheckpoint(encoded)
	require.NoError(t, err)
	require.Equal(t, cp, decoded)
	for name, modify := range map[string]func(*watchCheckpoint){
		"zero boundary":           func(c *watchCheckpoint) { c.Start = &primitive.Timestamp{} },
		"token and boundary":      func(c *watchCheckpoint) { c.Token = watchToken(t, "01") },
		"missing clock":           func(c *watchCheckpoint) { c.ClusterTime = nil },
		"missing cluster context": func(c *watchCheckpoint) { c.ClusterTime = mustBSON(t, bson.D{}) },
		"clock precedes boundary": func(c *watchCheckpoint) { c.Start = &primitive.Timestamp{T: 100, I: 2} },
		"missing signature": func(c *watchCheckpoint) {
			c.ClusterTime = mustBSON(t, bson.M{"$clusterTime": bson.M{"clusterTime": *c.Start}})
		},
		"token with leftover clock": func(c *watchCheckpoint) { c.Start = nil; c.Token = watchToken(t, "01") },
	} {
		t.Run(name, func(t *testing.T) { bad := cp; modify(&bad); _, err := bad.marshal(); require.Error(t, err) })
	}
	resumed, err := cp.encode(watchToken(t, "02"))
	require.NoError(t, err)
	token, err := decodeWatchCheckpoint(resumed)
	require.NoError(t, err)
	require.Nil(t, token.Start)
	require.Empty(t, token.ClusterTime)
	require.Error(t, validateWatchBootstrap(token))
}

func TestWatchLogicalDeleteIdentity(t *testing.T) {
	for _, fromBefore := range []bool{false, true} {
		for _, collection := range []string{"", "users"} {
			w := &documentWatch{binding: watchBinding()}
			w.binding.Collection = collection
			change := watchChange(t, "update")
			change.FullDocument.Data = nil
			change.UpdateDescription.UpdatedFields = bson.M{"deleted": true}
			if fromBefore {
				change.FullDocumentBeforeChange = change.FullDocument
				change.FullDocument = nil
			}
			event, err := w.convertChangeEvent(change)
			require.NoError(t, err)
			require.Equal(t, types.EventDelete, event.Type)
			require.Nil(t, event.Document)
			require.Equal(t, "users", event.Collection)
			require.Equal(t, "alice", event.DocumentID)
			require.Nil(t, event.Before)
		}
	}
	w := &documentWatch{binding: watchBinding()}
	w.binding.Collection = ""
	change := watchChange(t, "update")
	change.FullDocument = nil
	change.UpdateDescription.UpdatedFields = bson.M{"deleted": true}
	_, err := w.convertChangeEvent(change)
	requireWatchCode(t, err, types.WatchPayloadUnavailable)
	// Physical cleanup requires neither an image nor a routable business key.
	event, err := w.convertChangeEvent(changeStreamEvent{OperationType: "delete"})
	require.NoError(t, err)
	require.Nil(t, event)
}

func TestWatchFrameWatermarkAndBytes(t *testing.T) {
	change := watchChange(t, "delete")
	change.FullDocument = nil
	raw := mustBSON(t, change)
	native := &stubChangeStream{token: watchToken(t, "02"), events: []bson.Raw{raw}}
	store := nativeWatch(t, native)
	stream, err := store.Watch(context.Background(), "tenant", "users", "", types.WatchOptions{MaxAwaitTime: 10 * time.Millisecond})
	require.NoError(t, err)
	defer stream.Close()
	filtered, err := stream.Next(context.Background())
	require.NoError(t, err)
	require.Nil(t, filtered.Event)
	require.False(t, filtered.CaughtUp)
	require.EqualValues(t, len(raw), filtered.SourceBytes)
	idle, err := stream.Next(context.Background())
	require.NoError(t, err)
	require.True(t, idle.CaughtUp)
	require.Zero(t, idle.SourceBytes)
	require.NotEqual(t, filtered.Checkpoint, idle.Checkpoint)
}

func TestWatchNativeResumeErrorClassification(t *testing.T) {
	w := &documentWatch{binding: watchBinding(), resuming: true}
	for _, code := range []int32{260, 40647, 40648, 9} {
		err := mongo.CommandError{Code: code, Message: "secret token"}
		classified := w.classifyStream(err)
		requireWatchCode(t, classified, types.WatchInvalidCheckpoint)
		require.NotContains(t, classified.Error(), "secret token")
	}
	requireWatchCode(t, w.classify(mongo.CommandError{Code: 9}), types.WatchSourceUnavailable)
	w.resuming = false
	requireWatchCode(t, w.classifyStream(mongo.CommandError{Code: 9}), types.WatchSourceUnavailable)
	requireWatchCode(t, w.classifyStream(mongo.CommandError{Code: 280}), types.WatchHistoryUnavailable)
	store := nativeWatch(t, &stubChangeStream{})
	_, err := store.Watch(context.Background(), "tenant", "users", "", types.WatchOptions{MaxAwaitTime: -1})
	requireWatchCode(t, err, types.WatchInvalidScope)
}

func TestWatchReadBoundarySession(t *testing.T) {
	ctx := context.Background()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI("mongodb://127.0.0.1:1"))
	require.NoError(t, err)
	defer client.Disconnect(ctx)
	cp := watchScanBinding(t)
	cause := errors.New("read failed")
	err = withWatchReadBoundary(ctx, client, cp, func(ctx context.Context) error {
		session := mongo.SessionFromContext(ctx)
		require.Equal(t, cp.Start, session.OperationTime())
		require.Equal(t, cp.ClusterTime, session.ClusterTime())
		return cause
	})
	require.ErrorIs(t, err, cause)
	require.Error(t, withWatchReadBoundary(ctx, client, watchBinding(), func(context.Context) error { t.Fatal("invalid boundary reached read"); return nil }))
	unconnected, err := mongo.NewClient()
	require.NoError(t, err)
	_, _, err = watchReadSession(ctx, unconnected, nil)
	require.Error(t, err)
	require.Error(t, withWatchReadBoundary(ctx, unconnected, cp, func(context.Context) error { t.Fatal("session failure reached read"); return nil }))
}

func TestWatchCaptureCommittedBoundary(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	for _, scenario := range []string{"committed empty read", "missing operation time", "missing signature", "source read failure"} {
		mt.Run(scenario, func(mt *mtest.T) {
			cp := watchScanBinding(mt.T)
			response := mtest.CreateCursorResponse(0, mt.DB.Name()+"."+mt.Coll.Name(), mtest.FirstBatch)
			if scenario != "missing operation time" {
				response = append(response, bson.E{Key: "operationTime", Value: *cp.Start})
			}
			if scenario == "missing signature" {
				response = append(response, bson.E{Key: "$clusterTime", Value: bson.D{{Key: "clusterTime", Value: *cp.Start}}})
			} else {
				response = append(response, bson.E{Key: "$clusterTime", Value: cp.ClusterTime.Lookup("$clusterTime").Document()})
			}
			if scenario == "source read failure" {
				response = mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 13, Message: "denied"})
			}
			mt.AddMockResponses(response)
			cp.Start, cp.ClusterTime = nil, nil
			ctx, session, err := captureWatchBoundary(context.Background(), mt.Coll, &cp)
			if scenario != "committed empty read" {
				require.Error(mt, err)
				require.Nil(mt, session)
				return
			}
			require.NoError(mt, err)
			defer session.EndSession(ctx)
			require.NoError(mt, validateWatchBootstrap(cp))
			require.Equal(mt, cp.Start, session.OperationTime())
			command := mt.GetStartedEvent()
			require.Equal(mt, "find", command.CommandName)
			require.Equal(mt, "majority", command.Command.Lookup("readConcern", "level").StringValue())
		})
	}
	client, err := mongo.NewClient()
	require.NoError(t, err)
	cp := watchBinding()
	_, _, err = captureWatchBoundary(context.Background(), client.Database("test").Collection("docs"), &cp)
	require.ErrorIs(t, err, mongo.ErrClientDisconnected)
}

func TestWatchBootstrapNativeOptions(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("inclusive start and resumed context", func(mt *mtest.T) {
		cp := watchScanBinding(mt.T)
		response := mtest.CreateCursorResponse(0, mt.DB.Name()+".docs", mtest.FirstBatch)
		response = append(response, bson.E{Key: "operationTime", Value: *cp.Start}, bson.E{Key: "$clusterTime", Value: cp.ClusterTime.Lookup("$clusterTime").Document()})
		mt.AddMockResponses(response)
		store := &documentStore{client: mt.Client, db: mt.DB, dataCollection: "docs", sysCollection: "sys"}
		store.readSource = func(context.Context, *mongo.Collection, bool) (watchSource, error) { return cp.Source, nil }
		store.readWatchTarget = func(context.Context, *mongo.Collection, watchCheckpoint) (bson.Raw, error) {
			return watchToken(mt.T, "02"), nil
		}
		store.openStream = func(ctx context.Context, _ *mongo.Collection, _ mongo.Pipeline, opts *options.ChangeStreamOptions) (changeStream, error) {
			require.Equal(mt, cp.Start, opts.StartAtOperationTime)
			require.Nil(mt, opts.ResumeAfter)
			require.Equal(mt, cp.Start, mongo.SessionFromContext(ctx).OperationTime())
			require.Equal(mt, cp.ClusterTime, mongo.SessionFromContext(ctx).ClusterTime())
			require.Equal(mt, 10*time.Millisecond, *opts.MaxAwaitTime)
			return &stubChangeStream{token: watchToken(mt.T, "02")}, nil
		}
		stream, err := store.Watch(context.Background(), "tenant", "users", "", types.WatchOptions{StartMode: types.WatchStartForScan, MaxAwaitTime: 10 * time.Millisecond})
		require.NoError(mt, err)
		initial := stream.InitialCheckpoint()
		decoded, err := decodeWatchCheckpoint(initial)
		require.NoError(mt, err)
		require.Equal(mt, cp.Start, decoded.Start)
		require.NoError(mt, stream.Close())
		resumed, err := store.Watch(context.Background(), "tenant", "users", initial, types.WatchOptions{MaxAwaitTime: 10 * time.Millisecond})
		require.NoError(mt, err)
		require.Equal(mt, initial, resumed.InitialCheckpoint())
		frame, err := resumed.Next(context.Background())
		require.NoError(mt, err)
		require.True(mt, frame.CaughtUp)
		decoded, err = decodeWatchCheckpoint(frame.Checkpoint)
		require.NoError(mt, err)
		require.Nil(mt, decoded.Start)
		require.NoError(mt, resumed.Close())
	})
}

func TestMongoWatchBootstrapAcrossClients(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", 0)
	initial, err := store.Watch(ctx, "tenant", "users", "", types.WatchOptions{StartMode: types.WatchStartForScan})
	require.NoError(t, err)
	start := initial.InitialCheckpoint()
	cp, err := decodeWatchCheckpoint(start)
	require.NoError(t, err)
	require.NoError(t, validateWatchBootstrap(cp))
	require.NoError(t, initial.Close())
	doc := types.NewStoredDoc("tenant", "users", "alice", nil)
	require.NoError(t, store.Create(ctx, "tenant", doc))
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(testMongoURI))
	require.NoError(t, err)
	defer client.Disconnect(ctx)
	other := NewDocumentStore(client, client.Database(env.DBName), "docs", "sys", 0)
	resumed, err := other.Watch(ctx, "tenant", "users", start, types.WatchOptions{MaxAwaitTime: 10 * time.Millisecond})
	require.NoError(t, err)
	defer resumed.Close()
	require.Equal(t, start, resumed.InitialCheckpoint())
	frame := nextWatchEvent(t, resumed)
	require.Equal(t, "alice", frame.Event.DocumentID)
	token, err := decodeWatchCheckpoint(frame.Checkpoint)
	require.NoError(t, err)
	require.Nil(t, token.Start)
	require.NotEmpty(t, token.Token)
	_, err = other.Watch(ctx, "tenant", "users", start, types.WatchOptions{StartMode: types.WatchStartForScan})
	requireWatchCode(t, err, types.WatchInvalidScope)
}

func TestMongoWatchDeleteIdentityAfterCleanup(t *testing.T) {
	for _, preimages := range []bool{false, true} {
		name := "without preimage"
		if preimages {
			name = "with preimage"
		}
		t.Run(name, func(t *testing.T) {
			env := setupTestEnv(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			require.NoError(t, env.DB.CreateCollection(ctx, "docs", options.CreateCollection().SetChangeStreamPreAndPostImages(bson.M{"enabled": preimages})))
			store := NewDocumentStore(env.Client, env.DB, "docs", "sys", 0)
			doc := types.NewStoredDoc("tenant", "users", "alice", nil)
			require.NoError(t, store.Create(ctx, "tenant", doc))
			stream, err := store.Watch(ctx, "tenant", "", "", types.WatchOptions{})
			require.NoError(t, err)
			defer stream.Close()
			require.NoError(t, store.Delete(ctx, "tenant", "users/alice", nil))
			_, err = env.DB.Collection("docs").DeleteOne(ctx, bson.M{"_id": doc.Id})
			require.NoError(t, err)
			for {
				frame, err := stream.Next(ctx)
				if !preimages && err != nil {
					requireWatchCode(t, err, types.WatchPayloadUnavailable)
					require.Empty(t, frame.Checkpoint)
					break
				}
				require.NoError(t, err)
				if frame.Event == nil {
					continue
				}
				require.True(t, preimages)
				require.Equal(t, types.EventDelete, frame.Event.Type)
				require.Equal(t, "alice", frame.Event.DocumentID)
				require.Equal(t, "users", frame.Event.Collection)
				require.Nil(t, frame.Event.Document)
				require.Nil(t, frame.Event.Before)
				break
			}
		})
	}
}
