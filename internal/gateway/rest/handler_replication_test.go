package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/syntrixbase/syntrix/internal/core/storage"
	querycore "github.com/syntrixbase/syntrix/internal/query/core"
	"github.com/syntrixbase/syntrix/pkg/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestHandlePush(t *testing.T) {
	mockService := new(MockQueryService)
	server := createTestServer(mockService, nil, nil)

	resp := &storage.ReplicationPushResponse{
		Conflicts: []storage.ReplicationPushConflict{},
	}

	mockService.On("Push", mock.Anything, "default", mock.AnythingOfType("types.ReplicationPushRequest")).Return(resp, nil)

	pushReq := ReplicaPushRequest{
		Collection: "rooms/room-1/messages",
		Changes: []ReplicaChange{
			{
				Action: "update", Doc: model.Document{"id": "msg-1", "name": "Bob", "version": int64(1)},
			},
		},
	}
	body, _ := json.Marshal(pushReq)
	req, _ := http.NewRequest("POST", "/replication/v1/databases/default/push", bytes.NewBuffer(body))
	rr := httptest.NewRecorder()

	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func typedPushBody(t *testing.T, collection string, changes ...ReplicaChange) string {
	t.Helper()
	body, err := json.Marshal(ReplicaPushRequest{Collection: collection, Changes: changes})
	require.NoError(t, err)
	return string(body)
}

func pushCurrent(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	value, err := model.DecodeTypedValue(raw)
	require.NoError(t, err)
	doc, ok := value.(map[string]any)
	require.True(t, ok)
	return doc
}

