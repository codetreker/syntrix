package database

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/pkg/model"
)

// mockDBStore implements DatabaseStore for testing
type mockDBStore struct {
	listFunc   func(ctx context.Context, opts ListOptions) ([]*Database, int, error)
	deleteFunc func(ctx context.Context, id string) error
	databases  []*Database
}

func (m *mockDBStore) Create(ctx context.Context, db *Database) error {
	m.databases = append(m.databases, db)
	return nil
}

func (m *mockDBStore) Get(ctx context.Context, id string) (*Database, error) {
	for _, db := range m.databases {
		if db.ID == id {
			return db, nil
		}
	}
	return nil, ErrDatabaseNotFound
}

func (m *mockDBStore) GetBySlug(ctx context.Context, slug string) (*Database, error) {
	return nil, ErrDatabaseNotFound
}

func (m *mockDBStore) List(ctx context.Context, opts ListOptions) ([]*Database, int, error) {
	if m.listFunc != nil {
		return m.listFunc(ctx, opts)
	}
	return m.databases, len(m.databases), nil
}

func (m *mockDBStore) Update(ctx context.Context, db *Database) error {
	return nil
}

func (m *mockDBStore) Delete(ctx context.Context, id string) error {
	if m.deleteFunc != nil {
		return m.deleteFunc(ctx, id)
	}
	// Remove from databases
	for i, db := range m.databases {
		if db.ID == id {
			m.databases = append(m.databases[:i], m.databases[i+1:]...)
			return nil
		}
	}
	return ErrDatabaseNotFound
}

func (m *mockDBStore) CountByOwner(ctx context.Context, ownerID string) (int, error) {
	return 0, nil
}

func (m *mockDBStore) Exists(ctx context.Context, id string) (bool, error) {
	for _, db := range m.databases {
		if db.ID == id {
			return true, nil
		}
	}
	return false, nil
}

func (m *mockDBStore) Close(ctx context.Context) error { return nil }

// mockIndexInvalidator implements IndexInvalidator for testing
type mockIndexInvalidator struct {
	invalidateFunc func(ctx context.Context, database string) error
	callCount      atomic.Int32
}

func (m *mockIndexInvalidator) InvalidateDatabase(ctx context.Context, database string) error {
	m.callCount.Add(1)
	if m.invalidateFunc != nil {
		return m.invalidateFunc(ctx, database)
	}
	return nil
}

// fullMockDocStore implements the full types.DocumentStore interface
type fullMockDocStore struct {
	deleteByDBFunc func(ctx context.Context, database string, limit int) (int, error)
}

func (m *fullMockDocStore) Get(ctx context.Context, database string, path string, _ ...types.ReadOptions) (*types.StoredDoc, error) {
	return nil, nil
}
func (m *fullMockDocStore) GetMany(ctx context.Context, database string, paths []string, _ ...types.ReadOptions) ([]*types.StoredDoc, error) {
	return nil, nil
}
func (m *fullMockDocStore) Create(ctx context.Context, database string, doc types.StoredDoc) error {
	return nil
}
func (m *fullMockDocStore) Update(ctx context.Context, database string, path string, data map[string]interface{}, pred model.Filters) error {
	return nil
}
func (m *fullMockDocStore) Patch(ctx context.Context, database string, path string, data map[string]interface{}, pred model.Filters) error {
	return nil
}
func (m *fullMockDocStore) Delete(ctx context.Context, database string, path string, pred model.Filters) error {
	return nil
}
func (m *fullMockDocStore) DeleteByDatabase(ctx context.Context, database string, limit int) (int, error) {
	if m.deleteByDBFunc != nil {
		return m.deleteByDBFunc(ctx, database, limit)
	}
	return 0, nil
}
func (m *fullMockDocStore) Query(ctx context.Context, database string, q model.Query) ([]*types.StoredDoc, error) {
	return nil, nil
}
func (m *fullMockDocStore) Watch(ctx context.Context, database string, collection string, after types.WatchCheckpoint, opts types.WatchOptions) (types.WatchStream, error) {
	return nil, nil
}
func (m *fullMockDocStore) Close(ctx context.Context) error { return nil }

