package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/database"
	"github.com/codetreker/syntrix/internal/ctxkeys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestAdminHandler(dbService database.Service) *Handler {
	mockAuth := new(MockAuthService)
	h := &Handler{
		auth:     mockAuth,
		database: dbService,
	}
	return h
}

func addAdminContext(req *http.Request) *http.Request {
	ctx := context.WithValue(req.Context(), ctxkeys.KeyUserID, "admin-user")
	ctx = context.WithValue(ctx, ctxkeys.KeyRoles, []string{"admin"})
	return req.WithContext(ctx)
}

func TestHandler_handleAdminCreateDatabase(t *testing.T) {
	slug := "admin-db"
	testDB := &database.Database{
		ID:          "abc123",
		Slug:        &slug,
		DisplayName: "Admin Database",
		OwnerID:     "other-user",
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
			name: "successful create with owner",
			body: CreateDatabaseRequest{
				DisplayName: "Admin Database",
				Slug:        &slug,
				OwnerID:     "other-user",
			},
			setupMock: func(m *fullMockDatabaseService) {
				m.createFunc = func(ctx context.Context, userID string, isAdmin bool, req database.CreateRequest) (*database.Database, error) {
					assert.True(t, isAdmin)
					assert.Equal(t, "other-user", req.OwnerID)
					return testDB, nil
				}
			},
			expectedStatus: http.StatusCreated,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var resp DatabaseResponse
				err := json.Unmarshal(rec.Body.Bytes(), &resp)
				require.NoError(t, err)
				assert.Equal(t, "abc123", resp.ID)
				assert.Equal(t, "other-user", resp.OwnerID)
			},
		},
		{
			name: "create with settings",
			body: CreateDatabaseRequest{
				DisplayName: "Quota DB",
				Settings: &DatabaseSettingsRequest{
					MaxDocuments:    ptrInt64(10000),
					MaxStorageBytes: ptrInt64(1024 * 1024 * 100),
				},
			},
			setupMock: func(m *fullMockDatabaseService) {
				m.createFunc = func(ctx context.Context, userID string, isAdmin bool, req database.CreateRequest) (*database.Database, error) {
					assert.NotNil(t, req.Settings)
					assert.Equal(t, int64(10000), req.Settings.MaxDocuments)
					return testDB, nil
				}
			},
			expectedStatus: http.StatusCreated,
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
				// Service is nil
			},
			expectedStatus: http.StatusServiceUnavailable,
		},
		{
			name: "slug exists",
			body: CreateDatabaseRequest{DisplayName: "Test", Slug: ptr("existing-slug")},
			setupMock: func(m *fullMockDatabaseService) {
				m.createFunc = func(ctx context.Context, userID string, isAdmin bool, req database.CreateRequest) (*database.Database, error) {
					return nil, database.ErrSlugExists
				}
			},
			expectedStatus: http.StatusConflict,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockService := &fullMockDatabaseService{}
			tt.setupMock(mockService)

			var h *Handler
			if tt.name == "database service not available" {
				h = newTestAdminHandler(nil)
			} else {
				h = newTestAdminHandler(mockService)
			}

			body, _ := json.Marshal(tt.body)
			req := httptest.NewRequest("POST", "/admin/databases", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer test")
			req.Header.Set("Content-Type", "application/json")
			req = addAdminContext(req)

			rec := httptest.NewRecorder()
			h.handleAdminCreateDatabase(rec, req)

			assert.Equal(t, tt.expectedStatus, rec.Code)
			if tt.checkResponse != nil {
				tt.checkResponse(t, rec)
			}
		})
	}
}

func TestHandler_handleAdminListDatabases(t *testing.T) {
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
			name:  "list all databases",
			query: "",
			setupMock: func(m *fullMockDatabaseService) {
				m.listFunc = func(ctx context.Context, userID string, isAdmin bool, opts database.ListOptions) (*database.ListResult, error) {
					assert.True(t, isAdmin)
					return &database.ListResult{
						Databases: []*database.Database{testDB},
						Total:     1,
					}, nil
				}
			},
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var resp ListDatabasesResponse
				err := json.Unmarshal(rec.Body.Bytes(), &resp)
				require.NoError(t, err)
				assert.Equal(t, 1, resp.Total)
			},
		},
		{
			name:  "with owner filter",
			query: "?owner=specific-user",
			setupMock: func(m *fullMockDatabaseService) {
				m.listFunc = func(ctx context.Context, userID string, isAdmin bool, opts database.ListOptions) (*database.ListResult, error) {
					assert.Equal(t, "specific-user", opts.OwnerID)
					return &database.ListResult{Databases: []*database.Database{}, Total: 0}, nil
				}
			},
			expectedStatus: http.StatusOK,
		},
		{
			name:  "with pagination",
			query: "?limit=50&offset=100",
			setupMock: func(m *fullMockDatabaseService) {
				m.listFunc = func(ctx context.Context, userID string, isAdmin bool, opts database.ListOptions) (*database.ListResult, error) {
					assert.Equal(t, 50, opts.Limit)
					assert.Equal(t, 100, opts.Offset)
					return &database.ListResult{Databases: []*database.Database{}, Total: 0}, nil
				}
			},
			expectedStatus: http.StatusOK,
		},
		{
			name:  "list error returns internal error",
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

			h := newTestAdminHandler(mockService)

			req := httptest.NewRequest("GET", "/admin/databases"+tt.query, nil)
			req.Header.Set("Authorization", "Bearer test")
			req = addAdminContext(req)

			rec := httptest.NewRecorder()
			h.handleAdminListDatabases(rec, req)

			assert.Equal(t, tt.expectedStatus, rec.Code)
			if tt.checkResponse != nil {
				tt.checkResponse(t, rec)
			}
		})
	}
}

