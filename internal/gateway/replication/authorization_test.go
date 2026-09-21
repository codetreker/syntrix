package replication

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/database"
	"github.com/syntrixbase/syntrix/pkg/model"
)

type databaseResolver struct {
	database.Service
	resolve func(context.Context, string, bool) (*database.Database, error)
}

func (r databaseResolver) ResolveDatabase(ctx context.Context, namespace string) (*database.Database, error) {
	return r.resolve(ctx, namespace, false)
}
func (r databaseResolver) ResolveDatabaseAuthoritative(ctx context.Context, namespace string) (*database.Database, error) {
	return r.resolve(ctx, namespace, true)
}

func TestAuthorizeDatabaseGrantAndResolutionMode(t *testing.T) {
	const id = "0123456789abcdef"
	slug := "friendly-name"
	resolved := &database.Database{ID: id, Slug: &slug, OwnerID: "owner", Status: database.StatusActive}
	for _, tc := range []struct {
		name          string
		principal     Principal
		expected      *string
		authoritative bool
		wantFresh     bool
		status        int
	}{
		{"legacy owner", Principal{Subject: "owner"}, nil, false, false, 200},
		{"fresh owner", Principal{Subject: "owner"}, nil, true, true, 200},
		{"bound forces fresh", Principal{Subject: "owner"}, ptr(id), false, true, 200},
		{"ID grant", Principal{Subject: "user", DBAdmin: []string{id}}, nil, true, true, 200},
		{"slug grant", Principal{Subject: "user", DBAdmin: []string{slug}}, ptr(id), true, true, 200},
		{"wrong grant", Principal{Subject: "user", DBAdmin: []string{"other"}}, nil, true, true, 403},
		{"URL spelling is not grant", Principal{Subject: "user", DBAdmin: []string{"id:" + id}}, nil, true, true, 403},
		{"empty grant", Principal{Subject: "user", DBAdmin: []string{""}}, nil, true, true, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), struct{}{}, "context preserved")
			calls := 0
			service := databaseResolver{resolve: func(actual context.Context, namespace string, fresh bool) (*database.Database, error) {
				calls++
				require.Same(t, ctx, actual)
				require.Equal(t, "id:"+id, namespace)
				require.Equal(t, tc.wantFresh, fresh)
				return resolved, nil
			}}
			got, err := AuthorizeDatabase(ctx, service, "id:"+id, tc.principal, tc.expected, tc.authoritative)
			require.Equal(t, 1, calls)
			if tc.status == 200 {
				require.NoError(t, err)
				require.Same(t, resolved, got)
			} else {
				require.Nil(t, got)
				var failure *Failure
				require.ErrorAs(t, err, &failure)
				require.Equal(t, tc.status, failure.Status)
			}
		})
	}
}

func ptr(value string) *string { return &value }

func TestAuthorizeDatabaseRejectsBeforeResolutionAndProtectsReplacementIdentity(t *testing.T) {
	noRead := databaseResolver{resolve: func(context.Context, string, bool) (*database.Database, error) {
		t.Fatal("request must be rejected before resolution")
		return nil, nil
	}}
	for _, tc := range []struct {
		principal Principal
		expected  *string
		namespace string
		service   database.Service
		status    int
	}{
		{Principal{}, nil, "app", noRead, 401},
		{Principal{Subject: "owner"}, nil, "app", nil, 500},
		{Principal{Subject: "owner"}, nil, "", noRead, 400},
		{Principal{Subject: "owner"}, ptr(""), "app", noRead, 400},
		{Principal{Subject: "owner"}, ptr("ABCDEF0123456789"), "app", noRead, 400},
	} {
		_, err := AuthorizeDatabase(context.Background(), tc.service, tc.namespace, tc.principal, tc.expected, true)
		var failure *Failure
		require.ErrorAs(t, err, &failure)
		require.Equal(t, tc.status, failure.Status)
	}
	for _, db := range []*database.Database{nil, {OwnerID: "owner"}} {
		service := databaseResolver{resolve: func(context.Context, string, bool) (*database.Database, error) { return db, nil }}
		_, err := AuthorizeDatabase(context.Background(), service, "app", Principal{Subject: "owner"}, nil, true)
		var failure *Failure
		require.ErrorAs(t, err, &failure)
		require.Equal(t, 500, failure.Status)
	}
	service := databaseResolver{resolve: func(context.Context, string, bool) (*database.Database, error) {
		return &database.Database{ID: "fedcba9876543210", OwnerID: "new-owner", Status: database.StatusActive}, nil
	}}
	for _, principal := range []Principal{{Subject: "old-owner"}, {Subject: "new-owner"}} {
		_, err := AuthorizeDatabase(context.Background(), service, "app", principal, ptr("0123456789abcdef"), true)
		var failure *Failure
		require.ErrorAs(t, err, &failure)
		if principal.Subject == "old-owner" {
			require.Equal(t, "FORBIDDEN", failure.Code)
			require.Equal(t, http.StatusForbidden, failure.Status)
		} else {
			require.Equal(t, "DATABASE_IDENTITY_MISMATCH", failure.Code)
			require.Equal(t, http.StatusConflict, failure.Status)
		}
		require.NotContains(t, failure.Error(), "fedcba9876543210")
	}
}

func TestAuthorizeDatabasePreservesDomainErrorsAndCauses(t *testing.T) {
	for _, tc := range []struct {
		cause  error
		status int
		code   string
	}{
		{database.ErrDatabaseNotFound, 404, "DATABASE_NOT_FOUND"},
		{database.ErrDatabaseSuspended, 403, "DATABASE_SUSPENDED"},
		{database.ErrDatabaseDeleting, 410, "DATABASE_DELETING"},
		{context.Canceled, 499, ""},
		{context.DeadlineExceeded, 499, ""},
		{model.ErrCanceled, 499, ""},
		{errors.New("management unavailable"), 500, "INTERNAL_ERROR"},
	} {
		t.Run(fmt.Sprint(tc.cause), func(t *testing.T) {
			cause := fmt.Errorf("management lookup: %w", tc.cause)
			service := databaseResolver{resolve: func(context.Context, string, bool) (*database.Database, error) { return nil, cause }}
			_, err := AuthorizeDatabase(context.Background(), service, "app", Principal{Subject: "owner"}, nil, true)
			var failure *Failure
			require.ErrorAs(t, err, &failure)
			require.Equal(t, tc.status, failure.Status)
			require.Equal(t, tc.code, failure.Code)
			require.ErrorIs(t, err, tc.cause)
			require.Same(t, cause, failure.Cause)
			require.NotContains(t, failure.Error(), "management lookup")
		})
	}
}
