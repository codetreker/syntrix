package mongo

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func TestReplicationBootstrapRejectsUnavailableCapability(t *testing.T) {
	ctx := context.Background()
	store := &documentStore{}
	position, err := store.BeginBootstrap(ctx, "", "users", types.ReplicationBudget{})
	requireReplicationCode(t, err, types.ReplicationScopeMismatch)
	require.Empty(t, position.Opaque)
	position, err = store.BeginBootstrap(ctx, "tenant", "users", types.ReplicationBudget{Limit: -1})
	requireReplicationCode(t, err, types.ReplicationBudgetExceeded)
	require.Empty(t, position.Opaque)
	position, err = store.BeginBootstrap(ctx, "tenant", "users", types.ReplicationBudget{})
	requireReplicationCode(t, err, types.ReplicationUnsupported)
	require.Empty(t, position.Opaque)

	// A client that has never connected cannot establish a committed boundary or
	// a causal session. Both failures must preserve the driver's original error.
	client, err := mongo.NewClient()
	require.NoError(t, err)
	store = NewDocumentStore(client, client.Database("physical"), "docs", "sys", 0).(*documentStore)
	position, err = store.BeginBootstrap(ctx, "tenant", "users", types.ReplicationBudget{})
	requireReplicationCode(t, err, types.ReplicationUnavailable)
	require.ErrorIs(t, err, mongo.ErrClientDisconnected)
	require.Empty(t, position.Opaque)
	sctx, session, err := store.replicationSession(ctx, nil)
	require.ErrorIs(t, err, mongo.ErrClientDisconnected)
	require.Nil(t, sctx)
	require.Nil(t, session)
}

func TestReplicationRejectsIncompleteCausalContext(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*replicationCheckpoint)
	}{
		{"missing_scope", func(c *replicationCheckpoint) { c.Database = "" }},
		{"unknown_phase", func(c *replicationCheckpoint) { c.Phase = "other" }},
		{"scan_with_resume_token", func(c *replicationCheckpoint) { c.Token = watchToken(t, "token") }},
		{"malformed_resume_token", func(c *replicationCheckpoint) { c.Phase = types.ReplicationChanges; c.Token = bson.Raw{5, 0, 0, 0, 0} }},
		{"missing_cluster_clock", func(c *replicationCheckpoint) { c.ClusterTime, _ = bson.Marshal(bson.D{}) }},
		{"missing_signature", func(c *replicationCheckpoint) {
			c.ClusterTime, _ = bson.Marshal(bson.M{"$clusterTime": bson.M{"clusterTime": c.OperationTime}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cp := testReplicationCheckpoint(t)
			tc.mutate(&cp)
			position, err := cp.position()
			require.Error(t, err)
			require.Empty(t, position.Opaque)
		})
	}
	cp := testReplicationCheckpoint(t)
	cp.AfterID = strings.Repeat("x", types.MaxReplicationCursorBytes)
	position, err := cp.position()
	require.ErrorContains(t, err, "size limit")
	require.Empty(t, position.Opaque)
}

func TestReplicationMutationRejectsInvalidNativeSchema(t *testing.T) {
	cp := testReplicationCheckpoint(t)
	_, err := parseReplicationMutation(bson.Raw{1}, cp)
	require.Error(t, err)
	for _, tc := range []struct {
		name      string
		operation string
		identity  bson.D
		key       string
		code      types.ReplicationErrorCode
	}{
		{"unknown_operation", "unexpected", nil, "", types.ReplicationInvalidState},
		{"invalid_path", "update", bson.D{{Key: "_id", Value: "key"}, {Key: "database", Value: "tenant"}, {Key: "collection", Value: "users"}, {Key: "fullpath", Value: "other/alice"}}, "key", types.ReplicationIdentityUnavailable},
		{"mismatched_storage_key", "update", bson.D{{Key: "_id", Value: "key"}, {Key: "database", Value: "tenant"}, {Key: "collection", Value: "users"}, {Key: "fullpath", Value: "users/alice"}}, "key", types.ReplicationIdentityUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, encodeErr := bson.Marshal(bson.D{{Key: "_id", Value: watchToken(t, "event")}, {Key: "operationType", Value: tc.operation}, {Key: "documentKey", Value: bson.M{"_id": tc.key}}, {Key: "fullDocument", Value: tc.identity}})
			require.NoError(t, encodeErr)
			mutation, parseErr := parseReplicationMutation(raw, cp)
			requireReplicationCode(t, parseErr, tc.code)
			require.Nil(t, mutation.identity)
		})
	}
}

func TestReplicationStateRejectsWrongAuthoritativeIdentity(t *testing.T) {
	state, err := replicationState("tenant", "users", nil)
	requireReplicationCode(t, err, types.ReplicationIdentityUnavailable)
	require.Nil(t, state)
	foreign := types.NewStoredDoc("other", "users", "alice", nil)
	state, err = replicationState("tenant", "users", &foreign)
	requireReplicationCode(t, err, types.ReplicationInvalidState)
	require.Nil(t, state)
	doc := types.NewStoredDoc("tenant", "users", "alice", nil)
	doc.Id = "mismatched-key"
	state, err = replicationState("tenant", "users", &doc)
	requireReplicationCode(t, err, types.ReplicationInvalidState)
	require.Nil(t, state)
}

func TestReplicationHistoryProbeFailsClosed(t *testing.T) {
	cp := testReplicationCheckpoint(t)
	raw, err := bson.Marshal(bson.D{{Key: "operationType", Value: "update"}})
	require.NoError(t, err)
	decodeFailure := errors.New("source decode failed")
	for _, tc := range []struct {
		name     string
		native   *stubChangeStream
		maxBytes int64
		cause    error
		code     types.ReplicationErrorCode
	}{
		{"decode_failure", &stubChangeStream{events: []bson.Raw{raw}, decodeErr: decodeFailure}, 1024, decodeFailure, ""},
		{"probe_budget", &stubChangeStream{events: []bson.Raw{raw}}, 1, nil, types.ReplicationBudgetExceeded},
		{"closed_cursor", &stubChangeStream{exhausted: true}, 1024, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &documentStore{openStream: func(context.Context, *mongo.Collection, mongo.Pipeline, *options.ChangeStreamOptions) (changeStream, error) {
				return tc.native, nil
			}}
			_, probeErr := store.probeReplicationHistory(context.Background(), nil, cp, tc.maxBytes)
			require.Error(t, probeErr)
			if tc.cause != nil {
				require.ErrorIs(t, probeErr, tc.cause)
			}
			if tc.code != "" {
				requireReplicationCode(t, probeErr, tc.code)
			}
			require.Equal(t, int32(1), tc.native.closeCalls.Load())
			require.True(t, tc.native.closeBound.Load())
		})
	}
}
