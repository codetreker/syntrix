package replication

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	storagetypes "github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/indexer"
	querycore "github.com/syntrixbase/syntrix/internal/query/core"
	"github.com/syntrixbase/syntrix/pkg/model"
)

func TestDecodePullRequestRetainsTypedValuesAndModePresence(t *testing.T) {
	events := `{"collection":"users","source":{"version":1,"filters":[{"field":"score","op":"==","value":{"type":"int64","value":"9223372036854775807"}}]},"checkpoint":null,"limit":100}`
	request, err := DecodePullRequest(strings.NewReader(events))
	require.NoError(t, err)
	require.Equal(t, int64(9223372036854775807), request.Source.Filters[0].Value)
	require.Equal(t, "", request.Checkpoint)
	require.Equal(t, 100, request.Limit)
	require.Nil(t, request.RequestID)
	window := `{"collection":"users","source":{"version":1,"filters":[],"limit":2},"requestId":"read-1"}`
	request, err = DecodePullRequest(strings.NewReader(window))
	require.NoError(t, err)
	require.Equal(t, 2, *request.Source.Limit)
	require.Equal(t, "read-1", *request.RequestID)
	for _, field := range []string{`"checkpoint":null`, `"checkpoint":""`, `"limit":0`} {
		_, err = DecodePullRequest(strings.NewReader(strings.TrimSuffix(window, "}") + "," + field + "}"))
		require.ErrorIs(t, err, storagetypes.ErrInvalidReplicationSource)
	}
	for _, body := range []string{
		`{"collection":"users","source":null}`,
		`{"collection":"users","source":{"version":1,"filters":[]},"requestId":"r"}`,
		`{"collection":"users","source":{"version":1,"filters":[],"limit":1}}`,
		`{"collection":"users","source":{"version":2,"filters":[]}}`,
		`{"collection":"users","source":{"version":1,"filters":[]},"limit":1001}`,
		`{"collection":"users","source":{"version":1,"filters":[]},"collection":"other"}`,
		`{"collection":"users","source":{"version":1,"filters":[{"field":"x","op":"==","value":{"type":"int64","value":"1","value":"2"}}]}}`,
		`{"collection":"users","source":{"version":1,"filters":[]},"databaseIdentity":"internal"}`,
		`{"collection":"users","source":{"version":1,"filters":[]},"checkpoint":true}`,
	} {
		_, err := DecodePullRequest(strings.NewReader(body))
		require.ErrorIs(t, err, storagetypes.ErrInvalidReplicationSource, body)
	}
}

type failedReader struct{ cause error }

func (r failedReader) Read([]byte) (int, error) { return 0, r.cause }