func TestDeletionWorker_NewDeletionWorker(t *testing.T) {
	dbStore := &mockDBStore{}
	docStore := &fullMockDocStore{}
	logger := slog.Default()

	t.Run("with valid config", func(t *testing.T) {
		config := DeletionWorkerConfig{
			Interval:           1 * time.Minute,
			BatchSize:          50,
			MaxBatchesPerCycle: 5,
		}
		worker := NewDeletionWorker(dbStore, docStore, nil, config, logger)

		assert.NotNil(t, worker)
		assert.Equal(t, 50, worker.config.BatchSize)
		assert.Equal(t, 5, worker.config.MaxBatchesPerCycle)
	})

	t.Run("with zero config uses defaults", func(t *testing.T) {
		config := DeletionWorkerConfig{} // all zeros
		worker := NewDeletionWorker(dbStore, docStore, nil, config, logger)

		assert.NotNil(t, worker)
		// Defaults are applied
		assert.Equal(t, 100, worker.config.BatchSize)
		assert.Equal(t, 10, worker.config.MaxBatchesPerCycle)
	})
}

func TestDeletionWorker_StartStop(t *testing.T) {
	dbStore := &mockDBStore{
		listFunc: func(ctx context.Context, opts ListOptions) ([]*Database, int, error) {
			return []*Database{}, 0, nil // No databases to delete
		},
	}
	docStore := &fullMockDocStore{}
	logger := slog.Default()

	config := DeletionWorkerConfig{
		Interval:           50 * time.Millisecond,
		BatchSize:          10,
		MaxBatchesPerCycle: 1,
	}

	worker := NewDeletionWorker(dbStore, docStore, nil, config, logger)

	t.Run("start and stop", func(t *testing.T) {
		ctx := context.Background()

		// Start
		err := worker.Start(ctx)
		require.NoError(t, err)
		assert.True(t, worker.IsRunning())

		// Wait a bit
		time.Sleep(100 * time.Millisecond)

		// Stop
		err = worker.Stop(context.Background())
		require.NoError(t, err)
		assert.False(t, worker.IsRunning())
	})

	t.Run("double start is no-op", func(t *testing.T) {
		ctx := context.Background()

		err := worker.Start(ctx)
		require.NoError(t, err)

		err = worker.Start(ctx) // Second start should be no-op
		require.NoError(t, err)

		err = worker.Stop(context.Background())
		require.NoError(t, err)
	})
}

func TestDeletionWorker_ProcessDatabases(t *testing.T) {
	slug := "test-db"
	deletingDB := &Database{
		ID:     "db123",
		Slug:   &slug,
		Status: StatusDeleting,
	}

	deletedDBs := make([]string, 0)
	invalidatedDBs := make([]string, 0)

	dbStore := &mockDBStore{
		databases: []*Database{deletingDB},
		listFunc: func(ctx context.Context, opts ListOptions) ([]*Database, int, error) {
			if opts.Status == StatusDeleting {
				result := make([]*Database, 0)
				for _, db := range []*Database{deletingDB} {
					if db.Status == StatusDeleting {
						result = append(result, db)
					}
				}
				return result, len(result), nil
			}
			return []*Database{}, 0, nil
		},
		deleteFunc: func(ctx context.Context, id string) error {
			deletedDBs = append(deletedDBs, id)
			return nil
		},
	}

	docDeleteCalls := 0
	docStore := &fullMockDocStore{
		deleteByDBFunc: func(ctx context.Context, database string, limit int) (int, error) {
			docDeleteCalls++
			if docDeleteCalls == 1 {
				return 0, nil // No documents to delete
			}
			return 0, nil
		},
	}

	indexer := &mockIndexInvalidator{
		invalidateFunc: func(ctx context.Context, database string) error {
			invalidatedDBs = append(invalidatedDBs, database)
			return nil
		},
	}

	logger := slog.Default()
	config := DeletionWorkerConfig{
		Interval:           50 * time.Millisecond,
		BatchSize:          10,
		MaxBatchesPerCycle: 1,
	}

	worker := NewDeletionWorker(dbStore, docStore, indexer, config, logger)

	// Run one cycle manually
	ctx := context.Background()
	worker.processDeletingDatabases(ctx)

	// Verify database was processed
	assert.Contains(t, deletedDBs, "db123")
	assert.Contains(t, invalidatedDBs, "db123")
}

