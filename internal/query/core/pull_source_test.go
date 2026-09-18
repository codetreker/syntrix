package core

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/query/wire"
	"github.com/syntrixbase/syntrix/pkg/model"
)

func sourceRequest() types.ReplicationPullRequest {
	return types.ReplicationPullRequest{Collection: "users", DatabaseIdentity: "database-entity", Source: &types.ReplicationSource{Version: 1, Filters: model.Filters{{Field: "active", Op: model.OpEq, Value: true}}}}
}

func sourceToken(t *testing.T, req types.ReplicationPullRequest, phase string, position types.WatchCheckpoint) string {
	t.Helper()
	source, err := normalizePullSource(req)
	require.NoError(t, err)
	encoded, err := encodePullCursor(pullCursor{Version: 4, Database: "db", DatabaseIdentity: req.DatabaseIdentity, Collection: req.Collection, Phase: phase, Position: position, SourceHash: source.hash, GenerationID: uuid.NewString()})
	require.NoError(t, err)
	return encoded
}

func sourceStored(id string, active bool) *types.StoredDoc {
	doc := pullStored(id)
	doc.Data["active"] = active
	return doc
}

func TestPullSourceScanFiltersWithoutLosingProgress(t *testing.T) {
	req := sourceRequest()
	req.Limit = 2
	watchCalls, scanCalls := 0, 0
	store := &pullStore{watch: func(context.Context, string, string, types.WatchCheckpoint, types.WatchOptions) (types.WatchStream, error) {
		watchCalls++
		return &pullWatch{initial: "C0"}, nil
	}, scan: func(_ context.Context, _ string, request types.SourceScanRequest) (types.SourceScanPage, error) {
		scanCalls++
		require.Equal(t, 2, request.Limit)
		require.Equal(t, maxPullSourceBytes, request.MaxBytes)
		require.Equal(t, types.ReadAuthoritative, request.Consistency)
		require.Equal(t, types.WatchCheckpoint("C0"), request.AtLeast)
		if scanCalls == 1 {
			require.Empty(t, request.AfterID)
			deleted := sourceStored("b", true)
			deleted.Deleted = true
			return types.SourceScanPage{Documents: []*types.StoredDoc{sourceStored("a", false), deleted}, NextAfter: "b"}, nil
		}
		require.Equal(t, "b", request.AfterID)
		return types.SourceScanPage{Documents: []*types.StoredDoc{sourceStored("c", true)}, NextAfter: "c", Exhausted: true}, nil
	}}
	first, err := New(store, nil).Pull(context.Background(), "db", req)
	require.NoError(t, err)
	require.Empty(t, first.Events)
	require.Nil(t, first.Documents)
	require.Equal(t, 1, first.ProtocolVersion)
	require.Equal(t, "events", first.Mode)
	require.Equal(t, req.DatabaseIdentity, first.DatabaseIdentity)
	require.Equal(t, "scan", first.Phase)
	require.False(t, first.BootstrapComplete)
	require.False(t, first.CaughtUp)
	cursor, err := decodePullCursor("db", req.DatabaseIdentity, "users", first.Checkpoint, first.SourceHash)
	require.NoError(t, err)
	require.Equal(t, "b", cursor.AfterID)
	req.Checkpoint = first.Checkpoint
	second, err := New(store, nil).Pull(context.Background(), "db", req)
	require.NoError(t, err)
	require.Len(t, second.Events, 1)
	require.Equal(t, "c", second.Events[0].Document.GetID())
	require.Equal(t, types.ReplicationUpsert, second.Events[0].Type)
	require.Equal(t, "replay", second.Phase)
	require.False(t, second.BootstrapComplete)
	require.False(t, second.CaughtUp)
	require.Equal(t, first.GenerationID, second.GenerationID)
	require.Equal(t, 1, watchCalls)
	require.Equal(t, 2, scanCalls)
}

