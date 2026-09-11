package rest

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/syntrixbase/syntrix/pkg/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestHandleTriggerGet(t *testing.T) {
	mockEngine := new(MockQueryService)
	server := createTestServer(mockEngine, nil, nil)

	// Mock Data
	doc1 := model.Document{"id": "alice", "collection": "users", "name": "Alice", "version": int64(1)}
	doc2 := model.Document{"id": "bob", "collection": "users", "name": "Bob", "version": int64(1)}

	mockEngine.On("GetDocument", mock.Anything, "default", "users/alice").Return(doc1, nil)
	mockEngine.On("GetDocument", mock.Anything, "default", "users/bob").Return(doc2, nil)

	// Request
	reqBody := TriggerGetRequest{
		Paths: []string{"users/alice", "users/bob"},
	}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/trigger/v1/databases/default/get", bytes.NewReader(body))
	w := httptest.NewRecorder()

	// Execute
	server.ServeHTTP(w, req)

	// Assert
	assert.Equal(t, http.StatusOK, w.Code)

	var resp TriggerGetResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NoError(t, err)
	assert.Len(t, resp.Documents, 2)
	assert.Equal(t, "Alice", resp.Documents[0]["name"])
	assert.Equal(t, "Bob", resp.Documents[1]["name"])
}

