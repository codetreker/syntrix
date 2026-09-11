package rest

import (
	"context"
	"net/http"

	"github.com/syntrixbase/syntrix/internal/core/identity"
	"github.com/syntrixbase/syntrix/internal/core/identity/authn"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/query"
	"github.com/syntrixbase/syntrix/pkg/model"

	"github.com/stretchr/testify/mock"
)

// MockQueryService is a mock implementation of engine.Service
type MockQueryService struct {
	mock.Mock
}

func (m *MockQueryService) GetDocument(ctx context.Context, database string, path string) (model.Document, error) {
	args := m.Called(ctx, database, path)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(model.Document), args.Error(1)
}

func (m *MockQueryService) CreateDocument(ctx context.Context, database string, doc model.Document) error {
	args := m.Called(ctx, database, doc)
	return args.Error(0)
}

func (m *MockQueryService) ReplaceDocument(ctx context.Context, database string, data model.Document, pred model.Filters) (model.Document, error) {
	args := m.Called(ctx, database, data, pred)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(model.Document), args.Error(1)
}

func (m *MockQueryService) PatchDocument(ctx context.Context, database string, data model.Document, pred model.Filters) (model.Document, error) {
	args := m.Called(ctx, database, data, pred)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(model.Document), args.Error(1)
}

func (m *MockQueryService) DeleteDocument(ctx context.Context, database string, path string, pred model.Filters) error {
	args := m.Called(ctx, database, path, pred)
	return args.Error(0)
}

func (m *MockQueryService) ExecuteQueryPage(ctx context.Context, database string, q model.Query) (model.QueryPage, error) {
	args := m.Called(ctx, database, q)
	if args.Get(0) == nil {
		return model.QueryPage{}, args.Error(1)
	}
	return args.Get(0).(model.QueryPage), args.Error(1)
}

func (m *MockQueryService) ExecuteQuery(ctx context.Context, database string, q model.Query) ([]model.Document, error) {
	args := m.Called(ctx, database, q)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]model.Document), args.Error(1)
}

func (m *MockQueryService) WatchCollection(ctx context.Context, database string, collection string) (<-chan storage.Event, error) {
	args := m.Called(ctx, database, collection)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(<-chan storage.Event), args.Error(1)
}

func (m *MockQueryService) Pull(ctx context.Context, database string, req storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
	args := m.Called(ctx, database, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.ReplicationPullResponse), args.Error(1)
}

func (m *MockQueryService) Push(ctx context.Context, database string, req storage.ReplicationPushRequest) (*storage.ReplicationPushResponse, error) {
	args := m.Called(ctx, database, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.ReplicationPushResponse), args.Error(1)
}

// MockAuthService is a mock implementation of AuthService
type MockAuthService struct {
	mock.Mock
}

func (m *MockAuthService) Middleware(next http.Handler) http.Handler {
	if len(m.ExpectedCalls) == 0 {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Database is now extracted from URL path, not context
			ctx := context.WithValue(r.Context(), identity.ContextKeyRoles, []string{"system"})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}

	args := m.Called(next)
	if handler, ok := args.Get(0).(http.Handler); ok {
		return handler
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), identity.ContextKeyRoles, []string{"system"})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (m *MockAuthService) MiddlewareOptional(next http.Handler) http.Handler {
	if len(m.ExpectedCalls) == 0 {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Database is now extracted from URL path, not context
			ctx := context.WithValue(r.Context(), identity.ContextKeyRoles, []string{"system"})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}

	args := m.Called(next)
	if handler, ok := args.Get(0).(http.Handler); ok {
		return handler
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), identity.ContextKeyRoles, []string{"system"})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (m *MockAuthService) SignIn(ctx context.Context, req identity.LoginRequest) (*identity.TokenPair, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*identity.TokenPair), args.Error(1)
}

func (m *MockAuthService) SignUp(ctx context.Context, req identity.SignupRequest) (*identity.TokenPair, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*identity.TokenPair), args.Error(1)
}

func (m *MockAuthService) Refresh(ctx context.Context, req identity.RefreshRequest) (*identity.TokenPair, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*identity.TokenPair), args.Error(1)
}

func (m *MockAuthService) ListUsers(ctx context.Context, limit int, offset int) ([]*authn.User, error) {
	args := m.Called(ctx, limit, offset)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*authn.User), args.Error(1)
}

func (m *MockAuthService) UpdateUser(ctx context.Context, id string, roles []string, dbAdmin []string, disabled bool) error {
	args := m.Called(ctx, id, roles, dbAdmin, disabled)
	return args.Error(0)
}

func (m *MockAuthService) Logout(ctx context.Context, refreshToken string) error {
	args := m.Called(ctx, refreshToken)
	return args.Error(0)
}

func (m *MockAuthService) GenerateSystemToken(serviceName string) (string, error) {
	args := m.Called(serviceName)
	return args.String(0), args.Error(1)
}

func (m *MockAuthService) ValidateToken(tokenString string) (*identity.Claims, error) {
	args := m.Called(tokenString)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*identity.Claims), args.Error(1)
}

// MockAuthzService is a mock implementation of AuthzService
type MockAuthzService struct {
	mock.Mock
}

func (m *MockAuthzService) Evaluate(ctx context.Context, database string, path string, action string, req identity.AuthzRequest, existingRes *identity.Resource) (bool, error) {
	args := m.Called(ctx, database, path, action, req, existingRes)
	return args.Bool(0), args.Error(1)
}

func (m *MockAuthzService) GetRules() *identity.RuleSet {
	args := m.Called()
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(*identity.RuleSet)
}

func (m *MockAuthzService) GetRulesForDatabase(database string) *identity.RuleSet {
	args := m.Called(database)
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(*identity.RuleSet)
}

func (m *MockAuthzService) UpdateRules(database string, content []byte) error {
	args := m.Called(database, content)
	return args.Error(0)
}

func (m *MockAuthzService) LoadRulesFromDir(dirPath string) error {
	args := m.Called(dirPath)
	return args.Error(0)
}

// AllowAllAuthzService is a simple authz service that allows all requests.
// Use this in tests that don't need to test authorization logic.
type AllowAllAuthzService struct{}

func (a *AllowAllAuthzService) Evaluate(ctx context.Context, database string, path string, action string, req identity.AuthzRequest, existingRes *identity.Resource) (bool, error) {
	return true, nil
}

func (a *AllowAllAuthzService) GetRules() *identity.RuleSet {
	return nil
}

func (a *AllowAllAuthzService) GetRulesForDatabase(database string) *identity.RuleSet {
	return nil
}

func (a *AllowAllAuthzService) UpdateRules(database string, content []byte) error {
	return nil
}

func (a *AllowAllAuthzService) LoadRulesFromDir(dirPath string) error {
	return nil
}

type TestServer struct {
	*Handler
	mux *http.ServeMux
}

func (s *TestServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// CORS headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	s.mux.ServeHTTP(w, r)
}

func createTestServer(engine query.Service, auth identity.AuthN, authz identity.AuthZ) *TestServer {
	if auth == nil {
		auth = new(MockAuthService)
	}
	if authz == nil {
		authz = new(AllowAllAuthzService)
	}

	h, _ := NewHandler(engine, auth, authz)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return &TestServer{
		Handler: h,
		mux:     mux,
	}
}
