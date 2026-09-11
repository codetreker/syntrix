package core

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/core/storage/router"
	"github.com/syntrixbase/syntrix/internal/indexer"
	"github.com/syntrixbase/syntrix/pkg/model"
)

func TestPush_AuthoritativeReadRouting(t *testing.T) {
	primaryErr := errors.New("primary unavailable")
	for _, deleted := range []bool{false, true} {
		action := "update"
		if deleted {
			action = "delete"
		}
		t.Run(action, func(t *testing.T) {
			type testCase struct {
				name            string
				baseVersion     int64
				preReadErr      error
				writeErr        error
				refetchErr      error
				conflictVersion int64
				expectedErr     error
			}
			tests := []testCase{
				{name: "matching primary version", baseVersion: 5},
				{name: "stale replica version conflicts with primary", baseVersion: 4, conflictVersion: 5},
				{name: "CAS race refetches primary", baseVersion: 5, writeErr: model.ErrPreconditionFailed, conflictVersion: 6},
				{name: "primary read failure", baseVersion: 5, preReadErr: primaryErr, expectedErr: primaryErr},
				{name: "primary read cancellation", baseVersion: 5, preReadErr: context.Canceled, expectedErr: context.Canceled},
				{name: "conflict read failure", baseVersion: 5, writeErr: model.ErrPreconditionFailed, refetchErr: primaryErr, expectedErr: primaryErr},
				{name: "conflict read cancellation", baseVersion: 5, writeErr: model.ErrPreconditionFailed, refetchErr: context.Canceled, expectedErr: context.Canceled},
				{name: "conflict document no longer visible", baseVersion: 5, writeErr: model.ErrPreconditionFailed, refetchErr: model.ErrNotFound},
			}
			if deleted {
				tests = append(tests,
					testCase{name: "missing delete refetches primary", baseVersion: 5, writeErr: model.ErrNotFound, conflictVersion: 6},
					testCase{name: "missing delete refetch failure", baseVersion: 5, writeErr: model.ErrNotFound, refetchErr: primaryErr, expectedErr: primaryErr},
					testCase{name: "missing delete remains invisible", baseVersion: 5, writeErr: model.ErrNotFound, refetchErr: model.ErrNotFound},
				)
			}
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					ctx := context.Background()
					primary := &routedReadStorage{}
					replica := &routedReadStorage{}
					primary.Test(t)
					replica.Test(t)
					t.Cleanup(func() {
						primary.AssertExpectations(t)
						replica.AssertExpectations(t)
					})
					engine := newTestEngine(router.NewRoutedDocumentStore(router.NewSplitDocumentRouter(primary, replica)))
					primaryDoc := &storage.StoredDoc{Fullpath: "items/1", Collection: "items", Version: 5}
					replicaDoc := &storage.StoredDoc{Fullpath: "items/1", Collection: "items", Version: 4}
					authoritative := []storage.ReadOptions{{Consistency: storage.ReadAuthoritative}}
					replica.On("Get", ctx, "default", "items/1", []storage.ReadOptions(nil)).Return(replicaDoc, nil).Once()
					ordinary, err := engine.GetDocument(ctx, "default", "items/1")
					require.NoError(t, err)
					require.Equal(t, int64(4), ordinary["version"])

					initial := primaryDoc
					if tt.preReadErr != nil {
						initial = nil
					}
					primary.On("Get", ctx, "default", "items/1", authoritative).Return(initial, tt.preReadErr).Once()
					data := map[string]interface{}{"value": "changed"}
					if tt.preReadErr == nil && tt.baseVersion == 5 {
						filters := model.Filters{{Field: "version", Op: "==", Value: tt.baseVersion}}
						if deleted {
							primary.On("Delete", ctx, "default", "items/1", filters).Return(tt.writeErr).Once()
						} else {
							primary.On("Update", ctx, "default", "items/1", data, filters).Return(tt.writeErr).Once()
						}
						if tt.writeErr != nil {
							var latest *storage.StoredDoc
							if tt.refetchErr == nil {
								latest = &storage.StoredDoc{Fullpath: "items/1", Collection: "items", Version: 6}
							}
							primary.On("Get", ctx, "default", "items/1", authoritative).Return(latest, tt.refetchErr).Once()
						}
					}
					resp, err := engine.Push(ctx, "default", storage.ReplicationPushRequest{
						Collection: "items",
						Changes: []storage.ReplicationPushChange{{
							Doc:         &storage.StoredDoc{Fullpath: "items/1", Data: data, Deleted: deleted},
							BaseVersion: &tt.baseVersion,
						}},
					})
					if tt.expectedErr != nil {
						require.ErrorIs(t, err, tt.expectedErr)
						require.Nil(t, resp)
					} else {
						require.NoError(t, err)
						require.NotNil(t, resp)
						if tt.conflictVersion == 0 {
							require.Empty(t, resp.Conflicts)
						} else {
							require.Len(t, resp.Conflicts, 1)
							require.Equal(t, tt.conflictVersion, resp.Conflicts[0].Version)
						}
					}
					replica.AssertNumberOfCalls(t, "Get", 1)
				})
			}
		})
	}
}

