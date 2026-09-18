package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/database"
	"github.com/syntrixbase/syntrix/internal/core/identity"
)

// fullMockDatabaseService implements database.Service for handler testing
type fullMockDatabaseService struct {
	createFunc   func(ctx context.Context, userID string, isAdmin bool, req database.CreateRequest) (*database.Database, error)
	listFunc     func(ctx context.Context, userID string, isAdmin bool, opts database.ListOptions) (*database.ListResult, error)
	getFunc      func(ctx context.Context, userID string, isAdmin bool, identifier string) (*database.Database, error)
	updateFunc   func(ctx context.Context, userID string, isAdmin bool, identifier string, req database.UpdateRequest) (*database.Database, error)
	deleteFunc   func(ctx context.Context, userID string, isAdmin bool, identifier string) error
	resolveFunc  func(ctx context.Context, identifier string) (*database.Database, error)
	validateFunc func(ctx context.Context, identifier string) error
}

func (m *fullMockDatabaseService) CreateDatabase(ctx context.Context, userID string, isAdmin bool, req database.CreateRequest) (*database.Database, error) {
	if m.createFunc != nil {
		return m.createFunc(ctx, userID, isAdmin, req)
	}
	return nil, nil
}

func (m *fullMockDatabaseService) ListDatabases(ctx context.Context, userID string, isAdmin bool, opts database.ListOptions) (*database.ListResult, error) {
	if m.listFunc != nil {
		return m.listFunc(ctx, userID, isAdmin, opts)
	}
	return &database.ListResult{}, nil
}

func (m *fullMockDatabaseService) GetDatabase(ctx context.Context, userID string, isAdmin bool, identifier string) (*database.Database, error) {
	if m.getFunc != nil {
		return m.getFunc(ctx, userID, isAdmin, identifier)
	}
	return nil, database.ErrDatabaseNotFound
}

func (m *fullMockDatabaseService) UpdateDatabase(ctx context.Context, userID string, isAdmin bool, identifier string, req database.UpdateRequest) (*database.Database, error) {
	if m.updateFunc != nil {
		return m.updateFunc(ctx, userID, isAdmin, identifier, req)
	}
	return nil, nil
}

func (m *fullMockDatabaseService) DeleteDatabase(ctx context.Context, userID string, isAdmin bool, identifier string) error {
	if m.deleteFunc != nil {
		return m.deleteFunc(ctx, userID, isAdmin, identifier)
	}
	return nil
}

func (m *fullMockDatabaseService) ResolveDatabase(ctx context.Context, identifier string) (*database.Database, error) {
	if m.resolveFunc != nil {
		return m.resolveFunc(ctx, identifier)
	}
	return nil, database.ErrDatabaseNotFound
}

func (m *fullMockDatabaseService) ResolveDatabaseAuthoritative(ctx context.Context, identifier string) (*database.Database, error) {
	return m.ResolveDatabase(ctx, identifier)
}

func (m *fullMockDatabaseService) ValidateDatabase(ctx context.Context, identifier string) error {
	if m.validateFunc != nil {
		return m.validateFunc(ctx, identifier)
	}
	_, err := m.ResolveDatabase(ctx, identifier)
	return err
}

func newTestHandlerWithDB(dbService database.Service) *Handler {
	mockAuth := new(MockAuthService)
	h := &Handler{
		auth:     mockAuth,
		database: dbService,
	}
	return h
}

