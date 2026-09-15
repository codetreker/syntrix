package mongo

import (
	"context"
	"fmt"
	"os"
	"sync"
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

type watchTopologyEnv struct {
	client  *mongo.Client
	db      *mongo.Database
	store   *documentStore
	hosts   []string
	primary string
}

// These tests change replica-set availability. Their dedicated URI prevents
// failpoints and elections from disrupting ordinary storage and Puller tests.
func setupWatchTopologyEnv(t *testing.T, ctx context.Context) *watchTopologyEnv {
	t.Helper()
	client := watchTopologyClient(t, ctx, nil)
	var hello struct {
		Hosts   []string `bson:"hosts"`
		Primary string   `bson:"primary"`
	}
	require.NoError(t, client.Database("admin").RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&hello))
	require.Len(t, hello.Hosts, 3, "replication boundary test requires three data-bearing replica-set members")
	db := client.Database(fmt.Sprintf("test_watch_topology_%d", time.Now().UnixNano()))
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require.NoError(t, db.Drop(cleanupCtx))
	})
	store := NewDocumentStore(client, db, "docs", "sys", 0).(*documentStore)
	require.NoError(t, store.EnsureIndexes(ctx))
	env := &watchTopologyEnv{client: client, db: db, store: store, hosts: hello.Hosts, primary: hello.Primary}
	// Create and configure the source before preventing majority writes.
	boundary := watchTopologyBoundary(t, ctx, store)
	_, err := store.ScanDocuments(ctx, "tenant", types.SourceScanRequest{Collection: "users", AtLeast: boundary, Limit: 1})
	require.NoError(t, err)
	return env
}

func watchTopologyClient(t *testing.T, ctx context.Context, monitor *event.CommandMonitor) *mongo.Client {
	t.Helper()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(watchTopologyURI(t)).
		SetWriteConcern(writeconcern.Majority()).SetServerSelectionTimeout(10*time.Second).SetMonitor(monitor))
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, client.Disconnect(cleanupCtx))
	})
	return client
}

func watchTopologyDirectClient(t *testing.T, ctx context.Context, host string) *mongo.Client {
	t.Helper()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(watchTopologyURI(t)).SetHosts([]string{host}).SetDirect(true).
		SetWriteConcern(writeconcern.W1()).SetServerSelectionTimeout(5*time.Second))
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, client.Disconnect(cleanupCtx))
	})
	return client
}

func watchTopologyURI(t *testing.T) string {
	t.Helper()
	uri := os.Getenv("SYNTRIX_WATCH_TOPOLOGY_URI")
	if uri == "" {
		t.Skip("set SYNTRIX_WATCH_TOPOLOGY_URI to an isolated three-node replica set with enableTestCommands; tests pause replication and step down its primary")
	}
	return uri
}

func pauseWatchTopologySecondary(t *testing.T, ctx context.Context, client *mongo.Client) func() {
	t.Helper()
	admin := client.Database("admin")
	var enabled struct {
		Count int64 `bson:"count"`
	}
	require.NoError(t, admin.RunCommand(ctx, bson.D{{Key: "configureFailPoint", Value: "stopReplProducer"}, {Key: "mode", Value: "alwaysOn"}}).Decode(&enabled))
	var once sync.Once
	resume := func() {
		once.Do(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			require.NoError(t, admin.RunCommand(cleanupCtx, bson.D{{Key: "configureFailPoint", Value: "stopReplProducer"}, {Key: "mode", Value: "off"}}).Err())
		})
	}
	t.Cleanup(resume)
	// Waiting for entry excludes an in-flight fetch that could otherwise commit
	// the supposedly uncommitted test write. MongoDB uses the same protocol:
	// https://github.com/mongodb/mongo/blob/b41cda4fe697dce6fd9b83b3805362ccc02fbeb3/jstests/libs/write_concern_util.js#L22-L37
	require.NoError(t, admin.RunCommand(ctx, bson.D{{Key: "waitForFailPoint", Value: "stopReplProducer"}, {Key: "timesEntered", Value: enabled.Count + 1}, {Key: "maxTimeMS", Value: 10000}}).Err())
	return resume
}

func watchTopologyBoundary(t *testing.T, ctx context.Context, store types.DocumentStore) types.WatchCheckpoint {
	t.Helper()
	stream, err := store.Watch(ctx, "tenant", "users", "", types.WatchOptions{StartMode: types.WatchStartForScan, MaxAwaitTime: 10 * time.Millisecond})
	require.NoError(t, err)
	boundary := stream.InitialCheckpoint()
	require.NoError(t, stream.Close())
	return boundary
}