// newTestEngine creates an engine for tests
func newTestEngine(store storage.DocumentStore) *Engine {
	// For coverage tests, we use a mock indexer that returns basic results or errors
	// if we wanted to test full integration, we'd need a real indexer engine
	mockIndexer := new(MockIndexerService)
	// Make mockIndexer return empty result by default
	mockIndexer.On("Search", mock.Anything, mock.Anything, mock.Anything).Return([]indexer.DocRef{}, nil).Maybe()

	return New(store, mockIndexer)
}

func TestEngine_Push_Coverage(t *testing.T) {
	type testCase struct {
		name              string
		req               storage.ReplicationPushRequest
		mockSetup         func(*MockStorageBackend)
		expectedConflicts []*storage.StoredDoc
		expectError       bool
	}

	tests := []testCase{
		{
			name: "Conflict (Version Mismatch)",
			req: storage.ReplicationPushRequest{
				Collection: "test",
				Changes: []storage.ReplicationPushChange{
					{
						Doc:         &storage.StoredDoc{Id: "test/1", Fullpath: "test/1", Collection: "test", Data: map[string]interface{}{"foo": "bar"}, Version: 2},
						BaseVersion: ptr(int64(1)),
					},
				},
			},
			mockSetup: func(m *MockStorageBackend) {
				// Existing doc has version 3, but we expect base version 1 -> Conflict
				existingDoc := &storage.StoredDoc{Id: "test/1", Fullpath: "test/1", Version: 3, Data: map[string]interface{}{"foo": "old"}}
				m.On("Get", mock.Anything, "default", "test/1").Return(existingDoc, nil)
			},
			expectedConflicts: []*storage.StoredDoc{
				{Id: "test/1", Fullpath: "test/1", Version: 3, Data: map[string]interface{}{"foo": "old"}},
			},
			expectError: false,
		},
		{
			name: "Delete Success",
			req: storage.ReplicationPushRequest{
				Collection: "test",
				Changes: []storage.ReplicationPushChange{
					{
						Doc:         &storage.StoredDoc{Id: "test/1", Fullpath: "test/1", Collection: "test", Deleted: true, Version: 2},
						BaseVersion: ptr(int64(1)),
					},
				},
			},
			mockSetup: func(m *MockStorageBackend) {
				existingDoc := &storage.StoredDoc{Id: "test/1", Fullpath: "test/1", Version: 1}
				m.On("Get", mock.Anything, "default", "test/1").Return(existingDoc, nil)
				m.On("Delete", mock.Anything, "default", "test/1", mock.MatchedBy(func(f model.Filters) bool {
					return f[0].Field == "version" && f[0].Value == int64(1)
				})).Return(nil)
			},
			expectedConflicts: nil,
			expectError:       false,
		},
		{
			name: "Delete Conflict (Precondition Failed)",
			req: storage.ReplicationPushRequest{
				Collection: "test",
				Changes: []storage.ReplicationPushChange{
					{
						Doc:         &storage.StoredDoc{Id: "test/1", Fullpath: "test/1", Collection: "test", Deleted: true, Version: 2},
						BaseVersion: ptr(int64(1)),
					},
				},
			},
			mockSetup: func(m *MockStorageBackend) {
				existingDoc := &storage.StoredDoc{Id: "test/1", Fullpath: "test/1", Version: 1}
				m.On("Get", mock.Anything, "default", "test/1").Return(existingDoc, nil).Once()

				// Delete fails with PreconditionFailed
				m.On("Delete", mock.Anything, "default", "test/1", mock.Anything).Return(model.ErrPreconditionFailed)

				// Fetch latest for conflict
				latestDoc := &storage.StoredDoc{Id: "test/1", Fullpath: "test/1", Version: 3}
				m.On("Get", mock.Anything, "default", "test/1").Return(latestDoc, nil).Once()
			},
			expectedConflicts: []*storage.StoredDoc{
				{Id: "test/1", Fullpath: "test/1", Version: 3},
			},
			expectError: false,
		},
		{
			name: "Create Conflict (Already Exists)",
			req: storage.ReplicationPushRequest{
				Collection: "test",
				Changes: []storage.ReplicationPushChange{
					{
						Doc: &storage.StoredDoc{Id: "test/1", Fullpath: "test/1", Collection: "test", Version: 1},
					},
				},
			},
			mockSetup: func(m *MockStorageBackend) {
				// Get returns NotFound, so we try to Create
				m.On("Get", mock.Anything, "default", "test/1").Return(nil, model.ErrNotFound)
				// Create fails (maybe race condition)
				m.On("Create", mock.Anything, "default", mock.Anything).Return(assert.AnError)
			},
			expectedConflicts: []*storage.StoredDoc{
				{Id: "test/1", Fullpath: "test/1", Collection: "test", Version: 1},
			},
			expectError: false,
		},
		{
			name: "Get Error",
			req: storage.ReplicationPushRequest{
				Collection: "test",
				Changes: []storage.ReplicationPushChange{
					{Doc: &storage.StoredDoc{Id: "test/1", Fullpath: "test/1"}},
				},
			},
			mockSetup: func(m *MockStorageBackend) {
				m.On("Get", mock.Anything, "default", "test/1").Return(nil, assert.AnError)
			},
			expectError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mockStorage := new(MockStorageBackend)
			if tc.mockSetup != nil {
				tc.mockSetup(mockStorage)
			}
			engine := newTestEngine(mockStorage)
			ctx := context.Background()

			resp, err := engine.Push(ctx, "default", tc.req)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, len(tc.expectedConflicts), len(resp.Conflicts))
			}
			mockStorage.AssertExpectations(t)
		})
	}
}