func TestHandler_handleCreateDatabase(t *testing.T) {
	slug := "test-db"
	testDB := &database.Database{
		ID:          "abc123",
		Slug:        &slug,
		DisplayName: "Test Database",
		OwnerID:     "user-123",
		Status:      database.StatusActive,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	tests := []struct {
		name           string
		body           interface{}
		setupMock      func(*fullMockDatabaseService)
		expectedStatus int
		checkResponse  func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name: "successful create",
			body: CreateDatabaseRequest{
				DisplayName: "Test Database",
				Slug:        &slug,
			},
			setupMock: func(m *fullMockDatabaseService) {
				m.createFunc = func(ctx context.Context, userID string, isAdmin bool, req database.CreateRequest) (*database.Database, error) {
					return testDB, nil
				}
			},
			expectedStatus: http.StatusCreated,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var resp DatabaseResponse
				err := json.Unmarshal(rec.Body.Bytes(), &resp)
				require.NoError(t, err)
				assert.Equal(t, "abc123", resp.ID)
				assert.Equal(t, "Test Database", resp.DisplayName)
			},
		},
		{
			name:           "invalid request body",
			body:           "invalid json",
			setupMock:      func(m *fullMockDatabaseService) {},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name: "database service not available",
			body: CreateDatabaseRequest{DisplayName: "Test"},
			setupMock: func(m *fullMockDatabaseService) {
				// Handler will have nil database service
			},
			expectedStatus: http.StatusServiceUnavailable,
		},
		{
			name: "quota exceeded",
			body: CreateDatabaseRequest{DisplayName: "Test"},
			setupMock: func(m *fullMockDatabaseService) {
				m.createFunc = func(ctx context.Context, userID string, isAdmin bool, req database.CreateRequest) (*database.Database, error) {
					return nil, database.ErrQuotaExceeded
				}
			},
			expectedStatus: http.StatusForbidden,
		},
		{
			name: "invalid slug",
			body: CreateDatabaseRequest{DisplayName: "Test", Slug: ptr("invalid slug!")},
			setupMock: func(m *fullMockDatabaseService) {
				m.createFunc = func(ctx context.Context, userID string, isAdmin bool, req database.CreateRequest) (*database.Database, error) {
					return nil, database.ErrInvalidSlugFormat
				}
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name: "reserved slug",
			body: CreateDatabaseRequest{DisplayName: "Test", Slug: ptr("admin")},
			setupMock: func(m *fullMockDatabaseService) {
				m.createFunc = func(ctx context.Context, userID string, isAdmin bool, req database.CreateRequest) (*database.Database, error) {
					return nil, database.ErrReservedSlug
				}
			},
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockService := &fullMockDatabaseService{}
			tt.setupMock(mockService)

			var h *Handler
			if tt.name == "database service not available" {
				h = newTestHandlerWithDB(nil)
			} else {
				h = newTestHandlerWithDB(mockService)
			}

			body, _ := json.Marshal(tt.body)
			req := httptest.NewRequest("POST", "/api/v1/databases", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")

			// Add user context
			ctx := context.WithValue(req.Context(), identity.ContextKeyUserID, "user-123")
			req = req.WithContext(ctx)

			rec := httptest.NewRecorder()
			h.handleCreateDatabase(rec, req)

			assert.Equal(t, tt.expectedStatus, rec.Code)
			if tt.checkResponse != nil {
				tt.checkResponse(t, rec)
			}
		})
	}
}