func TestDecodePullRequestBudgetsAndLegacyFailures(t *testing.T) {
	for _, suffix := range []string{``, `,"checkpoint":null`, `,"checkpoint":"resume"`, `,"limit":0`} {
		request, err := DecodePullRequest(strings.NewReader(`{"collection":"users"` + suffix + `}`))
		require.NoError(t, err)
		require.Nil(t, request.Source)
	}
	for _, body := range []string{`[]`, `{"collection":null}`, `{"collection":3}`, `{"collection":"users","limit":null}`,
		`{"collection":"users","limit":1.2}`, `{"collection":"users","unknown":0}`, `{"collection":"users"} {}`,
		`{"collection":"\ud800"}`, `{"collection":"users","checkpoint":true}`, `{"collection":"users",`} {
		_, err := DecodePullRequest(strings.NewReader(body))
		require.Error(t, err, body)
		require.NotErrorIs(t, err, storagetypes.ErrInvalidReplicationSource, body)
		failure := ClassifyPullError(storage.ReplicationPullRequest{}, err)
		require.Equal(t, http.StatusBadRequest, failure.Status)
		require.Equal(t, "BAD_REQUEST", failure.Code)
		require.Equal(t, "Invalid pull request body", failure.Message)
	}
	_, err := DecodePullRequest(strings.NewReader(`{"collection":"users","checkpoint":0}`))
	var watch *storagetypes.WatchError
	require.ErrorAs(t, err, &watch)
	require.Equal(t, storagetypes.WatchHistoryUnavailable, watch.Code)
	_, err = DecodePullRequest(strings.NewReader(`{"collection":"users","checkpoint":"` + strings.Repeat("x", querycore.MaxPullCursorBytes+1) + `"}`))
	var tooLarge *http.MaxBytesError
	require.ErrorAs(t, err, &tooLarge)
	require.EqualValues(t, querycore.MaxPullCursorBytes, tooLarge.Limit)
	input := strings.Repeat("x", querycore.MaxPullRequestBytes+2)
	reader := strings.NewReader(input)
	_, err = DecodePullRequest(reader)
	require.ErrorAs(t, err, &tooLarge)
	require.EqualValues(t, querycore.MaxPullRequestBytes, tooLarge.Limit)
	require.Equal(t, 1, reader.Len(), "the decoder stops before reading an unbounded body")
	_, err = DecodePullRequest(failedReader{cause: io.ErrUnexpectedEOF})
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestClassifyPullErrorPreservesHTTPClassificationAndCause(t *testing.T) {
	limit := 3
	window := storage.ReplicationPullRequest{Source: &storage.ReplicationSource{Version: 1, Limit: &limit}}
	for _, tc := range []struct {
		cause  error
		status int
		code   string
	}{
		{storagetypes.ErrReplicationWindowIncomplete, 503, "REPLICATION_WINDOW_INCOMPLETE"},
		{indexer.ErrNoMatchingIndex, 400, "NO_MATCHING_INDEX"},
		{indexer.ErrIndexNotReady, 503, "INDEX_UNAVAILABLE"},
		{indexer.ErrIndexRebuilding, 503, "INDEX_UNAVAILABLE"},
		{storagetypes.ErrInvalidReplicationSource, 400, "INVALID_REPLICATION_SOURCE"},
		{context.Canceled, 499, ""},
		{context.DeadlineExceeded, 504, "DEADLINE_EXCEEDED"},
		{&http.MaxBytesError{Limit: 10}, 413, "REQUEST_TOO_LARGE"},
		{model.ErrQueryWorkLimit, 422, "REPLICATION_BUDGET_EXCEEDED"},
		{&storagetypes.WatchError{Code: storagetypes.WatchInvalidScope}, 400, "BAD_REQUEST"},
		{&storagetypes.WatchError{Code: storagetypes.WatchInvalidCheckpoint}, 400, "BAD_REQUEST"},
		{&storagetypes.WatchError{Code: storagetypes.WatchScopeMismatch}, 400, "BAD_REQUEST"},
		{&storagetypes.WatchError{Code: storagetypes.WatchSourceMismatch}, 409, "RESYNC_REQUIRED"},
		{&storagetypes.WatchError{Code: storagetypes.WatchHistoryUnavailable}, 409, "RESYNC_REQUIRED"},
		{&storagetypes.WatchError{Code: storagetypes.WatchPayloadUnavailable}, 409, "RESYNC_REQUIRED"},
		{&storagetypes.WatchError{Code: storagetypes.WatchUnsupported}, 501, "REPLICATION_UNSUPPORTED"},
		{&storagetypes.WatchError{Code: storagetypes.WatchSourceUnavailable}, 503, "REPLICATION_UNAVAILABLE"},
		{&storagetypes.WatchError{Code: storagetypes.WatchPermissionDenied}, 403, "FORBIDDEN"},
		{&storagetypes.WatchError{Code: storagetypes.WatchInvalidEvent}, 500, "INTERNAL_ERROR"},
		{errors.New("unexpected source failure"), 500, "INTERNAL_ERROR"},
	} {
		t.Run(tc.code+fmt.Sprint(tc.cause), func(t *testing.T) {
			cause := fmt.Errorf("source: %w", tc.cause)
			failure := ClassifyPullError(storage.ReplicationPullRequest{}, cause)
			require.Equal(t, tc.status, failure.Status)
			require.Equal(t, tc.code, failure.Code)
			require.Same(t, cause, failure.Cause)
			require.ErrorIs(t, failure, tc.cause)
			require.NotContains(t, failure.Error(), "source:")
		})
	}
	failure := ClassifyPullError(window, model.ErrQueryWorkLimit)
	require.Equal(t, "QUERY_WORK_LIMIT", failure.Code)
	require.Equal(t, "Query exceeds work or size limits", failure.Message)
	classified := Failure{Status: 409, Code: "DATABASE_IDENTITY_MISMATCH", Message: "identity changed", Cause: io.ErrClosedPipe}
	for _, err := range []error{classified, &classified, fmt.Errorf("wrapped: %w", &classified)} {
		require.Equal(t, classified, ClassifyPullError(storage.ReplicationPullRequest{}, err))
	}
}