func TestPullSourceChangesEnterLeaveDeleteAndReentry(t *testing.T) {
	req := sourceRequest()
	req.Checkpoint = sourceToken(t, req, "replay", "C0")
	frame := func(cp string, active bool) types.WatchFrame {
		out := pullFrame("alice", cp, types.EventUpdate)
		out.Event.Document.Data["active"] = active
		return out
	}
	enrichedDelete := frame("C4", true)
	enrichedDelete.Event.Document.Deleted = true
	recreated := frame("C5", true)
	recreated.Event.Document.Version = 1
	stream := &pullWatch{frames: []types.WatchFrame{frame("C1", true), frame("C2", false), pullFrame("alice", "C3", types.EventDelete), enrichedDelete, recreated, {Checkpoint: "C6", CaughtUp: true}}}
	page, err := changesEngine(t, stream).Pull(context.Background(), "db", req)
	require.NoError(t, err)
	require.Len(t, page.Events, 5)
	require.Equal(t, types.ReplicationUpsert, page.Events[0].Type)
	require.Equal(t, int64(math.MaxInt64), page.Events[0].Document["counter"])
	require.Equal(t, types.ReplicationEvent{Type: types.ReplicationLeave, ID: "alice"}, page.Events[1])
	require.Equal(t, types.ReplicationEvent{Type: types.ReplicationDelete, ID: "alice"}, page.Events[2])
	require.Equal(t, page.Events[2], page.Events[3])
	require.Equal(t, types.ReplicationUpsert, page.Events[4].Type)
	require.Equal(t, int64(1), page.Events[4].Document["version"])
	require.Equal(t, "live", page.Phase)
	require.True(t, page.CaughtUp)
	require.True(t, page.BootstrapComplete)
	require.True(t, stream.closed)
	_, err = wire.EncodePullPage(page)
	require.NoError(t, err)

	req.Checkpoint, req.Limit = page.Checkpoint, 1
	nextStream := &pullWatch{initial: "C6", frames: []types.WatchFrame{frame("C7", false)}}
	otherNode := New(&pullStore{watch: func(_ context.Context, _, _ string, cp types.WatchCheckpoint, _ types.WatchOptions) (types.WatchStream, error) {
		require.Equal(t, types.WatchCheckpoint("C6"), cp)
		return nextStream, nil
	}}, nil)
	next, err := otherNode.Pull(context.Background(), "db", req)
	require.NoError(t, err)
	require.False(t, next.CaughtUp)
	require.True(t, next.BootstrapComplete)
	require.Equal(t, "live", next.Phase)
	require.Equal(t, page.GenerationID, next.GenerationID)
}

func TestPullSourceUsesEnvelopeMetadata(t *testing.T) {
	req := sourceRequest()
	req.Source.Filters = model.Filters{{Field: "id", Op: model.OpEq, Value: "alice"}, {Field: "deleted", Op: model.OpEq, Value: false}, {Field: "version", Op: model.OpEq, Value: int64(7)}}
	req.Checkpoint = sourceToken(t, req, "replay", "C0")
	frame := pullFrame("alice", "C1", types.EventUpdate)
	frame.Event.Document.Data["deleted"] = true
	frame.Event.Document.Data["version"] = int64(99)
	page, err := changesEngine(t, &pullWatch{frames: []types.WatchFrame{frame, {Checkpoint: "C2", CaughtUp: true}}}).Pull(context.Background(), "db", req)
	require.NoError(t, err)
	require.Equal(t, types.ReplicationUpsert, page.Events[0].Type)
	require.NotContains(t, page.Events[0].Document, "deleted")
}

func TestPullSourceInvalidPayloadCannotAdvanceCheckpoint(t *testing.T) {
	for _, scanning := range []bool{false, true} {
		t.Run(fmt.Sprintf("scan=%t", scanning), func(t *testing.T) {
			req := sourceRequest()
			bad := sourceStored("alice", true)
			bad.Data["active"] = make(chan bool)
			store := &pullStore{watch: func(_ context.Context, _, _ string, cp types.WatchCheckpoint, _ types.WatchOptions) (types.WatchStream, error) {
				return &pullWatch{initial: "C0", frames: []types.WatchFrame{{Checkpoint: "C1", Event: &types.Event{Database: "db", Collection: "users", DocumentID: "alice", Type: types.EventUpdate, Document: bad}}}}, nil
			}, scan: func(context.Context, string, types.SourceScanRequest) (types.SourceScanPage, error) {
				return types.SourceScanPage{Documents: []*types.StoredDoc{bad}, NextAfter: "alice", Exhausted: true}, nil
			}}
			if !scanning {
				req.Checkpoint = sourceToken(t, req, "replay", "C0")
			}
			page, err := New(store, nil).Pull(context.Background(), "db", req)
			pullCode(t, err, types.WatchInvalidEvent)
			require.Nil(t, page)
		})
	}
}

func TestPullSourceScopeCanonicalization(t *testing.T) {
	first := sourceRequest()
	first.Source.Filters = model.Filters{{Field: "x", Op: model.OpIn, Value: []any{int64(2), float64(1), int64(2)}}, {Field: "deleted", Op: model.OpEq, Value: false}}
	second := sourceRequest()
	second.Source.Filters = model.Filters{{Field: "deleted", Op: model.OpEq, Value: false}, {Field: "x", Op: model.OpIn, Value: []any{int64(1), float64(2)}}}
	second.Source.OrderBy = []model.Order{{Field: "id", Direction: "asc"}}
	a, err := normalizePullSource(first)
	require.NoError(t, err)
	b, err := normalizePullSource(second)
	require.NoError(t, err)
	require.Equal(t, a.hash, b.hash)
	first.Checkpoint = sourceToken(t, first, "replay", "C0")
	second.Checkpoint = first.Checkpoint
	require.NoError(t, ValidatePullRequest("db", second))
	second.Source.OrderBy[0].Direction = "desc"
	pullCode(t, ValidatePullRequest("db", second), types.WatchScopeMismatch)
}