func TestDeletionWorker_BatchDeletion(t *testing.T) {
	slug := "batch-db"
	deletingDB := &Database{
		ID:     "db-batch",
		Slug:   &slug,
		Status: StatusDeleting,
	}

	dbStore := &mockDBStore{
		databases: []*Database{deletingDB},
		listFunc: func(ctx context.Context, opts ListOptions) ([]*Database, int, error) {
			if opts.Status == StatusDeleting {
				return []*Database{deletingDB}, 1, nil
			}
			return []*Database{}, 0, nil
		},
	}

	batchCalls := 0
	docStore := &fullMockDocStore{
		deleteByDBFunc: func(ctx context.Context, database string, limit int) (int, error) {
			batchCalls++
			// First batch returns full batch size (more to delete)
			// Second batch returns 0 (no more documents)
			if batchCalls <= 2 {
				return limit, nil // Return full batch
			}
			return 0, nil // No more documents
		},
	}

	logger := slog.Default()
	config := DeletionWorkerConfig{
		Interval:           50 * time.Millisecond,
		BatchSize:          10,
		MaxBatchesPerCycle: 3, // Allow 3 batches per cycle
	}

	worker := NewDeletionWorker(dbStore, docStore, nil, config, logger)

	// Run one cycle
	ctx := context.Background()
	worker.processDeletingDatabases(ctx)

	// Should have called delete at least 3 times (batches) + 1 (check for remaining)
	assert.GreaterOrEqual(t, batchCalls, 3)
}

func TestDeletionWorker_LastRunTime(t *testing.T) {
	dbStore := &mockDBStore{
		listFunc: func(ctx context.Context, opts ListOptions) ([]*Database, int, error) {
			return []*Database{}, 0, nil
		},
	}
	docStore := &fullMockDocStore{}
	logger := slog.Default()

	config := DeletionWorkerConfig{
		Interval:           50 * time.Millisecond,
		BatchSize:          10,
		MaxBatchesPerCycle: 1,
	}

	worker := NewDeletionWorker(dbStore, docStore, nil, config, logger)

	// Before running, last run time should be zero
	assert.True(t, worker.LastRunTime().IsZero())

	// Run one cycle
	ctx := context.Background()
	worker.processDeletingDatabases(ctx)

	// After running, last run time should be set
	assert.False(t, worker.LastRunTime().IsZero())
	assert.WithinDuration(t, time.Now(), worker.LastRunTime(), 1*time.Second)
}

func TestNewDeletionWorker_DefaultsOnZeroBatchSize(t *testing.T) {
	dbStore := &mockDBStore{}
	docStore := &fullMockDocStore{}
	logger := slog.Default()

	// Config with valid Interval but zero BatchSize
	config := DeletionWorkerConfig{
		Interval:           5 * time.Minute,
		BatchSize:          0, // Should default to 100
		MaxBatchesPerCycle: 0, // Should default to 10
	}

	worker := NewDeletionWorker(dbStore, docStore, nil, config, logger)
	assert.NotNil(t, worker)
	assert.Equal(t, 100, worker.config.BatchSize)
	assert.Equal(t, 10, worker.config.MaxBatchesPerCycle)
}

func TestNewDeletionWorker_DefaultsOnNegativeBatchSize(t *testing.T) {
	dbStore := &mockDBStore{}
	docStore := &fullMockDocStore{}
	logger := slog.Default()

	// Config with negative values
	config := DeletionWorkerConfig{
		Interval:           5 * time.Minute,
		BatchSize:          -1,
		MaxBatchesPerCycle: -1,
	}

	worker := NewDeletionWorker(dbStore, docStore, nil, config, logger)
	assert.NotNil(t, worker)
	assert.Equal(t, 100, worker.config.BatchSize)
	assert.Equal(t, 10, worker.config.MaxBatchesPerCycle)
}

func TestDeletionWorker_ProcessDeletingDatabases_ListError(t *testing.T) {
	listErr := assert.AnError
	dbStore := &mockDBStore{
		listFunc: func(ctx context.Context, opts ListOptions) ([]*Database, int, error) {
			return nil, 0, listErr
		},
	}
	docStore := &fullMockDocStore{}
	logger := slog.Default()

	config := DeletionWorkerConfig{
		Interval:           50 * time.Millisecond,
		BatchSize:          10,
		MaxBatchesPerCycle: 1,
	}

	worker := NewDeletionWorker(dbStore, docStore, nil, config, logger)

	// Run one cycle - should handle error gracefully
	ctx := context.Background()
	worker.processDeletingDatabases(ctx)

	// Verify last run time was updated despite the error
	assert.False(t, worker.LastRunTime().IsZero())
}

