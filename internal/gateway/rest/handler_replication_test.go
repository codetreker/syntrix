package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		Conflicts: []*storage.StoredDoc{},
	}

	mockService.On("Push", mock.Anything, "default", mock.AnythingOfType("types.ReplicationPushRequest")).Return(resp, nil)

	pushReq := ReplicaPushRequest{
		Collection: "rooms/room-1/messages",
		Changes: []ReplicaChange{
			{
				Doc: model.Document{"id": "msg-1", "name": "Bob", "version": float64(1)},
			},
		},
	}
	body, _ := json.Marshal(pushReq)
	req, _ := http.NewRequest("POST", "/replication/v1/databases/default/push", bytes.NewBuffer(body))
	rr := httptest.NewRecorder()

	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestHandlePush_VersionPrecondition(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		action  string
		version *int64
	}{
		{name: "omitted"},
		{name: "explicit zero", field: `,"version":0`, action: "update", version: replicationVersion(0)},
		{name: "negative zero", field: `,"version":-0`, action: "update", version: replicationVersion(0)},
		{name: "create version one", field: `,"version":1`, action: "create", version: replicationVersion(1)},
		{name: "beyond float precision", field: `,"version":9007199254740993`, action: "update", version: replicationVersion(9007199254740993)},
		{name: "int64 maximum", field: `,"version":9223372036854775807`, action: "delete", version: replicationVersion(math.MaxInt64)},
		{name: "case sensitive", field: `,"Version":7`, action: "update"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service := new(MockQueryService)
			var captured storage.ReplicationPushRequest
			service.On("Push", mock.Anything, "default", mock.Anything).
				Run(func(args mock.Arguments) { captured = args.Get(2).(storage.ReplicationPushRequest) }).
				Return(&storage.ReplicationPushResponse{}, nil).Once()
			body := fmt.Sprintf(`{"collection":"rooms/room-1/messages","changes":[{"action":%q,"document":{"id":"msg-1","count":42,"price":1.25,"nested":{"number":3},"numbers":[4],"collection":"forged","createdAt":-1,"updatedAt":-1,"deleted":true%s}}]}`, tc.action, tc.field)
			req := httptest.NewRequest(http.MethodPost, "/replication/v1/databases/default/push", bytes.NewBufferString(body))
			rr := httptest.NewRecorder()
			createTestServer(service, nil, nil).ServeHTTP(rr, req)

			require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
			require.Len(t, captured.Changes, 1)
			assert.Equal(t, tc.version, captured.Changes[0].BaseVersion)
			assert.Equal(t, "rooms/room-1/messages", captured.Collection)
			doc := captured.Changes[0].Doc
			require.NotNil(t, doc)
			assert.Equal(t, int64(1), doc.Version)
			assert.Equal(t, "default", doc.Database)
			assert.Equal(t, captured.Collection, doc.Collection)
			assert.Equal(t, "rooms/room-1/messages/msg-1", doc.Fullpath)
			assert.Equal(t, tc.action == "delete", doc.Deleted)
			assert.Positive(t, doc.CreatedAt)
			assert.Positive(t, doc.UpdatedAt)
			assert.Equal(t, "msg-1", doc.Data["id"])
			assert.Equal(t, float64(42), doc.Data["count"])
			assert.Equal(t, 1.25, doc.Data["price"])
			assert.Equal(t, map[string]interface{}{"number": float64(3)}, doc.Data["nested"])
			assert.Equal(t, []interface{}{float64(4)}, doc.Data["numbers"])
			for _, field := range []string{"version", "collection", "createdAt", "updatedAt", "deleted"} {
				assert.NotContains(t, doc.Data, field)
			}
			if tc.name == "case sensitive" {
				assert.Equal(t, float64(7), doc.Data["Version"])
			}
			service.AssertExpectations(t)
		})
	}
}

