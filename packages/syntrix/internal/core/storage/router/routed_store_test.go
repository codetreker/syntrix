package router

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/storage/types"
	"github.com/codetreker/syntrix/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Mock Router
type mockDocRouter struct {
	mock.Mock
}

func (m *mockDocRouter) Select(database string, op types.OpKind) (types.DocumentStore, error) {
	args := m.Called(database, op)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(types.DocumentStore), args.Error(1)
}

// Mock Store
type mockDocumentStore struct {
	mock.Mock
}

func (m *mockDocumentStore) Get(ctx context.Context, database string, path string, opts ...types.ReadOptions) (*types.StoredDoc, error) {
	args := m.Called(ctx, database, path, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*types.StoredDoc), args.Error(1)
}

func (m *mockDocumentStore) Create(ctx context.Context, database string, doc types.StoredDoc) error {
	args := m.Called(ctx, database, doc)
	return args.Error(0)
}

func (m *mockDocumentStore) Update(ctx context.Context, database string, path string, data map[string]interface{}, pred model.Filters) error {
	args := m.Called(ctx, database, path, data, pred)
	return args.Error(0)
}

func (m *mockDocumentStore) Patch(ctx context.Context, database string, path string, data map[string]interface{}, pred model.Filters) error {
	args := m.Called(ctx, database, path, data, pred)
	return args.Error(0)
}

func (m *mockDocumentStore) Delete(ctx context.Context, database string, path string, pred model.Filters) error {
	args := m.Called(ctx, database, path, pred)
	return args.Error(0)
}

func (m *mockDocumentStore) DeleteByDatabase(ctx context.Context, database string, limit int) (int, error) {
	args := m.Called(ctx, database, limit)
	return args.Int(0), args.Error(1)
}

func (m *mockDocumentStore) Query(ctx context.Context, database string, q model.Query) ([]*types.StoredDoc, error) {
	args := m.Called(ctx, database, q)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*types.StoredDoc), args.Error(1)
}

func (m *mockDocumentStore) GetMany(ctx context.Context, database string, paths []string, opts ...types.ReadOptions) ([]*types.StoredDoc, error) {
	args := m.Called(ctx, database, paths, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*types.StoredDoc), args.Error(1)
}