func TestDeletionWorker_ProcessDeletingDatabases_ContextCancelled(t *testing.T) {
	slug := "test-db"
	processedDBs := make([]string, 0)

	dbStore := &mockDBStore{
		listFunc: func(ctx context.Context, opts ListOptions) ([]*Database, int, error) {
			return []*Database{
				{ID: "db1", Slug: &slug, Status: StatusDeleting},
				{ID: "db2", Slug: &slug, Status: StatusDeleting},
				{ID: "db3", Slug: &slug, Status: StatusDeleting},
			}, 3, nil
		},
	}

	docStore := &fullMockDocStore{
		deleteByDBFunc: func(ctx context.Context, database string, limit int) (int, error) {
			processedDBs = append(processedDBs, database)
			return 0, nil
		},
	}
	logger := slog.Default()

	config := DeletionWorkerConfig{
		Interval:           50 * time.Millisecond,
		BatchSize:          10,
		MaxBatchesPerCycle: 1,
	}

	worker := NewDeletionWorker(dbStore, docStore, nil, config, logger)

	// Create a context that's already cancelled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Run the cycle with cancelled context
	worker.processDeletingDatabases(ctx)

	// The loop should exit early due to context cancellation
	// At most the first database might be processed before context check
	assert.LessOrEqual(t, len(processedDBs), 1)
}

func TestDeletionWorker_ProcessDatabase_DeleteDocumentsError(t *testing.T) {
	slug := "test-db"
	deletingDB := &Database{
		ID:     "db-error",
		Slug:   &slug,
		Status: StatusDeleting,
	}

	hardDeleteCalled := false
	dbStore := &mockDBStore{
		listFunc: func(ctx context.Context, opts ListOptions) ([]*Database, int, error) {
			if opts.Status == StatusDeleting {
				return []*Database{deletingDB}, 1, nil
			}
			return []*Database{}, 0, nil
		},
		deleteFunc: func(ctx context.Context, id string) error {
			hardDeleteCalled = true
			return nil
		},
	}

	docStore := &fullMockDocStore{
		deleteByDBFunc: func(ctx context.Context, database string, limit int) (int, error) {
			return 0, assert.AnError // Simulate document deletion error
		},
	}

	logger := slog.Default()
	config := DeletionWorkerConfig{
		Interval:           50 * time.Millisecond,
		BatchSize:          10,
		MaxBatchesPerCycle: 1,
	}

	worker := NewDeletionWorker(dbStore, docStore, nil, config, logger)

	// Run one cycle
	ctx := context.Background()
	worker.processDeletingDatabases(ctx)

	// Hard delete should NOT be called due to document deletion error
	assert.False(t, hardDeleteCalled, "hard delete should not be called when document deletion fails")
}

func TestDeletionWorker_ProcessDatabase_RemainingDocumentsCheck(t *testing.T) {
	slug := "test-db"
	deletingDB := &Database{
		ID:     "db-remaining",
		Slug:   &slug,
		Status: StatusDeleting,
	}

	hardDeleteCalled := false
	dbStore := &mockDBStore{
		listFunc: func(ctx context.Context, opts ListOptions) ([]*Database, int, error) {
			if opts.Status == StatusDeleting {
				return []*Database{deletingDB}, 1, nil
			}
			return []*Database{}, 0, nil
		},
		deleteFunc: func(ctx context.Context, id string) error {
			hardDeleteCalled = true
			return nil
		},
	}

	deleteCallCount := 0
	docStore := &fullMockDocStore{
		deleteByDBFunc: func(ctx context.Context, database string, limit int) (int, error) {
			deleteCallCount++
			if deleteCallCount <= 1 {
				return 0, nil // First batch: no documents deleted
			}
			// Check for remaining documents: return 1 to indicate more work
			return 1, nil
		},
	}

	logger := slog.Default()
	config := DeletionWorkerConfig{
		Interval:           50 * time.Millisecond,
		BatchSize:          10,
		MaxBatchesPerCycle: 1,
	}

	worker := NewDeletionWorker(dbStore, docStore, nil, config, logger)

	// Run one cycle
	ctx := context.Background()
	worker.processDeletingDatabases(ctx)

	// Hard delete should NOT be called because there are remaining documents
	assert.False(t, hardDeleteCalled, "hard delete should not be called when remaining documents exist")
}

