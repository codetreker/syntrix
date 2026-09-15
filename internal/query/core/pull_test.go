package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/query/wire"
	"github.com/syntrixbase/syntrix/pkg/model"
)

type pullWatch struct {
	initial   types.WatchCheckpoint
	frames    []types.WatchFrame
	err       error
	closeErr  error
	closed    bool
	reads     int
	afterRead func()
}

func (s *pullWatch) InitialCheckpoint() types.WatchCheckpoint { return s.initial }
func (s *pullWatch) Close() error                             { s.closed = true; return s.closeErr }
func (s *pullWatch) Next(ctx context.Context) (types.WatchFrame, error) {
	if err := ctx.Err(); err != nil {
		return types.WatchFrame{}, err
	}
	s.reads++
	if s.afterRead != nil {
		s.afterRead()
	}
	if len(s.frames) == 0 {
		return types.WatchFrame{}, s.err
	}
	frame := s.frames[0]
	s.frames = s.frames[1:]
	return frame, nil
}

type pullStore struct {
	types.DocumentStore
	watch func(context.Context, string, string, types.WatchCheckpoint, types.WatchOptions) (types.WatchStream, error)
	scan  func(context.Context, string, types.SourceScanRequest) (types.SourceScanPage, error)
}

func (s *pullStore) Watch(ctx context.Context, db, col string, cp types.WatchCheckpoint, opts types.WatchOptions) (types.WatchStream, error) {
	return s.watch(ctx, db, col, cp, opts)
}
func (s *pullStore) ScanDocuments(ctx context.Context, db string, req types.SourceScanRequest) (types.SourceScanPage, error) {
	return s.scan(ctx, db, req)
}

func pullStored(id string) *types.StoredDoc {
	doc := types.NewStoredDoc("db", "users", id, map[string]any{"id": "business-id", "counter": int64(math.MaxInt64)})
	doc.Version, doc.UpdatedAt = 7, 123
	return &doc
}

func pullFrame(id, checkpoint string, kind types.EventType) types.WatchFrame {
	event := &types.Event{Database: "db", Collection: "users", DocumentID: id, Type: kind}
	if kind != types.EventDelete {
		event.Document = pullStored(id)
	}
	return types.WatchFrame{Checkpoint: types.WatchCheckpoint(checkpoint), Event: event, SourceBytes: 100}
}

func pullToken(t *testing.T, phase string, position types.WatchCheckpoint, after string) string {
	t.Helper()
	encoded, err := encodePullCursor(pullCursor{Version: 3, Database: "db", DatabaseIdentity: "db", Collection: "users", Phase: phase, Position: position, AfterID: after})
	require.NoError(t, err)
	return encoded
}

func pullCode(t *testing.T, err error, code types.WatchErrorCode) {
	t.Helper()
	var failure *types.WatchError
	require.ErrorAs(t, err, &failure)
	require.Equal(t, code, failure.Code)
}

func changesEngine(t *testing.T, stream *pullWatch) *Engine {
	t.Helper()
	stream.initial = "C0"
	return New(&pullStore{watch: func(_ context.Context, db, col string, after types.WatchCheckpoint, opts types.WatchOptions) (types.WatchStream, error) {
		require.Equal(t, "db", db)
		require.Equal(t, "users", col)
		require.Equal(t, types.WatchCheckpoint("C0"), after)
		require.Equal(t, 100*time.Millisecond, opts.MaxAwaitTime)
		require.Equal(t, types.WatchStartCurrent, opts.StartMode)
		return stream, nil
	}}, nil)
}

