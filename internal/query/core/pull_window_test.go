package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/indexer"
	"github.com/syntrixbase/syntrix/internal/indexer/manager"
	"github.com/syntrixbase/syntrix/internal/query/wire"
	"github.com/syntrixbase/syntrix/pkg/model"
)

func windowRequest(limit int, order ...model.Order) types.ReplicationPullRequest {
	requestID := "refresh-1"
	return types.ReplicationPullRequest{DatabaseIdentity: "identity", Collection: "items", RequestID: &requestID,
		Source: &types.ReplicationSource{Version: 1, Filters: model.Filters{}, OrderBy: order, Limit: &limit}}
}

func windowIDs(page *types.ReplicationPullResponse) []string {
	return pageIDs(model.QueryPage{Documents: page.Documents})
}

func TestPullWindowCompleteness(t *testing.T) {
	cursor := "more"
	for _, test := range []struct {
		name         string
		count, limit int
		next         *string
		err          error
	}{
		{"full continued", 2, 2, &cursor, nil},
		{"full exhausted", 2, 2, nil, nil},
		{"short exhausted", 1, 2, nil, nil},
		{"empty exhausted", 0, 2, nil, nil},
		{"short continued", 1, 2, &cursor, types.ErrReplicationWindowIncomplete},
		{"empty continued", 0, 2, &cursor, types.ErrReplicationWindowIncomplete},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := queryWindowComplete(model.QueryPage{Documents: make([]model.Document, test.count), NextCursor: test.next}, test.limit)
			require.ErrorIs(t, err, test.err)
		})
	}
	pullCode(t, queryWindowComplete(model.QueryPage{Documents: make([]model.Document, 3)}, 2), types.WatchInvalidEvent)
	pullCode(t, queryWindowComplete(model.QueryPage{}, 0), types.WatchInvalidEvent)
}

func TestPullWindowRefreshFillsEvictsAndExcludesDeletes(t *testing.T) {
	for _, backend := range []string{"memory", "pebble"} {
		t.Run(backend, func(t *testing.T) {
			engine, source, st, idx := pageFixture(t, backend, "      - {field: score, order: asc}\n")
			put := func(id string, score int64) {
				doc := queryDocument(id, map[string]any{"score": score})
				source.docs[doc.Fullpath] = doc
				projectPageDocument(t, st, idx, doc)
			}
			put("a", 10)
			put("b", 20)
			put("c", 30)
			req := windowRequest(2, model.Order{Field: "score", Direction: "asc"})
			first, err := engine.Pull(context.Background(), "db", req)
			require.NoError(t, err)
			require.Equal(t, []string{"a", "b"}, windowIDs(first))
			require.Equal(t, "replace", first.Mode)
			require.True(t, *first.Complete)
			require.Equal(t, req.RequestID, first.RequestID)
			require.Equal(t, []model.Order{{Field: "score", Direction: "asc"}, {Field: "id", Direction: "asc"}}, first.EffectiveOrder)
			require.NoError(t, ValidatePullResponseScope(req, first))
			source.docs["items/a"].Deleted = true
			second, err := engine.Pull(context.Background(), "db", req)
			require.NoError(t, err)
			require.Equal(t, []string{"b", "c"}, windowIDs(second))
			require.NotEqual(t, first.GenerationID, second.GenerationID)
			require.Equal(t, first.SourceHash, second.SourceHash)
			put("d", 5)
			third, err := engine.Pull(context.Background(), "db", req)
			require.NoError(t, err)
			require.Equal(t, []string{"d", "b"}, windowIDs(third))
			require.Empty(t, source.scans)
			require.Empty(t, third.Checkpoint)
			require.Empty(t, third.Events)
			require.Empty(t, third.Phase)
			require.False(t, third.CaughtUp)
			require.False(t, third.BootstrapComplete)
		})
	}
}