func TestDeletionWorker_ProcessDatabase_CheckRemainingError(t *testing.T) {
	slug := "test-db"
	deletingDB := &Database{
		ID:     "db-check-error",
		Slug:   &slug,
		Status: StatusDeleting,
	}

	hardDeleteCalled := false
	dbStore := &mockDBStore{
		listFunc: func(ctx context.Context, opts ListOptions) ([]*Database, int, error) {
			if opts.Status == StatusDeleting {
				return []*Database{deletingDB}, 1, nil
			}
			return []*Database{}, 0, nil
		},
		deleteFunc: func(ctx context.Context, id string) error {
			hardDeleteCalled = true
			return nil
		},
	}

	deleteCallCount := 0
	docStore := &fullMockDocStore{
		deleteByDBFunc: func(ctx context.Context, database string, limit int) (int, error) {
			deleteCallCount++
			if deleteCallCount <= 1 {
				return 0, nil // First batch: no documents
			}
			// Check remaining: return error
			return 0, assert.AnError
		},
	}

	logger := slog.Default()
	config := DeletionWorkerConfig{
		Interval:           50 * time.Millisecond,
		BatchSize:          10,
		MaxBatchesPerCycle: 1,
	}

	worker := NewDeletionWorker(dbStore, docStore, nil, config, logger)

	// Run one cycle
	ctx := context.Background()
	worker.processDeletingDatabases(ctx)

	// Hard delete should NOT be called due to check error
	assert.False(t, hardDeleteCalled, "hard delete should not be called when check remaining fails")
}

func TestDeletionWorker_ProcessDatabase_IndexerError(t *testing.T) {
	slug := "test-db"
	deletingDB := &Database{
		ID:     "db-indexer-error",
		Slug:   &slug,
		Status: StatusDeleting,
	}

	hardDeleteCalled := false
	dbStore := &mockDBStore{
		listFunc: func(ctx context.Context, opts ListOptions) ([]*Database, int, error) {
			if opts.Status == StatusDeleting {
				return []*Database{deletingDB}, 1, nil
			}
			return []*Database{}, 0, nil
		},
		deleteFunc: func(ctx context.Context, id string) error {
			hardDeleteCalled = true
			return nil
		},
	}

	docStore := &fullMockDocStore{
		deleteByDBFunc: func(ctx context.Context, database string, limit int) (int, error) {
			return 0, nil // No documents
		},
	}

	indexer := &mockIndexInvalidator{
		invalidateFunc: func(ctx context.Context, database string) error {
			return assert.AnError // Indexer returns error
		},
	}

	logger := slog.Default()
	config := DeletionWorkerConfig{
		Interval:           50 * time.Millisecond,
		BatchSize:          10,
		MaxBatchesPerCycle: 1,
	}

	worker := NewDeletionWorker(dbStore, docStore, indexer, config, logger)

	// Run one cycle
	ctx := context.Background()
	worker.processDeletingDatabases(ctx)

	// Hard delete should STILL be called despite indexer error (per code comment)
	assert.True(t, hardDeleteCalled, "hard delete should be called even when indexer fails")
}

func TestDeletionWorker_ProcessDatabase_HardDeleteError(t *testing.T) {
	slug := "test-db"
	deletingDB := &Database{
		ID:     "db-hard-delete-error",
		Slug:   &slug,
		Status: StatusDeleting,
	}

	dbStore := &mockDBStore{
		listFunc: func(ctx context.Context, opts ListOptions) ([]*Database, int, error) {
			if opts.Status == StatusDeleting {
				return []*Database{deletingDB}, 1, nil
			}
			return []*Database{}, 0, nil
		},
		deleteFunc: func(ctx context.Context, id string) error {
			return assert.AnError // Hard delete fails
		},
	}

	docStore := &fullMockDocStore{
		deleteByDBFunc: func(ctx context.Context, database string, limit int) (int, error) {
			return 0, nil // No documents
		},
	}

	logger := slog.Default()
	config := DeletionWorkerConfig{
		Interval:           50 * time.Millisecond,
		BatchSize:          10,
		MaxBatchesPerCycle: 1,
	}

	worker := NewDeletionWorker(dbStore, docStore, nil, config, logger)

	// Run one cycle - should complete without panic
	ctx := context.Background()
	worker.processDeletingDatabases(ctx)

	// Just verify it doesn't crash; error is logged
	assert.False(t, worker.LastRunTime().IsZero())
}