func TestPullBootstrapPagesKeepInitialBoundary(t *testing.T) {
	initial := &pullWatch{initial: "C0"}
	watchCalls, scanCalls := 0, 0
	source := &pullStore{watch: func(_ context.Context, db, col string, after types.WatchCheckpoint, opts types.WatchOptions) (types.WatchStream, error) {
		watchCalls++
		require.Equal(t, "db", db)
		require.Equal(t, "users", col)
		require.Empty(t, after)
		require.Equal(t, types.WatchStartForScan, opts.StartMode)
		return initial, nil
	}, scan: func(_ context.Context, db string, req types.SourceScanRequest) (types.SourceScanPage, error) {
		scanCalls++
		require.True(t, initial.closed)
		require.Equal(t, types.WatchCheckpoint("C0"), req.AtLeast)
		require.Equal(t, types.ReadAuthoritative, req.Consistency)
		require.Equal(t, maxPullSourceBytes, req.MaxBytes)
		if scanCalls == 1 {
			require.Empty(t, req.AfterID)
			return types.SourceScanPage{Documents: []*types.StoredDoc{pullStored("alice"), pullStored("bob")}, NextAfter: "bob"}, nil
		}
		require.Equal(t, "bob", req.AfterID)
		tombstone := pullStored("charlie")
		tombstone.Deleted = true
		tombstone.Data = map[string]any{}
		return types.SourceScanPage{Documents: []*types.StoredDoc{tombstone}, NextAfter: "charlie", Exhausted: true}, nil
	}}
	first, err := New(source, nil).Pull(context.Background(), "db", types.ReplicationPullRequest{Collection: "users", Limit: 2})
	require.NoError(t, err)
	require.Len(t, first.Documents, 2)
	require.False(t, first.CaughtUp)
	cursor, err := decodePullCursor("db", "db", "users", first.Checkpoint)
	require.NoError(t, err)
	require.Equal(t, "bob", cursor.AfterID)
	require.Equal(t, "scan", cursor.Phase)
	second, err := New(source, nil).Pull(context.Background(), "db", types.ReplicationPullRequest{Collection: "users", Limit: 2, Checkpoint: first.Checkpoint})
	require.NoError(t, err)
	require.Len(t, second.Documents, 1)
	require.True(t, second.Documents[0]["deleted"].(bool))
	require.False(t, second.CaughtUp)
	cursor, err = decodePullCursor("db", "db", "users", second.Checkpoint)
	require.NoError(t, err)
	require.Equal(t, "changes", cursor.Phase)
	require.Equal(t, types.WatchCheckpoint("C0"), cursor.Position)
	require.Empty(t, cursor.AfterID)
	require.Equal(t, 1, watchCalls)
	require.Equal(t, 0, initial.reads)
}

func TestPullChangesOrderedStateWithoutReads(t *testing.T) {
	update := pullFrame("alice", "C1", types.EventUpdate)
	deleted := pullFrame("alice", "C2", types.EventDelete)
	recreated := pullFrame("alice", "C3", types.EventCreate)
	recreated.Event.Document.Version = 1
	laterDelete := pullFrame("bob", "C4", types.EventUpdate)
	laterDelete.Event.Document.Deleted = true
	laterDelete.Event.Document.Data = map[string]any{}
	stream := &pullWatch{frames: []types.WatchFrame{update, deleted, recreated, laterDelete, {Checkpoint: "C5", CaughtUp: true}}}
	page, err := changesEngine(t, stream).Pull(context.Background(), "db", types.ReplicationPullRequest{Collection: "users", Checkpoint: pullToken(t, "changes", "C0", "")})
	require.NoError(t, err)
	require.True(t, stream.closed)
	require.True(t, page.CaughtUp)
	require.Len(t, page.Documents, 4)
	require.Equal(t, int64(math.MaxInt64), page.Documents[0]["counter"])
	require.Equal(t, model.Document{"id": "alice", "collection": "users", "deleted": true}, page.Documents[1])
	require.Equal(t, int64(1), page.Documents[2]["version"])
	require.True(t, page.Documents[3]["deleted"].(bool))
	require.NotContains(t, page.Documents[3], "counter")
	cursor, err := decodePullCursor("db", "db", "users", page.Checkpoint)
	require.NoError(t, err)
	require.Equal(t, types.WatchCheckpoint("C5"), cursor.Position)
}