func TestHandler_handleAdminGetDatabase(t *testing.T) {
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
			name:       "get any database",
			identifier: "test-db",
			setupMock: func(m *fullMockDatabaseService) {
				m.getFunc = func(ctx context.Context, userID string, isAdmin bool, identifier string) (*database.Database, error) {
					assert.True(t, isAdmin)
					return testDB, nil
				}
			},
			expectedStatus: http.StatusOK,
		},
		{
			name:       "get by id",
			identifier: "id:abc123",
			setupMock: func(m *fullMockDatabaseService) {
				m.getFunc = func(ctx context.Context, userID string, isAdmin bool, identifier string) (*database.Database, error) {
					assert.Equal(t, "id:abc123", identifier)
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

			h := newTestAdminHandler(mockService)

			req := httptest.NewRequest("GET", "/admin/databases/"+tt.identifier, nil)
			req.Header.Set("Authorization", "Bearer test")
			req.SetPathValue("identifier", tt.identifier)
			req = addAdminContext(req)

			rec := httptest.NewRecorder()
			h.handleAdminGetDatabase(rec, req)

			assert.Equal(t, tt.expectedStatus, rec.Code)
		})
	}
}

func TestHandler_handleAdminUpdateDatabase(t *testing.T) {
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
			name:       "update display name",
			identifier: "test-db",
			body:       UpdateDatabaseRequest{DisplayName: ptr("Updated Database")},
			setupMock: func(m *fullMockDatabaseService) {
				m.updateFunc = func(ctx context.Context, userID string, isAdmin bool, identifier string, req database.UpdateRequest) (*database.Database, error) {
					assert.True(t, isAdmin)
					return testDB, nil
				}
			},
			expectedStatus: http.StatusOK,
		},
		{
			name:       "update status (admin only)",
			identifier: "test-db",
			body:       UpdateDatabaseRequest{Status: ptr("suspended")},
			setupMock: func(m *fullMockDatabaseService) {
				m.updateFunc = func(ctx context.Context, userID string, isAdmin bool, identifier string, req database.UpdateRequest) (*database.Database, error) {
					require.NotNil(t, req.Status)
					assert.Equal(t, database.DatabaseStatus("suspended"), *req.Status)
					return testDB, nil
				}
			},
			expectedStatus: http.StatusOK,
		},
		{
			name:       "update settings (admin only)",
			identifier: "test-db",
			body: UpdateDatabaseRequest{
				Settings: &DatabaseSettingsRequest{
					MaxDocuments: ptrInt64(50000),
				},
			},
			setupMock: func(m *fullMockDatabaseService) {
				m.updateFunc = func(ctx context.Context, userID string, isAdmin bool, identifier string, req database.UpdateRequest) (*database.Database, error) {
					require.NotNil(t, req.Settings)
					assert.Equal(t, int64(50000), req.Settings.MaxDocuments)
					return testDB, nil
				}
			},
			expectedStatus: http.StatusOK,
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

			h := newTestAdminHandler(mockService)

			body, _ := json.Marshal(tt.body)
			req := httptest.NewRequest("PATCH", "/admin/databases/"+tt.identifier, bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer test")
			req.SetPathValue("identifier", tt.identifier)
			req.Header.Set("Content-Type", "application/json")
			req = addAdminContext(req)

			rec := httptest.NewRecorder()
			h.handleAdminUpdateDatabase(rec, req)

			assert.Equal(t, tt.expectedStatus, rec.Code)
		})
	}
}

func TestHandler_handleAdminDeleteDatabase(t *testing.T) {
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
					assert.True(t, isAdmin)
					return nil
				}
			},
			expectedStatus: http.StatusOK,
		},
		{
			name:       "delete protected database",
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

			h := newTestAdminHandler(mockService)

			req := httptest.NewRequest("DELETE", "/admin/databases/"+tt.identifier, nil)
			req.Header.Set("Authorization", "Bearer test")
			req.SetPathValue("identifier", tt.identifier)
			req = addAdminContext(req)

			rec := httptest.NewRecorder()
			h.handleAdminDeleteDatabase(rec, req)

			assert.Equal(t, tt.expectedStatus, rec.Code)
		})
	}
}

// ptrInt64 is a helper function to create a pointer to an int64
func ptrInt64(i int64) *int64 {
	return &i
}
