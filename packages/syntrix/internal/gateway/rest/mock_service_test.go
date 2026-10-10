package rest

import (
	"context"
	"net/http"

	"github.com/codetreker/syntrix/internal/core/storage"
	"github.com/codetreker/syntrix/internal/gateway/authorization"
	"github.com/codetreker/syntrix/internal/identity"
	"github.com/codetreker/syntrix/internal/query"
	"github.com/codetreker/syntrix/pkg/model"
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

func (m *MockAuthService) ListUsers(ctx context.Context, actor *identity.VerifiedIdentity, limit int, offset int) ([]*identity.User, error) {
	args := m.Called(ctx, actor, limit, offset)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*identity.User), args.Error(1)
}

func (m *MockAuthService) UpdateUser(ctx context.Context, actor *identity.VerifiedIdentity, id string, roles []string, dbAdmin []string, disabled bool) error {
	args := m.Called(ctx, actor, id, roles, dbAdmin, disabled)
	return args.Error(0)
}

func (m *MockAuthService) Logout(ctx context.Context, refreshToken string) error {
	args := m.Called(ctx, refreshToken)
	return args.Error(0)
}

// MockAuthzService is a mock implementation of AuthzService
type MockAuthzService struct {
	mock.Mock
}

func (m *MockAuthzService) Evaluate(ctx context.Context, database string, path string, action string, req authorization.Request, existingRes *authorization.Resource) (bool, error) {
	args := m.Called(ctx, database, path, action, req, existingRes)
	return args.Bool(0), args.Error(1)
}

func (m *MockAuthzService) GetRules() *authorization.RuleSet {
	args := m.Called()
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(*authorization.RuleSet)
}

func (m *MockAuthzService) GetRulesForDatabase(database string) *authorization.RuleSet {
	args := m.Called(database)
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(*authorization.RuleSet)
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

func (a *AllowAllAuthzService) Evaluate(ctx context.Context, database string, path string, action string, req authorization.Request, existingRes *authorization.Resource) (bool, error) {
	return true, nil
}

func (a *AllowAllAuthzService) GetRules() *authorization.RuleSet {
	return nil
}

func (a *AllowAllAuthzService) GetRulesForDatabase(database string) *authorization.RuleSet {
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

type testIdentityService interface {
	identity.AccountService
	identity.TokenVerifier
}

func createTestServer(engine query.Service, auth testIdentityService, authz authorization.Engine) *TestServer {
	if auth == nil {
		auth = new(MockAuthService)
	}
	if authz == nil {
		authz = new(AllowAllAuthzService)
	}

	h, _ := NewHandler(engine, auth, auth, authz)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return &TestServer{
		Handler: h,
		mux:     mux,
	}
}

func (m *MockAuthService) VerifyToken(token string) (*identity.VerifiedIdentity, error) {
	for _, call := range m.ExpectedCalls {
		if call.Method == "VerifyToken" {
			args := m.Called(token)
			if args.Get(0) == nil {
				return nil, args.Error(1)
			}
			return args.Get(0).(*identity.VerifiedIdentity), args.Error(1)
		}
	}
	verifier, _ := identity.NewVerifier(func(string) (*identity.Claims, error) { return &identity.Claims{Roles: []string{"system"}}, nil })
	return verifier.VerifyToken(token)
}