func TestHandler_handleListDatabases(t *testing.T) {
	slug := "test-db"
	testDB := &database.Database{
		ID:          "abc123",
		Slug:        &slug,
		DisplayName: "Test Database",
		OwnerID:     "user-123",
		Status:      database.StatusActive,
		CreatedAt:   time.Now(),
	}

	tests := []struct {
		name           string
		query          string
		setupMock      func(*fullMockDatabaseService)
		expectedStatus int
		checkResponse  func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:  "successful list",
			query: "",
			setupMock: func(m *fullMockDatabaseService) {
				m.listFunc = func(ctx context.Context, userID string, isAdmin bool, opts database.ListOptions) (*database.ListResult, error) {
					return &database.ListResult{
						Databases: []*database.Database{testDB},
						Total:     1,
						Quota:     &database.QuotaInfo{Used: 1, Limit: 10},
					}, nil
				}
			},
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var resp ListDatabasesResponse
				err := json.Unmarshal(rec.Body.Bytes(), &resp)
				require.NoError(t, err)
				assert.Equal(t, 1, resp.Total)
				assert.Len(t, resp.Databases, 1)
				assert.NotNil(t, resp.Quota)
				assert.Equal(t, 1, resp.Quota.Used)
			},
		},
		{
			name:  "with pagination",
			query: "?limit=10&offset=5",
			setupMock: func(m *fullMockDatabaseService) {
				m.listFunc = func(ctx context.Context, userID string, isAdmin bool, opts database.ListOptions) (*database.ListResult, error) {
					assert.Equal(t, 10, opts.Limit)
					assert.Equal(t, 5, opts.Offset)
					return &database.ListResult{Databases: []*database.Database{}, Total: 0}, nil
				}
			},
			expectedStatus: http.StatusOK,
		},
		{
			name:  "with status filter",
			query: "?status=active",
			setupMock: func(m *fullMockDatabaseService) {
				m.listFunc = func(ctx context.Context, userID string, isAdmin bool, opts database.ListOptions) (*database.ListResult, error) {
					assert.Equal(t, database.StatusActive, opts.Status)
					return &database.ListResult{Databases: []*database.Database{}, Total: 0}, nil
				}
			},
			expectedStatus: http.StatusOK,
		},
		{
			name:  "service error",
			query: "",
			setupMock: func(m *fullMockDatabaseService) {
				m.listFunc = func(ctx context.Context, userID string, isAdmin bool, opts database.ListOptions) (*database.ListResult, error) {
					return nil, database.ErrDatabaseNotFound
				}
			},
			expectedStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockService := &fullMockDatabaseService{}
			tt.setupMock(mockService)

			h := newTestHandlerWithDB(mockService)

			req := httptest.NewRequest("GET", "/api/v1/databases"+tt.query, nil)
			ctx := context.WithValue(req.Context(), identity.ContextKeyUserID, "user-123")
			req = req.WithContext(ctx)

			rec := httptest.NewRecorder()
			h.handleListDatabases(rec, req)

			assert.Equal(t, tt.expectedStatus, rec.Code)
			if tt.checkResponse != nil {
				tt.checkResponse(t, rec)
			}
		})
	}

	t.Run("database service not available", func(t *testing.T) {
		h := newTestHandlerWithDB(nil)

		req := httptest.NewRequest("GET", "/api/v1/databases", nil)
		ctx := context.WithValue(req.Context(), identity.ContextKeyUserID, "user-123")
		req = req.WithContext(ctx)

		rec := httptest.NewRecorder()
		h.handleListDatabases(rec, req)

		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})

	t.Run("unauthenticated", func(t *testing.T) {
		mockService := &fullMockDatabaseService{}
		h := newTestHandlerWithDB(mockService)

		req := httptest.NewRequest("GET", "/api/v1/databases", nil)
		// No user ID in context

		rec := httptest.NewRecorder()
		h.handleListDatabases(rec, req)

		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}

func TestHandler_handleGetDatabase(t *testing.T) {
	slug := "test-db"
	testDB := &database.Database{
		ID:          "abc123",
		Slug:        &slug,
		DisplayName: "Test Database",
		OwnerID:     "user-123",
		Status:      database.StatusActive,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	tests := []struct {
		name           string
		identifier     string
		setupMock      func(*fullMockDatabaseService)
		expectedStatus int
	}{
		{
			name:       "successful get by slug",
			identifier: "test-db",
			setupMock: func(m *fullMockDatabaseService) {
				m.getFunc = func(ctx context.Context, userID string, isAdmin bool, identifier string) (*database.Database, error) {
					return testDB, nil
				}
			},
			expectedStatus: http.StatusOK,
		},
		{
			name:       "successful get by id",
			identifier: "id:abc123",
			setupMock: func(m *fullMockDatabaseService) {
				m.getFunc = func(ctx context.Context, userID string, isAdmin bool, identifier string) (*database.Database, error) {
					return testDB, nil
				}
			},
			expectedStatus: http.StatusOK,
		},
		{
			name:       "not found",
			identifier: "nonexistent",
			setupMock: func(m *fullMockDatabaseService) {
				m.getFunc = func(ctx context.Context, userID string, isAdmin bool, identifier string) (*database.Database, error) {
					return nil, database.ErrDatabaseNotFound
				}
			},
			expectedStatus: http.StatusNotFound,
		},
		{
			name:       "not owner",
			identifier: "other-db",
			setupMock: func(m *fullMockDatabaseService) {
				m.getFunc = func(ctx context.Context, userID string, isAdmin bool, identifier string) (*database.Database, error) {
					return nil, database.ErrNotOwner
				}
			},
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "missing identifier",
			identifier:     "",
			setupMock:      func(m *fullMockDatabaseService) {},
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockService := &fullMockDatabaseService{}
			tt.setupMock(mockService)

			h := newTestHandlerWithDB(mockService)

			req := httptest.NewRequest("GET", "/api/v1/databases/"+tt.identifier, nil)
			req.SetPathValue("identifier", tt.identifier)
			ctx := context.WithValue(req.Context(), identity.ContextKeyUserID, "user-123")
			req = req.WithContext(ctx)

			rec := httptest.NewRecorder()
			h.handleGetDatabase(rec, req)

			assert.Equal(t, tt.expectedStatus, rec.Code)
		})
	}

	t.Run("database service not available", func(t *testing.T) {
		h := newTestHandlerWithDB(nil)

		req := httptest.NewRequest("GET", "/api/v1/databases/test-db", nil)
		req.SetPathValue("identifier", "test-db")
		ctx := context.WithValue(req.Context(), identity.ContextKeyUserID, "user-123")
		req = req.WithContext(ctx)

		rec := httptest.NewRecorder()
		h.handleGetDatabase(rec, req)

		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})
}

func TestHandler_handleUpdateDatabase(t *testing.T) {
	slug := "test-db"
	testDB := &database.Database{
		ID:          "abc123",
		Slug:        &slug,
		DisplayName: "Updated Database",
		OwnerID:     "user-123",
		Status:      database.StatusActive,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	tests := []struct {
		name           string
		identifier     string
		body           interface{}
		setupMock      func(*fullMockDatabaseService)
		expectedStatus int
	}{
		{
			name:       "successful update",
			identifier: "test-db",
			body:       UpdateDatabaseRequest{DisplayName: ptr("Updated Database")},
			setupMock: func(m *fullMockDatabaseService) {
				m.updateFunc = func(ctx context.Context, userID string, isAdmin bool, identifier string, req database.UpdateRequest) (*database.Database, error) {
					return testDB, nil
				}
			},
			expectedStatus: http.StatusOK,
		},
		{
			name:       "slug immutable",
			identifier: "test-db",
			body:       UpdateDatabaseRequest{Slug: ptr("new-slug")},
			setupMock: func(m *fullMockDatabaseService) {
				m.updateFunc = func(ctx context.Context, userID string, isAdmin bool, identifier string, req database.UpdateRequest) (*database.Database, error) {
					return nil, database.ErrSlugImmutable
				}
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:       "not found",
			identifier: "nonexistent",
			body:       UpdateDatabaseRequest{DisplayName: ptr("Test")},
			setupMock: func(m *fullMockDatabaseService) {
				m.updateFunc = func(ctx context.Context, userID string, isAdmin bool, identifier string, req database.UpdateRequest) (*database.Database, error) {
					return nil, database.ErrDatabaseNotFound
				}
			},
			expectedStatus: http.StatusNotFound,
		},
		{
			name:           "invalid body",
			identifier:     "test-db",
			body:           "invalid",
			setupMock:      func(m *fullMockDatabaseService) {},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "missing identifier",
			identifier:     "",
			body:           UpdateDatabaseRequest{DisplayName: ptr("Test")},
			setupMock:      func(m *fullMockDatabaseService) {},
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockService := &fullMockDatabaseService{}
			tt.setupMock(mockService)

			h := newTestHandlerWithDB(mockService)

			body, _ := json.Marshal(tt.body)
			req := httptest.NewRequest("PATCH", "/api/v1/databases/"+tt.identifier, bytes.NewReader(body))
			req.SetPathValue("identifier", tt.identifier)
			req.Header.Set("Content-Type", "application/json")
			ctx := context.WithValue(req.Context(), identity.ContextKeyUserID, "user-123")
			req = req.WithContext(ctx)

			rec := httptest.NewRecorder()
			h.handleUpdateDatabase(rec, req)

			assert.Equal(t, tt.expectedStatus, rec.Code)
		})
	}

	t.Run("database service not available", func(t *testing.T) {
		h := newTestHandlerWithDB(nil)

		body, _ := json.Marshal(UpdateDatabaseRequest{DisplayName: ptr("Test")})
		req := httptest.NewRequest("PATCH", "/api/v1/databases/test-db", bytes.NewReader(body))
		req.SetPathValue("identifier", "test-db")
		req.Header.Set("Content-Type", "application/json")
		ctx := context.WithValue(req.Context(), identity.ContextKeyUserID, "user-123")
		req = req.WithContext(ctx)

		rec := httptest.NewRecorder()
		h.handleUpdateDatabase(rec, req)

		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})
}

func TestHandler_handleDeleteDatabase(t *testing.T) {
	slug := "test-db"
	testDB := &database.Database{
		ID:          "abc123",
		Slug:        &slug,
		DisplayName: "Test Database",
		OwnerID:     "user-123",
		Status:      database.StatusActive,
	}

	tests := []struct {
		name           string
		identifier     string
		setupMock      func(*fullMockDatabaseService)
		expectedStatus int
	}{
		{
			name:       "successful delete",
			identifier: "test-db",
			setupMock: func(m *fullMockDatabaseService) {
				m.getFunc = func(ctx context.Context, userID string, isAdmin bool, identifier string) (*database.Database, error) {
					return testDB, nil
				}
				m.deleteFunc = func(ctx context.Context, userID string, isAdmin bool, identifier string) error {
					return nil
				}
			},
			expectedStatus: http.StatusOK,
		},
		{
			name:       "protected database",
			identifier: "default",
			setupMock: func(m *fullMockDatabaseService) {
				m.getFunc = func(ctx context.Context, userID string, isAdmin bool, identifier string) (*database.Database, error) {
					return testDB, nil
				}
				m.deleteFunc = func(ctx context.Context, userID string, isAdmin bool, identifier string) error {
					return database.ErrProtectedDatabase
				}
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:       "not found",
			identifier: "nonexistent",
			setupMock: func(m *fullMockDatabaseService) {
				m.getFunc = func(ctx context.Context, userID string, isAdmin bool, identifier string) (*database.Database, error) {
					return nil, database.ErrDatabaseNotFound
				}
			},
			expectedStatus: http.StatusNotFound,
		},
		{
			name:       "database suspended",
			identifier: "suspended-db",
			setupMock: func(m *fullMockDatabaseService) {
				m.getFunc = func(ctx context.Context, userID string, isAdmin bool, identifier string) (*database.Database, error) {
					return nil, database.ErrDatabaseSuspended
				}
			},
			expectedStatus: http.StatusForbidden,
		},
		{
			name:       "database deleting",
			identifier: "deleting-db",
			setupMock: func(m *fullMockDatabaseService) {
				m.getFunc = func(ctx context.Context, userID string, isAdmin bool, identifier string) (*database.Database, error) {
					return nil, database.ErrDatabaseDeleting
				}
			},
			expectedStatus: http.StatusGone,
		},
		{
			name:           "missing identifier",
			identifier:     "",
			setupMock:      func(m *fullMockDatabaseService) {},
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockService := &fullMockDatabaseService{}
			tt.setupMock(mockService)

			h := newTestHandlerWithDB(mockService)

			req := httptest.NewRequest("DELETE", "/api/v1/databases/"+tt.identifier, nil)
			req.SetPathValue("identifier", tt.identifier)
			ctx := context.WithValue(req.Context(), identity.ContextKeyUserID, "user-123")
			req = req.WithContext(ctx)

			rec := httptest.NewRecorder()
			h.handleDeleteDatabase(rec, req)

			assert.Equal(t, tt.expectedStatus, rec.Code)
		})
	}

	t.Run("database service not available", func(t *testing.T) {
		h := newTestHandlerWithDB(nil)

		req := httptest.NewRequest("DELETE", "/api/v1/databases/test-db", nil)
		req.SetPathValue("identifier", "test-db")
		ctx := context.WithValue(req.Context(), identity.ContextKeyUserID, "user-123")
		req = req.WithContext(ctx)

		rec := httptest.NewRecorder()
		h.handleDeleteDatabase(rec, req)

		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})
}

func TestHandler_writeDatabaseError(t *testing.T) {
	tests := []struct {
		name           string
		err            error
		expectedStatus int
		expectedCode   string
	}{
		{"not found", database.ErrDatabaseNotFound, http.StatusNotFound, ErrCodeDatabaseNotFound},
		{"exists", database.ErrDatabaseExists, http.StatusConflict, ErrCodeDatabaseExists},
		{"suspended", database.ErrDatabaseSuspended, http.StatusForbidden, ErrCodeDatabaseSuspended},
		{"deleting", database.ErrDatabaseDeleting, http.StatusGone, ErrCodeDatabaseDeleting},
		{"protected", database.ErrProtectedDatabase, http.StatusBadRequest, ErrCodeProtectedDatabase},
		{"not owner", database.ErrNotOwner, http.StatusForbidden, ErrCodeForbidden},
		{"quota exceeded", database.ErrQuotaExceeded, http.StatusForbidden, ErrCodeQuotaExceeded},
		{"invalid slug format", database.ErrInvalidSlugFormat, http.StatusBadRequest, ErrCodeInvalidSlug},
		{"invalid slug length", database.ErrInvalidSlugLength, http.StatusBadRequest, ErrCodeInvalidSlug},
		{"reserved slug", database.ErrReservedSlug, http.StatusBadRequest, ErrCodeReservedSlug},
		{"slug immutable", database.ErrSlugImmutable, http.StatusBadRequest, ErrCodeSlugImmutable},
		{"slug exists", database.ErrSlugExists, http.StatusConflict, ErrCodeSlugExists},
		{"display name required", database.ErrDisplayNameRequired, http.StatusBadRequest, ErrCodeDisplayNameRequired},
	}

	h := newTestHandlerWithDB(nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.writeDatabaseError(rec, tt.err)

			assert.Equal(t, tt.expectedStatus, rec.Code)
			assert.Contains(t, rec.Body.String(), tt.expectedCode)
		})
	}
}