func TestDeletionWorker_ProcessDatabase_NilSlug(t *testing.T) {
	// Database without slug
	deletingDB := &Database{
		ID:     "db-no-slug",
		Slug:   nil, // No slug
		Status: StatusDeleting,
	}

	deletedDBs := make([]string, 0)
	dbStore := &mockDBStore{
		listFunc: func(ctx context.Context, opts ListOptions) ([]*Database, int, error) {
			if opts.Status == StatusDeleting {
				return []*Database{deletingDB}, 1, nil
			}
			return []*Database{}, 0, nil
		},
		deleteFunc: func(ctx context.Context, id string) error {
			deletedDBs = append(deletedDBs, id)
			return nil
		},
	}

	docStore := &fullMockDocStore{
		deleteByDBFunc: func(ctx context.Context, database string, limit int) (int, error) {
			return 0, nil // No documents
		},
	}

	logger := slog.Default()
	config := DeletionWorkerConfig{
		Interval:           50 * time.Millisecond,
		BatchSize:          10,
		MaxBatchesPerCycle: 1,
	}

	worker := NewDeletionWorker(dbStore, docStore, nil, config, logger)

	// Run one cycle
	ctx := context.Background()
	worker.processDeletingDatabases(ctx)

	// Verify database was processed successfully even without slug
	assert.Contains(t, deletedDBs, "db-no-slug")
}

func TestDeletionWorker_ProcessDatabase_ContextCancelledDuringDeletion(t *testing.T) {
	slug := "test-db"
	deletingDB := &Database{
		ID:     "db-ctx-cancel",
		Slug:   &slug,
		Status: StatusDeleting,
	}

	hardDeleteCalled := false
	dbStore := &mockDBStore{
		listFunc: func(ctx context.Context, opts ListOptions) ([]*Database, int, error) {
			if opts.Status == StatusDeleting {
				return []*Database{deletingDB}, 1, nil
			}
			return []*Database{}, 0, nil
		},
		deleteFunc: func(ctx context.Context, id string) error {
			hardDeleteCalled = true
			return nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	batchCount := 0
	docStore := &fullMockDocStore{
		deleteByDBFunc: func(c context.Context, database string, limit int) (int, error) {
			batchCount++
			if batchCount == 1 {
				cancel() // Cancel context during first batch
			}
			return limit, nil // Return full batch to continue loop
		},
	}

	logger := slog.Default()
	config := DeletionWorkerConfig{
		Interval:           50 * time.Millisecond,
		BatchSize:          10,
		MaxBatchesPerCycle: 5, // Allow multiple batches
	}

	worker := NewDeletionWorker(dbStore, docStore, nil, config, logger)

	// Run one cycle with context that will be cancelled during processing
	worker.processDatabase(ctx, deletingDB)

	// Hard delete should NOT be called because context was cancelled
	assert.False(t, hardDeleteCalled, "hard delete should not be called when context is cancelled")
}

func TestDeletionWorker_Stop_ContextTimeout(t *testing.T) {
	dbStore := &mockDBStore{
		listFunc: func(ctx context.Context, opts ListOptions) ([]*Database, int, error) {
			// Simulate slow operation
			time.Sleep(200 * time.Millisecond)
			return []*Database{}, 0, nil
		},
	}
	docStore := &fullMockDocStore{}
	logger := slog.Default()

	config := DeletionWorkerConfig{
		Interval:           10 * time.Millisecond,
		BatchSize:          10,
		MaxBatchesPerCycle: 1,
	}

	worker := NewDeletionWorker(dbStore, docStore, nil, config, logger)

	// Start the worker
	ctx := context.Background()
	err := worker.Start(ctx)
	require.NoError(t, err)

	// Wait for worker to start processing
	time.Sleep(50 * time.Millisecond)

	// Stop with a very short timeout
	stopCtx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	err = worker.Stop(stopCtx)
	// Should return context.DeadlineExceeded
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	// Clean up: stop with longer timeout
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cleanupCancel()
	_ = worker.Stop(cleanupCtx)
}

func TestDeletionWorker_Stop_WhenNotRunning(t *testing.T) {
	dbStore := &mockDBStore{}
	docStore := &fullMockDocStore{}
	logger := slog.Default()

	config := DeletionWorkerConfig{
		Interval:           50 * time.Millisecond,
		BatchSize:          10,
		MaxBatchesPerCycle: 1,
	}

	worker := NewDeletionWorker(dbStore, docStore, nil, config, logger)

	// Stop without starting - should be no-op
	err := worker.Stop(context.Background())
	assert.NoError(t, err)
	assert.False(t, worker.IsRunning())
}
