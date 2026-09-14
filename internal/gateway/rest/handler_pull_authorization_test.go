package rest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/database"
	"github.com/syntrixbase/syntrix/internal/core/identity"
	"github.com/syntrixbase/syntrix/internal/core/storage"
)

type pullRouteAuth struct {
	*MockAuthService
	uid           string
	roles, grants []string
	calls         int
}

func (auth *pullRouteAuth) Middleware(next http.Handler) http.Handler {
	auth.calls++
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), identity.ContextKeyUserID, auth.uid)
		ctx = context.WithValue(ctx, identity.ContextKeyRoles, auth.roles)
		ctx = context.WithValue(ctx, identity.ContextKeyDBAdmin, auth.grants)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func TestPullRouteFullScopeAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name, uid     string
		roles, grants []string
		status        int
	}{
		{name: "no identity", grants: []string{"canonical-id"}, status: 401},
		{name: "ordinary user", uid: "user", roles: []string{"user"}, status: 403},
		{name: "admin role alone", uid: "user", roles: []string{"admin"}, status: 403},
		{name: "wrong scope", uid: "user", grants: []string{"another-database"}, status: 403},
		{name: "URL spelling is not grant", uid: "user", grants: []string{"id:canonical-id"}, status: 403},
		{name: "matching canonical ID", uid: "user", grants: []string{"canonical-id"}, status: 200},
		{name: "matching validated slug", uid: "user", grants: []string{"friendly-name"}, status: 200},
		{name: "database owner", uid: "owner", status: 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := new(MockQueryService)
			auth := &pullRouteAuth{MockAuthService: new(MockAuthService), uid: tc.uid, roles: tc.roles, grants: tc.grants}
			handler, err := NewHandler(source, auth, new(AllowAllAuthzService))
			require.NoError(t, err)
			slug := "friendly-name"
			handler.SetDatabaseService(&mockDatabaseService{resolveFunc: func(ctx context.Context, identifier string) (*database.Database, error) {
				assert.Equal(t, "id:canonical-id", identifier)
				assert.Equal(t, tc.uid, ctx.Value(identity.ContextKeyUserID))
				return &database.Database{ID: "canonical-id", Slug: &slug, OwnerID: "owner", Status: database.StatusActive}, nil
			}})
			if tc.status == 200 {
				source.On("Pull", mock.Anything, "id:canonical-id", storage.ReplicationPullRequest{Collection: "users", Limit: 100, DatabaseIdentity: "canonical-id"}).Run(func(args mock.Arguments) {
					deadline, ok := args.Get(0).(context.Context).Deadline()
					require.True(t, ok)
					assert.WithinDuration(t, time.Now().Add(30*time.Second), deadline, time.Second)
				}).Return(&storage.ReplicationPullResponse{Checkpoint: "next", CaughtUp: true}, nil).Once()
			}
			mux := http.NewServeMux()
			handler.RegisterRoutes(mux)
			request := httptest.NewRequest(http.MethodPost, "/replication/v1/databases/id:canonical-id/pull", strings.NewReader(`{"collection":"users"}`))
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, request)
			assert.Equal(t, tc.status, rr.Code, rr.Body.String())
			assert.Equal(t, 1, auth.calls)
			if tc.status != 200 {
				source.AssertNotCalled(t, "Pull", mock.Anything, mock.Anything, mock.Anything)
			}
			source.AssertExpectations(t)
		})
	}
}

func TestPullRouteRejectsLegacyGET(t *testing.T) {
	source := new(MockQueryService)
	auth := &pullRouteAuth{MockAuthService: new(MockAuthService), uid: "user", grants: []string{"canonical-id"}}
	server := createTestServer(source, auth, nil)
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/replication/v1/databases/default/pull?collection=users&checkpoint=0", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	assert.Contains(t, rr.Header().Get("Allow"), http.MethodPost)
	assert.Zero(t, auth.calls)
	source.AssertNotCalled(t, "Pull", mock.Anything, mock.Anything, mock.Anything)
}

func TestPullAuthorizationRequiresValidatedScope(t *testing.T) {
	source := new(MockQueryService)
	auth := &pullRouteAuth{MockAuthService: new(MockAuthService), uid: "user", grants: []string{"canonical-id"}}
	server := createTestServer(source, auth, nil)
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/replication/v1/databases/canonical-id/pull", strings.NewReader(`{"collection":"users"}`)))
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	source.AssertNotCalled(t, "Pull", mock.Anything, mock.Anything, mock.Anything)
}