func ptr(i int64) *int64 {
	return &i
}

func TestReplaceDocument_StorageError(t *testing.T) {
	mockStorage := new(MockStorageBackend)
	engine := newTestEngine(mockStorage)

	doc := model.Document{"id": "doc1", "collection": "col", "foo": "bar"}

	// Get returns error
	mockStorage.On("Get", mock.Anything, "default", "col/doc1").Return(nil, errors.New("db error"))

	_, err := engine.ReplaceDocument(context.Background(), "default", doc, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "db error")
}

func TestReplaceDocument_CreateError(t *testing.T) {
	mockStorage := new(MockStorageBackend)
	engine := newTestEngine(mockStorage)

	doc := model.Document{"id": "doc1", "collection": "col", "foo": "bar"}

	// Get returns NotFound
	mockStorage.On("Get", mock.Anything, "default", "col/doc1").Return(nil, model.ErrNotFound)
	// Create returns error
	mockStorage.On("Create", mock.Anything, "default", mock.Anything).Return(errors.New("create error"))

	_, err := engine.ReplaceDocument(context.Background(), "default", doc, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "create error")
}

func TestReplaceDocument_UpdateError(t *testing.T) {
	mockStorage := new(MockStorageBackend)
	engine := newTestEngine(mockStorage)

	doc := model.Document{"id": "doc1", "collection": "col", "foo": "bar"}

	// Get returns success
	mockStorage.On("Get", mock.Anything, "default", "col/doc1").Return(&storage.StoredDoc{}, nil).Once()
	// Update returns error
	mockStorage.On("Update", mock.Anything, "default", "col/doc1", mock.Anything, mock.Anything).Return(errors.New("update error"))

	_, err := engine.ReplaceDocument(context.Background(), "default", doc, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "update error")
}

