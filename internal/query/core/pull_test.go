package core

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/query/wire"
	"github.com/syntrixbase/syntrix/pkg/model"
)

type pullSource struct {
	MockStorageBackend
	begin   func(context.Context, string, string, types.ReplicationBudget) (types.ReplicationPosition, error)
	scan    func(context.Context, string, string, types.ReplicationPosition, types.ReplicationBudget) (types.ReplicationPage, error)
	changes func(context.Context, string, string, types.ReplicationPosition, types.ReplicationBudget) (types.ReplicationPage, error)
}

func (s *pullSource) BeginBootstrap(ctx context.Context, db, collection string, budget types.ReplicationBudget) (types.ReplicationPosition, error) {
	return s.begin(ctx, db, collection, budget)
}
func (s *pullSource) ReadBootstrapPage(ctx context.Context, db, collection string, after types.ReplicationPosition, budget types.ReplicationBudget) (types.ReplicationPage, error) {
	return s.scan(ctx, db, collection, after, budget)
}
func (s *pullSource) ReadChangesPage(ctx context.Context, db, collection string, after types.ReplicationPosition, budget types.ReplicationBudget) (types.ReplicationPage, error) {
	return s.changes(ctx, db, collection, after, budget)
}

func pullPosition(phase types.ReplicationPhase, name string) types.ReplicationPosition {
	return types.ReplicationPosition{Phase: phase, Opaque: name}
}
func pullToken(t *testing.T, db string, position types.ReplicationPosition) string {
	t.Helper()
	cp, err := encodePullCursor(db, db, "items", position)
	require.NoError(t, err)
	return cp
}
func pullState(id string) *types.ReplicationState {
	return &types.ReplicationState{ID: id, Collection: "items", Document: queryDocument(id, map[string]any{"id": "untrusted", "value": int64(9007199254740993)})}
}
func sourcePage(frames []types.ReplicationFrame, end types.ReplicationPosition, reason types.ReplicationEndReason) types.ReplicationPage {
	return types.ReplicationPage{Frames: frames, End: end, EndReason: reason, CaughtUp: reason == types.ReplicationEndWatermark, Usage: types.ReplicationUsage{FramesExamined: len(frames)}}
}

func TestPullBootstrapThenCrossInstanceChanges(t *testing.T) {
	source := &pullSource{}
	start := pullPosition(types.ReplicationScan, "bootstrap")
	replay := pullPosition(types.ReplicationChanges, "original-boundary")
	var initialBudget types.ReplicationBudget
	source.begin = func(ctx context.Context, db, collection string, b types.ReplicationBudget) (types.ReplicationPosition, error) {
		require.Equal(t, "db", db)
		require.Equal(t, "items", collection)
		require.Equal(t, 100, b.Limit)
		initialBudget = b
		return start, nil
	}
	source.scan = func(ctx context.Context, db, collection string, pos types.ReplicationPosition, b types.ReplicationBudget) (types.ReplicationPage, error) {
		require.Equal(t, start, pos)
		require.Equal(t, initialBudget, b)
		return sourcePage([]types.ReplicationFrame{{State: pullState("alice"), After: pullPosition(types.ReplicationScan, "alice")}}, replay, types.ReplicationEndScan), nil
	}
	page, err := New(source, nil).Pull(context.Background(), "db", types.ReplicationPullRequest{Collection: "items"})
	require.NoError(t, err)
	require.False(t, page.CaughtUp)
	require.Len(t, page.Documents, 1)
	require.Equal(t, "alice", page.Documents[0]["id"])
	require.Equal(t, int64(9007199254740993), page.Documents[0]["value"])
	nextSource := &pullSource{changes: func(ctx context.Context, db, collection string, pos types.ReplicationPosition, b types.ReplicationBudget) (types.ReplicationPage, error) {
		require.Equal(t, replay, pos)
		return sourcePage(nil, pos, types.ReplicationEndWatermark), nil
	}}
	page, err = New(nextSource, nil).Pull(context.Background(), "db", types.ReplicationPullRequest{Collection: "items", Checkpoint: page.Checkpoint})
	require.NoError(t, err)
	require.True(t, page.CaughtUp)
	require.Empty(t, page.Documents)
}