func TestHandleTriggerGet_EmptyPaths(t *testing.T) {
	mockEngine := new(MockQueryService)
	server := createTestServer(mockEngine, nil, nil)

	reqBody := TriggerGetRequest{Paths: []string{}}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/trigger/v1/databases/default/get", bytes.NewReader(body))
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleTriggerGet_BadJSON(t *testing.T) {
	mockEngine := new(MockQueryService)
	server := createTestServer(mockEngine, nil, nil)

	req := httptest.NewRequest("POST", "/trigger/v1/databases/default/get", bytes.NewReader([]byte("{bad")))
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleTriggerGet_SkipNotFound(t *testing.T) {
	mockEngine := new(MockQueryService)
	server := createTestServer(mockEngine, nil, nil)

	doc := model.Document{"id": "bob", "collection": "users", "name": "Bob"}
	mockEngine.On("GetDocument", mock.Anything, "default", "users/missing").Return(nil, model.ErrNotFound)
	mockEngine.On("GetDocument", mock.Anything, "default", "users/bob").Return(doc, nil)

	reqBody := TriggerGetRequest{Paths: []string{"users/missing", "users/bob"}}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/trigger/v1/databases/default/get", bytes.NewReader(body))
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp TriggerGetResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	assert.Len(t, resp.Documents, 1)
	mockEngine.AssertExpectations(t)
}

func TestHandleTriggerGet_EngineError(t *testing.T) {
	mockEngine := new(MockQueryService)
	server := createTestServer(mockEngine, nil, nil)

	mockEngine.On("GetDocument", mock.Anything, "default", "users/alice").Return(nil, errors.New("boom"))

	reqBody := TriggerGetRequest{Paths: []string{"users/alice"}}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/trigger/v1/databases/default/get", bytes.NewReader(body))
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	mockEngine.AssertExpectations(t)
}

func TestHandleTriggerWrite(t *testing.T) {
	mockEngine := new(MockQueryService)
	server := createTestServer(mockEngine, nil, nil)

	// Mock Expectations
	mockEngine.On("CreateDocument", mock.Anything, "default", mock.MatchedBy(func(doc model.Document) bool {
		return doc.GetCollection() == "users" && doc.GetID() == "charlie" && doc["name"] == "Charlie"
	})).Return(nil)

	mockEngine.On("PatchDocument", mock.Anything, "default", mock.MatchedBy(func(data model.Document) bool {
		return data.GetCollection() == "users" && data.GetID() == "alice" && data["active"] == true
	}), mock.Anything).Return(model.Document{"active": true}, nil)

	mockEngine.On("DeleteDocument", mock.Anything, "default", "users/bob", model.Filters(nil)).Return(nil)

	// Request
	reqBody := TriggerWriteRequest{
		Writes: []TriggerWriteOp{
			{Type: "create", Path: "users/charlie", Data: map[string]interface{}{"name": "Charlie"}},
			{Type: "update", Path: "users/alice", Data: map[string]interface{}{"active": true}},
			{Type: "delete", Path: "users/bob"},
		},
	}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/trigger/v1/databases/default/write", bytes.NewReader(body))
	w := httptest.NewRecorder()

	// Execute
	server.ServeHTTP(w, req)

	// Assert
	assert.Equal(t, http.StatusOK, w.Code)
	mockEngine.AssertExpectations(t)
}

func TestHandleTriggerWrite_UpdateError(t *testing.T) {
	mockEngine := new(MockQueryService)
	server := createTestServer(mockEngine, nil, nil)

	mockEngine.On("PatchDocument", mock.Anything, "default", mock.Anything, mock.Anything).Return(nil, model.ErrPreconditionFailed)

	reqBody := TriggerWriteRequest{
		Writes: []TriggerWriteOp{{Type: "update", Path: "users/alice", Data: map[string]interface{}{"active": true}}},
	}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/trigger/v1/databases/default/write", bytes.NewReader(body))
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusPreconditionFailed, w.Code)
	mockEngine.AssertExpectations(t)
}

func TestHandleTriggerWrite_ReplacePathInvalid(t *testing.T) {
	mockEngine := new(MockQueryService)
	server := createTestServer(mockEngine, nil, nil)

	reqBody := TriggerWriteRequest{
		Writes: []TriggerWriteOp{{Type: "replace", Path: "invalid", Data: map[string]interface{}{"name": "x"}}},
	}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/trigger/v1/databases/default/write", bytes.NewReader(body))
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleTriggerWrite_ReplaceError(t *testing.T) {
	mockEngine := new(MockQueryService)
	server := createTestServer(mockEngine, nil, nil)

	mockEngine.On("ReplaceDocument", mock.Anything, "default", mock.Anything, mock.Anything).Return(nil, assert.AnError)

	reqBody := TriggerWriteRequest{
		Writes: []TriggerWriteOp{{Type: "replace", Path: "users/alice", Data: map[string]interface{}{"name": "Alice"}}},
	}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/trigger/v1/databases/default/write", bytes.NewReader(body))
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	mockEngine.AssertExpectations(t)
}

func TestHandleTriggerWrite_DeleteNotFound(t *testing.T) {
	mockEngine := new(MockQueryService)
	server := createTestServer(mockEngine, nil, nil)

	mockEngine.On("DeleteDocument", mock.Anything, "default", "users/bob", model.Filters(nil)).Return(model.ErrNotFound)

	reqBody := TriggerWriteRequest{
		Writes: []TriggerWriteOp{{Type: "delete", Path: "users/bob"}},
	}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/trigger/v1/databases/default/write", bytes.NewReader(body))
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
	mockEngine.AssertExpectations(t)
}

func TestHandleTriggerWrite_BadJSON(t *testing.T) {
	mockEngine := new(MockQueryService)
	server := createTestServer(mockEngine, nil, nil)

	req := httptest.NewRequest("POST", "/trigger/v1/databases/default/write", bytes.NewReader([]byte("{bad")))
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleTriggerWrite_InvalidType(t *testing.T) {
	mockEngine := new(MockQueryService)
	server := createTestServer(mockEngine, nil, nil)

	reqBody := TriggerWriteRequest{
		Writes: []TriggerWriteOp{{Type: "unknown", Path: "users/x", Data: map[string]interface{}{"a": 1}}},
	}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/trigger/v1/databases/default/write", bytes.NewReader(body))
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleTriggerWrite_InvalidPath(t *testing.T) {
	mockEngine := new(MockQueryService)
	server := createTestServer(mockEngine, nil, nil)

	reqBody := TriggerWriteRequest{
		Writes: []TriggerWriteOp{{Type: "create", Path: "invalid", Data: map[string]interface{}{"name": "x"}}},
	}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/trigger/v1/databases/default/write", bytes.NewReader(body))
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleTriggerWrite_CreateExists(t *testing.T) {
	mockEngine := new(MockQueryService)
	server := createTestServer(mockEngine, nil, nil)

	mockEngine.On("CreateDocument", mock.Anything, "default", mock.Anything).Return(model.ErrExists)

	reqBody := TriggerWriteRequest{
		Writes: []TriggerWriteOp{{Type: "create", Path: "users/alice", Data: map[string]interface{}{"name": "Alice"}}},
	}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/trigger/v1/databases/default/write", bytes.NewReader(body))
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
	mockEngine.AssertExpectations(t)
}

func TestHandleTriggerWrite_EmptyWrites(t *testing.T) {
	mockEngine := new(MockQueryService)
	server := createTestServer(mockEngine, nil, nil)

	reqBody := TriggerWriteRequest{Writes: []TriggerWriteOp{}}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/trigger/v1/databases/default/write", bytes.NewReader(body))
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleTriggerQuery(t *testing.T) {
	mockEngine := new(MockQueryService)
	server := createTestServer(mockEngine, nil, nil)

	// Mock Data
	docs := []model.Document{
		{"id": "1", "collection": "users", "a": 1, "version": int64(1)},
	}
	mockEngine.On("ExecuteQueryPage", mock.Anything, "default", mock.Anything).Return(model.QueryPage{Documents: docs, EffectiveOrder: []model.Order{{Field: "id", Direction: "asc"}}}, nil)

	// Request
	q := model.Query{Collection: "users"}
	body, _ := json.Marshal(q)
	req := httptest.NewRequest("POST", "/trigger/v1/databases/default/query", bytes.NewReader(body))
	w := httptest.NewRecorder()

	// Execute
	server.ServeHTTP(w, req)

	// Assert
	assert.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Documents      []json.RawMessage `json:"documents"`
		NextCursor     *string           `json:"nextCursor"`
		EffectiveOrder []model.Order     `json:"effectiveOrder"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Documents, 1)
	value, err := model.DecodeTypedValue(resp.Documents[0])
	require.NoError(t, err)
	doc, ok := value.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, int64(1), doc["a"])
	assert.Equal(t, "1", doc["id"])
	assert.Equal(t, "users", doc["collection"])
	assert.Nil(t, resp.NextCursor)
	assert.Equal(t, []model.Order{{Field: "id", Direction: "asc"}}, resp.EffectiveOrder)
	mockEngine.AssertExpectations(t)
}

func TestHandleTriggerQuery_BadJSON(t *testing.T) {
	mockEngine := new(MockQueryService)
	server := createTestServer(mockEngine, nil, nil)

	req := httptest.NewRequest("POST", "/trigger/v1/databases/default/query", bytes.NewReader([]byte("{bad")))
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleTriggerQuery_ValidateError(t *testing.T) {
	mockEngine := new(MockQueryService)
	server := createTestServer(mockEngine, nil, nil)

	q := model.Query{Collection: ""} // invalid
	body, _ := json.Marshal(q)
	req := httptest.NewRequest("POST", "/trigger/v1/databases/default/query", bytes.NewReader(body))
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleTriggerQuery_Error(t *testing.T) {
	mockEngine := new(MockQueryService)
	server := createTestServer(mockEngine, nil, nil)

	q := model.Query{Collection: "users"}
	mockEngine.On("ExecuteQueryPage", mock.Anything, "default", q).Return(nil, assert.AnError)

	body, _ := json.Marshal(q)
	req := httptest.NewRequest("POST", "/trigger/v1/databases/default/query", bytes.NewReader(body))
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	mockEngine.AssertExpectations(t)
}

func TestHandleTriggerWrite_UnexpectedError(t *testing.T) {
	mockEngine := new(MockQueryService)
	server := createTestServer(mockEngine, nil, nil)

	mockEngine.On("CreateDocument", mock.Anything, "default", mock.Anything).Return(assert.AnError)

	reqBody := TriggerWriteRequest{
		Writes: []TriggerWriteOp{{Type: "create", Path: "users/fail", Data: map[string]interface{}{"name": "Fail"}}},
	}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/trigger/v1/databases/default/write", bytes.NewReader(body))
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}