func TestPullWindowIndexLagCanTemporarilyRemoveExistingMember(t *testing.T) {
	for _, backend := range []string{"memory", "pebble"} {
		t.Run(backend, func(t *testing.T) {
			engine, source, st, idx := pageFixture(t, backend, "      - {field: score, order: asc}\n")
			for i, id := range []string{"a", "b", "c"} {
				doc := queryDocument(id, map[string]any{"score": int64((i + 1) * 10)})
				source.docs[doc.Fullpath] = doc
				projectPageDocument(t, st, idx, doc)
			}
			req := windowRequest(2, model.Order{Field: "score", Direction: "asc"})
			page, err := engine.Pull(context.Background(), "db", req)
			require.NoError(t, err)
			require.Equal(t, []string{"a", "b"}, windowIDs(page))
			source.docs["items/a"].Data["score"] = int64(11)
			page, err = engine.Pull(context.Background(), "db", req)
			require.NoError(t, err)
			require.Equal(t, []string{"b", "c"}, windowIDs(page))
			projectPageDocument(t, st, idx, source.docs["items/a"])
			page, err = engine.Pull(context.Background(), "db", req)
			require.NoError(t, err)
			require.Equal(t, []string{"a", "b"}, windowIDs(page))
		})
	}
}

func TestPullWindowDefaultOrderEmptyAndShortResults(t *testing.T) {
	engine, source, st, idx := pageFixture(t, "memory", "      - {field: id, order: asc}\n")
	tombstone := queryDocument("gone", nil)
	tombstone.Deleted = true
	source.docs[tombstone.Fullpath] = tombstone
	projectPageDocument(t, st, idx, tombstone)
	req := windowRequest(3)
	page, err := engine.Pull(context.Background(), "db", req)
	require.NoError(t, err)
	require.Empty(t, page.Documents)
	require.NotNil(t, page.Documents)
	require.True(t, *page.Complete)
	for _, id := range []string{"z", "a"} {
		doc := queryDocument(id, map[string]any{"id": "shadow"})
		source.docs[doc.Fullpath] = doc
		projectPageDocument(t, st, idx, doc)
	}
	page, err = engine.Pull(context.Background(), "db", req)
	require.NoError(t, err)
	require.Equal(t, []string{"a", "z"}, windowIDs(page))
	require.Equal(t, []model.Order{{Field: "id", Direction: "asc"}}, page.EffectiveOrder)
	require.NoError(t, ValidatePullResponseScope(req, page))
	require.Empty(t, source.scans)
}

func TestPullWindowSourceErrorsDiscardReplacement(t *testing.T) {
	failure := errors.New("source unavailable")
	engine, source, st, idx := pageFixture(t, "memory", "      - {field: score, order: asc}\n")
	doc := queryDocument("a", map[string]any{"score": int64(1)})
	source.docs[doc.Fullpath] = doc
	projectPageDocument(t, st, idx, doc)
	source.fail = failure
	page, err := engine.Pull(context.Background(), "db", windowRequest(1, model.Order{Field: "score", Direction: "asc"}))
	require.ErrorIs(t, err, failure)
	require.Nil(t, page)
	page, err = New(&pageSource{}, nil).Pull(context.Background(), "db", windowRequest(1, model.Order{Field: "score", Direction: "asc"}))
	require.ErrorIs(t, err, ErrIndexerRequired)
	require.Nil(t, page)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	page, err = New(&pageSource{}, nil).Pull(ctx, "db", windowRequest(1))
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, page)
	source.fail = nil
	for i := 0; i < 20; i++ {
		doc := queryDocument(fmt.Sprintf("%03d", i), map[string]any{"score": int64(i), "payload": strings.Repeat("x", 900<<10)})
		source.docs[doc.Fullpath] = doc
		projectPageDocument(t, st, idx, doc)
	}
	page, err = engine.Pull(context.Background(), "db", windowRequest(20, model.Order{Field: "score", Direction: "asc"}))
	require.ErrorIs(t, err, model.ErrQueryWorkLimit)
	require.Nil(t, page)
}