func TestReplaceDocument_GetAfterUpdateError(t *testing.T) {
	mockStorage := new(MockStorageBackend)
	engine := newTestEngine(mockStorage)

	doc := model.Document{"id": "doc1", "collection": "col", "foo": "bar"}

	// Get returns success
	mockStorage.On("Get", mock.Anything, "default", "col/doc1").Return(&storage.StoredDoc{}, nil).Once()
	// Update returns success
	mockStorage.On("Update", mock.Anything, "default", "col/doc1", mock.Anything, mock.Anything).Return(nil)
	// Get after update returns error
	mockStorage.On("Get", mock.Anything, "default", "col/doc1").Return(nil, errors.New("get error")).Once()

	_, err := engine.ReplaceDocument(context.Background(), "default", doc, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "get error")
}

func TestPull_QueryEmpty(t *testing.T) {
	mockStorage := new(MockStorageBackend)
	engine := newTestEngine(mockStorage)

	mockStorage.On("Query", mock.Anything, "default", mock.Anything).Return(nil, nil)

	resp, err := engine.Pull(context.Background(), "default", storage.ReplicationPullRequest{
		Collection: "col",
		Checkpoint: 100,
		Limit:      10,
	})

	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Empty(t, resp.Documents)
	assert.Equal(t, int64(100), resp.Checkpoint)
}

func TestPush_DeleteNotFound(t *testing.T) {
	mockStorage := new(MockStorageBackend)
	engine := newTestEngine(mockStorage)

	req := storage.ReplicationPushRequest{
		Collection: "col",
		Changes: []storage.ReplicationPushChange{
			{
				Doc: &storage.StoredDoc{
					Fullpath: "col/doc1",
					Deleted:  true,
				},
			},
		},
	}

	mockStorage.On("Get", mock.Anything, "default", "col/doc1").Return(&storage.StoredDoc{Version: 1}, nil).Once()
	mockStorage.On("Get", mock.Anything, "default", "col/doc1").Return(nil, model.ErrNotFound)
	mockStorage.On("Delete", mock.Anything, "default", "col/doc1", mock.Anything).Return(model.ErrNotFound)

	resp, err := engine.Push(context.Background(), "default", req)
	assert.NoError(t, err)
	assert.Empty(t, resp.Conflicts)
}

// TestPush_DeleteNotFoundThenGetSuccess tests Push delete when Delete returns NotFound
// but subsequent Get finds a document (race condition - someone recreated it)
func TestPush_DeleteNotFoundThenGetSuccess(t *testing.T) {
	mockStorage := new(MockStorageBackend)
	engine := newTestEngine(mockStorage)

	req := storage.ReplicationPushRequest{
		Collection: "col",
		Changes: []storage.ReplicationPushChange{
			{
				Doc: &storage.StoredDoc{
					Fullpath: "col/doc1",
					Deleted:  true,
				},
			},
		},
	}

	// First Get returns existing doc
	mockStorage.On("Get", mock.Anything, "default", "col/doc1").Return(&storage.StoredDoc{
		Fullpath: "col/doc1",
		Version:  1,
	}, nil).Once()

	// Delete fails with NotFound (doc was deleted between Get and Delete)
	mockStorage.On("Delete", mock.Anything, "default", "col/doc1", mock.Anything).Return(model.ErrNotFound)

	// Get after NotFound finds a doc (someone recreated it - race condition)
	mockStorage.On("Get", mock.Anything, "default", "col/doc1").Return(&storage.StoredDoc{
		Fullpath: "col/doc1",
		Version:  2,
		Data:     map[string]interface{}{"recreated": true},
	}, nil).Once()

	resp, err := engine.Push(context.Background(), "default", req)
	assert.NoError(t, err)
	// The recreated doc should be in conflicts
	assert.Len(t, resp.Conflicts, 1)
	assert.Equal(t, int64(2), resp.Conflicts[0].Version)
	mockStorage.AssertExpectations(t)
}

func TestPush_UpdateConflict(t *testing.T) {
	mockStorage := new(MockStorageBackend)
	engine := newTestEngine(mockStorage)

	baseVer := int64(1)
	req := storage.ReplicationPushRequest{
		Collection: "col",
		Changes: []storage.ReplicationPushChange{
			{
				Doc: &storage.StoredDoc{
					Fullpath: "col/doc1",
					Data:     map[string]interface{}{"foo": "bar"},
				},
				BaseVersion: &baseVer,
			},
		},
	}

	// Get returns existing doc with version 2 (conflict)
	mockStorage.On("Get", mock.Anything, "default", "col/doc1").Return(&storage.StoredDoc{
		Fullpath: "col/doc1",
		Version:  2,
		Data:     map[string]interface{}{"foo": "baz"},
	}, nil)

	resp, err := engine.Push(context.Background(), "default", req)
	assert.NoError(t, err)
	assert.Len(t, resp.Conflicts, 1)
	assert.Equal(t, int64(2), resp.Conflicts[0].Version)
}