func TestPullBindsEntityIdentityWithoutChangingStorageNamespace(t *testing.T) {
	start := pullPosition(types.ReplicationScan, "bootstrap")
	replay := pullPosition(types.ReplicationChanges, "replay")
	reads := 0
	source := &pullSource{
		begin: func(_ context.Context, database, _ string, _ types.ReplicationBudget) (types.ReplicationPosition, error) {
			require.Equal(t, "db", database)
			return start, nil
		},
		scan: func(_ context.Context, database, _ string, position types.ReplicationPosition, _ types.ReplicationBudget) (types.ReplicationPage, error) {
			require.Equal(t, "db", database)
			require.Equal(t, start, position)
			return sourcePage([]types.ReplicationFrame{{State: pullState("alice"), After: pullPosition(types.ReplicationScan, "alice")}}, replay, types.ReplicationEndScan), nil
		},
		changes: func(_ context.Context, database, _ string, position types.ReplicationPosition, _ types.ReplicationBudget) (types.ReplicationPage, error) {
			require.Equal(t, "db", database)
			require.Equal(t, replay, position)
			reads++
			return sourcePage(nil, replay, types.ReplicationEndWatermark), nil
		},
	}
	engine := New(source, nil)
	request := types.ReplicationPullRequest{Collection: "items", DatabaseIdentity: "entity-1"}
	page, err := engine.Pull(context.Background(), "db", request)
	require.NoError(t, err)
	require.Len(t, page.Documents, 1)
	request.Checkpoint = page.Checkpoint
	page, err = engine.Pull(context.Background(), "db", request)
	require.NoError(t, err)
	require.True(t, page.CaughtUp)
	require.Equal(t, 1, reads)
	for _, scope := range []struct{ namespace, identity string }{
		{"db", "entity-2"}, {"another-alias", "entity-1"}, {"db", ""},
	} {
		request.DatabaseIdentity = scope.identity
		page, err = engine.Pull(context.Background(), scope.namespace, request)
		var failure *types.ReplicationError
		require.ErrorAs(t, err, &failure)
		require.Equal(t, types.ReplicationScopeMismatch, failure.Code)
		require.Nil(t, page)
		require.Equal(t, 1, reads)
	}
}

func TestPullMinimalDeletionAndFilteredProgress(t *testing.T) {
	start := pullPosition(types.ReplicationChanges, "start")
	end := pullPosition(types.ReplicationChanges, "after-delete")
	source := &pullSource{changes: func(context.Context, string, string, types.ReplicationPosition, types.ReplicationBudget) (types.ReplicationPage, error) {
		return sourcePage([]types.ReplicationFrame{
			{After: pullPosition(types.ReplicationChanges, "physical-cleanup")},
			{State: &types.ReplicationState{ID: "alice", Collection: "items", Deleted: true}, After: end},
		}, end, types.ReplicationEndCount), nil
	}}
	page, err := New(source, nil).Pull(context.Background(), "db", types.ReplicationPullRequest{Collection: "items", Checkpoint: pullToken(t, "db", start)})
	require.NoError(t, err)
	require.False(t, page.CaughtUp)
	require.Equal(t, []model.Document{{"id": "alice", "collection": "items", "deleted": true}}, page.Documents)
	decoded, err := decodePullCursor("db", "db", "items", page.Checkpoint)
	require.NoError(t, err)
	require.Equal(t, end, decoded)
}

