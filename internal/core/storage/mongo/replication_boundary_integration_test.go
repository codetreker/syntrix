package mongo

import (
	"context"
	"errors"
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
	"go.mongodb.org/mongo-driver/mongo/readconcern"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
)

type replicationBoundaryEnv struct {
	client  *mongo.Client
	db      *mongo.Database
	store   types.DocumentStore
	source  types.ReplicationSource
	hosts   []string
	primary string
}

// These tests change replica-set availability. Their dedicated URI prevents
// failpoints and elections from disrupting ordinary storage and Puller tests.
func setupReplicationBoundaryEnv(t *testing.T, ctx context.Context) *replicationBoundaryEnv {
	t.Helper()
	client := replicationBoundaryClient(t, ctx, nil)
	var hello struct {
		Hosts   []string `bson:"hosts"`
		Primary string   `bson:"primary"`
	}
	require.NoError(t, client.Database("admin").RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&hello))
	require.Len(t, hello.Hosts, 3, "replication boundary test requires three data-bearing replica-set members")
	db := client.Database(fmt.Sprintf("test_replication_boundary_%d", time.Now().UnixNano()))
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require.NoError(t, db.Drop(cleanupCtx))
	})
	store := NewDocumentStore(client, db, "docs", "sys", 0)
	require.NoError(t, store.(*documentStore).EnsureIndexes(ctx))
	env := &replicationBoundaryEnv{client: client, db: db, store: store, source: store.(types.ReplicationSource), hosts: hello.Hosts, primary: hello.Primary}
	// Create and configure the source before preventing majority writes.
	_, err := env.source.BeginBootstrap(ctx, "tenant", "users", types.ReplicationBudget{})
	require.NoError(t, err)
	return env
}

func replicationBoundaryClient(t *testing.T, ctx context.Context, monitor *event.CommandMonitor) *mongo.Client {
	t.Helper()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(replicationBoundaryURI(t)).
		SetWriteConcern(writeconcern.Majority()).SetServerSelectionTimeout(10*time.Second).SetMonitor(monitor))
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, client.Disconnect(cleanupCtx))
	})
	return client
}

func replicationBoundaryDirectClient(t *testing.T, ctx context.Context, host string) *mongo.Client {
	t.Helper()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(replicationBoundaryURI(t)).SetHosts([]string{host}).SetDirect(true).
		SetWriteConcern(writeconcern.W1()).SetServerSelectionTimeout(5*time.Second))
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, client.Disconnect(cleanupCtx))
	})
	return client
}

func replicationBoundaryURI(t *testing.T) string {
	t.Helper()
	uri := os.Getenv("SYNTRIX_REPLICATION_TOPOLOGY_URI")
	if uri == "" {
		t.Skip("set SYNTRIX_REPLICATION_TOPOLOGY_URI to an isolated three-node replica set with enableTestCommands; tests pause replication and step down its primary")
	}
	return uri
}

func pauseReplicationBoundarySecondary(t *testing.T, ctx context.Context, client *mongo.Client) func() {
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

func TestMongoReplicationBootstrapExcludesDelayedMajorityWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	env := setupReplicationBoundaryEnv(t, ctx)
	require.NoError(t, env.store.Create(ctx, "tenant", types.NewStoredDoc("tenant", "users", "seed", map[string]interface{}{"committed": true})))
	var resume []func()
	for _, host := range env.hosts {
		if host != env.primary {
			resume = append(resume, pauseReplicationBoundarySecondary(t, ctx, replicationBoundaryDirectClient(t, ctx, host)))
		}
	}
	require.Len(t, resume, 2)
	primary := replicationBoundaryDirectClient(t, ctx, env.primary)
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
	position, err := env.source.BeginBootstrap(boundaryCtx, "tenant", "users", types.ReplicationBudget{})
	require.NoError(t, err, "bootstrap boundary underlying error: %v", errors.Unwrap(err))
	cp, err := decodeReplicationCheckpoint(position)
	require.NoError(t, err)
	require.True(t, timestampBefore(cp.Start, uncommittedTime), "bootstrap boundary must precede the uncommitted write")
	page, err := env.source.ReadBootstrapPage(boundaryCtx, "tenant", "users", position, types.ReplicationBudget{Limit: 10})
	require.NoError(t, err, "bootstrap must read committed state without waiting for unrelated uncommitted writes; underlying error: %v", errors.Unwrap(err))
	require.Len(t, page.Frames, 1)
	require.Equal(t, "seed", page.Frames[0].State.ID)
	require.Equal(t, types.ReplicationChanges, page.End.Phase)
	for _, restart := range resume {
		restart()
	}

	// This write is ordered after the delayed write and waits for majority;
	// replay must therefore include both without moving the original boundary.
	require.NoError(t, env.store.Create(ctx, "tenant", types.NewStoredDoc("tenant", "users", "committed-after", nil)))
	fresh := replicationBoundaryClient(t, ctx, nil)
	source := NewDocumentStore(fresh, fresh.Database(env.db.Name()), "docs", "sys", 0).(types.ReplicationSource)
	mirror := map[string]*types.ReplicationState{"seed": page.Frames[0].State}
	drainReplication(t, ctx, source, page.End, 1, mirror)
	require.Contains(t, mirror, "delayed")
	require.Contains(t, mirror, "committed-after")
}

