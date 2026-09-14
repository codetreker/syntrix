package mongo

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
)

func TestReplicationRejectsWrongPhaseBeforeSourceAccess(t *testing.T) {
	store := &documentStore{}
	cp := testReplicationCheckpoint(t)
	scan, err := cp.position()
	require.NoError(t, err)
	cp.Phase = types.ReplicationChanges
	changes, err := cp.position()
	require.NoError(t, err)
	ctx := context.Background()
	page, err := store.ReadBootstrapPage(ctx, "tenant", "users", changes, types.ReplicationBudget{})
	requireReplicationCode(t, err, types.ReplicationInvalidCursor)
	require.Empty(t, page.End.Opaque)
	page, err = store.ReadChangesPage(ctx, "tenant", "users", scan, types.ReplicationBudget{})
	requireReplicationCode(t, err, types.ReplicationInvalidCursor)
	require.Empty(t, page.End.Opaque)
	for _, bad := range []types.ReplicationPosition{
		{Phase: types.ReplicationChanges, Opaque: "not-a-cursor"},
		{Phase: types.ReplicationChanges, Opaque: scan.Opaque},
	} {
		page, err = store.ReadChangesPage(ctx, "tenant", "users", bad, types.ReplicationBudget{})
		requireReplicationCode(t, err, types.ReplicationInvalidCursor)
		require.Empty(t, page.End.Opaque)
	}
}

func TestReplicationPreservesSourceCapabilityFailures(t *testing.T) {
	for _, tc := range []struct {
		watch       types.WatchErrorCode
		replication types.ReplicationErrorCode
	}{
		{types.WatchUnsupported, types.ReplicationUnsupported},
		{types.WatchSourceMismatch, types.ReplicationSourceMismatch},
	} {
		cause := errors.New("source capability unavailable")
		err := replicationClassify("tenant", "users", &types.WatchError{Code: tc.watch, Cause: cause})
		requireReplicationCode(t, err, tc.replication)
		require.ErrorIs(t, err, cause)
	}
}

func TestMongoReplicationScanBudgetsRetainExactPrefix(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", 0).(*documentStore)
	collection, err := env.DB.Collection("docs").Clone(options.Collection().SetWriteConcern(writeconcern.Majority()))
	require.NoError(t, err)
	var documentBytes int64
	for _, id := range []string{"a", "b"} {
		doc := types.NewStoredDoc("tenant", "users", id, map[string]interface{}{"payload": strings.Repeat("x", 4096)})
		raw, encodeErr := bson.Marshal(doc)
		require.NoError(t, encodeErr)
		documentBytes = int64(len(raw))
		_, err = collection.InsertOne(ctx, doc)
		require.NoError(t, err)
	}
	start, err := store.BeginBootstrap(ctx, "tenant", "users", types.ReplicationBudget{})
	require.NoError(t, err)
	// Both budgets carry a cursor, but neither can admit a first complete state.
	for _, budget := range []types.ReplicationBudget{
		{MaxSourceBytes: 1},
		{MaxPageBytes: int64(2 * len(start.Opaque))},
	} {
		page, pageErr := store.ReadBootstrapPage(ctx, "tenant", "users", start, budget)
		requireReplicationCode(t, pageErr, types.ReplicationBudgetExceeded)
		require.Empty(t, page.Frames)
		require.Empty(t, page.End.Opaque)
	}
	one, err := store.ReadBootstrapPage(ctx, "tenant", "users", start, types.ReplicationBudget{Limit: 1})
	require.NoError(t, err)
	require.Len(t, one.Frames, 1)
	require.Equal(t, "a", one.Frames[0].State.ID)
	for _, budget := range []types.ReplicationBudget{
		{Limit: 2, MaxSourceBytes: documentBytes},
		{Limit: 2, MaxPageBytes: one.Usage.PageBytes},
	} {
		prefix, pageErr := store.ReadBootstrapPage(ctx, "tenant", "users", start, budget)
		require.NoError(t, pageErr)
		require.Len(t, prefix.Frames, 1)
		require.Equal(t, "a", prefix.Frames[0].State.ID)
		require.Equal(t, types.ReplicationEndBytes, prefix.EndReason)
		require.False(t, prefix.CaughtUp)
		require.Equal(t, one.End, prefix.End)
		remainder, readErr := store.ReadBootstrapPage(ctx, "tenant", "users", prefix.End, types.ReplicationBudget{})
		require.NoError(t, readErr)
		require.Len(t, remainder.Frames, 1)
		require.Equal(t, "b", remainder.Frames[0].State.ID)
		require.Equal(t, types.ReplicationChanges, remainder.End.Phase)
	}
}

func TestMongoReplicationNativePayloadBudgetDoesNotSkipChanges(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", 0).(*documentStore)
	start, err := store.BeginBootstrap(ctx, "tenant", "users", types.ReplicationBudget{})
	require.NoError(t, err)
	start = drainReplication(t, ctx, store, start, 10, make(map[string]*types.ReplicationState))
	collection, err := env.DB.Collection("docs").Clone(options.Collection().SetWriteConcern(writeconcern.Majority()))
	require.NoError(t, err)
	for i, id := range []string{"a", "b"} {
		size := 64
		if i == 1 {
			size = 6000
		}
		doc := types.NewStoredDoc("tenant", "users", id, map[string]interface{}{"payload": strings.Repeat("x", size)})
		_, err = collection.InsertOne(ctx, doc)
		require.NoError(t, err)
	}
	page, err := store.ReadChangesPage(ctx, "tenant", "users", start, types.ReplicationBudget{MaxSourceBytes: 1})
	requireReplicationCode(t, err, types.ReplicationBudgetExceeded)
	require.Empty(t, page.End.Opaque)
	prefix, err := store.ReadChangesPage(ctx, "tenant", "users", start, types.ReplicationBudget{Limit: 2, MaxSourceBytes: 4000})
	require.NoError(t, err)
	require.Len(t, prefix.Frames, 1)
	require.Equal(t, "a", prefix.Frames[0].State.ID)
	require.Equal(t, types.ReplicationEndBytes, prefix.EndReason)
	require.False(t, prefix.CaughtUp)
	require.LessOrEqual(t, prefix.Usage.SourceBytes, int64(4000))
	require.Equal(t, prefix.Frames[0].After, prefix.End)
	remainder, err := store.ReadChangesPage(ctx, "tenant", "users", prefix.End, types.ReplicationBudget{})
	require.NoError(t, err)
	require.Len(t, remainder.Frames, 1)
	require.Equal(t, "b", remainder.Frames[0].State.ID)
	require.True(t, remainder.CaughtUp)
}