func TestPullEmptyProgressAndOversizedUnit(t *testing.T) {
	start := pullPosition(types.ReplicationChanges, "start")
	progress := pullPosition(types.ReplicationChanges, "filtered")
	large := pullState("large")
	large.Document.Data["payload"] = strings.Repeat("x", 17<<20)
	for _, test := range []struct {
		name        string
		frames      []types.ReplicationFrame
		budgetError bool
	}{
		{name: "filtered page", frames: []types.ReplicationFrame{{After: progress}}},
		{name: "filtered prefix", frames: []types.ReplicationFrame{{After: progress}, {State: large, After: pullPosition(types.ReplicationChanges, "large")}}},
		{name: "first unit too large", frames: []types.ReplicationFrame{{State: large, After: pullPosition(types.ReplicationChanges, "large")}}, budgetError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := &pullSource{changes: func(context.Context, string, string, types.ReplicationPosition, types.ReplicationBudget) (types.ReplicationPage, error) {
				return sourcePage(test.frames, test.frames[len(test.frames)-1].After, types.ReplicationEndBytes), nil
			}}
			page, err := New(source, nil).Pull(context.Background(), "db", types.ReplicationPullRequest{Collection: "items", Checkpoint: pullToken(t, "db", start)})
			if test.budgetError {
				var failure *types.ReplicationError
				require.ErrorAs(t, err, &failure)
				require.Equal(t, types.ReplicationBudgetExceeded, failure.Code)
				require.Nil(t, page)
				return
			}
			require.NoError(t, err)
			require.Empty(t, page.Documents)
			require.False(t, page.CaughtUp)
			decoded, err := decodePullCursor("db", "db", "items", page.Checkpoint)
			require.NoError(t, err)
			require.Equal(t, progress, decoded)
		})
	}
}

func TestPullWireAdmissionPreservesExactPrefix(t *testing.T) {
	for _, phase := range []types.ReplicationPhase{types.ReplicationScan, types.ReplicationChanges} {
		t.Run(string(phase), func(t *testing.T) {
			start := pullPosition(phase, "start")
			frames := make([]types.ReplicationFrame, 3)
			for i := range frames {
				state := pullState(fmt.Sprint(i))
				state.Document.Data["large"] = strings.Repeat("x", 6<<20)
				frames[i] = types.ReplicationFrame{State: state, After: pullPosition(phase, fmt.Sprint(i))}
			}
			end, reason := pullPosition(types.ReplicationChanges, "end"), types.ReplicationEndWatermark
			if phase == types.ReplicationScan {
				reason = types.ReplicationEndScan
			}
			reader := func(context.Context, string, string, types.ReplicationPosition, types.ReplicationBudget) (types.ReplicationPage, error) {
				return sourcePage(frames, end, reason), nil
			}
			source := &pullSource{scan: reader, changes: reader}
			page, err := New(source, nil).Pull(context.Background(), "db", types.ReplicationPullRequest{Collection: "items", Checkpoint: pullToken(t, "db", start)})
			require.NoError(t, err)
			require.Len(t, page.Documents, 2)
			require.False(t, page.CaughtUp)
			after, err := decodePullCursor("db", "db", "items", page.Checkpoint)
			require.NoError(t, err)
			require.Equal(t, frames[1].After, after)
			_, err = wire.EncodeJSONPullPage(page)
			require.NoError(t, err)
		})
	}
}

func TestPullDefersOversizedTerminalCursorUntilEmptyPage(t *testing.T) {
	for _, phase := range []types.ReplicationPhase{types.ReplicationScan, types.ReplicationChanges} {
		t.Run(string(phase), func(t *testing.T) {
			start := pullPosition(phase, "start")
			afterDocument := pullPosition(phase, "alice")
			end := pullPosition(types.ReplicationChanges, strings.Repeat("e", types.MaxReplicationCursorBytes))
			reason := types.ReplicationEndWatermark
			if phase == types.ReplicationScan {
				reason = types.ReplicationEndScan
			}
			state := pullState("alice")
			state.Document.Data["payload"] = strings.Repeat("x", wire.MaxPageBytes-(64<<10))
			calls := 0
			reader := func(_ context.Context, _, _ string, position types.ReplicationPosition, _ types.ReplicationBudget) (types.ReplicationPage, error) {
				calls++
				if calls == 1 {
					require.Equal(t, start, position)
					return sourcePage([]types.ReplicationFrame{{State: state, After: afterDocument}}, end, reason), nil
				}
				require.Equal(t, 2, calls)
				require.Equal(t, afterDocument, position)
				return sourcePage(nil, end, reason), nil
			}
			engine := New(&pullSource{scan: reader, changes: reader}, nil)
			request := types.ReplicationPullRequest{Collection: "items", Checkpoint: pullToken(t, "db", start)}
			page, err := engine.Pull(context.Background(), "db", request)
			require.NoError(t, err)
			require.Len(t, page.Documents, 1)
			require.Equal(t, state.Document.Data["payload"], page.Documents[0]["payload"])
			require.False(t, page.CaughtUp)
			position, err := decodePullCursor("db", "db", "items", page.Checkpoint)
			require.NoError(t, err)
			require.Equal(t, afterDocument, position)
			encoded, err := wire.EncodeJSONPullPage(page)
			require.NoError(t, err)
			require.Greater(t, len(encoded), wire.MaxPageBytes-(64<<10))
			require.LessOrEqual(t, len(encoded), wire.MaxPageBytes)

			request.Checkpoint = page.Checkpoint
			page, err = engine.Pull(context.Background(), "db", request)
			require.NoError(t, err)
			require.Empty(t, page.Documents)
			require.Equal(t, phase == types.ReplicationChanges, page.CaughtUp)
			position, err = decodePullCursor("db", "db", "items", page.Checkpoint)
			require.NoError(t, err)
			require.Equal(t, end, position)
			_, err = wire.EncodeJSONPullPage(page)
			require.NoError(t, err)
			require.Equal(t, 2, calls)
		})
	}
}