func (m *mockDocumentStore) Watch(ctx context.Context, database string, collection string, after types.WatchCheckpoint, opts types.WatchOptions) (types.WatchStream, error) {
	args := m.Called(ctx, database, collection, after, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(types.WatchStream), args.Error(1)
}

func (m *mockDocumentStore) Close(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

type mockWatchStream struct {
	mock.Mock
}

func (s *mockWatchStream) InitialCheckpoint() types.WatchCheckpoint {
	return s.Called().Get(0).(types.WatchCheckpoint)
}

func (s *mockWatchStream) Next(ctx context.Context) (types.WatchFrame, error) {
	args := s.Called(ctx)
	return args.Get(0).(types.WatchFrame), args.Error(1)
}

func (s *mockWatchStream) Close() error {
	return s.Called().Error(0)
}

func TestRoutedDocumentStore(t *testing.T) {
	ctx := context.Background()
	database := "default"

	t.Run("Get uses Read op", func(t *testing.T) {
		router := new(mockDocRouter)
		store := new(mockDocumentStore)

		router.On("Select", database, types.OpRead).Return(store, nil)
		store.On("Get", ctx, database, "path", []types.ReadOptions(nil)).Return(&types.StoredDoc{}, nil)

		rs := NewRoutedDocumentStore(router)
		_, err := rs.Get(ctx, database, "path")

		assert.NoError(t, err)
		router.AssertExpectations(t)
		store.AssertExpectations(t)
	})

	t.Run("Create uses Write op", func(t *testing.T) {
		router := new(mockDocRouter)
		store := new(mockDocumentStore)

		router.On("Select", database, types.OpWrite).Return(store, nil)
		store.On("Create", ctx, database, mock.Anything).Return(nil)

		rs := NewRoutedDocumentStore(router)
		err := rs.Create(ctx, database, types.StoredDoc{})

		assert.NoError(t, err)
		router.AssertExpectations(t)
		store.AssertExpectations(t)
	})

	t.Run("Update uses Write op", func(t *testing.T) {
		router := new(mockDocRouter)
		store := new(mockDocumentStore)

		router.On("Select", database, types.OpWrite).Return(store, nil)
		store.On("Update", ctx, database, "path", mock.Anything, mock.Anything).Return(nil)

		rs := NewRoutedDocumentStore(router)
		err := rs.Update(ctx, database, "path", nil, nil)

		assert.NoError(t, err)
		router.AssertExpectations(t)
		store.AssertExpectations(t)
	})

	t.Run("Close does nothing", func(t *testing.T) {
		router := new(mockDocRouter)
		rs := NewRoutedDocumentStore(router)
		err := rs.Close(ctx)
		assert.NoError(t, err)
	})

	t.Run("Patch uses Write op", func(t *testing.T) {
		router := new(mockDocRouter)
		store := new(mockDocumentStore)

		router.On("Select", database, types.OpWrite).Return(store, nil)
		store.On("Patch", ctx, database, "path", mock.Anything, mock.Anything).Return(nil)

		rs := NewRoutedDocumentStore(router)
		err := rs.Patch(ctx, database, "path", nil, nil)

		assert.NoError(t, err)
		router.AssertExpectations(t)
		store.AssertExpectations(t)
	})

	t.Run("Delete uses Write op", func(t *testing.T) {
		router := new(mockDocRouter)
		store := new(mockDocumentStore)

		router.On("Select", database, types.OpWrite).Return(store, nil)
		store.On("Delete", ctx, database, "path", mock.Anything).Return(nil)

		rs := NewRoutedDocumentStore(router)
		err := rs.Delete(ctx, database, "path", nil)

		assert.NoError(t, err)
		router.AssertExpectations(t)
		store.AssertExpectations(t)
	})

	t.Run("Query uses Read op", func(t *testing.T) {
		router := new(mockDocRouter)
		store := new(mockDocumentStore)

		router.On("Select", database, types.OpRead).Return(store, nil)
		store.On("Query", ctx, database, mock.Anything).Return([]*types.StoredDoc{}, nil)

		rs := NewRoutedDocumentStore(router)
		_, err := rs.Query(ctx, database, model.Query{})

		assert.NoError(t, err)
		router.AssertExpectations(t)
		store.AssertExpectations(t)
	})

	t.Run("Watch forwards checkpoint and options with Watch op", func(t *testing.T) {
		router := new(mockDocRouter)
		store := new(mockDocumentStore)
		stream := new(mockWatchStream)
		after := types.WatchCheckpoint("source-checkpoint")
		opts := types.WatchOptions{IncludeBefore: true}

		router.On("Select", database, types.OpWatch).Return(store, nil).Once()
		store.On("Watch", ctx, database, "col", after, opts).Return(stream, nil).Once()

		rs := NewRoutedDocumentStore(router)
		actual, err := rs.Watch(ctx, database, "col", after, opts)

		require.NoError(t, err)
		assert.Same(t, stream, actual)
		router.AssertExpectations(t)
		store.AssertExpectations(t)
	})

	t.Run("GetMany uses Read op", func(t *testing.T) {
		router := new(mockDocRouter)
		store := new(mockDocumentStore)

		paths := []string{"users/user1", "users/user2"}
		expectedDocs := []*types.StoredDoc{
			{Id: "testdb:users/user1", Fullpath: "users/user1"},
			{Id: "testdb:users/user2", Fullpath: "users/user2"},
		}

		router.On("Select", database, types.OpRead).Return(store, nil)
		store.On("GetMany", ctx, database, paths, []types.ReadOptions(nil)).Return(expectedDocs, nil)

		rs := NewRoutedDocumentStore(router)
		docs, err := rs.GetMany(ctx, database, paths)

		assert.NoError(t, err)
		assert.Len(t, docs, 2)
		router.AssertExpectations(t)
		store.AssertExpectations(t)
	})

	t.Run("DeleteByDatabase uses Write op", func(t *testing.T) {
		router := new(mockDocRouter)
		store := new(mockDocumentStore)

		router.On("Select", database, types.OpWrite).Return(store, nil)
		store.On("DeleteByDatabase", ctx, database, 100).Return(50, nil)

		rs := NewRoutedDocumentStore(router)
		deleted, err := rs.DeleteByDatabase(ctx, database, 100)

		assert.NoError(t, err)
		assert.Equal(t, 50, deleted)
		router.AssertExpectations(t)
		store.AssertExpectations(t)
	})

	t.Run("DeleteByDatabase router error", func(t *testing.T) {
		router := new(mockDocRouter)

		router.On("Select", database, types.OpWrite).Return(nil, assert.AnError)

		rs := NewRoutedDocumentStore(router)
		_, err := rs.DeleteByDatabase(ctx, database, 100)

		assert.Error(t, err)
		router.AssertExpectations(t)
	})
}

func TestRoutedDocumentStoreWatchRejectsEmptyDatabase(t *testing.T) {
	router := new(mockDocRouter)
	routed := NewRoutedDocumentStore(router)

	stream, err := routed.Watch(context.Background(), "", "users", "", types.WatchOptions{})

	require.Nil(t, stream)
	var watchErr *types.WatchError
	require.ErrorAs(t, err, &watchErr)
	assert.Equal(t, types.WatchInvalidScope, watchErr.Code)
	assert.Empty(t, watchErr.Database)
	assert.Equal(t, "users", watchErr.Collection)
	assert.ErrorIs(t, err, ErrDatabaseRequired)
	router.AssertNotCalled(t, "Select", mock.Anything, mock.Anything)
}

func TestRoutedDocumentStoreWatchRejectsInvalidOptionsBeforeRouting(t *testing.T) {
	for _, opts := range []types.WatchOptions{
		{StartMode: types.WatchStartForScan},
		{StartMode: -1},
		{MaxAwaitTime: time.Microsecond},
	} {
		router := new(mockDocRouter)
		stream, err := NewRoutedDocumentStore(router).Watch(context.Background(), "app", "users", "opaque", opts)
		require.Nil(t, stream)
		var watchErr *types.WatchError
		require.ErrorAs(t, err, &watchErr)
		require.Equal(t, types.WatchInvalidScope, watchErr.Code)
		require.Empty(t, router.Calls)
	}
}

func TestRoutedDocumentStoreWatchPreservesBackendError(t *testing.T) {
	ctx := context.Background()
	router := new(mockDocRouter)
	store := new(mockDocumentStore)
	after := types.WatchCheckpoint("other-source-checkpoint")
	cause := errors.New("checkpoint belongs to another source")
	expected := &types.WatchError{
		Code:       types.WatchSourceMismatch,
		Database:   "app",
		Collection: "users",
		Cause:      cause,
	}
	router.On("Select", "app", types.OpWatch).Return(store, nil).Once()
	store.On("Watch", ctx, "app", "users", after, types.WatchOptions{}).Return(nil, expected).Once()

	stream, err := NewRoutedDocumentStore(router).Watch(ctx, "app", "users", after, types.WatchOptions{})

	assert.Nil(t, stream)
	assert.Same(t, expected, err)
	assert.ErrorIs(t, err, cause)
	router.AssertExpectations(t)
	store.AssertExpectations(t)
}

func TestRoutedDocumentStoreWatchStaysOnSelectedPrimary(t *testing.T) {
	ctx := context.Background()
	primary := new(mockDocumentStore)
	replica := new(mockDocumentStore)
	replacement := new(mockDocumentStore)
	stream := new(mockWatchStream)
	after := types.WatchCheckpoint("primary-checkpoint")
	frame := types.WatchFrame{Checkpoint: "primary-progress"}
	doc := &types.StoredDoc{Id: "app:document-key", Fullpath: "users/user1"}
	terminal := &types.WatchError{Code: types.WatchSourceUnavailable, Database: "app", Cause: errors.New("source disconnected")}
	cleanupErr := errors.New("cursor cleanup failed")

	primary.On("Watch", ctx, "app", "", after, types.WatchOptions{}).Return(stream, nil).Once()
	replica.On("Get", ctx, "app", doc.Fullpath, []types.ReadOptions(nil)).Return(doc, nil).Once()
	replacement.On("Get", ctx, "app", doc.Fullpath, []types.ReadOptions(nil)).Return(doc, nil).Once()
	stream.On("InitialCheckpoint").Return(after).Once()
	stream.On("Next", ctx).Return(frame, nil).Once()
	stream.On("Next", ctx).Return(types.WatchFrame{}, terminal).Once()
	stream.On("Close").Return(cleanupErr).Once()

	routes := map[string]types.DocumentRouter{"app": NewSplitDocumentRouter(primary, replica)}
	routed := NewRoutedDocumentStore(NewDatabaseDocumentRouter(nil, routes))
	actual, err := routed.Watch(ctx, "app", "", after, types.WatchOptions{})
	require.NoError(t, err)
	require.Same(t, stream, actual)
	assert.Equal(t, after, actual.InitialCheckpoint())

	read, err := routed.Get(ctx, "app", doc.Fullpath)
	require.NoError(t, err)
	assert.Same(t, doc, read)

	routes["app"] = NewSingleDocumentRouter(replacement)
	progress, err := actual.Next(ctx)
	require.NoError(t, err)
	assert.Equal(t, frame, progress)
	_, err = actual.Next(ctx)
	assert.Same(t, terminal, err)
	assert.ErrorIs(t, err, terminal.Cause)
	assert.ErrorIs(t, actual.Close(), cleanupErr)

	read, err = routed.Get(ctx, "app", doc.Fullpath)
	require.NoError(t, err)
	assert.Same(t, doc, read)
	primary.AssertNotCalled(t, "Close", mock.Anything)
	replica.AssertNotCalled(t, "Close", mock.Anything)
	replacement.AssertNotCalled(t, "Close", mock.Anything)
	primary.AssertExpectations(t)
	replica.AssertExpectations(t)
	replacement.AssertExpectations(t)
	stream.AssertExpectations(t)
}

// Mock User Router & Store
func TestRoutedDocumentStoreGetReadOptions(t *testing.T) {
	for _, database := range []string{"default", "override"} {
		for _, path := range []string{"users/user1", "sys/databases/app"} {
			for _, tc := range []struct {
				name    string
				opts    []types.ReadOptions
				primary bool
			}{
				{name: "implicit default"},
				{name: "explicit default", opts: []types.ReadOptions{{}}},
				{name: "authoritative", opts: []types.ReadOptions{{Consistency: types.ReadAuthoritative}}, primary: true},
			} {
				t.Run(database+"/"+path+"/"+tc.name, func(t *testing.T) {
					primary, replica := new(mockDocumentStore), new(mockDocumentStore)
					overridePrimary, overrideReplica := new(mockDocumentStore), new(mockDocumentStore)
					selected := replica
					if tc.primary {
						selected = primary
					}
					if database == "override" {
						selected = overrideReplica
						if tc.primary {
							selected = overridePrimary
						}
					}
					ctx := context.Background()
					expected := &types.StoredDoc{Database: database, Fullpath: path, Version: 9}
					selected.On("Get", ctx, database, path, tc.opts).Return(expected, nil).Once()
					routed := NewRoutedDocumentStore(NewDatabaseDocumentRouter(
						NewSplitDocumentRouter(primary, replica),
						map[string]types.DocumentRouter{"override": NewSplitDocumentRouter(overridePrimary, overrideReplica)},
					))
					actual, err := routed.Get(ctx, database, path, tc.opts...)
					require.NoError(t, err)
					assert.Same(t, expected, actual)
					for _, store := range []*mockDocumentStore{primary, replica, overridePrimary, overrideReplica} {
						store.AssertExpectations(t)
						if store != selected {
							assert.Empty(t, store.Calls)
						}
					}
				})
			}
		}
	}
}

func TestRoutedDocumentStoreGetAuthoritativeErrors(t *testing.T) {
	opts := types.ReadOptions{Consistency: types.ReadAuthoritative}
	t.Run("selection failure", func(t *testing.T) {
		router := new(mockDocRouter)
		expected := errors.New("primary unavailable")
		router.On("Select", "app", types.OpWrite).Return(nil, expected).Once()
		doc, err := NewRoutedDocumentStore(router).Get(context.Background(), "app", "users/user1", opts)
		assert.Nil(t, doc)
		assert.Same(t, expected, err)
		router.AssertExpectations(t)
	})
	for _, expected := range []error{errors.New("primary read failed"), model.ErrNotFound, context.Canceled, context.DeadlineExceeded} {
		t.Run(expected.Error(), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if expected == context.Canceled {
				cancel()
			}
			if expected == context.DeadlineExceeded {
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer deadlineCancel()
			}
			primary, replica := new(mockDocumentStore), new(mockDocumentStore)
			primary.On("Get", ctx, "app", "users/user1", []types.ReadOptions{opts}).Return(nil, expected).Once()
			doc, err := NewRoutedDocumentStore(NewSplitDocumentRouter(primary, replica)).Get(ctx, "app", "users/user1", opts)
			assert.Nil(t, doc)
			assert.ErrorIs(t, err, expected)
			assert.Equal(t, expected, err)
			primary.AssertExpectations(t)
			assert.Empty(t, replica.Calls)
		})
	}
}

func TestRoutedDocumentStoreGetRejectsInvalidOptions(t *testing.T) {
	for _, opts := range [][]types.ReadOptions{
		{{Consistency: 255}},
		{{}, {}},
		{{Consistency: types.ReadAuthoritative}, {Consistency: types.ReadAuthoritative}},
	} {
		router := new(mockDocRouter)
		doc, err := NewRoutedDocumentStore(router).Get(context.Background(), "app", "users/user1", opts...)
		assert.Nil(t, doc)
		assert.Error(t, err)
		assert.Empty(t, router.Calls)
	}
}

func TestRoutedDocumentStoreGetAuthoritativeDatabaseIsolation(t *testing.T) {
	ctx := context.Background()
	opts := types.ReadOptions{Consistency: types.ReadAuthoritative}
	defaultPrimary, defaultReplica := new(mockDocumentStore), new(mockDocumentStore)
	overridePrimary, overrideReplica := new(mockDocumentStore), new(mockDocumentStore)
	path := "users/shared"
	defaultDoc := &types.StoredDoc{Database: "default", Fullpath: path, Version: 3}
	overrideDoc := &types.StoredDoc{Database: "override", Fullpath: path, Version: 8}
	defaultPrimary.On("Get", ctx, "default", path, []types.ReadOptions{opts}).Return(defaultDoc, nil).Twice()
	overridePrimary.On("Get", ctx, "override", path, []types.ReadOptions{opts}).Return(overrideDoc, nil).Once()
	store := NewRoutedDocumentStore(NewDatabaseDocumentRouter(
		NewSplitDocumentRouter(defaultPrimary, defaultReplica),
		map[string]types.DocumentRouter{"override": NewSplitDocumentRouter(overridePrimary, overrideReplica)},
	))
	for _, expected := range []*types.StoredDoc{defaultDoc, overrideDoc, defaultDoc} {
		actual, err := store.Get(ctx, expected.Database, path, opts)
		require.NoError(t, err)
		assert.Same(t, expected, actual)
	}
	defaultPrimary.AssertExpectations(t)
	overridePrimary.AssertExpectations(t)
	assert.Empty(t, defaultReplica.Calls)
	assert.Empty(t, overrideReplica.Calls)
}

func TestRoutedDocumentStoreGetManyReadOptions(t *testing.T) {
	ctx := context.Background()
	paths := []string{"users/a", "sys/config/a", "users/missing", "users/a"}
	for _, consistency := range []types.ReadConsistency{types.ReadDefault, types.ReadAuthoritative} {
		for _, database := range []string{"default", "override"} {
			t.Run(fmt.Sprintf("%s/%d", database, consistency), func(t *testing.T) {
				primary, replica := new(mockDocumentStore), new(mockDocumentStore)
				otherPrimary, otherReplica := new(mockDocumentStore), new(mockDocumentStore)
				selected := replica
				if consistency == types.ReadAuthoritative {
					selected = primary
				}
				if database == "override" {
					selected = otherReplica
					if consistency == types.ReadAuthoritative {
						selected = otherPrimary
					}
				}
				opts := types.ReadOptions{Consistency: consistency, ShowDeleted: true, MaxBytes: 1024}
				doc := &types.StoredDoc{Deleted: true}
				expected := []*types.StoredDoc{doc, doc, nil, doc}
				selected.On("GetMany", ctx, database, paths, []types.ReadOptions{opts}).Return(expected, nil).Once()
				routed := NewRoutedDocumentStore(NewDatabaseDocumentRouter(NewSplitDocumentRouter(primary, replica), map[string]types.DocumentRouter{"override": NewSplitDocumentRouter(otherPrimary, otherReplica)}))
				actual, err := routed.GetMany(ctx, database, paths, opts)
				require.NoError(t, err)
				assert.Equal(t, expected, actual)
				for _, store := range []*mockDocumentStore{primary, replica, otherPrimary, otherReplica} {
					store.AssertExpectations(t)
					if store != selected {
						assert.Empty(t, store.Calls)
					}
				}
			})
		}
	}
}

func TestRoutedDocumentStoreGetManyErrors(t *testing.T) {
	ctx := context.Background()
	for _, opts := range [][]types.ReadOptions{{{}, {}}, {{Consistency: -1}}, {{MaxBytes: -1}}} {
		router := new(mockDocRouter)
		_, err := NewRoutedDocumentStore(router).GetMany(ctx, "app", nil, opts...)
		require.Error(t, err)
		assert.Empty(t, router.Calls)
	}
	primary, replica := new(mockDocumentStore), new(mockDocumentStore)
	opts := types.ReadOptions{Consistency: types.ReadAuthoritative, ShowDeleted: true}
	expected := errors.New("source unavailable")
	primary.On("GetMany", ctx, "app", []string{"users/a"}, []types.ReadOptions{opts}).Return(nil, expected).Once()
	_, err := NewRoutedDocumentStore(NewSplitDocumentRouter(primary, replica)).GetMany(ctx, "app", []string{"users/a"}, opts)
	assert.ErrorIs(t, err, expected)
	assert.Empty(t, replica.Calls)
}

type scanningDocumentStore struct {
	*mockDocumentStore
	request  types.SourceScanRequest
	database string
	page     types.SourceScanPage
	err      error
}

func (s *scanningDocumentStore) ScanDocuments(_ context.Context, database string, request types.SourceScanRequest) (types.SourceScanPage, error) {
	s.database = database
	s.request = request
	return s.page, s.err
}
func TestRoutedDocumentStoreScan(t *testing.T) {
	ctx := context.Background()
	primary, replica := &scanningDocumentStore{mockDocumentStore: new(mockDocumentStore)}, &scanningDocumentStore{mockDocumentStore: new(mockDocumentStore)}
	sourceErr := errors.New("scan failed")
	primary.err = sourceErr
	routed := NewRoutedDocumentStore(NewSplitDocumentRouter(primary, replica)).(types.DocumentScanner)
	request := types.SourceScanRequest{Collection: "users", Limit: 2, Consistency: types.ReadAuthoritative}
	_, err := routed.ScanDocuments(ctx, "app", request)
	assert.ErrorIs(t, err, sourceErr)
	assert.Equal(t, request, primary.request)
	assert.Equal(t, "app", primary.database)
	assert.Empty(t, replica.database)
	request.Consistency = types.ReadDefault
	_, err = routed.ScanDocuments(ctx, "other", request)
	assert.NoError(t, err)
	assert.Equal(t, "other", replica.database)
	unsupported := NewRoutedDocumentStore(NewSingleDocumentRouter(new(mockDocumentStore))).(types.DocumentScanner)
	_, err = unsupported.ScanDocuments(ctx, "app", request)
	assert.ErrorContains(t, err, "does not support bounded scanning")
	request.Collection = "*"
	_, err = routed.ScanDocuments(ctx, "app", request)
	assert.Error(t, err)
}

func TestRoutedDocumentStoreFencedScanUsesWatchSource(t *testing.T) {
	ctx := context.Background()
	for _, consistency := range []types.ReadConsistency{types.ReadDefault, types.ReadAuthoritative} {
		router := new(mockDocRouter)
		failure := &types.WatchError{Code: types.WatchSourceMismatch, Cause: errors.New("different source")}
		primary := &scanningDocumentStore{mockDocumentStore: new(mockDocumentStore), err: failure}
		request := types.SourceScanRequest{Collection: "users", Limit: 2, AtLeast: "scan-boundary", Consistency: consistency}
		router.On("Select", "app", types.OpWatch).Return(primary, nil).Once()
		page, err := NewRoutedDocumentStore(router).(types.DocumentScanner).ScanDocuments(ctx, "app", request)
		require.Same(t, failure, err)
		require.Zero(t, page)
		require.Equal(t, request, primary.request)
		router.AssertExpectations(t)
	}
}

func TestRoutedDocumentStoreFencedScanDoesNotFallback(t *testing.T) {
	ctx := context.Background()
	request := types.SourceScanRequest{Collection: "users", Limit: 2, AtLeast: "scan-boundary"}
	t.Run("source selection fails", func(t *testing.T) {
		router := new(mockDocRouter)
		failure := errors.New("source unavailable")
		router.On("Select", "app", types.OpWatch).Return(nil, failure).Once()
		_, err := NewRoutedDocumentStore(router).(types.DocumentScanner).ScanDocuments(ctx, "app", request)
		require.ErrorIs(t, err, failure)
		router.AssertExpectations(t)
	})
	t.Run("primary lacks scanning capability", func(t *testing.T) {
		primary := new(mockDocumentStore)
		replica := &scanningDocumentStore{mockDocumentStore: new(mockDocumentStore)}
		routed := NewRoutedDocumentStore(NewSplitDocumentRouter(primary, replica)).(types.DocumentScanner)
		_, err := routed.ScanDocuments(ctx, "app", request)
		require.ErrorContains(t, err, "does not support bounded scanning")
		require.Empty(t, replica.database)
	})
}

type enumeratingDocumentStore struct {
	*mockDocumentStore
	called          bool
	database, after string
	limit           int
	opts            []types.CollectionEnumerationOptions
	err             error
}

func (s *enumeratingDocumentStore) EnumerateCollections(_ context.Context, database, after string, limit int, opts ...types.CollectionEnumerationOptions) ([]string, error) {
	s.called = true
	s.database = database
	s.after = after
	s.limit = limit
	s.opts = opts
	return []string{"users"}, s.err
}
func TestRoutedDocumentStoreEnumerateCollections(t *testing.T) {
	ctx := context.Background()
	primary, replica := &enumeratingDocumentStore{mockDocumentStore: new(mockDocumentStore)}, &enumeratingDocumentStore{mockDocumentStore: new(mockDocumentStore)}
	routed := NewRoutedDocumentStore(NewSplitDocumentRouter(primary, replica)).(types.DocumentCollectionEnumerator)
	opts := types.CollectionEnumerationOptions{IncludeSystem: true}
	result, err := routed.EnumerateCollections(ctx, "app", "items", 10, opts)
	require.NoError(t, err)
	assert.Equal(t, []string{"users"}, result)
	assert.True(t, primary.called)
	assert.False(t, replica.called)
	assert.Equal(t, "app", primary.database)
	assert.Equal(t, "items", primary.after)
	assert.Equal(t, 10, primary.limit)
	assert.Equal(t, []types.CollectionEnumerationOptions{opts}, primary.opts)
	primary.err = errors.New("enumeration failed")
	_, err = routed.EnumerateCollections(ctx, "app", "", 10)
	assert.ErrorIs(t, err, primary.err)
	assert.False(t, replica.called)
	_, err = routed.EnumerateCollections(ctx, "app", "", 0)
	assert.Error(t, err)
	unsupported := NewRoutedDocumentStore(NewSingleDocumentRouter(new(mockDocumentStore))).(types.DocumentCollectionEnumerator)
	_, err = unsupported.EnumerateCollections(ctx, "app", "", 1)
	assert.ErrorContains(t, err, "does not support collection enumeration")
}

func TestRoutedDocumentStoreReadBudgetError(t *testing.T) {
	ctx := context.Background()
	primary, replica := new(mockDocumentStore), new(mockDocumentStore)
	opts := types.ReadOptions{Consistency: types.ReadAuthoritative, ShowDeleted: true, MaxBytes: 1}
	primary.On("Get", ctx, "app", "items/a", []types.ReadOptions{opts}).Return(nil, types.ErrReadBudget).Once()
	primary.On("GetMany", ctx, "app", []string{"items/a", "items/b"}, []types.ReadOptions{opts}).Return(nil, types.ErrReadBudget).Once()
	routed := NewRoutedDocumentStore(NewSplitDocumentRouter(primary, replica))
	doc, err := routed.Get(ctx, "app", "items/a", opts)
	assert.Nil(t, doc)
	assert.ErrorIs(t, err, types.ErrReadBudget)
	docs, err := routed.GetMany(ctx, "app", []string{"items/a", "items/b"}, opts)
	assert.Nil(t, docs)
	assert.ErrorIs(t, err, types.ErrReadBudget)
	primary.AssertExpectations(t)
	assert.Empty(t, replica.Calls)
}