func TestPush_UpdatePreconditionFailed(t *testing.T) {
	mockStorage := new(MockStorageBackend)
	engine := newTestEngine(mockStorage)

	baseVer := int64(1)
	req := storage.ReplicationPushRequest{
		Collection: "col",
		Changes: []storage.ReplicationPushChange{
			{
				Doc: &storage.StoredDoc{
					Fullpath: "col/doc1",
					Data:     map[string]interface{}{"foo": "bar"},
				},
				BaseVersion: &baseVer,
			},
		},
	}

	// Get returns existing doc with version 1 (match)
	mockStorage.On("Get", mock.Anything, "default", "col/doc1").Return(&storage.StoredDoc{
		Fullpath: "col/doc1",
		Version:  1,
	}, nil).Once()

	// Update fails with PreconditionFailed (race condition)
	mockStorage.On("Update", mock.Anything, "default", "col/doc1", mock.Anything, mock.Anything).Return(model.ErrPreconditionFailed)

	// Fetch latest for conflict
	mockStorage.On("Get", mock.Anything, "default", "col/doc1").Return(&storage.StoredDoc{
		Fullpath: "col/doc1",
		Version:  2,
	}, nil).Once()

	resp, err := engine.Push(context.Background(), "default", req)
	assert.NoError(t, err)
	assert.Len(t, resp.Conflicts, 1)
	assert.Equal(t, int64(2), resp.Conflicts[0].Version)
}

// TestPush_EmptyFullpathWithIDInData tests Push when Fullpath is empty but ID is in Data
func TestPush_EmptyFullpathWithIDInData(t *testing.T) {
	mockStorage := new(MockStorageBackend)
	engine := newTestEngine(mockStorage)

	req := storage.ReplicationPushRequest{
		Collection: "col",
		Changes: []storage.ReplicationPushChange{
			{
				Doc: &storage.StoredDoc{
					Fullpath: "", // Empty fullpath
					Data:     map[string]interface{}{"id": "doc1", "foo": "bar"},
				},
			},
		},
	}

	// Should construct fullpath as col/doc1 from Data["id"]
	mockStorage.On("Get", mock.Anything, "default", "col/doc1").Return(nil, model.ErrNotFound)
	mockStorage.On("Create", mock.Anything, "default", mock.MatchedBy(func(d storage.StoredDoc) bool {
		return d.Fullpath == "col/doc1"
	})).Return(nil)

	resp, err := engine.Push(context.Background(), "default", req)
	assert.NoError(t, err)
	assert.Empty(t, resp.Conflicts)
	mockStorage.AssertExpectations(t)
}

// TestPush_CreateConflict tests Push when Create fails (document already exists race)
func TestPush_CreateConflict(t *testing.T) {
	mockStorage := new(MockStorageBackend)
	engine := newTestEngine(mockStorage)

	req := storage.ReplicationPushRequest{
		Collection: "col",
		Changes: []storage.ReplicationPushChange{
			{
				Doc: &storage.StoredDoc{
					Fullpath: "col/doc1",
					Data:     map[string]interface{}{"foo": "bar"},
				},
			},
		},
	}

	// Document doesn't exist initially
	mockStorage.On("Get", mock.Anything, "default", "col/doc1").Return(nil, model.ErrNotFound)
	// But Create fails (race condition - someone else created it)
	mockStorage.On("Create", mock.Anything, "default", mock.Anything).Return(model.ErrExists)

	resp, err := engine.Push(context.Background(), "default", req)
	assert.NoError(t, err)
	assert.Len(t, resp.Conflicts, 1)
	mockStorage.AssertExpectations(t)
}

