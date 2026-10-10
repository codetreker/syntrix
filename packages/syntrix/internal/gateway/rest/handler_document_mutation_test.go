package rest

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/codetreker/syntrix/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// setupMockServiceForAuthz sets up the default GetDocument mock for authorization checks.
// This is needed because the authorized() middleware calls GetDocument to fetch existing resources.
func setupMockServiceForAuthz(mockService *MockQueryService) {
	mockService.On("GetDocument", mock.Anything, mock.Anything, mock.Anything).Maybe().Return(nil, model.ErrNotFound)
}

func TestHandleReplaceDocument_IdMutation(t *testing.T) {
	mockService := new(MockQueryService)
	mockAuth := new(MockAuthService)
	// Mock GetDocument for authorization check (replace is not create, so existing resource is fetched)
	mockService.On("GetDocument", mock.Anything, "default", "rooms/room-1/messages/msg-1").Return(nil, model.ErrNotFound)
	server := createTestServer(mockService, mockAuth, nil)

	// Try to replace document msg-1 with body containing id: msg-2
	body := []byte(`{"doc":{"id": "msg-2", "name": "Bob"}}`)
	req, _ := http.NewRequest("PUT", "/api/v1/databases/default/documents/rooms/room-1/messages/msg-1", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer test")
	rr := httptest.NewRecorder()

	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "Document ID cannot be changed")
}

func TestHandleUpdateDocument_IdMutation(t *testing.T) {
	mockService := new(MockQueryService)
	mockAuth := new(MockAuthService)
	// Mock GetDocument for authorization check (update is not create, so existing resource is fetched)
	mockService.On("GetDocument", mock.Anything, "default", "rooms/room-1/messages/msg-1").Return(nil, model.ErrNotFound)
	server := createTestServer(mockService, mockAuth, nil)

	// Try to update document msg-1 with body containing id: msg-2
	body := []byte(`{"doc":{"id": "msg-2", "name": "Bob"}}`)
	req, _ := http.NewRequest("PATCH", "/api/v1/databases/default/documents/rooms/room-1/messages/msg-1", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer test")
	rr := httptest.NewRecorder()

	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "Document ID cannot be changed")
}

func TestHandleReplaceDocument_InvalidPath(t *testing.T) {
	mockService := new(MockQueryService)
	mockAuth := new(MockAuthService)
	setupMockServiceForAuthz(mockService)
	server := createTestServer(mockService, mockAuth, nil)

	body := []byte(`{"doc":{"name": "Bob"}}`)
	// Invalid path - only collection, no document ID for PUT
	req, _ := http.NewRequest("PUT", "/api/v1/databases/default/documents/rooms", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer test")
	rr := httptest.NewRecorder()

	server.ServeHTTP(rr, req)

	// PUT on collection should return 405 (Method Not Allowed) or route mismatch
	// Since PUT is only defined for document paths, this should return 404 or 400
	assert.Contains(t, []int{http.StatusBadRequest, http.StatusNotFound, http.StatusMethodNotAllowed}, rr.Code)
}

func TestHandleReplaceDocument_InvalidBody(t *testing.T) {
	mockService := new(MockQueryService)
	mockAuth := new(MockAuthService)
	setupMockServiceForAuthz(mockService)
	server := createTestServer(mockService, mockAuth, nil)

	// Invalid JSON body
	body := []byte(`{invalid json}`)
	req, _ := http.NewRequest("PUT", "/api/v1/databases/default/documents/rooms/room-1/messages/msg-1", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer test")
	rr := httptest.NewRecorder()

	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "Invalid request body")
}

func TestHandleReplaceDocument_InvalidDocId(t *testing.T) {
	mockService := new(MockQueryService)
	mockAuth := new(MockAuthService)
	setupMockServiceForAuthz(mockService)
	server := createTestServer(mockService, mockAuth, nil)

	// Invalid doc data - empty id field
	body := []byte(`{"doc":{"id": ""}}`)
	req, _ := http.NewRequest("PUT", "/api/v1/databases/default/documents/rooms/room-1/messages/msg-1", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer test")
	rr := httptest.NewRecorder()

	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "Invalid document data")
}

func TestHandlePatchDocument_InvalidPath(t *testing.T) {
	mockService := new(MockQueryService)
	mockAuth := new(MockAuthService)
	setupMockServiceForAuthz(mockService)
	server := createTestServer(mockService, mockAuth, nil)

	body := []byte(`{"doc":{"name": "Bob"}}`)
	// Invalid path - only collection for PATCH
	req, _ := http.NewRequest("PATCH", "/api/v1/databases/default/documents/rooms", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer test")
	rr := httptest.NewRecorder()

	server.ServeHTTP(rr, req)

	// PATCH on collection should return 405 or route mismatch
	assert.Contains(t, []int{http.StatusBadRequest, http.StatusNotFound, http.StatusMethodNotAllowed}, rr.Code)
}

func TestHandlePatchDocument_InvalidBody(t *testing.T) {
	mockService := new(MockQueryService)
	mockAuth := new(MockAuthService)
	setupMockServiceForAuthz(mockService)
	server := createTestServer(mockService, mockAuth, nil)

	// Invalid JSON body
	body := []byte(`{invalid json}`)
	req, _ := http.NewRequest("PATCH", "/api/v1/databases/default/documents/rooms/room-1/messages/msg-1", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer test")
	rr := httptest.NewRecorder()

	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "Invalid request body")
}

func TestHandlePatchDocument_InvalidDocId(t *testing.T) {
	mockService := new(MockQueryService)
	mockAuth := new(MockAuthService)
	setupMockServiceForAuthz(mockService)
	server := createTestServer(mockService, mockAuth, nil)

	// Invalid doc data - empty id field
	body := []byte(`{"doc":{"id": ""}}`)
	req, _ := http.NewRequest("PATCH", "/api/v1/databases/default/documents/rooms/room-1/messages/msg-1", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer test")
	rr := httptest.NewRecorder()

	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "Invalid document data")
}

func TestHandleDeleteDocument_InvalidBody(t *testing.T) {
	mockService := new(MockQueryService)
	mockAuth := new(MockAuthService)
	setupMockServiceForAuthz(mockService)
	server := createTestServer(mockService, mockAuth, nil)

	// Invalid JSON body
	body := []byte(`{invalid json}`)
	req, _ := http.NewRequest("DELETE", "/api/v1/databases/default/documents/rooms/room-1/messages/msg-1", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer test")
	rr := httptest.NewRecorder()

	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "Invalid request body")
}

func TestHandleCreateDocument_InvalidBody(t *testing.T) {
	mockService := new(MockQueryService)
	mockAuth := new(MockAuthService)
	// Note: create action doesn't fetch existing document for authz
	server := createTestServer(mockService, mockAuth, nil)

	// Invalid JSON body
	body := []byte(`{invalid json}`)
	req, _ := http.NewRequest("POST", "/api/v1/databases/default/documents/rooms", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer test")
	rr := httptest.NewRecorder()

	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "Invalid request body")
}

func TestHandleCreateDocument_InvalidDocId(t *testing.T) {
	mockService := new(MockQueryService)
	mockAuth := new(MockAuthService)
	// Note: create action doesn't fetch existing document for authz
	server := createTestServer(mockService, mockAuth, nil)

	// Invalid doc data - empty id field
	body := []byte(`{"id": ""}`)
	req, _ := http.NewRequest("POST", "/api/v1/databases/default/documents/rooms", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer test")
	rr := httptest.NewRecorder()

	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "Invalid document data")
}
