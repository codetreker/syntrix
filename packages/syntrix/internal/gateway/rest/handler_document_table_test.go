package rest

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/codetreker/syntrix/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestDocumentHandlers_TableDriven(t *testing.T) {
	tests := []struct {
		name           string
		method         string
		path           string
		body           string
		setupMock      func(*MockQueryService)
		expectedStatus int
		expectedBody   map[string]interface{}
	}{
		// GET Tests
		{
			name:   "GetDocument_Success",
			method: "GET",
			path:   "/api/v1/databases/default/documents/rooms/room-1/messages/msg-1",
			setupMock: func(m *MockQueryService) {
				doc := model.Document{"id": "msg-1", "collection": "rooms/room-1/messages", "name": "Alice", "version": 1}
				m.On("GetDocument", mock.Anything, "default", "rooms/room-1/messages/msg-1").Return(doc, nil)
			},
			expectedStatus: http.StatusOK,
			expectedBody:   map[string]interface{}{"name": "Alice"},
		},
		{
			name:   "GetDocument_NotFound",
			method: "GET",
			path:   "/api/v1/databases/default/documents/rooms/room-1/messages/unknown",
			setupMock: func(m *MockQueryService) {
				m.On("GetDocument", mock.Anything, "default", "rooms/room-1/messages/unknown").Return(nil, model.ErrNotFound)
			},
			expectedStatus: http.StatusNotFound,
		},
		{
			name:   "GetDocument_InternalError",
			method: "GET",
			path:   "/api/v1/databases/default/documents/rooms/room-1/messages/msg-1",
			setupMock: func(m *MockQueryService) {
				m.On("GetDocument", mock.Anything, "default", "rooms/room-1/messages/msg-1").Return(nil, errors.New("boom"))
			},
			expectedStatus: http.StatusInternalServerError,
		},

		// POST Tests
		{
			name:   "CreateDocument_Success",
			method: "POST",
			path:   "/api/v1/databases/default/documents/rooms/room-1/messages",
			body:   `{"id":"msg-1","name": "Bob"}`,
			setupMock: func(m *MockQueryService) {
				m.On("CreateDocument", mock.Anything, "default", mock.MatchedBy(func(doc model.Document) bool {
					return doc.GetCollection() == "rooms/room-1/messages" && doc.GetID() == "msg-1" && doc["name"] == "Bob"
				})).Return(nil)
				createdDoc := model.Document{"id": "msg-1", "collection": "rooms/room-1/messages", "name": "Bob", "version": 1}
				m.On("GetDocument", mock.Anything, "default", "rooms/room-1/messages/msg-1").Return(createdDoc, nil)
			},
			expectedStatus: http.StatusCreated,
		},
		{
			name:           "CreateDocument_InvalidCollection",
			method:         "POST",
			path:           "/api/v1/databases/default/documents/Invalid!",
			body:           `{"id":"1"}`,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "CreateDocument_BadBody",
			method:         "POST",
			path:           "/api/v1/databases/default/documents/rooms/room-1/messages",
			body:           "{invalid",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "CreateDocument_ValidationError",
			method:         "POST",
			path:           "/api/v1/databases/default/documents/rooms/room-1/messages",
			body:           `{"id":""}`,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:   "CreateDocument_Conflict",
			method: "POST",
			path:   "/api/v1/databases/default/documents/rooms/room-1/messages",
			body:   `{"id":"msg-1"}`,
			setupMock: func(m *MockQueryService) {
				m.On("CreateDocument", mock.Anything, "default", mock.AnythingOfType("model.Document")).Return(model.ErrExists)
			},
			expectedStatus: http.StatusConflict,
		},
		{
			name:   "CreateDocument_GetError",
			method: "POST",
			path:   "/api/v1/databases/default/documents/rooms/room-1/messages",
			body:   `{"id":"msg-1"}`,
			setupMock: func(m *MockQueryService) {
				m.On("CreateDocument", mock.Anything, "default", mock.AnythingOfType("model.Document")).Return(nil)
				m.On("GetDocument", mock.Anything, "default", "rooms/room-1/messages/msg-1").Return(nil, errors.New("fetch"))
			},
			expectedStatus: http.StatusInternalServerError,
		},

		// PUT Tests
		{
			name:   "ReplaceDocument_Success",
			method: "PUT",
			path:   "/api/v1/databases/default/documents/rooms/room-1/messages/msg-1",
			body:   `{"doc":{"name": "Bob"}}`,
			setupMock: func(m *MockQueryService) {
				// Mock GetDocument for authorization check
				m.On("GetDocument", mock.Anything, "default", "rooms/room-1/messages/msg-1").Return(nil, model.ErrNotFound).Once()
				returnedDoc := model.Document{"name": "Bob", "id": "msg-1", "collection": "rooms/room-1/messages", "version": 2}
				m.On("ReplaceDocument", mock.Anything, "default", mock.MatchedBy(func(doc model.Document) bool {
					return doc.GetCollection() == "rooms/room-1/messages" && doc.GetID() == "msg-1" && doc["name"] == "Bob"
				}), mock.Anything).Return(returnedDoc, nil)
			},
			expectedStatus: http.StatusOK,
			expectedBody:   map[string]interface{}{"name": "Bob"},
		},
		{
			name:   "ReplaceDocument_IfMatch",
			method: "PUT",
			path:   "/api/v1/databases/default/documents/rooms/room-1/messages/msg-1",
			body:   `{"doc":{"name": "Bob"}, "ifMatch": [{"field": "version", "op": "==", "value": 1}]}`,
			setupMock: func(m *MockQueryService) {
				// Mock GetDocument for authorization check
				m.On("GetDocument", mock.Anything, "default", "rooms/room-1/messages/msg-1").Return(nil, model.ErrNotFound).Once()
				filters := model.Filters{{Field: "version", Op: model.OpEq, Value: float64(1)}}
				returnedDoc := model.Document{"name": "Bob", "id": "msg-1", "collection": "rooms/room-1/messages", "version": 2}
				m.On("ReplaceDocument", mock.Anything, "default", mock.MatchedBy(func(doc model.Document) bool {
					return doc.GetCollection() == "rooms/room-1/messages" && doc.GetID() == "msg-1" && doc["name"] == "Bob"
				}), filters).Return(returnedDoc, nil)
			},
			expectedStatus: http.StatusOK,
			expectedBody:   map[string]interface{}{"name": "Bob"},
		},
		{
			name:   "ReplaceDocument_InvalidPath",
			method: "PUT",
			path:   "/api/v1/databases/default/documents/rooms",
			body:   `{"doc":{}}`,
			setupMock: func(m *MockQueryService) {
				// Mock GetDocument for authorization check
				m.On("GetDocument", mock.Anything, "default", "rooms").Return(nil, model.ErrNotFound).Maybe()
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:   "ReplaceDocument_InvalidBody",
			method: "PUT",
			path:   "/api/v1/databases/default/documents/rooms/room-1/messages/msg-1",
			body:   "{invalid",
			setupMock: func(m *MockQueryService) {
				// Mock GetDocument for authorization check
				m.On("GetDocument", mock.Anything, "default", "rooms/room-1/messages/msg-1").Return(nil, model.ErrNotFound).Maybe()
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:   "ReplaceDocument_ValidationError",
			method: "PUT",
			path:   "/api/v1/databases/default/documents/rooms/room-1/messages/msg-1",
			body:   `{"doc":{"id":""}}`,
			setupMock: func(m *MockQueryService) {
				// Mock GetDocument for authorization check
				m.On("GetDocument", mock.Anything, "default", "rooms/room-1/messages/msg-1").Return(nil, model.ErrNotFound).Maybe()
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:   "ReplaceDocument_IDMismatch",
			method: "PUT",
			path:   "/api/v1/databases/default/documents/rooms/room-1/messages/msg-1",
			body:   `{"doc":{"id":"msg-2"}}`,
			setupMock: func(m *MockQueryService) {
				// Mock GetDocument for authorization check
				m.On("GetDocument", mock.Anything, "default", "rooms/room-1/messages/msg-1").Return(nil, model.ErrNotFound).Maybe()
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:   "ReplaceDocument_VersionConflict",
			method: "PUT",
			path:   "/api/v1/databases/default/documents/rooms/room-1/messages/msg-1",
			body:   `{"doc":{"id":"msg-1"}}`,
			setupMock: func(m *MockQueryService) {
				// Mock GetDocument for authorization check
				m.On("GetDocument", mock.Anything, "default", "rooms/room-1/messages/msg-1").Return(nil, model.ErrNotFound).Once()
				m.On("ReplaceDocument", mock.Anything, "default", mock.AnythingOfType("model.Document"), mock.Anything).Return(nil, model.ErrPreconditionFailed)
			},
			expectedStatus: http.StatusPreconditionFailed,
		},

		// PATCH Tests
		{
			name:   "PatchDocument_Success",
			method: "PATCH",
			path:   "/api/v1/databases/default/documents/rooms/room-1/messages/msg-1",
			body:   `{"doc":{"status": "read"}}`,
			setupMock: func(m *MockQueryService) {
				// Mock GetDocument for authorization check
				m.On("GetDocument", mock.Anything, "default", "rooms/room-1/messages/msg-1").Return(nil, model.ErrNotFound).Once()
				returnedDoc := model.Document{"name": "Alice", "status": "read", "id": "msg-1", "collection": "rooms/room-1/messages", "version": 2}
				m.On("PatchDocument", mock.Anything, "default", mock.MatchedBy(func(doc model.Document) bool {
					return doc.GetCollection() == "rooms/room-1/messages" && doc.GetID() == "msg-1" && doc["status"] == "read"
				}), mock.Anything).Return(returnedDoc, nil)
			},
			expectedStatus: http.StatusOK,
			expectedBody:   map[string]interface{}{"status": "read"},
		},
		{
			name:   "PatchDocument_IfMatch",
			method: "PATCH",
			path:   "/api/v1/databases/default/documents/rooms/room-1/messages/msg-1",
			body:   `{"doc":{"status": "read"}, "ifMatch": [{"field": "status", "op": "==", "value": "unread"}]}`,
			setupMock: func(m *MockQueryService) {
				// Mock GetDocument for authorization check
				m.On("GetDocument", mock.Anything, "default", "rooms/room-1/messages/msg-1").Return(nil, model.ErrNotFound).Once()
				filters := model.Filters{{Field: "status", Op: model.OpEq, Value: "unread"}}
				returnedDoc := model.Document{"name": "Alice", "status": "read", "id": "msg-1", "collection": "rooms/room-1/messages", "version": 2}
				m.On("PatchDocument", mock.Anything, "default", mock.MatchedBy(func(doc model.Document) bool {
					return doc.GetCollection() == "rooms/room-1/messages" && doc.GetID() == "msg-1" && doc["status"] == "read"
				}), filters).Return(returnedDoc, nil)
			},
			expectedStatus: http.StatusOK,
			expectedBody:   map[string]interface{}{"status": "read"},
		},
		{
			name:   "PatchDocument_InvalidPath",
			method: "PATCH",
			path:   "/api/v1/databases/default/documents/rooms",
			body:   `{"doc":{"status":"read"}}`,
			setupMock: func(m *MockQueryService) {
				// Mock GetDocument for authorization check
				m.On("GetDocument", mock.Anything, "default", "rooms").Return(nil, model.ErrNotFound).Maybe()
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:   "PatchDocument_InvalidBody",
			method: "PATCH",
			path:   "/api/v1/databases/default/documents/rooms/room-1/messages/msg-1",
			body:   "{invalid",
			setupMock: func(m *MockQueryService) {
				// Mock GetDocument for authorization check
				m.On("GetDocument", mock.Anything, "default", "rooms/room-1/messages/msg-1").Return(nil, model.ErrNotFound).Maybe()
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:   "PatchDocument_NoData",
			method: "PATCH",
			path:   "/api/v1/databases/default/documents/rooms/room-1/messages/msg-1",
			body:   `{"doc":{"id":"msg-1"}}`,
			setupMock: func(m *MockQueryService) {
				// Mock GetDocument for authorization check
				m.On("GetDocument", mock.Anything, "default", "rooms/room-1/messages/msg-1").Return(nil, model.ErrNotFound).Maybe()
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:   "PatchDocument_NotFound",
			method: "PATCH",
			path:   "/api/v1/databases/default/documents/rooms/room-1/messages/msg-1",
			body:   `{"doc":{"status":"read"}}`,
			setupMock: func(m *MockQueryService) {
				// Mock GetDocument for authorization check
				m.On("GetDocument", mock.Anything, "default", "rooms/room-1/messages/msg-1").Return(nil, model.ErrNotFound).Once()
				m.On("PatchDocument", mock.Anything, "default", mock.AnythingOfType("model.Document"), mock.Anything).Return(nil, model.ErrNotFound)
			},
			expectedStatus: http.StatusNotFound,
		},
		{
			name:   "PatchDocument_VersionConflict",
			method: "PATCH",
			path:   "/api/v1/databases/default/documents/rooms/room-1/messages/msg-1",
			body:   `{"doc":{"status":"read"}}`,
			setupMock: func(m *MockQueryService) {
				// Mock GetDocument for authorization check
				m.On("GetDocument", mock.Anything, "default", "rooms/room-1/messages/msg-1").Return(nil, model.ErrNotFound).Once()
				m.On("PatchDocument", mock.Anything, "default", mock.AnythingOfType("model.Document"), mock.Anything).Return(nil, model.ErrPreconditionFailed)
			},
			expectedStatus: http.StatusPreconditionFailed,
		},
		{
			name:   "PatchDocument_InternalError",
			method: "PATCH",
			path:   "/api/v1/databases/default/documents/rooms/room-1/messages/msg-1",
			body:   `{"doc":{"status":"read"}}`,
			setupMock: func(m *MockQueryService) {
				// Mock GetDocument for authorization check
				m.On("GetDocument", mock.Anything, "default", "rooms/room-1/messages/msg-1").Return(nil, model.ErrNotFound).Once()
				m.On("PatchDocument", mock.Anything, "default", mock.AnythingOfType("model.Document"), mock.Anything).Return(nil, errors.New("boom"))
			},
			expectedStatus: http.StatusInternalServerError,
		},

		// DELETE Tests
		{
			name:   "DeleteDocument_Success",
			method: "DELETE",
			path:   "/api/v1/databases/default/documents/rooms/room-1/messages/msg-1",
			setupMock: func(m *MockQueryService) {
				// Mock GetDocument for authorization check
				m.On("GetDocument", mock.Anything, "default", "rooms/room-1/messages/msg-1").Return(nil, model.ErrNotFound).Once()
				m.On("DeleteDocument", mock.Anything, "default", "rooms/room-1/messages/msg-1", model.Filters(nil)).Return(nil)
			},
			expectedStatus: http.StatusNoContent,
		},
		{
			name:   "DeleteDocument_IfMatch",
			method: "DELETE",
			path:   "/api/v1/databases/default/documents/rooms/room-1/messages/msg-1",
			body:   `{"ifMatch": [{"field": "version", "op": "==", "value": 1}]}`,
			setupMock: func(m *MockQueryService) {
				// Mock GetDocument for authorization check
				m.On("GetDocument", mock.Anything, "default", "rooms/room-1/messages/msg-1").Return(nil, model.ErrNotFound).Once()
				pred := model.Filters{{Field: "version", Op: model.OpEq, Value: float64(1)}}
				m.On("DeleteDocument", mock.Anything, "default", "rooms/room-1/messages/msg-1", pred).Return(nil)
			},
			expectedStatus: http.StatusNoContent,
		},
		{
			name:   "DeleteDocument_NotFound",
			method: "DELETE",
			path:   "/api/v1/databases/default/documents/rooms/room-1/messages/msg-1",
			setupMock: func(m *MockQueryService) {
				// Mock GetDocument for authorization check
				m.On("GetDocument", mock.Anything, "default", "rooms/room-1/messages/msg-1").Return(nil, model.ErrNotFound).Once()
				m.On("DeleteDocument", mock.Anything, "default", "rooms/room-1/messages/msg-1", model.Filters(nil)).Return(model.ErrNotFound)
			},
			expectedStatus: http.StatusNotFound,
		},
		{
			name:   "DeleteDocument_InternalError",
			method: "DELETE",
			path:   "/api/v1/databases/default/documents/rooms/room-1/messages/msg-1",
			setupMock: func(m *MockQueryService) {
				// Mock GetDocument for authorization check
				m.On("GetDocument", mock.Anything, "default", "rooms/room-1/messages/msg-1").Return(nil, model.ErrNotFound).Once()
				m.On("DeleteDocument", mock.Anything, "default", "rooms/room-1/messages/msg-1", model.Filters(nil)).Return(errors.New("boom"))
			},
			expectedStatus: http.StatusInternalServerError,
		},
		{
			name:   "DeleteDocument_PreconditionFailed",
			method: "DELETE",
			path:   "/api/v1/databases/default/documents/rooms/room-1/messages/msg-1",
			body:   `{"ifMatch":[{"field":"version","op":"==","value":2}]}`,
			setupMock: func(m *MockQueryService) {
				// Mock GetDocument for authorization check
				m.On("GetDocument", mock.Anything, "default", "rooms/room-1/messages/msg-1").Return(nil, model.ErrNotFound).Once()
				pred := model.Filters{{Field: "version", Op: model.OpEq, Value: float64(2)}}
				m.On("DeleteDocument", mock.Anything, "default", "rooms/room-1/messages/msg-1", pred).Return(model.ErrPreconditionFailed)
			},
			expectedStatus: http.StatusPreconditionFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockService := new(MockQueryService)
			if tt.setupMock != nil {
				tt.setupMock(mockService)
			}

			server := createTestServer(mockService, nil, nil)

			var reqBody *bytes.Buffer
			if tt.body != "" {
				reqBody = bytes.NewBufferString(tt.body)
			} else {
				reqBody = bytes.NewBuffer(nil)
			}

			req, _ := http.NewRequest(tt.method, tt.path, reqBody)
			rr := httptest.NewRecorder()

			server.ServeHTTP(rr, req)

			assert.Equal(t, tt.expectedStatus, rr.Code)

			if tt.expectedBody != nil {
				var resp map[string]interface{}
				json.Unmarshal(rr.Body.Bytes(), &resp)
				for k, v := range tt.expectedBody {
					assert.Equal(t, v, resp[k])
				}
			}

			mockService.AssertExpectations(t)
		})
	}
}