func TestPullPageLimitsRetainHandledPrefix(t *testing.T) {
	for _, test := range []struct {
		name   string
		frames []types.WatchFrame
		limit  int
		want   string
		count  int
		caught bool
	}{
		{"count", []types.WatchFrame{pullFrame("a", "C1", types.EventCreate), pullFrame("b", "C2", types.EventCreate)}, 1, "C1", 1, false},
		{"filtered full budget", []types.WatchFrame{{Checkpoint: "C1", SourceBytes: maxPullSourceBytes}}, 0, "C1", 0, false},
		{"filtered then overflow", []types.WatchFrame{{Checkpoint: "C1", SourceBytes: 10}, {Checkpoint: "C2", SourceBytes: maxPullSourceBytes}}, 0, "C1", 0, false},
		{"filtered then watermark", []types.WatchFrame{{Checkpoint: "C1"}, {Checkpoint: "C2", CaughtUp: true}}, 0, "C2", 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			stream := &pullWatch{frames: test.frames}
			page, err := changesEngine(t, stream).Pull(context.Background(), "db", types.ReplicationPullRequest{Collection: "users", Checkpoint: pullToken(t, "changes", "C0", ""), Limit: test.limit})
			require.NoError(t, err)
			require.Len(t, page.Documents, test.count)
			require.Equal(t, test.caught, page.CaughtUp)
			require.True(t, stream.closed)
			cursor, err := decodePullCursor("db", "db", "users", page.Checkpoint)
			require.NoError(t, err)
			require.Equal(t, types.WatchCheckpoint(test.want), cursor.Position)
		})
	}
	stream := &pullWatch{frames: make([]types.WatchFrame, maxPullFrames+1)}
	for i := range stream.frames {
		stream.frames[i].Checkpoint = types.WatchCheckpoint(fmt.Sprint(i))
	}
	page, err := changesEngine(t, stream).Pull(context.Background(), "db", types.ReplicationPullRequest{Collection: "users", Checkpoint: pullToken(t, "changes", "C0", "")})
	require.NoError(t, err)
	require.False(t, page.CaughtUp)
	require.Equal(t, maxPullFrames, stream.reads)
}

func TestPullErrorsDiscardWholePage(t *testing.T) {
	for _, test := range []struct {
		name      string
		frame     types.WatchFrame
		sourceErr error
		closeErr  error
		code      types.WatchErrorCode
	}{
		{"history", types.WatchFrame{}, &types.WatchError{Code: types.WatchHistoryUnavailable}, nil, types.WatchHistoryUnavailable},
		{"negative source bytes", types.WatchFrame{Checkpoint: "C2", SourceBytes: -1}, nil, nil, types.WatchInvalidEvent},
		{"missing checkpoint", types.WatchFrame{}, nil, nil, types.WatchInvalidEvent},
		{"watermark with event", types.WatchFrame{Checkpoint: "C2", CaughtUp: true, Event: pullFrame("a", "C2", types.EventUpdate).Event}, nil, nil, types.WatchInvalidEvent},
		{"close failure", types.WatchFrame{Checkpoint: "C2", CaughtUp: true}, nil, &types.WatchError{Code: types.WatchSourceUnavailable}, types.WatchSourceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			stream := &pullWatch{frames: []types.WatchFrame{pullFrame("a", "C1", types.EventUpdate)}, err: test.sourceErr, closeErr: test.closeErr}
			if test.sourceErr == nil {
				stream.frames = append(stream.frames, test.frame)
			}
			page, err := changesEngine(t, stream).Pull(context.Background(), "db", types.ReplicationPullRequest{Collection: "users", Checkpoint: pullToken(t, "changes", "C0", "")})
			require.Nil(t, page)
			pullCode(t, err, test.code)
			require.True(t, stream.closed)
		})
	}
	stream := &pullWatch{frames: []types.WatchFrame{{Checkpoint: "C1", SourceBytes: maxPullSourceBytes + 1}}}
	page, err := changesEngine(t, stream).Pull(context.Background(), "db", types.ReplicationPullRequest{Collection: "users", Checkpoint: pullToken(t, "changes", "C0", "")})
	require.Nil(t, page)
	require.ErrorIs(t, err, model.ErrQueryWorkLimit)
}

func TestPullEventIdentityValidation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*types.Event)
		code   types.WatchErrorCode
	}{
		{"database", func(e *types.Event) { e.Database = "other" }, types.WatchInvalidEvent},
		{"collection", func(e *types.Event) { e.Collection = "other" }, types.WatchInvalidEvent},
		{"id", func(e *types.Event) { e.DocumentID = "a/b" }, types.WatchInvalidEvent},
		{"absent doc", func(e *types.Event) { e.Document = nil }, types.WatchPayloadUnavailable},
		{"mismatched doc", func(e *types.Event) { e.DocumentID = "other" }, types.WatchInvalidEvent},
		{"bad path", func(e *types.Event) { e.Document.Fullpath = "other/a" }, types.WatchInvalidEvent},
		{"cross namespace", func(e *types.Event) { e.Document.Database = "other" }, types.WatchInvalidEvent},
		{"delete document", func(e *types.Event) { e.Type = types.EventDelete }, types.WatchInvalidEvent},
		{"unsupported", func(e *types.Event) { e.Type = "unknown" }, types.WatchInvalidEvent},
	} {
		t.Run(test.name, func(t *testing.T) {
			frame := pullFrame("a", "C1", types.EventUpdate)
			test.mutate(frame.Event)
			page, err := changesEngine(t, &pullWatch{frames: []types.WatchFrame{frame}}).Pull(context.Background(), "db", types.ReplicationPullRequest{Collection: "users", Checkpoint: pullToken(t, "changes", "C0", "")})
			require.Nil(t, page)
			pullCode(t, err, test.code)
		})
	}
}

