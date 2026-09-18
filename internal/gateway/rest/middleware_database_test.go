package rest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/database"
)

// mockDatabaseService implements database.Service for testing
type mockDatabaseService struct {
	resolveFunc func(ctx context.Context, identifier string) (*database.Database, error)
}

func (m *mockDatabaseService) CreateDatabase(ctx context.Context, userID string, isAdmin bool, req database.CreateRequest) (*database.Database, error) {
	return nil, nil
}

func (m *mockDatabaseService) ListDatabases(ctx context.Context, userID string, isAdmin bool, opts database.ListOptions) (*database.ListResult, error) {
	return nil, nil
}

func (m *mockDatabaseService) GetDatabase(ctx context.Context, userID string, isAdmin bool, identifier string) (*database.Database, error) {
	return nil, nil
}

func (m *mockDatabaseService) UpdateDatabase(ctx context.Context, userID string, isAdmin bool, identifier string, req database.UpdateRequest) (*database.Database, error) {
	return nil, nil
}

func (m *mockDatabaseService) DeleteDatabase(ctx context.Context, userID string, isAdmin bool, identifier string) error {
	return nil
}

func (m *mockDatabaseService) ResolveDatabase(ctx context.Context, identifier string) (*database.Database, error) {
	if m.resolveFunc != nil {
		return m.resolveFunc(ctx, identifier)
	}
	return nil, database.ErrDatabaseNotFound
}

func (m *mockDatabaseService) ResolveDatabaseAuthoritative(ctx context.Context, identifier string) (*database.Database, error) {
	return m.ResolveDatabase(ctx, identifier)
}

func (m *mockDatabaseService) ValidateDatabase(ctx context.Context, identifier string) error {
	_, err := m.ResolveDatabase(ctx, identifier)
	return err
}

func TestDatabaseValidator_Middleware(t *testing.T) {
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
		pathDatabase   string
		resolveFunc    func(ctx context.Context, identifier string) (*database.Database, error)
		expectedStatus int
		expectedCode   string
		checkContext   bool
	}{
		{
			name:         "active database passes through",
			pathDatabase: "test-db",
			resolveFunc: func(ctx context.Context, identifier string) (*database.Database, error) {
				return testDB, nil
			},
			expectedStatus: http.StatusOK,
			checkContext:   true,
		},
		{
			name:         "database by id prefix",
			pathDatabase: "id:abc123",
			resolveFunc: func(ctx context.Context, identifier string) (*database.Database, error) {
				if identifier == "id:abc123" {
					return testDB, nil
				}
				return nil, database.ErrDatabaseNotFound
			},
			expectedStatus: http.StatusOK,
			checkContext:   true,
		},
		{
			name:         "database not found",
			pathDatabase: "nonexistent",
			resolveFunc: func(ctx context.Context, identifier string) (*database.Database, error) {
				return nil, database.ErrDatabaseNotFound
			},
			expectedStatus: http.StatusNotFound,
			expectedCode:   ErrCodeDatabaseNotFound,
		},
		{
			name:         "database suspended",
			pathDatabase: "suspended-db",
			resolveFunc: func(ctx context.Context, identifier string) (*database.Database, error) {
				return nil, database.ErrDatabaseSuspended
			},
			expectedStatus: http.StatusForbidden,
			expectedCode:   ErrCodeDatabaseSuspended,
		},
		{
			name:         "database deleting",
			pathDatabase: "deleting-db",
			resolveFunc: func(ctx context.Context, identifier string) (*database.Database, error) {
				return nil, database.ErrDatabaseDeleting
			},
			expectedStatus: http.StatusGone,
			expectedCode:   ErrCodeDatabaseDeleting,
		},
		{
			name:           "missing database identifier",
			pathDatabase:   "",
			expectedStatus: http.StatusBadRequest,
			expectedCode:   ErrCodeBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockService := &mockDatabaseService{
				resolveFunc: tt.resolveFunc,
			}
			validator := NewDatabaseValidator(mockService)

			var capturedDB *database.Database
			handler := validator.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Capture database from context
				db, ok := database.FromContext(r.Context())
				if ok {
					capturedDB = db
				}
				w.WriteHeader(http.StatusOK)
			}))

			// Create request with path value
			req := httptest.NewRequest("GET", "/api/v1/databases/"+tt.pathDatabase+"/documents/test", nil)
			req.SetPathValue("database", tt.pathDatabase)

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, tt.expectedStatus, rec.Code)

			if tt.checkContext {
				require.NotNil(t, capturedDB)
				assert.Equal(t, testDB.ID, capturedDB.ID)
			}

			if tt.expectedCode != "" {
				assert.Contains(t, rec.Body.String(), tt.expectedCode)
			}
		})
	}
}

func TestDatabaseValidator_MiddlewareFunc(t *testing.T) {
	slug := "test-db"
	testDB := &database.Database{
		ID:          "abc123",
		Slug:        &slug,
		DisplayName: "Test Database",
		OwnerID:     "user-123",
		Status:      database.StatusActive,
	}

	mockService := &mockDatabaseService{
		resolveFunc: func(ctx context.Context, identifier string) (*database.Database, error) {
			return testDB, nil
		},
	}
	validator := NewDatabaseValidator(mockService)

	called := false
	handler := validator.MiddlewareFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/api/v1/databases/test-db/documents/test", nil)
	req.SetPathValue("database", "test-db")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.True(t, called)
	assert.Equal(t, http.StatusOK, rec.Code)
}