func TestPullSourceInvalidRequestsDoNotAccessStorage(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*types.ReplicationPullRequest)
	}{
		{"missing identity", func(r *types.ReplicationPullRequest) { r.DatabaseIdentity = "" }},
		{"version", func(r *types.ReplicationPullRequest) { r.Source.Version = 2 }},
		{"negative page limit", func(r *types.ReplicationPullRequest) { r.Limit = -1 }},
		{"excess page limit", func(r *types.ReplicationPullRequest) { r.Limit = wire.MaxPullLimit + 1 }},
		{"missing filters", func(r *types.ReplicationPullRequest) { r.Source.Filters = nil }},
		{"invalid filter", func(r *types.ReplicationPullRequest) { r.Source.Filters[0].Field = "a.b" }},
		{"invalid order", func(r *types.ReplicationPullRequest) {
			r.Source.OrderBy = []model.Order{{Field: "id", Direction: "invalid"}}
		}},
		{"window", func(r *types.ReplicationPullRequest) { limit := 3; r.Source.Limit = &limit }},
		{"request id", func(r *types.ReplicationPullRequest) { id := "r"; r.RequestID = &id }},
		{"legacy request id", func(r *types.ReplicationPullRequest) { id := "r"; r.RequestID = &id; r.Source = nil }},
		{"source bytes", func(r *types.ReplicationPullRequest) {
			r.Source.Filters[0].Value = strings.Repeat("x", MaxPullRequestBytes)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := sourceRequest()
			test.change(&req)
			_, err := New(&pullStore{}, nil).Pull(context.Background(), "db", req)
			require.ErrorIs(t, err, types.ErrInvalidReplicationSource)
		})
	}
	for _, change := range []func(*types.ReplicationPullRequest){
		func(r *types.ReplicationPullRequest) { r.DatabaseIdentity = "other" },
		func(r *types.ReplicationPullRequest) { r.Collection = "other" },
		func(r *types.ReplicationPullRequest) { r.Source.Filters[0].Value = false },
	} {
		req := sourceRequest()
		req.Checkpoint = sourceToken(t, req, "replay", "C0")
		change(&req)
		_, err := New(&pullStore{}, nil).Pull(context.Background(), "db", req)
		pullCode(t, err, types.WatchScopeMismatch)
	}
}

func TestPullSourceRejectsInvalidWindowRequests(t *testing.T) {
	limit, requestID := 2, "request-1"
	for _, mutate := range []func(*types.ReplicationPullRequest){
		func(r *types.ReplicationPullRequest) { r.Limit = 1 },
		func(r *types.ReplicationPullRequest) { r.Checkpoint = "cursor" },
		func(r *types.ReplicationPullRequest) { r.LimitPresent = true },
		func(r *types.ReplicationPullRequest) { r.CheckpointPresent = true },
		func(r *types.ReplicationPullRequest) { id := ""; r.RequestID = &id },
		func(r *types.ReplicationPullRequest) { n := 0; r.Source.Limit = &n },
		func(r *types.ReplicationPullRequest) { r.Source.Filters[0].Op = "invalid" },
	} {
		other := sourceRequest()
		other.Source.Limit, other.RequestID = &limit, &requestID
		mutate(&other)
		_, err := New(&pullStore{}, nil).Pull(context.Background(), "db", other)
		require.ErrorIs(t, err, types.ErrInvalidReplicationSource)
	}
}

func TestPullSourceCursorValidation(t *testing.T) {
	req := sourceRequest()
	source, err := normalizePullSource(req)
	require.NoError(t, err)
	for _, change := range []func(*pullCursor){
		func(c *pullCursor) { c.SourceHash = "not-a-hash" },
		func(c *pullCursor) { c.GenerationID = "" },
		func(c *pullCursor) { c.GenerationID = uuid.Nil.String() },
		func(c *pullCursor) { c.Phase = "changes" },
		func(c *pullCursor) { c.Phase = "live"; c.AfterID = "a" },
	} {
		cursor := pullCursor{Version: 4, Database: "db", DatabaseIdentity: req.DatabaseIdentity, Collection: "users", Phase: "scan", Position: "C0", SourceHash: source.hash, GenerationID: uuid.NewString()}
		change(&cursor)
		req.Checkpoint, err = encodePullCursor(cursor)
		require.NoError(t, err)
		pullCode(t, ValidatePullRequest("db", req), types.WatchInvalidCheckpoint)
	}
	req.Checkpoint = pullToken(t, "changes", "C0", "")
	pullCode(t, ValidatePullRequest("db", req), types.WatchInvalidCheckpoint)
	req.Checkpoint = sourceToken(t, req, "replay", "C0")
	req.Source = nil
	pullCode(t, ValidatePullRequest("db", req), types.WatchInvalidCheckpoint)
}