func TestPullWindowScopeAndNormalizedOrder(t *testing.T) {
	engine, source, st, idx := pageFixture(t, "memory", "      - {field: score, order: asc}\n")
	for _, id := range []string{"z", "a", "b"} {
		doc := queryDocument(id, map[string]any{"score": int64(1)})
		source.docs[doc.Fullpath] = doc
		projectPageDocument(t, st, idx, doc)
	}
	req := windowRequest(2, model.Order{Field: "score", Direction: "asc"})
	page, err := engine.Pull(context.Background(), "db", req)
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, windowIDs(page))
	for _, test := range []struct {
		name   string
		mutate func(*types.ReplicationPullResponse)
	}{
		{"source hash", func(p *types.ReplicationPullResponse) { p.SourceHash = strings.Repeat("0", 64) }},
		{"order", func(p *types.ReplicationPullResponse) {
			p.EffectiveOrder = []model.Order{{Field: "id", Direction: "asc"}}
		}},
		{"request id", func(p *types.ReplicationPullResponse) { value := "other"; p.RequestID = &value }},
		{"count", func(p *types.ReplicationPullResponse) {
			p.Documents = append(append([]model.Document{}, p.Documents...), model.Document{"id": "z", "collection": "items"})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			bad := *page
			test.mutate(&bad)
			pullCode(t, ValidatePullResponseScope(req, &bad), types.WatchInvalidEvent)
		})
	}
	invalid := windowRequest(2)
	invalid.Source.Version = 2
	pullCode(t, ValidatePullResponseScope(invalid, page), types.WatchInvalidEvent)
	pullCode(t, ValidatePullResponseScope(req, nil), types.WatchInvalidEvent)
	firstSource, err := normalizePullSource(req)
	require.NoError(t, err)
	req.Source.OrderBy = append(req.Source.OrderBy, model.Order{Field: "id", Direction: "asc"})
	secondSource, err := normalizePullSource(req)
	require.NoError(t, err)
	require.Equal(t, firstSource.hash, secondSource.hash)
	require.NoError(t, ValidatePullResponseScope(req, page))
}

func TestPullWindowIndexErrorsDiscardReplacement(t *testing.T) {
	engine, _, _, _ := pageFixture(t, "memory", "      - {field: score, order: asc}\n")
	page, err := engine.Pull(context.Background(), "db", windowRequest(2, model.Order{Field: "score", Direction: "asc"}))
	require.ErrorIs(t, err, indexer.ErrIndexNotReady)
	require.Nil(t, page)
	page, err = engine.Pull(context.Background(), "db", windowRequest(2, model.Order{Field: "unknown", Direction: "asc"}))
	require.ErrorIs(t, err, manager.ErrNoMatchingIndex)
	require.Nil(t, page)
}

func TestPullWindowEnvelopeOverflowDiscardsSuccessfulQuery(t *testing.T) {
	engine, source, st, idx := pageFixture(t, "memory", "      - {field: score, order: asc}\n")
	doc := queryDocument("a", map[string]any{"score": int64(1), "payload": strings.Repeat("x", wire.MaxPageBytes-(256<<10))})
	source.docs[doc.Fullpath] = doc
	projectPageDocument(t, st, idx, doc)
	req := windowRequest(1, model.Order{Field: "score", Direction: "asc"})
	requestID := strings.Repeat("r", 512<<10)
	req.RequestID = &requestID
	normalized, err := normalizePullSource(req)
	require.NoError(t, err)
	queryPage, err := engine.ExecuteQueryPage(context.Background(), "db", normalized.query)
	require.NoError(t, err)
	require.Len(t, queryPage.Documents, 1)
	page, err := engine.Pull(context.Background(), "db", req)
	require.ErrorIs(t, err, model.ErrQueryWorkLimit)
	require.Nil(t, page)
}

type cancelOnWindowQueryClose struct {
	manager.CandidateStream
	cancel context.CancelFunc
}

func (s *cancelOnWindowQueryClose) Close() error {
	err := s.CandidateStream.Close()
	s.cancel()
	return err
}

func TestPullWindowCancellationAfterQueryDiscardsReplacement(t *testing.T) {
	engine, source, st, idx := pageFixture(t, "memory", "      - {field: score, order: asc}\n")
	doc := queryDocument("a", map[string]any{"score": int64(1)})
	source.docs[doc.Fullpath] = doc
	projectPageDocument(t, st, idx, doc)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	idx.wrap = func(stream manager.CandidateStream) manager.CandidateStream {
		return &cancelOnWindowQueryClose{CandidateStream: stream, cancel: cancel}
	}
	page, err := engine.Pull(ctx, "db", windowRequest(1, model.Order{Field: "score", Direction: "asc"}))
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, page)
	require.Equal(t, []int{1}, source.readLengths)
}