func TestPullScanShrinksBoundedReads(t *testing.T) {
	for _, oversize := range []bool{false, true} {
		t.Run(fmt.Sprint(oversize), func(t *testing.T) {
			limits := []int{}
			source := &pullStore{scan: func(_ context.Context, _ string, req types.SourceScanRequest) (types.SourceScanPage, error) {
				limits = append(limits, req.Limit)
				require.Equal(t, "before", req.AfterID)
				require.Equal(t, types.WatchCheckpoint("C0"), req.AtLeast)
				if req.Limit > 1 || oversize {
					return types.SourceScanPage{}, types.ErrSourceScanBudget
				}
				return types.SourceScanPage{Documents: []*types.StoredDoc{pullStored("z")}, NextAfter: "z"}, nil
			}}
			page, err := New(source, nil).Pull(context.Background(), "db", types.ReplicationPullRequest{Collection: "users", Limit: 1000, Checkpoint: pullToken(t, "scan", "C0", "before")})
			require.Len(t, limits, 10)
			if oversize {
				require.Nil(t, page)
				require.ErrorIs(t, err, model.ErrQueryWorkLimit)
			} else {
				require.NoError(t, err)
				require.Len(t, page.Documents, 1)
			}
		})
	}
}

func TestPullWireTrimmingReplaysUnreturnedDocument(t *testing.T) {
	large := pullStored("b")
	large.Data = map[string]any{"payload": strings.Repeat("\n", 5<<20)}
	small := pullStored("a")
	small.Data = map[string]any{"payload": strings.Repeat("\n", 5<<20)}
	for _, phase := range []string{"scan", "changes"} {
		t.Run(phase, func(t *testing.T) {
			source := &pullStore{scan: func(context.Context, string, types.SourceScanRequest) (types.SourceScanPage, error) {
				return types.SourceScanPage{Documents: []*types.StoredDoc{small, large}, NextAfter: "b", Exhausted: true}, nil
			}}
			stream := &pullWatch{initial: "C0", frames: []types.WatchFrame{pullFrame("a", "C1", types.EventCreate), pullFrame("b", "C2", types.EventCreate)}}
			stream.frames[0].Event.Document, stream.frames[1].Event.Document = small, large
			source.watch = func(context.Context, string, string, types.WatchCheckpoint, types.WatchOptions) (types.WatchStream, error) {
				return stream, nil
			}
			page, err := New(source, nil).Pull(context.Background(), "db", types.ReplicationPullRequest{Collection: "users", Checkpoint: pullToken(t, phase, "C0", "")})
			require.NoError(t, err)
			require.Len(t, page.Documents, 1)
			require.False(t, page.CaughtUp)
			cursor, err := decodePullCursor("db", "db", "users", page.Checkpoint)
			require.NoError(t, err)
			require.Equal(t, phase, cursor.Phase)
			if phase == "scan" {
				require.Equal(t, "a", cursor.AfterID)
				require.Equal(t, types.WatchCheckpoint("C0"), cursor.Position)
			} else {
				require.Equal(t, types.WatchCheckpoint("C1"), cursor.Position)
			}
			_, err = wire.EncodeJSONPullPage(page)
			require.NoError(t, err)
		})
	}
}

