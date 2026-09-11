package router

import (
	"context"
	"fmt"
	"time"

	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/pkg/model"
)

// RoutedDocumentStore implements DocumentStore by routing operations
type RoutedDocumentStore struct {
	router types.DocumentRouter
}

func NewRoutedDocumentStore(router types.DocumentRouter) types.DocumentStore {
	return &RoutedDocumentStore{router: router}
}

func (s *RoutedDocumentStore) Get(ctx context.Context, database string, path string, opts ...types.ReadOptions) (*types.StoredDoc, error) {
	readOpts, err := types.ResolveReadOptions(opts)
	if err != nil {
		return nil, err
	}
	op := types.OpRead
	if readOpts.Consistency == types.ReadAuthoritative {
		op = types.OpWrite
	}
	store, err := s.router.Select(database, op)
	if err != nil {
		return nil, err
	}
	return store.Get(ctx, database, path, opts...)
}

func (s *RoutedDocumentStore) GetMany(ctx context.Context, database string, paths []string, opts ...types.ReadOptions) ([]*types.StoredDoc, error) {
	readOpts, err := types.ResolveReadOptions(opts)
	if err != nil {
		return nil, err
	}
	op := types.OpRead
	if readOpts.Consistency == types.ReadAuthoritative {
		op = types.OpWrite
	}
	store, err := s.router.Select(database, op)
	if err != nil {
		return nil, err
	}
	return store.GetMany(ctx, database, paths, opts...)
}

func (s *RoutedDocumentStore) ScanDocuments(ctx context.Context, database string, request types.SourceScanRequest) (types.SourceScanPage, error) {
	if err := request.Validate(database); err != nil {
		return types.SourceScanPage{}, err
	}
	op := types.OpRead
	if request.Consistency == types.ReadAuthoritative {
		op = types.OpWrite
	}
	store, err := s.router.Select(database, op)
	if err != nil {
		return types.SourceScanPage{}, err
	}
	scanner, ok := store.(types.DocumentScanner)
	if !ok {
		return types.SourceScanPage{}, fmt.Errorf("document source does not support bounded scanning")
	}
	return scanner.ScanDocuments(ctx, database, request)
}

func (s *RoutedDocumentStore) EnumerateCollections(ctx context.Context, database, afterCollection string, limit int, opts ...types.CollectionEnumerationOptions) ([]string, error) {
	if _, err := types.ResolveCollectionEnumerationOptions(database, afterCollection, limit, opts); err != nil {
		return nil, err
	}
	store, err := s.router.Select(database, types.OpWrite)
	if err != nil {
		return nil, err
	}
	enumerator, ok := store.(types.DocumentCollectionEnumerator)
	if !ok {
		return nil, fmt.Errorf("document source does not support collection enumeration")
	}
	return enumerator.EnumerateCollections(ctx, database, afterCollection, limit, opts...)
}

func (s *RoutedDocumentStore) Create(ctx context.Context, database string, doc types.StoredDoc) error {
	store, err := s.router.Select(database, types.OpWrite)
	if err != nil {
		return err
	}
	return store.Create(ctx, database, doc)
}

func (s *RoutedDocumentStore) Update(ctx context.Context, database string, path string, data map[string]interface{}, pred model.Filters) error {
	store, err := s.router.Select(database, types.OpWrite)
	if err != nil {
		return err
	}
	return store.Update(ctx, database, path, data, pred)
}

func (s *RoutedDocumentStore) Patch(ctx context.Context, database string, path string, data map[string]interface{}, pred model.Filters) error {
	store, err := s.router.Select(database, types.OpWrite)
	if err != nil {
		return err
	}
	return store.Patch(ctx, database, path, data, pred)
}

func (s *RoutedDocumentStore) Delete(ctx context.Context, database string, path string, pred model.Filters) error {
	store, err := s.router.Select(database, types.OpWrite)
	if err != nil {
		return err
	}
	return store.Delete(ctx, database, path, pred)
}

func (s *RoutedDocumentStore) DeleteByDatabase(ctx context.Context, database string, limit int) (int, error) {
	store, err := s.router.Select(database, types.OpWrite)
	if err != nil {
		return 0, err
	}
	return store.DeleteByDatabase(ctx, database, limit)
}

func (s *RoutedDocumentStore) Query(ctx context.Context, database string, q model.Query) ([]*types.StoredDoc, error) {
	store, err := s.router.Select(database, types.OpRead)
	if err != nil {
		return nil, err
	}
	return store.Query(ctx, database, q)
}

func (s *RoutedDocumentStore) Watch(ctx context.Context, database string, collection string, after types.WatchCheckpoint, opts types.WatchOptions) (types.WatchStream, error) {
	if database == "" {
		return nil, &types.WatchError{
			Code:       types.WatchInvalidScope,
			Database:   database,
			Collection: collection,
			Cause:      ErrDatabaseRequired,
		}
	}
	store, err := s.router.Select(database, types.OpWatch)
	if err != nil {
		return nil, err
	}
	return store.Watch(ctx, database, collection, after, opts)
}

func (s *RoutedDocumentStore) Close(ctx context.Context) error {
	// We don't close the underlying store here as it might be shared.
	// The Provider manages lifecycle.
	return nil
}

// RoutedUserStore implements UserStore by routing operations
type RoutedUserStore struct {
	router types.UserRouter
}

func NewRoutedUserStore(router types.UserRouter) types.UserStore {
	return &RoutedUserStore{router: router}
}

func (s *RoutedUserStore) CreateUser(ctx context.Context, user *types.User) error {
	// Global user creation - use default database for routing
	store, err := s.router.Select("default", types.OpWrite)
	if err != nil {
		return err
	}
	return store.CreateUser(ctx, user)
}