func TestPullRejectsWholePageOnLateFailure(t *testing.T) {
	start := pullPosition(types.ReplicationChanges, "start")
	for _, test := range []struct {
		name   string
		mutate func(*types.ReplicationPage)
		err    error
	}{
		{name: "source error", err: &types.ReplicationError{Code: types.ReplicationUnavailable, Cause: errors.New("late read failure")}},
		{name: "late identity", mutate: func(p *types.ReplicationPage) { p.Frames[1].State.ID = "wrong" }},
		{name: "late encoding", mutate: func(p *types.ReplicationPage) { p.Frames[1].State.Document.Data["bad"] = make(chan int) }},
		{name: "frame phase", mutate: func(p *types.ReplicationPage) { p.Frames[1].After.Phase = types.ReplicationScan }},
		{name: "unchanged position", mutate: func(p *types.ReplicationPage) { p.Frames[1].After = p.Frames[0].After }},
		{name: "false watermark", mutate: func(p *types.ReplicationPage) { p.CaughtUp = true }},
		{name: "excessive usage", mutate: func(p *types.ReplicationPage) { p.Usage.SourceBytes = types.MaxReplicationSourceBytes + 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := &pullSource{changes: func(context.Context, string, string, types.ReplicationPosition, types.ReplicationBudget) (types.ReplicationPage, error) {
				page := sourcePage([]types.ReplicationFrame{{State: pullState("a"), After: pullPosition(types.ReplicationChanges, "a")}, {State: pullState("b"), After: pullPosition(types.ReplicationChanges, "b")}}, pullPosition(types.ReplicationChanges, "b"), types.ReplicationEndCount)
				if test.mutate != nil {
					test.mutate(&page)
				}
				return page, test.err
			}}
			page, err := New(source, nil).Pull(context.Background(), "db", types.ReplicationPullRequest{Collection: "items", Checkpoint: pullToken(t, "db", start)})
			require.Error(t, err)
			require.Nil(t, page)
			if test.err != nil {
				require.ErrorIs(t, err, test.err)
			}
		})
	}
}

func TestPullCancellationAndUnsupportedSource(t *testing.T) {
	engine := New(&MockStorageBackend{}, nil)
	page, err := engine.Pull(context.Background(), "db", types.ReplicationPullRequest{Collection: "items"})
	var failure *types.ReplicationError
	require.ErrorAs(t, err, &failure)
	require.Equal(t, types.ReplicationUnsupported, failure.Code)
	require.Nil(t, page)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	page, err = engine.Pull(ctx, "db", types.ReplicationPullRequest{Collection: "items"})
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, page)
	ctx, cancel = context.WithCancel(context.Background())
	source := &pullSource{changes: func(context.Context, string, string, types.ReplicationPosition, types.ReplicationBudget) (types.ReplicationPage, error) {
		cancel()
		return sourcePage(nil, pullPosition(types.ReplicationChanges, "end"), types.ReplicationEndWatermark), nil
	}}
	page, err = New(source, nil).Pull(ctx, "db", types.ReplicationPullRequest{Collection: "items", Checkpoint: pullToken(t, "db", pullPosition(types.ReplicationChanges, "start"))})
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, page)
}

