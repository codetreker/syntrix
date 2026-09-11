package indexer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/indexer/config"
	"github.com/syntrixbase/syntrix/internal/indexer/store"
	"github.com/syntrixbase/syntrix/internal/puller"
)

func decodeIndexerLogs(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(logs.Bytes()))
	var entries []map[string]any
	for {
		var entry map[string]any
		err := decoder.Decode(&entry)
		if errors.Is(err, io.EOF) {
			return entries
		}
		require.NoError(t, err)
		entries = append(entries, entry)
	}
}

type failingObservationStore struct {
	store.Store
	failure error
}

func (s *failingObservationStore) ApplyDocumentProjection([]store.Projection, string) error {
	return s.failure
}

func TestProjectionFailureLogsIdentityAndBudgetWithoutDocumentValues(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	svc, err := NewService(config.Config{}, nil, logger)
	require.NoError(t, err)
	s := svc.(*service)
	defer s.Stop(context.Background())
	require.NoError(t, s.manager.LoadTemplatesFromBytes([]byte(bootstrapTemplates)))
	publishTestGeneration(t, s, "db", "users/a/docs")
	secret := "private-document-value"
	doc := types.NewStoredDoc("db", "users/a/docs", "private-document-id", map[string]any{"score": strings.Repeat(secret, 300)})
	evt := &ChangeEvent{Database: "db", FullDocument: &doc, EventID: "event-correlation", Backend: "capture-source"}
	err = s.ApplyEvent(context.Background(), evt, "uncommitted")
	require.ErrorIs(t, err, store.ErrWorkLimit)
	entries := decodeIndexerLogs(t, &logs)
	require.Len(t, entries, 1)
	entry := entries[0]
	require.Equal(t, "index projection failed", entry["msg"])
	require.Equal(t, "projection_limit", entry["category"])
	require.Equal(t, "event-correlation", entry["event_id"])
	require.Equal(t, "position_bytes", entry["limit_name"])
	require.Equal(t, float64(4096), entry["limit"])
	require.Len(t, entry["scope_id"], 64)
	require.Len(t, entry["document_id"], 64)
	require.NotEmpty(t, entry["template_fingerprint"])
	require.Equal(t, "test-generation", entry["generation"])
	require.NotContains(t, logs.String(), secret)
	require.NotContains(t, logs.String(), "private-document-id")
	// A storage error may carry values; its identity/category log must not repeat it.
	other := types.NewStoredDoc("db", "users/b/docs", "valid", map[string]any{"score": int64(1)})
	publishTestGeneration(t, s, "db", other.Collection)
	failure := errors.New("driver error with private payload: " + secret)
	s.store = &failingObservationStore{Store: s.store, failure: failure}
	require.ErrorIs(t, s.ApplyEvent(context.Background(), &ChangeEvent{Database: "db", FullDocument: &other, EventID: "failed-write"}, "uncommitted"), failure)
	s.failSubscription(failure)
	require.NotContains(t, logs.String(), secret)
	entries = decodeIndexerLogs(t, &logs)
	require.Equal(t, "storage_failure", entries[len(entries)-2]["category"])
	require.Equal(t, "subscription_failure", entries[len(entries)-1]["category"])
}

func TestBootstrapLogsWholeOperationCountsAndSanitizedFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "failure"}[fail], func(t *testing.T) {
			var logs bytes.Buffer
			p := &bootstrapPuller{marker: "opaque-bootstrap", stream: make(chan *puller.Event)}
			s := newBootstrapTestService(t, config.StorageModeMemory, p)
			s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
			defer s.Stop(context.Background())
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			source := bootstrapTestDocuments()
			request := BootstrapRequest{Databases: []string{"db", "empty"}, Scanner: source, Enumerator: source, WritesQuiesced: true}
			failure := errors.New("private upstream payload")
			if fail {
				request.ValidateInventory = func(context.Context) error { return failure }
			}
			err := s.Bootstrap(ctx, request)
			if fail {
				require.ErrorIs(t, err, failure)
			} else {
				require.NoError(t, err)
			}
			entries := decodeIndexerLogs(t, &logs)
			require.Len(t, entries, 4)
			states := []string{"validating", "scanning", "publishing", map[bool]string{false: "ready", true: "failed"}[fail]}
			for i, entry := range entries {
				require.Equal(t, states[i], entry["state"])
				require.Equal(t, entries[0]["operation_id"], entry["operation_id"])
				require.Equal(t, entries[0]["generation"], entry["generation"])
			}
			last := entries[len(entries)-1]
			require.NotEmpty(t, last["operation_id"])
			require.NotEmpty(t, last["generation"])
			require.Equal(t, float64(2), last["databases"])
			require.Equal(t, float64(3), last["collections_scanned"])
			require.Equal(t, float64(3), last["documents_scanned"])
			require.Equal(t, float64(3), last["documents_projected"])
			require.Equal(t, float64(3), last["postings"])
			require.NotContains(t, logs.String(), "private upstream payload")
			if fail {
				require.Equal(t, "publication_failure", last["category"])
			}
		})
	}
}