func (s *RoutedUserStore) GetUserByUsername(ctx context.Context, username string) (*types.User, error) {
	// Global user lookup - use default database for routing
	store, err := s.router.Select("default", types.OpRead)
	if err != nil {
		return nil, err
	}
	return store.GetUserByUsername(ctx, username)
}

func (s *RoutedUserStore) GetUserByID(ctx context.Context, id string) (*types.User, error) {
	// For global user lookup by ID, we use default database routing
	store, err := s.router.Select("default", types.OpRead)
	if err != nil {
		return nil, err
	}
	return store.GetUserByID(ctx, id)
}

func (s *RoutedUserStore) ListUsers(ctx context.Context, limit int, offset int) ([]*types.User, error) {
	// Global user list operation
	store, err := s.router.Select("default", types.OpRead)
	if err != nil {
		return nil, err
	}
	return store.ListUsers(ctx, limit, offset)
}

func (s *RoutedUserStore) UpdateUser(ctx context.Context, user *types.User) error {
	// Global user update operation
	store, err := s.router.Select("default", types.OpWrite)
	if err != nil {
		return err
	}
	return store.UpdateUser(ctx, user)
}

func (s *RoutedUserStore) UpdateUserLoginStats(ctx context.Context, id string, lastLogin time.Time, attempts int, lockoutUntil time.Time) error {
	// Global user update - use default database for routing
	store, err := s.router.Select("default", types.OpWrite)
	if err != nil {
		return err
	}
	return store.UpdateUserLoginStats(ctx, id, lastLogin, attempts, lockoutUntil)
}

func (s *RoutedUserStore) EnsureIndexes(ctx context.Context) error {
	// Ensure indexes on all backends? Or just default?
	// Ideally we should iterate all databases/backends.
	// But Select requires database.
	// For now, let's assume we call it for "default" database or we need a way to iterate.
	// The interface `EnsureIndexes(ctx)` doesn't take database.
	// Wait, `UserStore.EnsureIndexes` DOES NOT take database in my previous update?
	// Let's check `types.go`.
	// `EnsureIndexes(ctx context.Context) error`
	// It does NOT take database.
	// This is a problem if we have multiple backends.
	// But `EnsureIndexes` is usually called at startup.
	// If we have multiple backends, we need to ensure indexes on ALL of them.
	// The `RoutedUserStore` should probably iterate over all known backends.
	// But `RoutedUserStore` only has `router`.
	// The `router` might know about backends.
	// But `router.Select` takes database.
	// If I pass "default", it ensures on default backend.
	// If I have dedicated database, I need to ensure on that backend too.
	// This is a limitation of current `EnsureIndexes` signature.
	// However, `EnsureIndexes` is usually called by `main.go` or `factory`.
	// Maybe `factory` should call `EnsureIndexes` on all created stores directly, instead of relying on `RoutedStore` to do it.
	// The `RoutedStore` implementation of `EnsureIndexes` is tricky.
	// For now, I'll just call it on "default" database, or fail.
	// Or I can update `EnsureIndexes` to take `database`? No, indexes are collection-wide (per backend).
	// If I have multiple backends, I need to run it on each backend.
	// I'll leave `EnsureIndexes` as is for now (calling on default database), and note that factory should handle it.
	// Actually, `factory` creates the stores. It can call `EnsureIndexes` on them.
	// `RoutedStore` might not need to implement `EnsureIndexes` or it should be a no-op if factory handles it.
	// But `UserStore` interface has it.
	// Let's just use "default" for now.
	store, err := s.router.Select("default", types.OpWrite)
	if err != nil {
		return err
	}
	return store.EnsureIndexes(ctx)
}

func (s *RoutedUserStore) Close(ctx context.Context) error {
	return nil
}

// RoutedRevocationStore implements TokenRevocationStore by routing operations
type RoutedRevocationStore struct {
	router types.RevocationRouter
}

func NewRoutedRevocationStore(router types.RevocationRouter) types.TokenRevocationStore {
	return &RoutedRevocationStore{router: router}
}

func (s *RoutedRevocationStore) RevokeToken(ctx context.Context, jti string, expiresAt time.Time) error {
	// Global revocation - use default database for routing
	store, err := s.router.Select("default", types.OpWrite)
	if err != nil {
		return err
	}
	return store.RevokeToken(ctx, jti, expiresAt)
}

func (s *RoutedRevocationStore) RevokeTokenImmediate(ctx context.Context, jti string, expiresAt time.Time) error {
	// Global revocation - use default database for routing
	store, err := s.router.Select("default", types.OpWrite)
	if err != nil {
		return err
	}
	return store.RevokeTokenImmediate(ctx, jti, expiresAt)
}

func (s *RoutedRevocationStore) RevokeTokenIfNotRevoked(ctx context.Context, jti string, expiresAt time.Time, gracePeriod time.Duration) error {
	// Global revocation - use default database for routing
	store, err := s.router.Select("default", types.OpWrite)
	if err != nil {
		return err
	}
	return store.RevokeTokenIfNotRevoked(ctx, jti, expiresAt, gracePeriod)
}

func (s *RoutedRevocationStore) IsRevoked(ctx context.Context, jti string, gracePeriod time.Duration) (bool, error) {
	// Global revocation check - use default database for routing
	store, err := s.router.Select("default", types.OpRead)
	if err != nil {
		return false, err
	}
	return store.IsRevoked(ctx, jti, gracePeriod)
}

func (s *RoutedRevocationStore) EnsureIndexes(ctx context.Context) error {
	store, err := s.router.Select("default", types.OpWrite)
	if err != nil {
		return err
	}
	return store.EnsureIndexes(ctx)
}

func (s *RoutedRevocationStore) Close(ctx context.Context) error {
	return nil
}
