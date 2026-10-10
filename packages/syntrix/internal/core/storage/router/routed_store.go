package router

import (
	"context"
	"fmt"

	"github.com/codetreker/syntrix/internal/core/storage/types"
	"github.com/codetreker/syntrix/pkg/model"
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
	if request.AtLeast != "" {
		op = types.OpWatch
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
	if _, err := opts.Resolve(after); err != nil {
		return nil, &types.WatchError{Code: types.WatchInvalidScope, Database: database, Collection: collection, Cause: err}
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