func TestPullCursorScopeValidation(t *testing.T) {
	valid := pullToken(t, "changes", "C0", "")
	for _, req := range []types.ReplicationPullRequest{
		{Collection: "users", Checkpoint: valid},
		{Collection: "users", Limit: 1000},
	} {
		require.NoError(t, ValidatePullRequest("db", req))
	}
	for _, test := range []struct {
		name, db, identity, col, token string
		limit                          int
		code                           types.WatchErrorCode
	}{
		{"legacy", "db", "", "users", "123", 0, types.WatchHistoryUnavailable},
		{"base64", "db", "", "users", "?", 0, types.WatchInvalidCheckpoint},
		{"empty db", "", "", "users", "", 0, types.WatchInvalidScope},
		{"wildcard", "db", "", "users/*", "", 0, types.WatchInvalidScope},
		{"document path", "db", "", "users/a", "", 0, types.WatchInvalidScope},
		{"negative limit", "db", "", "users", "", -1, types.WatchInvalidCheckpoint},
		{"large limit", "db", "", "users", "", 1001, types.WatchInvalidCheckpoint},
		{"namespace", "alias", "", "users", valid, 0, types.WatchScopeMismatch},
		{"entity", "db", "recreated-db", "users", valid, 0, types.WatchScopeMismatch},
		{"collection", "db", "", "items", valid, 0, types.WatchScopeMismatch},
		{"request bytes", "db", strings.Repeat("x", MaxPullRequestBytes), "users", "", 0, types.WatchInvalidCheckpoint},
		{"cursor bytes", "db", "", "users", strings.Repeat("x", MaxPullCursorBytes+1), 0, types.WatchInvalidCheckpoint},
	} {
		t.Run(test.name, func(t *testing.T) {
			pullCode(t, ValidatePullRequest(test.db, types.ReplicationPullRequest{Collection: test.col, DatabaseIdentity: test.identity, Checkpoint: test.token, Limit: test.limit}), test.code)
		})
	}
	for _, raw := range []string{`{}`, `{"unknown":true}`, `{} {}`, `{"version":3,"database":"db","databaseIdentity":"db","collection":"users","phase":"changes","position":"C0","afterId":"a"}`, `{"version":2,"database":"db","databaseIdentity":"db","collection":"users","phase":"changes","position":"C0"}`, "{\"position\":\"\\ud800\"}"} {
		_, err := decodePullCursor("db", "db", "users", base64.RawURLEncoding.EncodeToString([]byte(raw)))
		pullCode(t, err, types.WatchInvalidCheckpoint)
	}
	identityToken, err := encodePullCursor(pullCursor{Version: 3, Database: "alias", DatabaseIdentity: "entity", Collection: "users", Phase: "changes", Position: "C0"})
	require.NoError(t, err)
	require.NoError(t, ValidatePullRequest("alias", types.ReplicationPullRequest{DatabaseIdentity: "entity", Collection: "users", Checkpoint: identityToken}))
	_, err = decodePullCursor("entity", "entity", "users", identityToken)
	pullCode(t, err, types.WatchScopeMismatch)
	_, err = encodePullCursor(pullCursor{})
	pullCode(t, err, types.WatchInvalidEvent)
	_, err = encodePullCursor(pullCursor{Position: types.WatchCheckpoint(strings.Repeat("x", MaxPullCursorBytes))})
	require.ErrorIs(t, err, model.ErrQueryWorkLimit)
	var encoded map[string]any
	bytes, err := base64.RawURLEncoding.DecodeString(valid)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(bytes, &encoded))
	encoded["extra"] = true
	bytes, err = json.Marshal(encoded)
	require.NoError(t, err)
	_, err = decodePullCursor("db", "db", "users", base64.RawURLEncoding.EncodeToString(bytes))
	pullCode(t, err, types.WatchInvalidCheckpoint)
}

func TestPullCancellationAndSoftDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	page, err := New(nil, nil).Pull(ctx, "db", types.ReplicationPullRequest{Collection: "users"})
	require.Nil(t, page)
	require.ErrorIs(t, err, context.Canceled)
	stream := &pullWatch{frames: []types.WatchFrame{pullFrame("a", "C1", types.EventCreate)}}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	stream.afterRead = cancel
	page, err = changesEngine(t, stream).Pull(ctx, "db", types.ReplicationPullRequest{Collection: "users", Limit: 1, Checkpoint: pullToken(t, "changes", "C0", "")})
	require.Nil(t, page)
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, stream.closed)
	stream = &pullWatch{}
	engine := changesEngine(t, stream)
	cursor := pullCursor{Version: 3, Database: "db", DatabaseIdentity: "db", Collection: "users", Phase: "changes", Position: "C0"}
	p := &pullPage{response: types.ReplicationPullResponse{Checkpoint: pullToken(t, "changes", "C0", "")}}
	err = engine.pullChanges(context.Background(), cursor, 100, time.Now().Add(-time.Second), p)
	require.NoError(t, err)
	require.Equal(t, 0, stream.reads)
	require.False(t, p.response.CaughtUp)
	require.True(t, stream.closed)
}

