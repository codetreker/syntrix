package types

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReplicationBudgetBoundsAndSharedDeadlines(t *testing.T) {
	start := time.Now()
	b, err := ResolveReplicationBudget(ReplicationBudget{})
	require.NoError(t, err)
	require.Equal(t, DefaultReplicationLimit, b.Limit)
	require.Equal(t, MaxReplicationSourceBytes, b.MaxSourceBytes)
	require.Equal(t, MaxReplicationSourceBytes, b.MaxProbeBytes)
	require.Equal(t, MaxReplicationPageBytes, b.MaxPageBytes)
	require.Equal(t, MaxReplicationFrames, b.MaxFrames)
	require.WithinDuration(t, start.Add(ReplicationSoftTimeout), b.SoftDeadline, time.Second)
	require.WithinDuration(t, start.Add(ReplicationHardTimeout), b.HardDeadline, time.Second)
	again, err := ResolveReplicationBudget(b)
	require.NoError(t, err)
	require.Equal(t, b, again, "subsequent source calls must not renew the request budget")

	short, err := ResolveReplicationBudget(ReplicationBudget{HardDeadline: start.Add(time.Second), Limit: 1, MaxFrames: 2, MaxSourceBytes: 3, MaxPageBytes: 4, MaxProbeBytes: 5})
	require.NoError(t, err)
	require.Equal(t, short.HardDeadline, short.SoftDeadline)
	require.Equal(t, int64(3), short.MaxSourceBytes)

	for _, invalid := range []ReplicationBudget{
		{Limit: -1}, {Limit: MaxReplicationLimit + 1},
		{MaxSourceBytes: -1}, {MaxSourceBytes: MaxReplicationSourceBytes + 1},
		{MaxPageBytes: -1}, {MaxPageBytes: MaxReplicationPageBytes + 1},
		{MaxProbeBytes: -1}, {MaxProbeBytes: MaxReplicationSourceBytes + 1},
		{MaxFrames: -1}, {MaxFrames: MaxReplicationFrames + 1},
		{SoftDeadline: start.Add(time.Minute)},
	} {
		_, err := ResolveReplicationBudget(invalid)
		var failure *ReplicationError
		require.ErrorAs(t, err, &failure)
		require.Equal(t, ReplicationBudgetExceeded, failure.Code)
	}
	long, err := ResolveReplicationBudget(ReplicationBudget{HardDeadline: start.Add(time.Hour)})
	require.NoError(t, err)
	require.WithinDuration(t, start.Add(ReplicationHardTimeout), long.HardDeadline, time.Second)
}

func TestReplicationPositionRejectsMalformedShapeWithoutLeakingCursor(t *testing.T) {
	valid := ReplicationPosition{Phase: ReplicationScan, Opaque: "private-source-token"}
	require.NoError(t, valid.Validate(ReplicationScan))
	require.NoError(t, valid.Validate(""))
	for _, p := range []ReplicationPosition{
		{}, {Phase: "unknown", Opaque: valid.Opaque},
		{Phase: ReplicationChanges, Opaque: valid.Opaque},
		{Phase: ReplicationScan, Opaque: strings.Repeat("x", MaxReplicationCursorBytes+1)},
	} {
		err := p.Validate(ReplicationScan)
		var failure *ReplicationError
		require.ErrorAs(t, err, &failure)
		require.Equal(t, ReplicationInvalidCursor, failure.Code)
		require.NotContains(t, err.Error(), "private-source-token")
	}
}

func TestReplicationStateIdentityAndMinimalDeletion(t *testing.T) {
	doc := &StoredDoc{Database: "db", Collection: "users", Fullpath: "users/alice", Id: "physical-hash", Data: map[string]interface{}{"id": "untrusted"}}
	state := ReplicationState{ID: "alice", Collection: "users", Document: doc}
	require.NoError(t, state.Validate("db", "users"))
	require.NoError(t, (ReplicationState{ID: "alice", Collection: "users", Deleted: true}).Validate("db", "users"))
	for name, invalid := range map[string]ReplicationState{
		"missing live document": {ID: "alice", Collection: "users"},
		"business ID":           {ID: "untrusted", Collection: "users", Document: doc},
		"physical ID":           {ID: "physical-hash", Collection: "users", Document: doc},
		"foreign collection":    {ID: "alice", Collection: "other", Document: doc},
		"inconsistent deletion": {ID: "alice", Collection: "users", Deleted: true, Document: doc},
		"missing identity":      {Collection: "users", Deleted: true},
		"invalid identity":      {ID: "nested/alice", Collection: "users", Deleted: true},
	} {
		t.Run(name, func(t *testing.T) {
			err := invalid.Validate("db", "users")
			var failure *ReplicationError
			require.ErrorAs(t, err, &failure)
			require.Equal(t, ReplicationInvalidState, failure.Code)
		})
	}
	require.Error(t, state.Validate("other-db", "users"))
	doc.Deleted = true
	state.Deleted = true
	doc.Data = nil
	require.NoError(t, state.Validate("db", "users"))
}

func TestReplicationScopeAndFailureCause(t *testing.T) {
	require.NoError(t, ValidateReplicationScope("db", "users/alice/tasks"))
	for _, scope := range [][2]string{{"", "users"}, {"db\x00", "users"}, {"db", ""}, {"db", "users/*"}, {"db", "users//tasks"}} {
		var failure *ReplicationError
		require.ErrorAs(t, ValidateReplicationScope(scope[0], scope[1]), &failure)
		require.Equal(t, ReplicationScopeMismatch, failure.Code)
	}
	cause := fmt.Errorf("native-token-secret partial-document-secret: %w", context.DeadlineExceeded)
	err := &ReplicationError{Code: ReplicationUnavailable, Database: "db", Collection: "users", Cause: cause}
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorIs(t, err, cause)
	require.Same(t, cause, err.Unwrap())
	var failure *ReplicationError
	require.ErrorAs(t, fmt.Errorf("request failed: %w", err), &failure)
	require.Same(t, err, failure)
	require.Equal(t, `replication UNAVAILABLE for database "db" collection "users"`, err.Error())
	require.NotContains(t, err.Error(), "native-token-secret")
	require.NotContains(t, err.Error(), "partial-document-secret")
}