func TestHandlePush_InvalidVersion(t *testing.T) {
	for _, version := range []string{`null`, `"1"`, `true`, `false`, `-1`, `1.5`, `1.0`, `1e0`, `1E+2`, `9223372036854775808`, `[]`, `{}`} {
		for _, second := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/second=%t", version, second), func(t *testing.T) {
				service := new(MockQueryService)
				service.On("Push", mock.Anything, mock.Anything, mock.Anything).Return(&storage.ReplicationPushResponse{}, nil).Maybe()
				changes := fmt.Sprintf(`{"document":{"id":"invalid","version":%s}}`, version)
				if second {
					changes = `{"document":{"id":"valid","version":1}},` + changes
				}
				body := `{"collection":"rooms","changes":[` + changes + `]}`
				req := httptest.NewRequest(http.MethodPost, "/replication/v1/databases/default/push", bytes.NewBufferString(body))
				rr := httptest.NewRecorder()
				createTestServer(service, nil, nil).ServeHTTP(rr, req)

				assert.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
				service.AssertNotCalled(t, "Push", mock.Anything, mock.Anything, mock.Anything)
			})
		}
	}
}

func replicationVersion(version int64) *int64 {
	return &version
}

func TestReplicaChange_UnmarshalJSONReuse(t *testing.T) {
	var change ReplicaChange
	require.NoError(t, json.Unmarshal([]byte(`{"action":"delete","document":{"id":"old","version":9007199254740993}}`), &change))
	require.Equal(t, replicationVersion(9007199254740993), change.BaseVersion)

	for _, invalid := range []string{
		`[]`,
		`{"action":"update","document":{"id":"new","version":null}}`,
		`{"action":"update","document":[]}`,
		`{"action":true,"document":{"id":"new","version":2}}`,
		`{"action":"update","document":{"id":"new","version":2,"count":1e1000}}`,
	} {
		err := json.Unmarshal([]byte(invalid), &change)
		require.Error(t, err)
		assert.Equal(t, "delete", change.Action)
		assert.Equal(t, "old", change.Doc.GetID())
		assert.Equal(t, replicationVersion(9007199254740993), change.BaseVersion)
	}

	require.NoError(t, json.Unmarshal([]byte(`{"document":{"id":"new","count":2}}`), &change))
	assert.Empty(t, change.Action)
	assert.Nil(t, change.BaseVersion)
	assert.Equal(t, model.Document{"id": "new", "count": float64(2)}, change.Doc)

	for _, empty := range []string{`{}`, `{"document":null}`, `null`} {
		require.NoError(t, json.Unmarshal([]byte(`{"action":"delete","document":{"id":"old","version":1}}`), &change))
		require.NoError(t, json.Unmarshal([]byte(empty), &change))
		assert.Equal(t, ReplicaChange{}, change)
	}
}