type replicationBoundaryCommands struct {
	mu    sync.Mutex
	finds []bson.Raw
}

func (c *replicationBoundaryCommands) monitor() *event.CommandMonitor {
	return &event.CommandMonitor{Started: func(_ context.Context, e *event.CommandStartedEvent) {
		if e.CommandName == "find" && e.Command.Lookup("find").StringValue() == "docs" {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.finds = append(c.finds, append(bson.Raw(nil), e.Command...))
		}
	}}
}

func (c *replicationBoundaryCommands) requireFence(t *testing.T, expected primitive.Timestamp) {
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
		require.False(t, timestampBefore(primitive.Timestamp{T: seconds, I: increment}, expected))
		clock, ok := command.Lookup("$clusterTime").DocumentOK()
		require.True(t, ok)
		_, ok = clock.Lookup("signature").DocumentOK()
		require.True(t, ok, "fresh adapter must send signed clusterTime")
	}
}

func TestMongoReplicationResumesOnFreshClientAfterPrimaryStepdown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	env := setupReplicationBoundaryEnv(t, ctx)
	for _, id := range []string{"a", "b"} {
		require.NoError(t, env.store.Create(ctx, "tenant", types.NewStoredDoc("tenant", "users", id, map[string]interface{}{"generation": int64(1)})))
	}
	position, err := env.source.BeginBootstrap(ctx, "tenant", "users", types.ReplicationBudget{})
	require.NoError(t, err)
	first, err := env.source.ReadBootstrapPage(ctx, "tenant", "users", position, types.ReplicationBudget{Limit: 1})
	require.NoError(t, err)
	require.Len(t, first.Frames, 1)
	require.Equal(t, "a", first.Frames[0].State.ID)
	require.NoError(t, env.store.Update(ctx, "tenant", "users/a", map[string]interface{}{"generation": int64(2)}, nil))
	require.NoError(t, env.store.Create(ctx, "tenant", types.NewStoredDoc("tenant", "users", "0-before-a", nil)))

	primary := replicationBoundaryDirectClient(t, ctx, env.primary)
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

	commands := &replicationBoundaryCommands{}
	fresh := replicationBoundaryClient(t, ctx, commands.monitor())
	source := NewDocumentStore(fresh, fresh.Database(env.db.Name()), "docs", "sys", 0).(types.ReplicationSource)
	mirror := map[string]*types.ReplicationState{"a": first.Frames[0].State}
	position = drainReplication(t, ctx, source, first.End, 1, mirror)
	require.Len(t, mirror, 3)
	require.Equal(t, int64(2), mirror["a"].Document.Data["generation"])
	start, err := decodeReplicationCheckpoint(first.End)
	require.NoError(t, err)
	commands.requireFence(t, start.OperationTime)

	// A second client resumes an event-token checkpoint, independently of the
	// first client's topology and session clocks.
	require.NoError(t, env.store.Create(ctx, "tenant", types.NewStoredDoc("tenant", "users", "after-election", nil)))
	changesCommands := &replicationBoundaryCommands{}
	another := replicationBoundaryClient(t, ctx, changesCommands.monitor())
	anotherSource := NewDocumentStore(another, another.Database(env.db.Name()), "docs", "sys", 0).(types.ReplicationSource)
	changes, err := decodeReplicationCheckpoint(position)
	require.NoError(t, err)
	drainReplication(t, ctx, anotherSource, position, 1, mirror)
	require.Contains(t, mirror, "after-election")
	changesCommands.requireFence(t, changes.OperationTime)
	majorityDocs := another.Database(env.db.Name()).Collection("docs", options.Collection().SetReadConcern(readconcern.Majority()))
	require.NoError(t, majorityDocs.FindOne(ctx, bson.M{"_id": types.CalculateDatabase("tenant", "users/after-election")}).Err())
}