// TestPush_DeletePreconditionFailed tests Push delete with version mismatch
func TestPush_DeletePreconditionFailed(t *testing.T) {
	mockStorage := new(MockStorageBackend)
	engine := newTestEngine(mockStorage)

	baseVer := int64(1)
	req := storage.ReplicationPushRequest{
		Collection: "col",
		Changes: []storage.ReplicationPushChange{
			{
				Doc: &storage.StoredDoc{
					Fullpath: "col/doc1",
					Deleted:  true,
				},
				BaseVersion: &baseVer,
			},
		},
	}

	// Document exists with matching version
	mockStorage.On("Get", mock.Anything, "default", "col/doc1").Return(&storage.StoredDoc{
		Fullpath: "col/doc1",
		Version:  1,
	}, nil).Once()

	// Delete fails with PreconditionFailed
	mockStorage.On("Delete", mock.Anything, "default", "col/doc1", mock.Anything).Return(model.ErrPreconditionFailed)

	// Fetch latest for conflict
	mockStorage.On("Get", mock.Anything, "default", "col/doc1").Return(&storage.StoredDoc{
		Fullpath: "col/doc1",
		Version:  2,
	}, nil).Once()

	resp, err := engine.Push(context.Background(), "default", req)
	assert.NoError(t, err)
	assert.Len(t, resp.Conflicts, 1)
	assert.Equal(t, int64(2), resp.Conflicts[0].Version)
	mockStorage.AssertExpectations(t)
}

// TestPush_DeleteStorageError tests Push delete with storage error
func TestPush_DeleteStorageError(t *testing.T) {
	mockStorage := new(MockStorageBackend)
	engine := newTestEngine(mockStorage)

	req := storage.ReplicationPushRequest{
		Collection: "col",
		Changes: []storage.ReplicationPushChange{
			{
				Doc: &storage.StoredDoc{
					Fullpath: "col/doc1",
					Deleted:  true,
				},
			},
		},
	}

	// Document exists
	mockStorage.On("Get", mock.Anything, "default", "col/doc1").Return(&storage.StoredDoc{
		Fullpath: "col/doc1",
		Version:  1,
	}, nil)

	// Delete fails with unexpected error
	mockStorage.On("Delete", mock.Anything, "default", "col/doc1", mock.Anything).Return(errors.New("storage error"))

	resp, err := engine.Push(context.Background(), "default", req)
	assert.Error(t, err)
	assert.Nil(t, resp)
	mockStorage.AssertExpectations(t)
}

// TestPush_UpdateStorageError tests Push update with storage error
func TestPush_UpdateStorageError(t *testing.T) {
	mockStorage := new(MockStorageBackend)
	engine := newTestEngine(mockStorage)

	req := storage.ReplicationPushRequest{
		Collection: "col",
		Changes: []storage.ReplicationPushChange{
			{
				Doc: &storage.StoredDoc{
					Fullpath: "col/doc1",
					Data:     map[string]interface{}{"foo": "bar"},
				},
			},
		},
	}

	// Document exists
	mockStorage.On("Get", mock.Anything, "default", "col/doc1").Return(&storage.StoredDoc{
		Fullpath: "col/doc1",
		Version:  1,
	}, nil)

	// Update fails with unexpected error
	mockStorage.On("Update", mock.Anything, "default", "col/doc1", mock.Anything, mock.Anything).Return(errors.New("storage error"))

	resp, err := engine.Push(context.Background(), "default", req)
	assert.Error(t, err)
	assert.Nil(t, resp)
	mockStorage.AssertExpectations(t)
}

// TestPush_GetStorageError tests Push when Get returns unexpected error
func TestPush_GetStorageError(t *testing.T) {
	mockStorage := new(MockStorageBackend)
	engine := newTestEngine(mockStorage)

	req := storage.ReplicationPushRequest{
		Collection: "col",
		Changes: []storage.ReplicationPushChange{
			{
				Doc: &storage.StoredDoc{
					Fullpath: "col/doc1",
					Data:     map[string]interface{}{"foo": "bar"},
				},
			},
		},
	}

	// Get fails with unexpected error
	mockStorage.On("Get", mock.Anything, "default", "col/doc1").Return(nil, errors.New("connection error"))

	resp, err := engine.Push(context.Background(), "default", req)
	assert.Error(t, err)
	assert.Nil(t, resp)
	mockStorage.AssertExpectations(t)
}

// ==================================================
// DeleteDocument Tests
// ==================================================

func TestDeleteDocument_Success(t *testing.T) {
	mockStorage := new(MockStorageBackend)
	engine := newTestEngine(mockStorage)

	mockStorage.On("Delete", mock.Anything, "default", "col/doc1", model.Filters(nil)).Return(nil)

	err := engine.DeleteDocument(context.Background(), "default", "col/doc1", nil)
	assert.NoError(t, err)
	mockStorage.AssertExpectations(t)
}