func TestPullSourceAllFramesConsumeWorkBudget(t *testing.T) {
	req := sourceRequest()
	req.Checkpoint = sourceToken(t, req, "replay", "C0")
	frames := make([]types.WatchFrame, maxPullFrames+1)
	for i := range frames {
		frames[i] = types.WatchFrame{Checkpoint: types.WatchCheckpoint(fmt.Sprintf("C%d", i+1)), SourceBytes: 1}
	}
	stream := &pullWatch{frames: frames}
	page, err := changesEngine(t, stream).Pull(context.Background(), "db", req)
	require.NoError(t, err)
	require.Empty(t, page.Events)
	require.Equal(t, maxPullFrames, stream.reads)
	require.False(t, page.BootstrapComplete)
	cursor, err := decodePullCursor("db", req.DatabaseIdentity, "users", page.Checkpoint, page.SourceHash)
	require.NoError(t, err)
	require.Equal(t, types.WatchCheckpoint(fmt.Sprintf("C%d", maxPullFrames)), cursor.Position)

	stream = &pullWatch{frames: []types.WatchFrame{{Checkpoint: "C1", SourceBytes: maxPullSourceBytes - 1}, {Checkpoint: "C2", SourceBytes: 2}}}
	page, err = changesEngine(t, stream).Pull(context.Background(), "db", req)
	require.NoError(t, err)
	cursor, err = decodePullCursor("db", req.DatabaseIdentity, "users", page.Checkpoint, page.SourceHash)
	require.NoError(t, err)
	require.Equal(t, types.WatchCheckpoint("C1"), cursor.Position)
}

func TestPullSourceNonfittingEventDoesNotAdvanceCursor(t *testing.T) {
	req := sourceRequest()
	req.Checkpoint = sourceToken(t, req, "replay", "C0")
	makeFrame := func(id, checkpoint string) types.WatchFrame {
		frame := pullFrame(id, checkpoint, types.EventUpdate)
		frame.Event.Document.Data["active"] = true
		frame.Event.Document.Data["large"] = strings.Repeat("x", 9<<20)
		return frame
	}
	stream := &pullWatch{frames: []types.WatchFrame{makeFrame("alice", "C1"), makeFrame("bob", "C2")}}
	page, err := changesEngine(t, stream).Pull(context.Background(), "db", req)
	require.NoError(t, err)
	require.Len(t, page.Events, 1)
	cursor, err := decodePullCursor("db", req.DatabaseIdentity, "users", page.Checkpoint, page.SourceHash)
	require.NoError(t, err)
	require.Equal(t, types.WatchCheckpoint("C1"), cursor.Position)
	_, err = wire.EncodePullPage(page)
	require.NoError(t, err)
}

func TestPullSourceHistoryLossRequiresNewGeneration(t *testing.T) {
	req := sourceRequest()
	req.Checkpoint = sourceToken(t, req, "live", "C0")
	source, err := normalizePullSource(req)
	require.NoError(t, err)
	oldCursor, err := decodePullCursor("db", req.DatabaseIdentity, "users", req.Checkpoint, source.hash)
	require.NoError(t, err)
	expired := pullError(types.WatchHistoryUnavailable, "expired")
	engine := New(&pullStore{watch: func(_ context.Context, _, _ string, cp types.WatchCheckpoint, _ types.WatchOptions) (types.WatchStream, error) {
		if cp != "" {
			return nil, expired
		}
		return &pullWatch{initial: "C-new"}, nil
	}, scan: func(context.Context, string, types.SourceScanRequest) (types.SourceScanPage, error) {
		return types.SourceScanPage{Exhausted: true}, nil
	}}, nil)
	_, err = engine.Pull(context.Background(), "db", req)
	require.True(t, errors.Is(err, expired))
	req.Checkpoint = ""
	page, err := engine.Pull(context.Background(), "db", req)
	require.NoError(t, err)
	require.NotEqual(t, oldCursor.GenerationID, page.GenerationID)
	require.False(t, page.BootstrapComplete)
	require.Equal(t, "replay", page.Phase)
}
