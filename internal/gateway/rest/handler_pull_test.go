package rest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	databasecore "github.com/syntrixbase/syntrix/internal/core/database"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	storagetypes "github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/ctxkeys"
	querycore "github.com/syntrixbase/syntrix/internal/query/core"
	"github.com/syntrixbase/syntrix/internal/query/wire"
	"github.com/syntrixbase/syntrix/pkg/model"
)

type pullRecorder struct {
	*httptest.ResponseRecorder
	deadline    time.Time
	deadlineErr error
	writeErr    error
}

func newPullRecorder() *pullRecorder {
	return &pullRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (w *pullRecorder) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	return w.deadlineErr
}

func (w *pullRecorder) Write(data []byte) (int, error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return w.ResponseRecorder.Write(data)
}

func TestPullResponseDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	service := new(MockQueryService)
	service.On("Pull", mock.Anything, "friendly-name", mock.Anything).
		Return(&storage.ReplicationPullResponse{Checkpoint: "next", CaughtUp: true}, nil).Twice()
	rr := newPullRecorder()
	(&Handler{engine: service}).handlePull(rr, pullRequest(ctx, `{"collection":"users"}`))
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, deadline.Add(pullResponseWriteTimeout), rr.deadline)
	rr = newPullRecorder()
	rr.writeErr = io.ErrClosedPipe
	(&Handler{engine: service}).handlePull(rr, pullRequest(ctx, `{"collection":"users"}`))
	require.Empty(t, rr.Body.String())
	service.AssertExpectations(t)
}

func TestPullRejectsUnavailableResponseDeadline(t *testing.T) {
	for _, writer := range []http.ResponseWriter{
		httptest.NewRecorder(),
		&pullRecorder{ResponseRecorder: httptest.NewRecorder(), deadlineErr: io.ErrClosedPipe},
	} {
		service := new(MockQueryService)
		(&Handler{engine: service}).handlePull(writer, pullRequest(context.Background(), `{"collection":"users"}`))
		var rr *httptest.ResponseRecorder
		switch w := writer.(type) {
		case *httptest.ResponseRecorder:
			rr = w
		case *pullRecorder:
			rr = w.ResponseRecorder
		}
		require.Equal(t, http.StatusInternalServerError, rr.Code)
		require.Contains(t, rr.Body.String(), "Cannot establish replication response deadline")
		service.AssertNotCalled(t, "Pull", mock.Anything, mock.Anything, mock.Anything)
	}
}

func TestPullOverridesHTTPWriteTimeout(t *testing.T) {
	service := new(MockQueryService)
	service.On("Pull", mock.Anything, "friendly-name", mock.Anything).Run(func(args mock.Arguments) {
		ctx := args.Get(0).(context.Context)
		select {
		case <-time.After(150 * time.Millisecond):
		case <-ctx.Done():
			t.Error(ctx.Err())
		}
	}).Return(&storage.ReplicationPullResponse{Checkpoint: "next", CaughtUp: true}, nil).Once()
	handler := &Handler{engine: service}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("database", "friendly-name")
		r = r.WithContext(databasecore.WithDatabase(r.Context(), &databasecore.Database{ID: "canonical-id"}))
		handler.handlePull(w, r)
	}))
	srv.Config.WriteTimeout = 50 * time.Millisecond
	srv.Start()
	defer srv.Close()
	client := srv.Client()
	client.Timeout = 3 * time.Second
	response, err := client.Post(srv.URL, "application/json", strings.NewReader(`{"collection":"users"}`))
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	var page ReplicaPullResponse
	require.NoError(t, json.NewDecoder(response.Body).Decode(&page))
	require.Equal(t, "next", page.Checkpoint)
	require.True(t, page.CaughtUp)
	service.AssertExpectations(t)
}