func TestDeleteDocument_WithPredicate(t *testing.T) {
	mockStorage := new(MockStorageBackend)
	engine := newTestEngine(mockStorage)

	pred := model.Filters{{Field: "version", Op: model.OpEq, Value: int64(1)}}
	mockStorage.On("Delete", mock.Anything, "default", "col/doc1", pred).Return(nil)

	err := engine.DeleteDocument(context.Background(), "default", "col/doc1", pred)
	assert.NoError(t, err)
	mockStorage.AssertExpectations(t)
}

func TestDeleteDocument_NotFound(t *testing.T) {
	mockStorage := new(MockStorageBackend)
	engine := newTestEngine(mockStorage)

	mockStorage.On("Delete", mock.Anything, "default", "col/doc1", model.Filters(nil)).Return(model.ErrNotFound)

	err := engine.DeleteDocument(context.Background(), "default", "col/doc1", nil)
	assert.ErrorIs(t, err, model.ErrNotFound)
	mockStorage.AssertExpectations(t)
}

func TestDeleteDocument_PreconditionFailed(t *testing.T) {
	mockStorage := new(MockStorageBackend)
	engine := newTestEngine(mockStorage)

	pred := model.Filters{{Field: "version", Op: model.OpEq, Value: int64(1)}}
	mockStorage.On("Delete", mock.Anything, "default", "col/doc1", pred).Return(model.ErrPreconditionFailed)

	err := engine.DeleteDocument(context.Background(), "default", "col/doc1", pred)
	assert.ErrorIs(t, err, model.ErrPreconditionFailed)
	mockStorage.AssertExpectations(t)
}

func TestDeleteDocument_StorageError(t *testing.T) {
	mockStorage := new(MockStorageBackend)
	engine := newTestEngine(mockStorage)

	storageErr := errors.New("storage error")
	mockStorage.On("Delete", mock.Anything, "default", "col/doc1", model.Filters(nil)).Return(storageErr)

	err := engine.DeleteDocument(context.Background(), "default", "col/doc1", nil)
	assert.Error(t, err)
	assert.Equal(t, storageErr, err)
	mockStorage.AssertExpectations(t)
}

// ==================================================
// Additional Edge Case Tests
// ==================================================

func TestGetDocument_CustomDatabase(t *testing.T) {
	mockStorage := new(MockStorageBackend)
	engine := newTestEngine(mockStorage)

	doc := &storage.StoredDoc{
		Fullpath:   "col/doc1",
		Collection: "col",
		Data:       map[string]interface{}{"foo": "bar"},
		Version:    1,
	}
	mockStorage.On("Get", mock.Anything, "custom-database", "col/doc1").Return(doc, nil)

	result, err := engine.GetDocument(context.Background(), "custom-database", "col/doc1")
	assert.NoError(t, err)
	assert.Equal(t, "bar", result["foo"])
	mockStorage.AssertExpectations(t)
}

func TestCreateDocument_CustomDatabase(t *testing.T) {
	mockStorage := new(MockStorageBackend)
	engine := newTestEngine(mockStorage)

	mockStorage.On("Create", mock.Anything, "custom-database", mock.Anything).Return(nil)

	doc := model.Document{"collection": "col", "id": "doc1", "foo": "bar"}
	err := engine.CreateDocument(context.Background(), "custom-database", doc)
	assert.NoError(t, err)
	mockStorage.AssertExpectations(t)
}

func TestReplaceDocument_CustomDatabase(t *testing.T) {
	mockStorage := new(MockStorageBackend)
	engine := newTestEngine(mockStorage)

	// ReplaceDocument calls Get first to check if doc exists
	mockStorage.On("Get", mock.Anything, "custom-database", "col/doc1").Return(&storage.StoredDoc{
		Fullpath:   "col/doc1",
		Collection: "col",
		Version:    1,
	}, nil).Once()
	mockStorage.On("Update", mock.Anything, "custom-database", "col/doc1", mock.Anything, model.Filters(nil)).Return(nil)
	// After update, Get is called again to return updated doc
	mockStorage.On("Get", mock.Anything, "custom-database", "col/doc1").Return(&storage.StoredDoc{
		Fullpath:   "col/doc1",
		Collection: "col",
		Data:       map[string]interface{}{"foo": "bar"},
		Version:    2,
	}, nil).Once()

	doc := model.Document{"collection": "col", "id": "doc1", "foo": "bar"}
	_, err := engine.ReplaceDocument(context.Background(), "custom-database", doc, nil)
	assert.NoError(t, err)
	mockStorage.AssertExpectations(t)
}