func watchTopologyDrain(t *testing.T, ctx context.Context, store types.DocumentStore, after types.WatchCheckpoint, mirror map[string]*types.StoredDoc) types.WatchCheckpoint {
	t.Helper()
	stream, err := store.Watch(ctx, "tenant", "users", after, types.WatchOptions{MaxAwaitTime: 10 * time.Millisecond})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stream.Close()) })
	require.Equal(t, after, stream.InitialCheckpoint())
	for {
		frame, err := stream.Next(ctx)
		require.NoError(t, err)
		require.NotEmpty(t, frame.Checkpoint)
		if frame.Event != nil {
			require.NotNil(t, frame.Event.Document)
			id, err := types.LogicalDocumentID(frame.Event.Document)
			require.NoError(t, err)
			mirror[id] = frame.Event.Document
		}
		if frame.CaughtUp {
			require.NoError(t, stream.Close())
			return frame.Checkpoint
		}
	}
}

func watchTopologyTimestampBefore(left, right primitive.Timestamp) bool {
	return left.T < right.T || left.T == right.T && left.I < right.I
}

func TestWatchTopologyBootstrapExcludesDelayedMajorityWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	env := setupWatchTopologyEnv(t, ctx)
	require.NoError(t, env.store.Create(ctx, "tenant", types.NewStoredDoc("tenant", "users", "seed", map[string]interface{}{"committed": true})))
	var resume []func()
	for _, host := range env.hosts {
		if host != env.primary {
			resume = append(resume, pauseWatchTopologySecondary(t, ctx, watchTopologyDirectClient(t, ctx, host)))
		}
	}
	require.Len(t, resume, 2)
	primary := watchTopologyDirectClient(t, ctx, env.primary)
	session, err := primary.StartSession()
	require.NoError(t, err)
	defer session.EndSession(ctx)
	delayed := types.NewStoredDoc("tenant", "users", "delayed", map[string]interface{}{"committed": false})
	_, err = primary.Database(env.db.Name()).Collection("docs").InsertOne(mongo.NewSessionContext(ctx, session), delayed)
	require.NoError(t, err)
	require.NotNil(t, session.OperationTime())
	uncommittedTime := *session.OperationTime()
	require.NoError(t, primary.Database(env.db.Name()).Collection("docs").FindOne(ctx, bson.M{"_id": delayed.Id}).Err())

	boundaryCtx, boundaryCancel := context.WithTimeout(ctx, 8*time.Second)
	defer boundaryCancel()
	boundary := watchTopologyBoundary(t, boundaryCtx, env.store)
	cp, err := decodeWatchCheckpoint(boundary)
	require.NoError(t, err)
	require.NotNil(t, cp.Start)
	require.True(t, watchTopologyTimestampBefore(*cp.Start, uncommittedTime), "scan boundary must precede the uncommitted write")
	page, err := env.store.ScanDocuments(boundaryCtx, "tenant", types.SourceScanRequest{Collection: "users", AtLeast: boundary, Limit: 10})
	require.NoError(t, err, "committed scans must not wait for unrelated uncommitted writes")
	require.Len(t, page.Documents, 1)
	require.Equal(t, "users/seed", page.Documents[0].Fullpath)
	require.True(t, page.Exhausted)
	for _, restart := range resume {
		restart()
	}

	// This write follows the delayed write and waits for majority; replay from
	// the original boundary must include both after replication resumes.
	require.NoError(t, env.store.Create(ctx, "tenant", types.NewStoredDoc("tenant", "users", "committed-after", nil)))
	fresh := watchTopologyClient(t, ctx, nil)
	store := NewDocumentStore(fresh, fresh.Database(env.db.Name()), "docs", "sys", 0).(*documentStore)
	mirror := map[string]*types.StoredDoc{"seed": page.Documents[0]}
	watchTopologyDrain(t, ctx, store, boundary, mirror)
	require.Contains(t, mirror, "delayed")
	require.Contains(t, mirror, "committed-after")
}

type watchTopologyCommands struct {
	mu    sync.Mutex
	finds []bson.Raw
}

func (c *watchTopologyCommands) monitor() *event.CommandMonitor {
	return &event.CommandMonitor{Started: func(_ context.Context, e *event.CommandStartedEvent) {
		if e.CommandName == "find" && e.Command.Lookup("find").StringValue() == "docs" {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.finds = append(c.finds, append(bson.Raw(nil), e.Command...))
		}
	}}
}