func TestHandlePushRejectsMalformedLaterChange(t *testing.T) {
	for _, invalid := range []string{
		`{"document":{"type":"object","value":{"id":{"type":"string","value":"bad"}}}}`,
		`{"action":"","document":{"type":"object","value":{"id":{"type":"string","value":"bad"}}}}`,
		`{"action":"upsert","document":{"type":"object","value":{"id":{"type":"string","value":"bad"}}}}`,
		`{"action":"update","document":{"type":"object","value":{"id":{"type":"string","value":"bad"},"value":{"type":"string","value":"\ud800"}}}}`,
	} {
		t.Run(invalid, func(t *testing.T) {
			service := new(MockQueryService)
			first, err := json.Marshal(ReplicaChange{Action: "create", Doc: model.Document{"id": "first"}})
			require.NoError(t, err)
			body := `{"collection":"users","changes":[` + string(first) + `,` + invalid + `]}`
			req := httptest.NewRequest(http.MethodPost, "/replication/v1/databases/default/push", bytes.NewBufferString(body))
			rr := httptest.NewRecorder()
			createTestServer(service, nil, nil).ServeHTTP(rr, req)
			require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
			service.AssertNotCalled(t, "Push", mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

func TestHandlePushStructuredConflicts(t *testing.T) {
	service := new(MockQueryService)
	tombstone := storage.NewStoredDoc("default", "users", "dead", nil)
	tombstone.Deleted, tombstone.Version = true, 2
	live := storage.NewStoredDoc("default", "users", "alice", map[string]any{"id": "forged", "deleted": true, "nested": map[string]any{"min": int64(math.MinInt64), "values": []any{int64(math.MaxInt64), float64(1)}}})
	live.Version = 2
	service.On("Push", mock.Anything, "default", mock.Anything).Return(&storage.ReplicationPushResponse{
		Conflicts: []storage.ReplicationPushConflict{
			{ChangeIndex: 0, ID: "gone", Reason: storage.PushMissing},
			{ChangeIndex: 1, ID: "dead", Reason: storage.PushTombstoned, Current: &tombstone},
			{ChangeIndex: 2, ID: "alice", Reason: storage.PushVersionMismatch, Current: &live},
		},
	}, nil).Once()
	body := typedPushBody(t, "users",
		ReplicaChange{Action: "update", Doc: model.Document{"id": "gone", "version": int64(1)}},
		ReplicaChange{Action: "delete", Doc: model.Document{"id": "dead", "version": int64(1)}},
		ReplicaChange{Action: "update", Doc: model.Document{"id": "alice", "version": int64(1)}},
	)
	req := httptest.NewRequest(http.MethodPost, "/replication/v1/databases/default/push", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	createTestServer(service, nil, nil).ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var response ReplicaPushResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response))
	require.Len(t, response.Conflicts, 3)
	require.Equal(t, 0, response.Conflicts[0].ChangeIndex)
	require.JSONEq(t, `null`, string(response.Conflicts[0].Current))
	require.Contains(t, rr.Body.String(), `"current":null`)
	require.Equal(t, 1, response.Conflicts[1].ChangeIndex)
	require.Equal(t, true, pushCurrent(t, response.Conflicts[1].Current)["deleted"])
	require.Equal(t, int64(2), pushCurrent(t, response.Conflicts[1].Current)["version"])
	require.Equal(t, "alice", pushCurrent(t, response.Conflicts[2].Current)["id"])
	require.NotContains(t, pushCurrent(t, response.Conflicts[2].Current), "deleted")
	require.Equal(t, live.Data["nested"], pushCurrent(t, response.Conflicts[2].Current)["nested"])
	require.Equal(t, tombstone.CreatedAt, pushCurrent(t, response.Conflicts[1].Current)["createdAt"])
	require.Equal(t, tombstone.UpdatedAt, pushCurrent(t, response.Conflicts[1].Current)["updatedAt"])
	require.Contains(t, string(response.Conflicts[2].Current), `"type":"int64","value":"9223372036854775807"`)
	require.Contains(t, string(response.Conflicts[2].Current), `"type":"float64","value":1`)
	service.AssertExpectations(t)
}

func TestHandlePushRejectsInvalidResult(t *testing.T) {
	current := storage.NewStoredDoc("default", "users", "alice", map[string]any{"unsupported": make(chan int)})
	current.Version = 2
	for _, result := range []*storage.ReplicationPushResponse{
		nil,
		{Conflicts: []storage.ReplicationPushConflict{{ChangeIndex: 1, ID: "alice", Reason: storage.PushMissing}}},
		{Conflicts: []storage.ReplicationPushConflict{{ChangeIndex: 0, ID: "alice", Reason: storage.PushVersionMismatch, Current: &current}}},
	} {
		service := new(MockQueryService)
		service.On("Push", mock.Anything, "default", mock.Anything).Return(result, nil).Once()
		request := httptest.NewRequest(http.MethodPost, "/replication/v1/databases/default/push", bytes.NewBufferString(typedPushBody(t, "users", ReplicaChange{Action: "update", Doc: model.Document{"id": "alice", "version": int64(1)}})))
		rr := httptest.NewRecorder()
		createTestServer(service, nil, nil).ServeHTTP(rr, request)
		require.Equal(t, http.StatusInternalServerError, rr.Code, rr.Body.String())
		service.AssertExpectations(t)
	}
}

func TestHandlePushEncodeFailureDoesNotWritePartialResponse(t *testing.T) {
	valid := storage.NewStoredDoc("default", "users", "valid", map[string]any{"count": int64(3)})
	valid.Version = 2
	invalid := storage.NewStoredDoc("default", "users", "invalid", map[string]any{"number": math.Inf(1)})
	invalid.Version = 2
	service := new(MockQueryService)
	service.On("Push", mock.Anything, "default", mock.Anything).Return(&storage.ReplicationPushResponse{
		Conflicts: []storage.ReplicationPushConflict{
			{ChangeIndex: 0, ID: "valid", Reason: storage.PushVersionMismatch, Current: &valid},
			{ChangeIndex: 1, ID: "invalid", Reason: storage.PushVersionMismatch, Current: &invalid},
		},
	}, nil).Once()
	body := typedPushBody(t, "users",
		ReplicaChange{Action: "update", Doc: model.Document{"id": "valid", "version": int64(1)}},
		ReplicaChange{Action: "update", Doc: model.Document{"id": "invalid", "version": int64(1)}},
	)
	rr := httptest.NewRecorder()
	createTestServer(service, nil, nil).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/replication/v1/databases/default/push", bytes.NewBufferString(body)))
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	require.True(t, json.Valid(rr.Body.Bytes()))
	require.NotContains(t, rr.Body.String(), `"conflicts"`)
	service.AssertExpectations(t)
}

func TestHandlePushRejectsInvalidTypedDocument(t *testing.T) {
	for _, document := range []string{
		`null`, `{"id":"legacy"}`, `{"type":"null"}`, `{"type":"array","value":[]}`,
		`{"type":"object","value":{"id":{"type":"string","value":"bad"},"value":{"type":"unknown","value":1}}}`,
		`{"type":"object","value":{"id":{"type":"string","value":"bad"},"value":{"type":"string","value":"\ud800"}}}`,
	} {
		t.Run(document, func(t *testing.T) {
			service := new(MockQueryService)
			first, err := json.Marshal(ReplicaChange{Action: "create", Doc: model.Document{"id": "valid"}})
			require.NoError(t, err)
			body := `{"collection":"users","changes":[` + string(first) + `,{"action":"update","document":` + document + `}]}`
			rr := httptest.NewRecorder()
			createTestServer(service, nil, nil).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/replication/v1/databases/default/push", bytes.NewBufferString(body)))
			require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
			service.AssertNotCalled(t, "Push", mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

func TestHandlePushQueryValidationAndWriteErrors(t *testing.T) {
	for _, queryErr := range []error{model.ErrInvalidQuery, model.ErrQueryWorkLimit, nil} {
		service := new(MockQueryService)
		service.On("Push", mock.Anything, "default", mock.Anything).Return(&storage.ReplicationPushResponse{}, queryErr).Once()
		request := httptest.NewRequest(http.MethodPost, "/replication/v1/databases/default/push", bytes.NewBufferString(typedPushBody(t, "users", ReplicaChange{Action: "update", Doc: model.Document{"id": "alice"}})))
		if queryErr != nil {
			rr := httptest.NewRecorder()
			createTestServer(service, nil, nil).ServeHTTP(rr, request)
			if queryErr == model.ErrQueryWorkLimit {
				require.Equal(t, http.StatusUnprocessableEntity, rr.Code)
				require.Contains(t, rr.Body.String(), "REPLICATION_BUDGET_EXCEEDED")
			} else {
				require.Equal(t, http.StatusBadRequest, rr.Code)
			}
		} else {
			rr := newPullRecorder()
			rr.writeErr = io.ErrClosedPipe
			createTestServer(service, nil, nil).ServeHTTP(rr, request)
			require.Empty(t, rr.Body.String())
		}
		service.AssertExpectations(t)
	}
}

func TestHandlePush_VersionPrecondition(t *testing.T) {
	tests := []struct {
		name    string
		action  string
		version *int64
	}{
		{name: "omitted", action: "update"},
		{name: "explicit zero", action: "update", version: replicationVersion(0)},
		{name: "create version one", action: "create", version: replicationVersion(1)},
		{name: "beyond float precision", action: "update", version: replicationVersion(9007199254740993)},
		{name: "int64 maximum", action: "delete", version: replicationVersion(math.MaxInt64)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service := new(MockQueryService)
			var captured storage.ReplicationPushRequest
			service.On("Push", mock.Anything, "default", mock.Anything).
				Run(func(args mock.Arguments) { captured = args.Get(2).(storage.ReplicationPushRequest) }).
				Return(&storage.ReplicationPushResponse{}, nil).Once()
			data := model.Document{
				"id": "msg-1", "count": int64(math.MaxInt64), "price": float64(1),
				"nested":  map[string]any{"number": int64(math.MinInt64)},
				"numbers": []any{int64(1), float64(1)}, "Version": int64(7),
				"collection": "forged", "createdAt": int64(-1), "updatedAt": int64(-1), "deleted": true,
			}
			if tc.version != nil {
				data["version"] = *tc.version
			}
			body := typedPushBody(t, "rooms/room-1/messages", ReplicaChange{Action: tc.action, Doc: data})
			req := httptest.NewRequest(http.MethodPost, "/replication/v1/databases/default/push", bytes.NewBufferString(body))
			rr := httptest.NewRecorder()
			createTestServer(service, nil, nil).ServeHTTP(rr, req)
			require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
			require.Len(t, captured.Changes, 1)
			assert.Equal(t, tc.version, captured.Changes[0].BaseVersion)
			assert.Equal(t, storage.PushAction(tc.action), captured.Changes[0].Action)
			doc := captured.Changes[0].Doc
			require.NotNil(t, doc)
			assert.Equal(t, int64(1), doc.Version)
			assert.Equal(t, "default", doc.Database)
			assert.Equal(t, "rooms/room-1/messages", doc.Collection)
			assert.Equal(t, "rooms/room-1/messages/msg-1", doc.Fullpath)
			assert.Equal(t, tc.action == "delete", doc.Deleted)
			assert.Positive(t, doc.CreatedAt)
			assert.Positive(t, doc.UpdatedAt)
			assert.Equal(t, "msg-1", doc.Data["id"])
			assert.Equal(t, int64(math.MaxInt64), doc.Data["count"])
			assert.Equal(t, float64(1), doc.Data["price"])
			assert.Equal(t, map[string]any{"number": int64(math.MinInt64)}, doc.Data["nested"])
			assert.Equal(t, []any{int64(1), float64(1)}, doc.Data["numbers"])
			assert.Equal(t, int64(7), doc.Data["Version"])
			for _, field := range []string{"version", "collection", "createdAt", "updatedAt", "deleted"} {
				assert.NotContains(t, doc.Data, field)
			}
			service.AssertExpectations(t)
		})
	}
}

func TestHandlePush_InvalidVersion(t *testing.T) {
	for _, version := range []string{
		`{"type":"null"}`, `{"type":"string","value":"1"}`, `{"type":"bool","value":true}`,
		`{"type":"int64","value":"-1"}`, `{"type":"float64","value":1.5}`, `{"type":"float64","value":1}`,
		`{"type":"int64","value":"9223372036854775808"}`, `{"type":"int64","value":"-0"}`,
		`{"type":"array","value":[]}`, `{"type":"object","value":{}}`,
	} {
		for _, second := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/second=%t", version, second), func(t *testing.T) {
				service := new(MockQueryService)
				changes := `{"action":"update","document":{"type":"object","value":{"id":{"type":"string","value":"invalid"},"version":` + version + `}}}`
				if second {
					first, err := json.Marshal(ReplicaChange{Action: "update", Doc: model.Document{"id": "valid", "version": int64(1)}})
					require.NoError(t, err)
					changes = string(first) + `,` + changes
				}
				body := `{"collection":"rooms","changes":[` + changes + `]}`
				req := httptest.NewRequest(http.MethodPost, "/replication/v1/databases/default/push", bytes.NewBufferString(body))
				rr := httptest.NewRecorder()
				createTestServer(service, nil, nil).ServeHTTP(rr, req)
				require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
				service.AssertNotCalled(t, "Push", mock.Anything, mock.Anything, mock.Anything)
			})
		}
	}
}

func replicationVersion(version int64) *int64 {
	return &version
}

func TestReplicaChange_UnmarshalJSONReuse(t *testing.T) {
	encoded, err := json.Marshal(ReplicaChange{Action: "delete", Doc: model.Document{"id": "old", "version": int64(9007199254740993)}})
	require.NoError(t, err)
	var change ReplicaChange
	require.NoError(t, json.Unmarshal(encoded, &change))
	original := change
	for _, invalid := range []string{
		`[]`, `{}`, `null`, `{"document":null}`,
		`{"action":"update","document":{"type":"null"}}`,
		`{"action":"update","document":{"type":"array","value":[]}}`,
		`{"action":"update","document":{"type":"bool","value":true}}`,
		`{"action":"update","document":{"id":"legacy"}}`,
		`{"action":true,"document":{"type":"object","value":{}}}`,
		`{"action":"update","document":{"type":"object","value":{"count":{"type":"float64","value":1e1000}}}}`,
		`{"action":"update","document":{"type":"object","value":{"count":{"type":"unknown","value":1}}}}`,
	} {
		require.Error(t, json.Unmarshal([]byte(invalid), &change), invalid)
		assert.Equal(t, original, change)
	}
	encoded, err = json.Marshal(ReplicaChange{Doc: model.Document{"id": "new", "count": int64(2)}})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encoded, &change))
	assert.Empty(t, change.Action)
	assert.Nil(t, change.BaseVersion)
	assert.Equal(t, model.Document{"id": "new", "count": int64(2)}, change.Doc)
}

func TestReplicaChange_BaseVersionIsInternal(t *testing.T) {
	change := ReplicaChange{Doc: model.Document{"id": "doc"}, BaseVersion: replicationVersion(7)}
	encoded, err := json.Marshal(change)
	require.NoError(t, err)
	assert.JSONEq(t, `{"action":"","document":{"type":"object","value":{"id":{"type":"string","value":"doc"}}}}`, string(encoded))
	require.NoError(t, json.Unmarshal([]byte(`{"BaseVersion":7,"document":{"type":"object","value":{"id":{"type":"string","value":"doc"}}}}`), &change))
	assert.Nil(t, change.BaseVersion)
	_, err = json.Marshal(ReplicaChange{Doc: model.Document{"bad": make(chan int)}})
	require.Error(t, err)
}

type replicationPushStore struct {
	storage.DocumentStore
	live      *storage.StoredDoc
	reads     int
	mutations int
	action    string
	database  string
	path      string
	data      map[string]interface{}
	predicate model.Filters
}

func (s *replicationPushStore) Get(_ context.Context, database, path string, _ ...storage.ReadOptions) (*storage.StoredDoc, error) {
	s.reads++
	s.database, s.path = database, path
	return s.live, nil
}

func (s *replicationPushStore) Update(_ context.Context, database, path string, data map[string]interface{}, pred model.Filters) error {
	s.mutations++
	s.action, s.database, s.path = "update", database, path
	s.data, s.predicate = data, pred
	return nil
}

func (s *replicationPushStore) Delete(_ context.Context, database, path string, pred model.Filters) error {
	s.mutations++
	s.action, s.database, s.path = "delete", database, path
	s.predicate = pred
	return nil
}

func TestHandlePush_QueryEngineVersionPrecondition(t *testing.T) {
	for _, action := range []string{"update", "delete"} {
		for _, tc := range []struct {
			name     string
			version  *int64
			conflict bool
			pred     model.Filters
		}{
			{name: "stale", version: replicationVersion(4), conflict: true},
			{name: "matching", version: replicationVersion(5), pred: model.Filters{{Field: "version", Op: model.OpEq, Value: int64(5)}}},
			{name: "omitted", pred: model.Filters{}},
		} {
			t.Run(action+"/"+tc.name, func(t *testing.T) {
				live := storage.NewStoredDoc("default", "rooms", "doc", map[string]interface{}{"name": "current"})
				live.Version = 5
				store := &replicationPushStore{live: &live}
				server := createTestServer(querycore.New(store, nil), nil, nil)
				doc := model.Document{"id": "doc", "name": "incoming"}
				if tc.version != nil {
					doc["version"] = *tc.version
				}
				body := typedPushBody(t, "rooms", ReplicaChange{Action: action, Doc: doc})
				req := httptest.NewRequest(http.MethodPost, "/replication/v1/databases/default/push", bytes.NewBufferString(body))
				rr := httptest.NewRecorder()
				server.ServeHTTP(rr, req)

				require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
				assert.Equal(t, 1, store.reads)
				assert.Equal(t, "default", store.database)
				assert.Equal(t, "rooms/doc", store.path)
				var response ReplicaPushResponse
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response))
				if tc.conflict {
					assert.Zero(t, store.mutations)
					require.Len(t, response.Conflicts, 1)
					assert.Equal(t, "doc", response.Conflicts[0].ID)
					assert.Equal(t, "current", pushCurrent(t, response.Conflicts[0].Current)["name"])
					assert.Equal(t, int64(5), pushCurrent(t, response.Conflicts[0].Current)["version"])
					return
				}
				assert.Empty(t, response.Conflicts)
				assert.Equal(t, 1, store.mutations)
				assert.Equal(t, action, store.action)
				assert.Equal(t, tc.pred, store.predicate)
				if action == "update" {
					assert.Equal(t, map[string]interface{}{"id": "doc", "name": "incoming"}, store.data)
				}
			})
		}
	}
}

func TestHandlePush_InvalidBody(t *testing.T) {
	mockService := new(MockQueryService)
	server := createTestServer(mockService, nil, nil)

	req, _ := http.NewRequest("POST", "/replication/v1/databases/default/push", bytes.NewBufferString("{invalid"))
	rr := httptest.NewRecorder()

	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandlePush_MissingCollection(t *testing.T) {
	mockService := new(MockQueryService)
	server := createTestServer(mockService, nil, nil)

	reqBody := ReplicaPushRequest{Collection: "", Changes: []ReplicaChange{{Action: "update", Doc: model.Document{"id": "1"}}}}
	body, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest("POST", "/replication/v1/databases/default/push", bytes.NewBuffer(body))
	rr := httptest.NewRecorder()

	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandlePush_InvalidCollection(t *testing.T) {
	mockService := new(MockQueryService)
	server := createTestServer(mockService, nil, nil)

	reqBody := ReplicaPushRequest{Collection: "rooms!", Changes: []ReplicaChange{{Action: "update", Doc: model.Document{"id": "1"}}}}
	body, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest("POST", "/replication/v1/databases/default/push", bytes.NewBuffer(body))
	rr := httptest.NewRecorder()

	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandlePush_DocValidationFail(t *testing.T) {
	mockService := new(MockQueryService)
	server := createTestServer(mockService, nil, nil)

	reqBody := ReplicaPushRequest{Collection: "rooms", Changes: []ReplicaChange{{Action: "update", Doc: model.Document{"id": ""}}}}
	body, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest("POST", "/replication/v1/databases/default/push", bytes.NewBuffer(body))
	rr := httptest.NewRecorder()

	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandlePush_MissingDocID(t *testing.T) {
	mockService := new(MockQueryService)
	server := createTestServer(mockService, nil, nil)

	reqBody := ReplicaPushRequest{Collection: "rooms", Changes: []ReplicaChange{{Action: "update", Doc: model.Document{"name": "Bob"}}}}
	body, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest("POST", "/replication/v1/databases/default/push", bytes.NewBuffer(body))
	rr := httptest.NewRecorder()

	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandlePush_NoChanges(t *testing.T) {
	mockService := new(MockQueryService)
	server := createTestServer(mockService, nil, nil)

	reqBody := ReplicaPushRequest{Collection: "rooms", Changes: []ReplicaChange{}}
	body, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest("POST", "/replication/v1/databases/default/push", bytes.NewBuffer(body))
	rr := httptest.NewRecorder()

	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandlePush_EngineError(t *testing.T) {
	mockService := new(MockQueryService)
	server := createTestServer(mockService, nil, nil)

	mockService.On("Push", mock.Anything, "default", mock.AnythingOfType("types.ReplicationPushRequest")).Return(nil, errors.New("boom"))

	reqBody := ReplicaPushRequest{Collection: "rooms", Changes: []ReplicaChange{{Action: "update", Doc: model.Document{"id": "1"}}}}
	body, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest("POST", "/replication/v1/databases/default/push", bytes.NewBuffer(body))
	rr := httptest.NewRecorder()

	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	mockService.AssertExpectations(t)
}

func TestHandlePush_FlattensConflicts(t *testing.T) {
	mockService := new(MockQueryService)
	server := createTestServer(mockService, nil, nil)

	conflictDoc := &storage.StoredDoc{
		Database:   "default",
		Id:         "rooms/room-1/messages/msg-1",
		Fullpath:   "rooms/room-1/messages/msg-1",
		Collection: "rooms/room-1/messages",
		Data:       map[string]interface{}{"name": "Alice"},
		Version:    2,
	}
	mockService.On("Push", mock.Anything, "default", mock.AnythingOfType("types.ReplicationPushRequest")).Return(&storage.ReplicationPushResponse{
		Conflicts: []storage.ReplicationPushConflict{{ChangeIndex: 0, ID: "msg-1", Reason: storage.PushVersionMismatch, Current: conflictDoc}},
	}, nil)

	pushReq := ReplicaPushRequest{
		Collection: "rooms/room-1/messages",
		Changes: []ReplicaChange{
			{Action: "update", Doc: model.Document{"id": "msg-1", "name": "Bob", "version": int64(1)}},
		},
	}
	body, _ := json.Marshal(pushReq)
	req, _ := http.NewRequest("POST", "/replication/v1/databases/default/push", bytes.NewBuffer(body))
	rr := httptest.NewRecorder()

	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)

	var resp ReplicaPushResponse
	assert.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Len(t, resp.Conflicts, 1)
	assert.Equal(t, "msg-1", resp.Conflicts[0].ID)
	mockService.AssertExpectations(t)
}

func TestHandlePush_DeleteAction(t *testing.T) {
	mockService := new(MockQueryService)
	server := createTestServer(mockService, nil, nil)

	mockService.On("Push", mock.Anything, "default", mock.AnythingOfType("types.ReplicationPushRequest")).Return(&storage.ReplicationPushResponse{}, nil)

	pushReq := ReplicaPushRequest{
		Collection: "rooms/room-1/messages",
		Changes: []ReplicaChange{
			{Doc: model.Document{"id": "msg-2", "version": int64(2)}, Action: "delete"},
		},
	}
	body, _ := json.Marshal(pushReq)
	req, _ := http.NewRequest("POST", "/replication/v1/databases/default/push", bytes.NewBuffer(body))
	rr := httptest.NewRecorder()

	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	mockService.AssertExpectations(t)
}

func TestHandlePush_ValidateReplicationPushError(t *testing.T) {
	mockService := new(MockQueryService)
	server := createTestServer(mockService, nil, nil)

	pushReq := ReplicaPushRequest{
		Collection: "rooms/room-1/messages",
		Changes: []ReplicaChange{
			{Action: "update", Doc: model.Document{"id": "msg-3", "version": int64(1)}},
		},
	}
	body, _ := json.Marshal(pushReq)
	req, _ := http.NewRequest("POST", "/replication/v1/databases/default/push", bytes.NewBuffer(body))
	rr := httptest.NewRecorder()

	orig := validateReplicationPushFn
	validateReplicationPushFn = func(string, storage.ReplicationPushRequest) error {
		return errors.New("forced validation error")
	}
	defer func() { validateReplicationPushFn = orig }()

	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}