func TestPatchDocument_CustomDatabase(t *testing.T) {
	mockStorage := new(MockStorageBackend)
	engine := newTestEngine(mockStorage)

	// PatchDocument calls Patch then Get
	mockStorage.On("Patch", mock.Anything, "custom-database", "col/doc1", mock.Anything, model.Filters(nil)).Return(nil)
	mockStorage.On("Get", mock.Anything, "custom-database", "col/doc1").Return(&storage.StoredDoc{
		Fullpath:   "col/doc1",
		Collection: "col",
		Data:       map[string]interface{}{"foo": "bar", "baz": "qux"},
		Version:    2,
	}, nil)

	doc := model.Document{"collection": "col", "id": "doc1", "baz": "qux"}
	_, err := engine.PatchDocument(context.Background(), "custom-database", doc, nil)
	assert.NoError(t, err)
	mockStorage.AssertExpectations(t)
}

// TestPatchDocument_GetAfterPatchError tests PatchDocument when Get fails after Patch
func TestPatchDocument_GetAfterPatchError(t *testing.T) {
	mockStorage := new(MockStorageBackend)
	engine := newTestEngine(mockStorage)

	doc := model.Document{"collection": "col", "id": "doc1", "foo": "bar"}

	// Patch succeeds
	mockStorage.On("Patch", mock.Anything, "default", "col/doc1", mock.Anything, model.Filters(nil)).Return(nil)
	// Get after patch returns error
	mockStorage.On("Get", mock.Anything, "default", "col/doc1").Return(nil, errors.New("get error"))

	_, err := engine.PatchDocument(context.Background(), "default", doc, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "get error")
	mockStorage.AssertExpectations(t)
}

func TestDeleteDocument_CustomDatabase(t *testing.T) {
	mockStorage := new(MockStorageBackend)
	engine := newTestEngine(mockStorage)

	mockStorage.On("Delete", mock.Anything, "custom-database", "col/doc1", model.Filters(nil)).Return(nil)

	err := engine.DeleteDocument(context.Background(), "custom-database", "col/doc1", nil)
	assert.NoError(t, err)
	mockStorage.AssertExpectations(t)
}

func TestPull_CustomDatabase(t *testing.T) {
	mockStorage := new(MockStorageBackend)
	engine := newTestEngine(mockStorage)

	storedDocs := []*storage.StoredDoc{
		{
			Fullpath:   "col/doc1",
			Collection: "col",
			Data:       map[string]interface{}{"foo": "bar"},
			Version:    1,
			UpdatedAt:  200,
		},
	}
	mockStorage.On("Query", mock.Anything, "custom-database", mock.Anything).Return(storedDocs, nil)

	req := storage.ReplicationPullRequest{
		Collection: "col",
		Checkpoint: 100,
		Limit:      10,
	}
	resp, err := engine.Pull(context.Background(), "custom-database", req)
	assert.NoError(t, err)
	assert.Len(t, resp.Documents, 1)
	mockStorage.AssertExpectations(t)
}

func TestPush_CustomDatabase(t *testing.T) {
	mockStorage := new(MockStorageBackend)
	engine := newTestEngine(mockStorage)

	req := storage.ReplicationPushRequest{
		Collection: "col",
		Changes: []storage.ReplicationPushChange{
			{
				Doc: &storage.StoredDoc{
					Fullpath: "col/doc1",
					Data:     map[string]interface{}{"foo": "bar"},
				},
			},
		},
	}

	mockStorage.On("Get", mock.Anything, "custom-database", "col/doc1").Return(nil, model.ErrNotFound)
	mockStorage.On("Create", mock.Anything, "custom-database", mock.Anything).Return(nil)

	resp, err := engine.Push(context.Background(), "custom-database", req)
	assert.NoError(t, err)
	assert.Empty(t, resp.Conflicts)
	mockStorage.AssertExpectations(t)
}
