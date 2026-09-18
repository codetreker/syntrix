package database

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type failingMetadataStore struct {
	DatabaseStore
	err error
}

func (s *failingMetadataStore) Get(context.Context, string) (*Database, error) { return nil, s.err }
func (s *failingMetadataStore) GetBySlug(context.Context, string) (*Database, error) {
	return nil, s.err
}

func TestServiceAuthoritativeResolutionBypassesPositiveAndNegativeCache(t *testing.T) {
	ctx := context.Background()
	store := newMockStore()
	svc := NewService(store, DefaultServiceConfig(), nil)
	slug := "application"
	_, err := svc.ResolveDatabase(ctx, slug)
	require.ErrorIs(t, err, ErrDatabaseNotFound)
	db := &Database{ID: "1111111111111111", Slug: &slug, OwnerID: "owner", Status: StatusActive}
	require.NoError(t, store.Create(ctx, db))
	resolved, err := svc.ResolveDatabaseAuthoritative(ctx, slug)
	require.NoError(t, err)
	require.Equal(t, db.ID, resolved.ID)
	_, err = svc.ResolveDatabase(ctx, "id:"+db.ID)
	require.NoError(t, err)
	updated := *db
	updated.Status = StatusSuspended
	require.NoError(t, store.Update(ctx, &updated))
	_, err = svc.ResolveDatabaseAuthoritative(ctx, "id:"+db.ID)
	require.ErrorIs(t, err, ErrDatabaseSuspended)
	_, err = svc.ResolveDatabase(ctx, "id:"+db.ID)
	require.NoError(t, err, "cached metadata remains stale")
	updated.Status = StatusDeleting
	_, err = svc.ResolveDatabaseAuthoritative(ctx, slug)
	require.ErrorIs(t, err, ErrDatabaseDeleting)
	updated.Status = DatabaseStatus("unknown")
	_, err = svc.ResolveDatabaseAuthoritative(ctx, slug)
	require.ErrorIs(t, err, ErrDatabaseNotFound)
}

func TestServiceAuthoritativeResolutionPreservesStoreFailures(t *testing.T) {
	errUnavailable := errors.New("management unavailable")
	svc := NewService(&failingMetadataStore{err: errUnavailable}, DefaultServiceConfig(), nil)
	for _, identifier := range []string{"application", "id:1111111111111111"} {
		_, err := svc.ResolveDatabaseAuthoritative(context.Background(), identifier)
		require.ErrorIs(t, err, errUnavailable)
	}
}