func pullRequest(ctx context.Context, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/replication/v1/databases/friendly-name/pull", strings.NewReader(body))
	req.SetPathValue("database", "friendly-name")
	ctx = databasecore.WithDatabase(ctx, &databasecore.Database{ID: "canonical-id"})
	return req.WithContext(ctx)
}

func pullCheckpoint(database, collection string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"version":3,"database":%q,"databaseIdentity":"canonical-id","collection":%q,"phase":"changes","position":"native-position"}`, database, collection)))
}

func TestHandlePullTypedPage(t *testing.T) {
	for _, checkpoint := range []string{"", `,"checkpoint":null`, `,"checkpoint":""`, `,"checkpoint":"` + pullCheckpoint("friendly-name", "users") + `"`} {
		t.Run(checkpoint, func(t *testing.T) {
			service := new(MockQueryService)
			handler := &Handler{engine: service}
			documents := []model.Document{
				{"id": "alice", "collection": "users", "version": int64(math.MaxInt64), "nested": map[string]any{"count": int64(9007199254740993)}},
				{"id": "bob", "collection": "users", "deleted": true},
			}
			service.On("Pull", mock.Anything, "friendly-name", mock.MatchedBy(func(req storage.ReplicationPullRequest) bool {
				return req.Collection == "users" && req.Limit == 100 && req.DatabaseIdentity == "canonical-id"
			})).Run(func(args mock.Arguments) {
				ctx := args.Get(0).(context.Context)
				deadline, ok := ctx.Deadline()
				require.True(t, ok)
				require.WithinDuration(t, time.Now().Add(querycore.PullHardTimeout), deadline, time.Second)
				assert.Equal(t, "request-pull", ctx.Value(ctxkeys.KeyRequestID))
			}).Return(&storage.ReplicationPullResponse{Documents: documents, Checkpoint: "next-position", CaughtUp: true}, nil).Once()
			ctx := context.WithValue(context.Background(), ctxkeys.KeyRequestID, "request-pull")
			rr := newPullRecorder()
			handler.handlePull(rr, pullRequest(ctx, `{"collection":"users"`+checkpoint+`}`))
			require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
			require.WithinDuration(t, time.Now().Add(querycore.PullHardTimeout+pullResponseWriteTimeout), rr.deadline, time.Second)
			var response ReplicaPullResponse
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response))
			require.Len(t, response.Documents, 2)
			for i, raw := range response.Documents {
				value, err := model.DecodeTypedValue(raw)
				require.NoError(t, err)
				assert.Equal(t, map[string]any(documents[i]), value)
			}
			assert.Equal(t, "next-position", response.Checkpoint)
			assert.True(t, response.CaughtUp)
			service.AssertExpectations(t)
		})
	}
}

func TestHandlePullRejectsBeforeSource(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"missing body", "", 400},
		{"null body", `null`, 400},
		{"array", `[]`, 400},
		{"invalid JSON", `{"collection":`, 400},
		{"missing collection", `{}`, 400},
		{"null collection", `{"collection":null}`, 400},
		{"invalid collection", `{"collection":"users/alice"}`, 400},
		{"invalid unicode", `{"collection":"users\ud800"}`, 400},
		{"negative limit", `{"collection":"users","limit":-1}`, 400},
		{"oversized limit", `{"collection":"users","limit":1001}`, 400},
		{"fractional limit", `{"collection":"users","limit":1.5}`, 400},
		{"null limit", `{"collection":"users","limit":null}`, 400},
		{"string limit", `{"collection":"users","limit":"1"}`, 400},
		{"invalid checkpoint", `{"collection":"users","checkpoint":"garbage"}`, 400},
		{"boolean checkpoint", `{"collection":"users","checkpoint":true}`, 400},
		{"object checkpoint", `{"collection":"users","checkpoint":{}}`, 400},
		{"different database", `{"collection":"users","checkpoint":"` + pullCheckpoint("other", "users") + `"}`, 400},
		{"different collection", `{"collection":"users","checkpoint":"` + pullCheckpoint("friendly-name", "tasks") + `"}`, 400},
		{"legacy number", `{"collection":"users","checkpoint":0}`, 409},
		{"legacy string", `{"collection":"users","checkpoint":"1726000000000"}`, 409},
		{"duplicate", `{"collection":"users","collection":"tasks"}`, 400},
		{"escaped duplicate", `{"collection":"users","\u0063ollection":"tasks"}`, 400},
		{"unknown field", `{"collection":"users","database":"other"}`, 400},
		{"internal identity field", `{"collection":"users","databaseIdentity":"canonical-id"}`, 400},
		{"case alias", `{"Collection":"users"}`, 400},
		{"trailing object", `{"collection":"users"}{}`, 400},
		{"trailing invalid", `{"collection":"users"}!`, 400},
		{"malformed legacy", `{"collection":"users","checkpoint":0}x`, 400},
		{"cursor budget", `{"collection":"users","checkpoint":"` + strings.Repeat("x", querycore.MaxPullCursorBytes+1) + `"}`, 413},
		{"body budget", strings.Repeat(" ", querycore.MaxPullRequestBytes) + `{"collection":"users"}`, 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := new(MockQueryService)
			rr := newPullRecorder()
			(&Handler{engine: service}).handlePull(rr, pullRequest(context.Background(), tc.body))
			assert.Equal(t, tc.status, rr.Code, rr.Body.String())
			service.AssertNotCalled(t, "Pull", mock.Anything, mock.Anything, mock.Anything)
			if tc.status == 409 {
				assert.Contains(t, rr.Body.String(), `"code":"RESYNC_REQUIRED"`)
			}
		})
	}
}

func TestHandlePullFailureMapping(t *testing.T) {
	for _, tc := range []struct {
		code   storagetypes.WatchErrorCode
		status int
	}{
		{storagetypes.WatchInvalidScope, 400}, {storagetypes.WatchInvalidCheckpoint, 400}, {storagetypes.WatchScopeMismatch, 400},
		{storagetypes.WatchSourceMismatch, 409}, {storagetypes.WatchHistoryUnavailable, 409},
		{storagetypes.WatchPayloadUnavailable, 409}, {storagetypes.WatchUnsupported, 501},
		{storagetypes.WatchSourceUnavailable, 503}, {storagetypes.WatchPermissionDenied, 403},
		{storagetypes.WatchInvalidEvent, 500},
	} {
		t.Run(string(tc.code), func(t *testing.T) {
			service := new(MockQueryService)
			service.On("Pull", mock.Anything, "friendly-name", mock.Anything).Return(nil, &storagetypes.WatchError{Code: tc.code, Cause: errors.New("private-cursor-and-payload")}).Once()
			rr := newPullRecorder()
			(&Handler{engine: service}).handlePull(rr, pullRequest(context.Background(), `{"collection":"users"}`))
			assert.Equal(t, tc.status, rr.Code, rr.Body.String())
			assert.NotContains(t, rr.Body.String(), "private-cursor-and-payload")
			assert.NotContains(t, rr.Body.String(), "checkpoint\":")
			service.AssertExpectations(t)
		})
	}
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{"internal", errors.New("private-cursor-and-payload"), 500},
		{"budget", model.ErrQueryWorkLimit, 422},
		{"canceled", context.Canceled, 499}, {"deadline", context.DeadlineExceeded, 504},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := new(MockQueryService)
			service.On("Pull", mock.Anything, "friendly-name", mock.Anything).Return(nil, tc.err).Once()
			rr := newPullRecorder()
			(&Handler{engine: service}).handlePull(rr, pullRequest(context.Background(), `{"collection":"users"}`))
			assert.Equal(t, tc.status, rr.Code)
			assert.NotContains(t, rr.Body.String(), "private-cursor-and-payload")
			service.AssertExpectations(t)
		})
	}
}

func TestHandlePullRejectsReassignedDatabaseAlias(t *testing.T) {
	service := new(MockQueryService)
	body := `{"collection":"users","checkpoint":"` + pullCheckpoint("friendly-name", "users") + `"}`
	req := pullRequest(context.Background(), body)
	req = req.WithContext(databasecore.WithDatabase(req.Context(), &databasecore.Database{ID: "replacement-id"}))
	rr := newPullRecorder()
	(&Handler{engine: service}).handlePull(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	service.AssertNotCalled(t, "Pull", mock.Anything, mock.Anything, mock.Anything)
}

func TestHandlePullDoesNotReturnPartialPage(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response *storage.ReplicationPullResponse
		status   int
	}{
		{"oversized", &storage.ReplicationPullResponse{Documents: []model.Document{{"id": "a", "collection": "users"}, {"id": "b", "collection": "users", "data": strings.Repeat("x", wire.MaxPageBytes)}}, Checkpoint: "unsafe-progress"}, 422},
		{"invalid value", &storage.ReplicationPullResponse{Documents: []model.Document{{"id": "a", "collection": "users"}, {"id": "b", "collection": "users", "data": math.NaN()}}, Checkpoint: "unsafe-progress"}, 500},
		{"nil response", nil, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := new(MockQueryService)
			service.On("Pull", mock.Anything, "friendly-name", mock.Anything).Return(tc.response, nil).Once()
			rr := newPullRecorder()
			(&Handler{engine: service}).handlePull(rr, pullRequest(context.Background(), `{"collection":"users"}`))
			assert.Equal(t, tc.status, rr.Code, rr.Body.String())
			assert.NotContains(t, rr.Body.String(), "unsafe-progress")
			assert.NotContains(t, rr.Body.String(), `"documents"`)
			service.AssertExpectations(t)
		})
	}
}

func TestHandlePullLargeLegalPage(t *testing.T) {
	service := new(MockQueryService)
	documents := make([]model.Document, 6)
	for i := range documents {
		documents[i] = model.Document{"id": fmt.Sprintf("doc-%d", i), "collection": "users", "data": strings.Repeat("x", 900<<10)}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	service.On("Pull", mock.Anything, "friendly-name", storage.ReplicationPullRequest{Collection: "users", Limit: 1000, DatabaseIdentity: "canonical-id"}).Run(func(args mock.Arguments) {
		actual, ok := args.Get(0).(context.Context).Deadline()
		require.True(t, ok)
		assert.Equal(t, deadline, actual)
	}).Return(&storage.ReplicationPullResponse{Documents: documents, Checkpoint: "next-position"}, nil).Once()
	rr := newPullRecorder()
	(&Handler{engine: service}).handlePull(rr, pullRequest(ctx, `{"collection":"users","limit":1000}`))
	require.Equal(t, 200, rr.Code)
	assert.Greater(t, rr.Body.Len(), 4<<20)
	var response ReplicaPullResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response))
	require.Len(t, response.Documents, 6)
	for i, raw := range response.Documents {
		decoded, err := model.DecodeTypedValue(raw)
		require.NoError(t, err)
		assert.Equal(t, map[string]any(documents[i]), decoded)
	}
	service.AssertExpectations(t)
}

func TestHandlePullCancellationClosesRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	service := new(MockQueryService)
	var sourceContext context.Context
	service.On("Pull", mock.Anything, "friendly-name", mock.Anything).Run(func(args mock.Arguments) {
		sourceContext = args.Get(0).(context.Context)
		cancel()
	}).Return(&storage.ReplicationPullResponse{Checkpoint: "unsafe-progress"}, nil).Once()
	rr := newPullRecorder()
	(&Handler{engine: service}).handlePull(rr, pullRequest(ctx, `{"collection":"users"}`))
	assert.Equal(t, 499, rr.Code)
	assert.ErrorIs(t, sourceContext.Err(), context.Canceled)
	assert.Empty(t, rr.Body.String())
	service.AssertExpectations(t)
}