func TestPullInitialAndScanFailurePaths(t *testing.T) {
	for _, test := range []struct {
		name       string
		watchError error
		initial    types.WatchCheckpoint
		closeError error
		scanError  error
		code       types.WatchErrorCode
	}{
		{"watch failure", &types.WatchError{Code: types.WatchSourceUnavailable}, "", nil, nil, types.WatchSourceUnavailable},
		{"empty boundary", nil, "", nil, nil, types.WatchInvalidEvent},
		{"cleanup failure", nil, "C0", &types.WatchError{Code: types.WatchSourceUnavailable}, nil, types.WatchSourceUnavailable},
		{"scan history failure", nil, "C0", nil, &types.WatchError{Code: types.WatchHistoryUnavailable}, types.WatchHistoryUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := &pullStore{watch: func(context.Context, string, string, types.WatchCheckpoint, types.WatchOptions) (types.WatchStream, error) {
				return &pullWatch{initial: test.initial, closeErr: test.closeError}, test.watchError
			}, scan: func(context.Context, string, types.SourceScanRequest) (types.SourceScanPage, error) {
				return types.SourceScanPage{}, test.scanError
			}}
			page, err := New(source, nil).Pull(context.Background(), "db", types.ReplicationPullRequest{Collection: "users"})
			require.Nil(t, page)
			pullCode(t, err, test.code)
		})
	}
	for _, batch := range []types.SourceScanPage{
		{},
		{Documents: []*types.StoredDoc{nil}},
		{Documents: []*types.StoredDoc{pullStored("b"), pullStored("a")}, NextAfter: "a"},
		{Documents: []*types.StoredDoc{pullStored("a")}, NextAfter: "b"},
	} {
		source := &pullStore{scan: func(context.Context, string, types.SourceScanRequest) (types.SourceScanPage, error) {
			return batch, nil
		}}
		page, err := New(source, nil).Pull(context.Background(), "db", types.ReplicationPullRequest{Collection: "users", Checkpoint: pullToken(t, "scan", "C0", "")})
		require.Nil(t, page)
		pullCode(t, err, types.WatchInvalidEvent)
	}
	page, err := New(&struct{ types.DocumentStore }{}, nil).Pull(context.Background(), "db", types.ReplicationPullRequest{Collection: "users", Checkpoint: pullToken(t, "scan", "C0", "")})
	require.Nil(t, page)
	pullCode(t, err, types.WatchUnsupported)
}

func TestPullOversizedOrUnencodableFirstDocument(t *testing.T) {
	for _, payload := range []any{strings.Repeat("x", wire.MaxPageBytes), make(chan int)} {
		frame := pullFrame("a", "C1", types.EventUpdate)
		frame.Event.Document.Data = map[string]any{"payload": payload}
		page, err := changesEngine(t, &pullWatch{frames: []types.WatchFrame{frame}}).Pull(context.Background(), "db", types.ReplicationPullRequest{Collection: "users", Checkpoint: pullToken(t, "changes", "C0", "")})
		require.Nil(t, page)
		if _, ok := payload.(string); ok {
			require.ErrorIs(t, err, model.ErrQueryWorkLimit)
		} else {
			pullCode(t, err, types.WatchInvalidEvent)
		}
	}
}

func TestPullWatchResumeAndDeadlineFailures(t *testing.T) {
	for _, test := range []struct {
		name      string
		initial   types.WatchCheckpoint
		openError error
		nextError error
		want      error
		code      types.WatchErrorCode
	}{
		{"open", "", &types.WatchError{Code: types.WatchHistoryUnavailable}, nil, nil, types.WatchHistoryUnavailable},
		{"mismatched start", "C1", nil, nil, nil, types.WatchInvalidEvent},
		{"deadline", "C0", nil, context.DeadlineExceeded, context.DeadlineExceeded, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			stream := &pullWatch{initial: test.initial, err: test.nextError}
			source := &pullStore{watch: func(context.Context, string, string, types.WatchCheckpoint, types.WatchOptions) (types.WatchStream, error) {
				return stream, test.openError
			}}
			page, err := New(source, nil).Pull(context.Background(), "db", types.ReplicationPullRequest{Collection: "users", Checkpoint: pullToken(t, "changes", "C0", "")})
			require.Nil(t, page)
			if test.want != nil {
				require.ErrorIs(t, err, test.want)
			} else {
				pullCode(t, err, test.code)
			}
		})
	}
	page, err := New(nil, nil).Pull(context.Background(), "db", types.ReplicationPullRequest{Collection: "users", Checkpoint: "123"})
	require.Nil(t, page)
	pullCode(t, err, types.WatchHistoryUnavailable)
}