func TestReplicaChange_BaseVersionIsInternal(t *testing.T) {
	change := ReplicaChange{Doc: model.Document{"id": "doc"}, BaseVersion: replicationVersion(7)}
	encoded, err := json.Marshal(change)
	require.NoError(t, err)
	assert.JSONEq(t, `{"action":"","document":{"id":"doc"}}`, string(encoded))

	require.NoError(t, json.Unmarshal([]byte(`{"BaseVersion":7,"document":{"id":"doc"}}`), &change))
	assert.Nil(t, change.BaseVersion)
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
			field    string
			conflict bool
			pred     model.Filters
		}{
			{name: "stale", field: `,"version":4`, conflict: true},
			{name: "matching", field: `,"version":5`, pred: model.Filters{{Field: "version", Op: model.OpEq, Value: int64(5)}}},
			{name: "omitted", pred: model.Filters{}},
		} {
			t.Run(action+"/"+tc.name, func(t *testing.T) {
				live := storage.NewStoredDoc("default", "rooms", "doc", map[string]interface{}{"name": "current"})
				live.Version = 5
				store := &replicationPushStore{live: &live}
				server := createTestServer(querycore.New(store, nil), nil, nil)
				body := fmt.Sprintf(`{"collection":"rooms","changes":[{"action":%q,"document":{"id":"doc","name":"incoming"%s}}]}`, action, tc.field)
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
					assert.Equal(t, "doc", response.Conflicts[0]["id"])
					assert.Equal(t, "current", response.Conflicts[0]["name"])
					assert.Equal(t, float64(5), response.Conflicts[0]["version"])
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

	reqBody := ReplicaPushRequest{Collection: "", Changes: []ReplicaChange{{Doc: model.Document{"id": "1"}}}}
	body, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest("POST", "/replication/v1/databases/default/push", bytes.NewBuffer(body))
	rr := httptest.NewRecorder()

	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandlePush_InvalidCollection(t *testing.T) {
	mockService := new(MockQueryService)
	server := createTestServer(mockService, nil, nil)

	reqBody := ReplicaPushRequest{Collection: "rooms!", Changes: []ReplicaChange{{Doc: model.Document{"id": "1"}}}}
	body, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest("POST", "/replication/v1/databases/default/push", bytes.NewBuffer(body))
	rr := httptest.NewRecorder()

	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandlePush_DocValidationFail(t *testing.T) {
	mockService := new(MockQueryService)
	server := createTestServer(mockService, nil, nil)

	reqBody := ReplicaPushRequest{Collection: "rooms", Changes: []ReplicaChange{{Doc: model.Document{"id": ""}}}}
	body, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest("POST", "/replication/v1/databases/default/push", bytes.NewBuffer(body))
	rr := httptest.NewRecorder()

	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandlePush_MissingDocID(t *testing.T) {
	mockService := new(MockQueryService)
	server := createTestServer(mockService, nil, nil)

	reqBody := ReplicaPushRequest{Collection: "rooms", Changes: []ReplicaChange{{Doc: model.Document{"name": "Bob"}}}}
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

	reqBody := ReplicaPushRequest{Collection: "rooms", Changes: []ReplicaChange{{Doc: model.Document{"id": "1"}}}}
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
		Id:         "rooms/room-1/messages/msg-1",
		Fullpath:   "rooms/room-1/messages/msg-1",
		Collection: "rooms/room-1/messages",
		Data:       map[string]interface{}{"name": "Alice"},
		Version:    2,
	}
	mockService.On("Push", mock.Anything, "default", mock.AnythingOfType("types.ReplicationPushRequest")).Return(&storage.ReplicationPushResponse{
		Conflicts: []*storage.StoredDoc{conflictDoc},
	}, nil)

	pushReq := ReplicaPushRequest{
		Collection: "rooms/room-1/messages",
		Changes: []ReplicaChange{
			{Doc: model.Document{"id": "msg-1", "name": "Bob", "version": float64(1)}},
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
	assert.Equal(t, "msg-1", resp.Conflicts[0]["id"])
	mockService.AssertExpectations(t)
}

func TestHandlePush_DeleteAction(t *testing.T) {
	mockService := new(MockQueryService)
	server := createTestServer(mockService, nil, nil)

	mockService.On("Push", mock.Anything, "default", mock.AnythingOfType("types.ReplicationPushRequest")).Return(&storage.ReplicationPushResponse{}, nil)

	pushReq := ReplicaPushRequest{
		Collection: "rooms/room-1/messages",
		Changes: []ReplicaChange{
			{Doc: model.Document{"id": "msg-2", "version": float64(2)}, Action: "delete"},
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
			{Doc: model.Document{"id": "msg-3", "version": float64(1)}},
		},
	}
	body, _ := json.Marshal(pushReq)
	req, _ := http.NewRequest("POST", "/replication/v1/databases/default/push", bytes.NewBuffer(body))
	rr := httptest.NewRecorder()

	orig := validateReplicationPushFn
	validateReplicationPushFn = func(storage.ReplicationPushRequest) error {
		return errors.New("forced validation error")
	}
	defer func() { validateReplicationPushFn = orig }()

	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}