func TestHandler_parseListOptions(t *testing.T) {
	h := newTestHandlerWithDB(nil)

	tests := []struct {
		name          string
		query         string
		expectedLimit int
		expectedOff   int
		expectedStat  database.DatabaseStatus
	}{
		{
			name:          "empty query",
			query:         "",
			expectedLimit: 0,
			expectedOff:   0,
		},
		{
			name:          "with limit",
			query:         "?limit=25",
			expectedLimit: 25,
		},
		{
			name:        "with offset",
			query:       "?offset=10",
			expectedOff: 10,
		},
		{
			name:         "with status",
			query:        "?status=active",
			expectedStat: database.StatusActive,
		},
		{
			name:          "all options",
			query:         "?limit=50&offset=20&status=suspended",
			expectedLimit: 50,
			expectedOff:   20,
			expectedStat:  database.StatusSuspended,
		},
		{
			name:          "invalid limit ignored",
			query:         "?limit=invalid",
			expectedLimit: 0,
		},
		{
			name:        "negative offset ignored",
			query:       "?offset=-5",
			expectedOff: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/databases"+tt.query, nil)
			opts := h.parseListOptions(req)

			assert.Equal(t, tt.expectedLimit, opts.Limit)
			assert.Equal(t, tt.expectedOff, opts.Offset)
			assert.Equal(t, tt.expectedStat, opts.Status)
		})
	}
}

