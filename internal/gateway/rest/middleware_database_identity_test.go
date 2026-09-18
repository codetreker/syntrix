package rest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/database"
	"github.com/syntrixbase/syntrix/internal/core/identity"
	"github.com/syntrixbase/syntrix/internal/core/identity/config"
	"github.com/syntrixbase/syntrix/pkg/model"
)

type identityMetadataStore struct {
	database.DatabaseStore
	db    *database.Database
	err   error
	reads int
}

func (s *identityMetadataStore) Get(ctx context.Context, id string) (*database.Database, error) {
	s.reads++
	if s.err != nil {
		return nil, s.err
	}
	if s.db == nil || s.db.ID != id {
		return nil, database.ErrDatabaseNotFound
	}
	db := *s.db
	return &db, nil
}

func (s *identityMetadataStore) GetBySlug(ctx context.Context, slug string) (*database.Database, error) {
	if s.db != nil && s.db.Slug != nil && *s.db.Slug == slug {
		return s.Get(ctx, s.db.ID)
	}
	s.reads++
	if s.err != nil {
		return nil, s.err
	}
	return nil, database.ErrDatabaseNotFound
}

func identityDatabase(id, owner string) *database.Database {
	slug := "friendly-name"
	return &database.Database{ID: id, OwnerID: owner, Slug: &slug, Status: database.StatusActive}
}

func TestBoundRoutesRejectReassignedSlugAcrossCachedGateways(t *testing.T) {
	const oldID = "1111111111111111"
	store := &identityMetadataStore{db: identityDatabase(oldID, "owner")}
	services := []database.Service{
		database.NewService(store, database.DefaultServiceConfig(), nil),
		database.NewService(store, database.DefaultServiceConfig(), nil),
	}
	for _, svc := range services {
		db, err := svc.ResolveDatabase(context.Background(), "friendly-name")
		require.NoError(t, err)
		require.Equal(t, oldID, db.ID)
	}
	store.db = identityDatabase("2222222222222222", "owner")
	_, err := services[0].ResolveDatabaseAuthoritative(context.Background(), "friendly-name")
	require.NoError(t, err)
	for _, svc := range services {
		for _, route := range []struct{ method, path, body string }{
			{http.MethodPost, "/replication/v1/databases/friendly-name/push", `{"changes":[]}`},
			{http.MethodPost, "/api/v1/databases/friendly-name/query", `{"collection":"users"}`},
			{http.MethodGet, "/api/v1/databases/friendly-name/documents/users/alice", ""},
			{http.MethodPost, "/replication/v1/databases/friendly-name/pull", `{"collection":"users"}`},
		} {
			source := new(MockQueryService)
			auth := &pullRouteAuth{MockAuthService: new(MockAuthService), uid: "owner"}
			h, err := NewHandler(source, auth, new(AllowAllAuthzService), WithDatabaseService(svc))
			require.NoError(t, err)
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)
			r := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
			r.Header.Set(ExpectedDatabaseIdentityHeader, oldID)
			w := newPullRecorder()
			mux.ServeHTTP(w, r)
			require.Equal(t, http.StatusConflict, w.Code, route.path+": "+w.Body.String())
			assert.Contains(t, w.Body.String(), ErrCodeDatabaseIdentityMismatch)
			assert.Empty(t, source.Calls, "no document read, query, pull or write may occur")
		}
	}
}

func TestBoundIdentityUsesFreshStatusAndAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name    string
		current *database.Database
		err     error
		status  int
	}{
		{"owner changed", identityDatabase("2222222222222222", "other"), nil, 403},
		{"suspended", &database.Database{ID: "1111111111111111", OwnerID: "owner", Status: database.StatusSuspended}, nil, 403},
		{"deleting", &database.Database{ID: "1111111111111111", OwnerID: "owner", Status: database.StatusDeleting}, nil, 410},
		{"missing", nil, nil, 404},
		{"management failure", nil, errors.New("management unavailable"), 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &identityMetadataStore{db: identityDatabase("1111111111111111", "owner")}
			svc := database.NewService(store, database.DefaultServiceConfig(), nil)
			_, err := svc.ResolveDatabase(context.Background(), "id:1111111111111111")
			require.NoError(t, err)
			store.db, store.err = tc.current, tc.err
			h := &Handler{database: svc}
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.SetPathValue("database", "id:1111111111111111")
			if tc.name == "owner changed" {
				r.SetPathValue("database", "friendly-name")
			}
			r.Header.Set(ExpectedDatabaseIdentityHeader, "1111111111111111")
			r = r.WithContext(context.WithValue(r.Context(), identity.ContextKeyUserID, "owner"))
			w := httptest.NewRecorder()
			_, ok := h.resolveReplicationDatabase(w, r, true)
			require.False(t, ok)
			assert.Equal(t, tc.status, w.Code)
			assert.NotContains(t, w.Body.String(), "2222222222222222")
			assert.NotContains(t, w.Body.String(), ErrCodeDatabaseIdentityMismatch)
		})
	}
}

func TestExpectedDatabaseIdentityHeaderValidation(t *testing.T) {
	for _, values := range [][]string{nil, {""}, {"ABCDEF0123456789"}, {"abcdef012345678"}, {"abcdef0123456789,abcdef0123456789"}, {"abcdef0123456789", "abcdef0123456789"}, {" abcdef0123456789"}} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header[http.CanonicalHeaderKey(ExpectedDatabaseIdentityHeader)] = values
		_, present, err := expectedDatabaseIdentity(r)
		require.True(t, present)
		require.Error(t, err)
		h := &Handler{}
		w := httptest.NewRecorder()
		h.withBoundDatabaseIdentity(func(http.ResponseWriter, *http.Request) { t.Fatal("bound handler called") }, func(http.ResponseWriter, *http.Request) { t.Fatal("legacy handler called") })(w, r)
		require.Equal(t, 400, w.Code)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	_, present, err := expectedDatabaseIdentity(r)
	require.NoError(t, err)
	require.False(t, present)
}

