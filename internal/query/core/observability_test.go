package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/ctxkeys"
	"github.com/syntrixbase/syntrix/internal/indexer"
	"github.com/syntrixbase/syntrix/pkg/model"
	"log/slog"
	"strings"
	"testing"
)

func TestQueryCompletionRecordsCountersWithoutValues(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)
	engine, source, st, idx := pageFixture(t, "memory", "      - {field: score, order: asc}\n")
	for _, id := range []string{"0", "1", "2", "3"} {
		doc := queryDocument(id, map[string]any{"score": id, "secret": "FILTER_SECRET", "body": "DOCUMENT_SECRET"})
		source.docs[doc.Fullpath] = doc
		projectPageDocument(t, st, idx, doc)
	}
	delete(source.docs, "items/0")
	source.docs["items/1"].Data["secret"] = "different"
	source.docs["items/2"].Data["score"] = "9"
	ctx := context.WithValue(context.Background(), ctxkeys.KeyRequestID, "request-example")
	q := model.Query{Collection: "items", OrderBy: []model.Order{{Field: "score", Direction: "asc"}}, Filters: model.Filters{{Field: "secret", Op: model.OpEq, Value: "FILTER_SECRET"}}, Limit: 1}
	page, err := engine.ExecuteQueryPage(ctx, "db", q)
	require.NoError(t, err)
	require.Equal(t, []string{"3"}, pageIDs(page))
	var record map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record))
	require.Equal(t, "request-example", record["request_id"])
	require.Equal(t, "index-v2", record["route"])
	require.Equal(t, "page_limit", record["reason"])
	for _, field := range []string{"candidates", "examined", "source_reads"} {
		require.Equal(t, float64(4), record[field], field)
	}
	require.Equal(t, float64(3), record["source_documents"])
	for _, field := range []string{"rejected_missing", "rejected_predicate", "rejected_position", "returned"} {
		require.Equal(t, float64(1), record[field], field)
	}
	require.Len(t, record["plan_id"], 64)
	require.Len(t, record["generation_id"], 64)
	for _, secret := range []string{"FILTER_SECRET", "DOCUMENT_SECRET", "gen-1", *page.NextCursor} {
		require.NotContains(t, output.String(), secret)
	}
	output.Reset()
	q.StartAfter = *page.NextCursor
	_, err = engine.ExecuteQueryPage(ctx, "db", q)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record))
	require.Equal(t, "exhausted", record["reason"])
	require.Equal(t, float64(0), record["returned"])
}

func TestQueryTerminationReasons(t *testing.T) {
	for _, tc := range []struct {
		err    error
		reason string
	}{{context.Canceled, "canceled"}, {context.DeadlineExceeded, "deadline_exceeded"}, {model.ErrInvalidQuery, "invalid_query"}, {model.ErrStaleCursor, "stale_cursor"}, {model.ErrQueryWorkLimit, "work_limit"}, {indexer.ErrNoMatchingIndex, "no_complete_index_plan"}, {indexer.ErrIndexNotReady, "index_unavailable"}, {errors.New("PRIVATE_ERROR"), "internal_error"}} {
		require.Equal(t, tc.reason, queryTermination(model.QueryPage{}, tc.err))
	}
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)
	source := &pageSource{fail: errors.New("PRIVATE_ERROR")}
	_, err := New(source, nil).ExecuteQueryPage(context.Background(), "db", model.Query{Collection: "items"})
	require.Error(t, err)
	require.False(t, strings.Contains(output.String(), "PRIVATE_ERROR"))
	var record map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record))
	require.Equal(t, "internal_error", record["reason"])
	require.Equal(t, float64(1), record["source_reads"])
	require.Equal(t, float64(0), record["returned"])
}