func TestHandler_getUserID(t *testing.T) {
	h := newTestHandlerWithDB(nil)

	t.Run("user ID present", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		ctx := context.WithValue(req.Context(), identity.ContextKeyUserID, "user-123")
		req = req.WithContext(ctx)

		userID := h.getUserID(req)
		assert.Equal(t, "user-123", userID)
	})

	t.Run("user ID missing", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		userID := h.getUserID(req)
		assert.Equal(t, "", userID)
	})
}

func TestHandler_SetDatabaseService(t *testing.T) {
	mockAuth := new(MockAuthService)
	h := &Handler{auth: mockAuth}

	t.Run("set service", func(t *testing.T) {
		mockService := &fullMockDatabaseService{}
		h.SetDatabaseService(mockService)

		assert.NotNil(t, h.database)
		assert.NotNil(t, h.dbValidator)
	})

	t.Run("set nil service", func(t *testing.T) {
		h.SetDatabaseService(nil)
		assert.Nil(t, h.database)
	})
}

func TestHandler_withDatabaseValidation(t *testing.T) {
	t.Run("with validator", func(t *testing.T) {
		mockService := &fullMockDatabaseService{
			resolveFunc: func(ctx context.Context, identifier string) (*database.Database, error) {
				return &database.Database{ID: "abc123", Status: database.StatusActive}, nil
			},
		}
		mockAuth := new(MockAuthService)
		h := &Handler{
			auth:        mockAuth,
			database:    mockService,
			dbValidator: NewDatabaseValidator(mockService),
		}

		called := false
		handler := h.withDatabaseValidation(func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		})

		req := httptest.NewRequest("GET", "/test", nil)
		req.SetPathValue("database", "test-db")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)
		assert.True(t, called)
	})

	t.Run("without validator", func(t *testing.T) {
		mockAuth := new(MockAuthService)
		h := &Handler{auth: mockAuth}

		called := false
		handler := h.withDatabaseValidation(func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		})

		req := httptest.NewRequest("GET", "/test", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)
		assert.True(t, called)
	})
}

// ptr is a helper function to create a pointer to a string
func ptr(s string) *string {
	return &s
}