func TestIdentityGatePreservesNamespaceAndUsesFreshObject(t *testing.T) {
	store := &identityMetadataStore{db: identityDatabase("1111111111111111", "old-owner")}
	svc := database.NewService(store, database.DefaultServiceConfig(), nil)
	_, err := svc.ResolveDatabase(context.Background(), "friendly-name")
	require.NoError(t, err)
	store.db = identityDatabase("2222222222222222", "new-owner")
	h := &Handler{database: svc}
	for _, namespace := range []string{"friendly-name", "id:2222222222222222"} {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.SetPathValue("database", namespace)
		r = r.WithContext(context.WithValue(r.Context(), identity.ContextKeyUserID, "new-owner"))
		w := httptest.NewRecorder()
		resolved, ok := h.resolveReplicationDatabase(w, r, true)
		require.True(t, ok, w.Body.String())
		require.Equal(t, namespace, resolved.PathValue("database"))
		db, ok := database.FromContext(resolved.Context())
		require.True(t, ok)
		require.Equal(t, "2222222222222222", db.ID)
	}
	reads := store.reads
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.SetPathValue("database", "friendly-name")
	w := httptest.NewRecorder()
	_, ok := h.resolveReplicationDatabase(w, r, true)
	require.False(t, ok)
	require.Equal(t, 401, w.Code)
	require.Equal(t, reads, store.reads)
}

func TestBoundQueryDispatchesOnlyTheValidatedIdentityAndOriginalNamespace(t *testing.T) {
	const id = "abcdef0123456789"
	for _, grant := range []string{"", id, "friendly-name"} {
		store := &identityMetadataStore{db: identityDatabase(id, "owner")}
		auth := &pullRouteAuth{MockAuthService: new(MockAuthService), uid: "owner"}
		if grant != "" {
			auth.uid = "grantee"
			auth.grants = []string{grant}
		}
		source := new(MockQueryService)
		source.On("ExecuteQueryPage", mock.Anything, "friendly-name", mock.Anything).Run(func(args mock.Arguments) {
			db, ok := database.FromContext(args.Get(0).(context.Context))
			require.True(t, ok)
			require.Equal(t, id, db.ID)
		}).Return(model.QueryPage{}, nil).Once()
		h, err := NewHandler(source, auth, new(AllowAllAuthzService), WithDatabaseService(database.NewService(store, database.DefaultServiceConfig(), nil)))
		require.NoError(t, err)
		mux := http.NewServeMux()
		h.RegisterRoutes(mux)
		r := httptest.NewRequest(http.MethodPost, "/api/v1/databases/friendly-name/query", strings.NewReader(`{"collection":"users"}`))
		r.Header.Set(ExpectedDatabaseIdentityHeader, id)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		require.Equal(t, 200, w.Code, w.Body.String())
		require.Equal(t, 1, store.reads)
		require.Equal(t, 1, auth.calls)
		source.AssertExpectations(t)
	}
}

func TestBoundGetUsesFullScopeAuthorizationWithActualAuthZ(t *testing.T) {
	const id = "abcdef0123456789"
	for _, namespace := range []string{"friendly-name", "id:" + id} {
		for _, grant := range []string{"", id, "friendly-name"} {
			t.Run(namespace+"/grant="+grant, func(t *testing.T) {
				store := &identityMetadataStore{db: identityDatabase(id, "owner")}
				source := new(MockQueryService)
				source.On("GetDocument", mock.Anything, namespace, "users/alice").Run(func(args mock.Arguments) {
					db, ok := database.FromContext(args.Get(0).(context.Context))
					require.True(t, ok)
					require.Equal(t, id, db.ID)
				}).Return(model.Document{"id": "alice"}, nil).Once()
				auth := &pullRouteAuth{MockAuthService: new(MockAuthService), uid: "owner"}
				if grant != "" {
					auth.uid = "grantee"
					auth.grants = []string{grant}
				}
				authz, err := identity.NewAuthZ(config.AuthZConfig{}, source)
				require.NoError(t, err)
				h, err := NewHandler(source, auth, authz, WithDatabaseService(database.NewService(store, database.DefaultServiceConfig(), nil)))
				require.NoError(t, err)
				mux := http.NewServeMux()
				h.RegisterRoutes(mux)
				r := httptest.NewRequest(http.MethodGet, "/api/v1/databases/"+namespace+"/documents/users/alice", nil)
				r.Header.Set(ExpectedDatabaseIdentityHeader, id)
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, r)
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				source.AssertExpectations(t)
			})
		}
	}
}

func TestReplicationAuthorizationRequiresAuthenticatedValidatedDatabase(t *testing.T) {
	for _, tc := range []struct {
		name   string
		uid    string
		db     *database.Database
		status int
	}{
		{"missing authentication", "", identityDatabase("abcdef0123456789", "owner"), http.StatusUnauthorized},
		{"missing database", "owner", nil, http.StatusInternalServerError},
		{"missing database ID", "owner", &database.Database{OwnerID: "owner"}, http.StatusInternalServerError},
		{"insufficient scope", "other", identityDatabase("abcdef0123456789", "owner"), http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			ctx := context.WithValue(r.Context(), identity.ContextKeyUserID, tc.uid)
			if tc.db != nil {
				ctx = database.WithDatabase(ctx, tc.db)
			}
			w := httptest.NewRecorder()
			require.False(t, (&Handler{}).replicationAuthorized(w, r.WithContext(ctx)))
			require.Equal(t, tc.status, w.Code)
		})
	}
}