func TestPullCursorValidationBeforeSource(t *testing.T) {
	valid := pullToken(t, "db", pullPosition(types.ReplicationChanges, "native"))
	for _, test := range []struct {
		name, checkpoint string
		code             types.ReplicationErrorCode
	}{
		{"legacy", "1000", types.ReplicationHistoryUnavailable},
		{"malformed", "not-a-cursor", types.ReplicationInvalidCursor},
		{"scope", pullToken(t, "other", pullPosition(types.ReplicationChanges, "native")), types.ReplicationScopeMismatch},
		{"oversized", strings.Repeat("a", MaxPullCursorBytes+1), types.ReplicationInvalidCursor},
		{"duplicatefields", base64.RawURLEncoding.EncodeToString([]byte(`{"version":2,"version":2,"database":"db","collection":"items","phase":"changes","position":"native"}`)), types.ReplicationInvalidCursor},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidatePullRequest("db", types.ReplicationPullRequest{Collection: "items", Checkpoint: test.checkpoint})
			var failure *types.ReplicationError
			require.ErrorAs(t, err, &failure)
			require.Equal(t, test.code, failure.Code)
		})
	}
	require.NoError(t, ValidatePullRequest("db", types.ReplicationPullRequest{Collection: "items", Checkpoint: valid}))
	require.Error(t, ValidatePullRequest("db", types.ReplicationPullRequest{Collection: "items", Limit: 1001}))
	require.Error(t, ValidatePullRequest("db", types.ReplicationPullRequest{Collection: "items/alice"}))
	require.NoError(t, ValidatePullRequest("db", types.ReplicationPullRequest{Collection: "items/alice/tasks"}))
}

func TestPullCursorEncodingBoundsSourcePositions(t *testing.T) {
	position := pullPosition(types.ReplicationChanges, strings.Repeat("x", types.MaxReplicationCursorBytes))
	checkpoint, err := encodePullCursor("db", "entity", "items", position)
	require.NoError(t, err)
	require.LessOrEqual(t, len(checkpoint), MaxPullCursorBytes)
	decoded, err := decodePullCursor("db", "entity", "items", checkpoint)
	require.NoError(t, err)
	require.Equal(t, position, decoded)

	for _, test := range []struct {
		name     string
		position types.ReplicationPosition
		code     types.ReplicationErrorCode
	}{
		{name: "missing source position", position: types.ReplicationPosition{Phase: types.ReplicationChanges}, code: types.ReplicationInvalidState},
		{name: "escaped position exceeds public budget", position: pullPosition(types.ReplicationChanges, strings.Repeat("\x00", types.MaxReplicationCursorBytes)), code: types.ReplicationBudgetExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			checkpoint, err := encodePullCursor("db", "entity", "items", test.position)
			var failure *types.ReplicationError
			require.ErrorAs(t, err, &failure)
			require.Equal(t, test.code, failure.Code)
			require.Empty(t, checkpoint)
		})
	}
}

func TestPullRejectsInvalidDatabaseIdentityAndOversizedRequest(t *testing.T) {
	for _, test := range []struct {
		name     string
		database string
		identity string
		code     types.ReplicationErrorCode
	}{
		{name: "missing storage namespace", code: types.ReplicationScopeMismatch},
		{name: "invalid resolved identity", database: "db", identity: "invalid\x00identity", code: types.ReplicationScopeMismatch},
		{name: "oversized resolved identity", database: "db", identity: strings.Repeat("x", MaxPullRequestBytes), code: types.ReplicationInvalidCursor},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := &pullSource{}
			page, err := New(source, nil).Pull(context.Background(), test.database, types.ReplicationPullRequest{Collection: "items", DatabaseIdentity: test.identity})
			var failure *types.ReplicationError
			require.ErrorAs(t, err, &failure)
			require.Equal(t, test.code, failure.Code)
			require.Nil(t, page)
		})
	}
}