func (c *watchTopologyCommands) requireFence(t *testing.T, expected primitive.Timestamp) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	require.NotEmpty(t, c.finds)
	for _, command := range c.finds {
		concern, ok := command.Lookup("readConcern").DocumentOK()
		require.True(t, ok)
		require.Equal(t, "majority", concern.Lookup("level").StringValue())
		seconds, increment, ok := concern.Lookup("afterClusterTime").TimestampOK()
		require.True(t, ok, "fresh adapter must restore causal operationTime")
		require.False(t, watchTopologyTimestampBefore(primitive.Timestamp{T: seconds, I: increment}, expected))
		clock, ok := command.Lookup("$clusterTime").DocumentOK()
		require.True(t, ok)
		_, ok = clock.Lookup("signature").DocumentOK()
		require.True(t, ok, "fresh adapter must send signed clusterTime")
	}
}

func TestWatchTopologyResumesOnFreshClientAfterPrimaryStepdown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	env := setupWatchTopologyEnv(t, ctx)
	for _, id := range []string{"a", "b"} {
		require.NoError(t, env.store.Create(ctx, "tenant", types.NewStoredDoc("tenant", "users", id, map[string]interface{}{"generation": int64(1)})))
	}
	boundary := watchTopologyBoundary(t, ctx, env.store)
	first, err := env.store.ScanDocuments(ctx, "tenant", types.SourceScanRequest{Collection: "users", AtLeast: boundary, Limit: 1})
	require.NoError(t, err)
	require.Len(t, first.Documents, 1)
	require.Equal(t, "users/a", first.Documents[0].Fullpath)
	require.False(t, first.Exhausted)
	require.NoError(t, env.store.Update(ctx, "tenant", "users/a", map[string]interface{}{"generation": int64(2)}, nil))
	require.NoError(t, env.store.Create(ctx, "tenant", types.NewStoredDoc("tenant", "users", "0-before-a", nil)))

	primary := watchTopologyDirectClient(t, ctx, env.primary)
	stepdownCtx, stepdownCancel := context.WithTimeout(ctx, 10*time.Second)
	err = primary.Database("admin").RunCommand(stepdownCtx, bson.D{{Key: "replSetStepDown", Value: 20}, {Key: "secondaryCatchUpPeriodSecs", Value: 5}}).Err()
	stepdownCancel()
	if err != nil {
		require.True(t, mongo.IsNetworkError(err), "unexpected stepdown failure: %v", err)
	}
	require.Eventually(t, func() bool {
		probeCtx, probeCancel := context.WithTimeout(ctx, time.Second)
		defer probeCancel()
		var hello struct {
			Primary string `bson:"primary"`
		}
		err := env.client.Database("admin").RunCommand(probeCtx, bson.D{{Key: "hello", Value: 1}}).Decode(&hello)
		return err == nil && hello.Primary != "" && hello.Primary != env.primary
	}, 15*time.Second, 100*time.Millisecond)

	commands := &watchTopologyCommands{}
	fresh := watchTopologyClient(t, ctx, commands.monitor())
	store := NewDocumentStore(fresh, fresh.Database(env.db.Name()), "docs", "sys", 0).(*documentStore)
	mirror := map[string]*types.StoredDoc{"a": first.Documents[0]}
	afterID := first.NextAfter
	for {
		page, err := store.ScanDocuments(ctx, "tenant", types.SourceScanRequest{Collection: "users", AtLeast: boundary, AfterID: afterID, Limit: 1})
		require.NoError(t, err)
		for _, doc := range page.Documents {
			id, err := types.LogicalDocumentID(doc)
			require.NoError(t, err)
			mirror[id] = doc
		}
		if page.Exhausted {
			break
		}
		require.NotEqual(t, afterID, page.NextAfter)
		afterID = page.NextAfter
	}
	cp, err := decodeWatchCheckpoint(boundary)
	require.NoError(t, err)
	require.NotNil(t, cp.Start)
	commands.requireFence(t, *cp.Start)
	position := watchTopologyDrain(t, ctx, store, boundary, mirror)
	require.Len(t, mirror, 3)
	require.Equal(t, int64(2), mirror["a"].Data["generation"])
	changes, err := decodeWatchCheckpoint(position)
	require.NoError(t, err)
	require.Nil(t, changes.Start)
	require.NotEmpty(t, changes.Token)

	// A second fresh client resumes the native token without inheriting the
	// previous client's topology or session clocks.
	require.NoError(t, env.store.Create(ctx, "tenant", types.NewStoredDoc("tenant", "users", "after-election", nil)))
	another := watchTopologyClient(t, ctx, nil)
	anotherStore := NewDocumentStore(another, another.Database(env.db.Name()), "docs", "sys", 0)
	watchTopologyDrain(t, ctx, anotherStore, position, mirror)
	require.Contains(t, mirror, "after-election")
}
