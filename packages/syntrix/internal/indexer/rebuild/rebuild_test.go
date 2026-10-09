package rebuild

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codetreker/syntrix/internal/core/storage/types"
	"github.com/codetreker/syntrix/internal/indexer/mem_store"
	"github.com/codetreker/syntrix/internal/indexer/store"
	"github.com/codetreker/syntrix/internal/indexer/template"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// mockKeyBuilder implements OrderKeyBuilder for testing.
type mockKeyBuilder struct {
	err error
}

func (m *mockKeyBuilder) BuildOrderKey(data map[string]any, tmpl *template.Template) ([]byte, error) {
	if m.err != nil {
		return nil, m.err
	}
	// Simple key based on "id" field
	if id, ok := data["id"].(string); ok {
		return []byte(id), nil
	}
	if id, ok := data["id"].(float64); ok {
		return []byte{byte(id)}, nil
	}
	return []byte{0x00}, nil
}

// mockStorageScanner implements StorageScanner for testing.
type mockStorageScanner struct {
	docs []*types.StoredDoc
	err  error
}

func (m *mockStorageScanner) ScanCollection(ctx context.Context, database, collectionPattern string, batchSize int, startAfter string) (DocIterator, error) {
	if m.err != nil {
		return nil, m.err
	}

	// Find starting position
	startIdx := 0
	if startAfter != "" {
		for i, doc := range m.docs {
			if doc.Id == startAfter {
				startIdx = i + 1
				break
			}
		}
	}

	// Return batch
	endIdx := startIdx + batchSize
	if endIdx > len(m.docs) {
		endIdx = len(m.docs)
	}

	return &mockDocIterator{
		docs: m.docs[startIdx:endIdx],
		idx:  -1,
	}, nil
}

// mockDocIterator implements DocIterator for testing.
type mockDocIterator struct {
	docs []*types.StoredDoc
	idx  int
	err  error
}

func (m *mockDocIterator) Next() bool {
	m.idx++
	return m.idx < len(m.docs)
}

func (m *mockDocIterator) Doc() *types.StoredDoc {
	if m.idx >= 0 && m.idx < len(m.docs) {
		return m.docs[m.idx]
	}
	return nil
}

func (m *mockDocIterator) Err() error {
	return m.err
}

func (m *mockDocIterator) Close() error {
	return nil
}

// mockEventReplayer implements EventReplayer for testing.
type mockEventReplayer struct {
	events           []*Event
	err              error
	currentPosition  string // The current buffer position
	receivedStartKey string // Records the startKey passed to ReplayFrom
	positionErr      error  // Error to return from CurrentPosition
}

func (m *mockEventReplayer) CurrentPosition(ctx context.Context) (string, error) {
	if m.positionErr != nil {
		return "", m.positionErr
	}
	return m.currentPosition, nil
}

func (m *mockEventReplayer) ReplayFrom(ctx context.Context, startKey string, callback func(evt *Event) error) error {
	m.receivedStartKey = startKey // Record the startKey for verification
	if m.err != nil {
		return m.err
	}
	for _, evt := range m.events {
		if err := callback(evt); err != nil {
			return err
		}
	}
	return nil
}

func TestDefaultConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	assert.Equal(t, 500, cfg.BatchSize)
	assert.Equal(t, 5000, cfg.QPSLimit)
	assert.Equal(t, 2, cfg.MaxConcurrent)
}

func TestNew(t *testing.T) {
	t.Parallel()
	cfg := Config{BatchSize: 100, QPSLimit: 1000, MaxConcurrent: 1}
	o := New(cfg, &mockKeyBuilder{}, testLogger())

	require.NotNil(t, o)
	assert.Equal(t, 100, o.cfg.BatchSize)
	assert.Equal(t, 1000, o.cfg.QPSLimit)
	assert.Equal(t, 1, o.cfg.MaxConcurrent)
}

func TestNew_DefaultValues(t *testing.T) {
	t.Parallel()
	cfg := Config{} // All zeros
	o := New(cfg, &mockKeyBuilder{}, testLogger())

	assert.Equal(t, 500, o.cfg.BatchSize)
	assert.Equal(t, 5000, o.cfg.QPSLimit)
	assert.Equal(t, 2, o.cfg.MaxConcurrent)
}

func TestOrchestrator_StartRebuild(t *testing.T) {
	t.Parallel()
	cfg := Config{BatchSize: 10, QPSLimit: 10000, MaxConcurrent: 2}
	o := New(cfg, &mockKeyBuilder{}, testLogger())

	st := mem_store.New()
	idxRef := IndexRef{
		Database:   "testdb",
		Pattern:    "users/*/chats",
		TemplateID: "test_template",
		RawPattern: "users/{uid}/chats",
	}
	tmpl := &template.Template{
		Name:              "test_template",
		CollectionPattern: "users/{uid}/chats",
	}

	scanner := &mockStorageScanner{
		docs: []*types.StoredDoc{
			{Id: "doc1", Data: map[string]any{"id": "a"}},
			{Id: "doc2", Data: map[string]any{"id": "b"}},
			{Id: "doc3", Data: map[string]any{"id": "c"}},
		},
	}

	ctx := context.Background()
	jobID, err := o.StartRebuild(ctx, idxRef, tmpl, st, scanner, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, jobID)

	// Wait for job to complete
	time.Sleep(200 * time.Millisecond)

	// Check job status
	progress, err := o.GetJob(jobID)
	require.NoError(t, err)
	assert.Equal(t, StatusCompleted, progress.Status)
	assert.Equal(t, int64(3), progress.DocsTotal)
	assert.Equal(t, int64(3), progress.DocsAdded)

	// Index should be healthy
	state, _ := st.GetState(idxRef.Database, idxRef.Pattern, idxRef.TemplateID)
	assert.Equal(t, store.IndexStateHealthy, state)
}

func TestOrchestrator_StartRebuild_AlreadyRunning(t *testing.T) {
	t.Parallel()
	cfg := Config{BatchSize: 1, QPSLimit: 1, MaxConcurrent: 2}
	o := New(cfg, &mockKeyBuilder{}, testLogger())

	st := mem_store.New()
	idxRef := IndexRef{
		Database:   "testdb",
		Pattern:    "users/*/chats",
		TemplateID: "test_template",
		RawPattern: "users/{uid}/chats",
	}
	tmpl := &template.Template{
		Name:              "test_template",
		CollectionPattern: "users/{uid}/chats",
	}

	// Scanner with many docs to keep job running
	docs := make([]*types.StoredDoc, 100)
	for i := range docs {
		docs[i] = &types.StoredDoc{Id: string(rune('a' + i)), Data: map[string]any{"id": float64(i)}}
	}
	scanner := &mockStorageScanner{docs: docs}

	ctx := context.Background()
	jobID1, err := o.StartRebuild(ctx, idxRef, tmpl, st, scanner, nil)
	require.NoError(t, err)

	// Try to start another rebuild for same index
	_, err = o.StartRebuild(ctx, idxRef, tmpl, st, scanner, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already in progress")

	// Cancel the first job
	err = o.CancelRebuild(jobID1)
	require.NoError(t, err)

	// Wait for cancellation
	time.Sleep(100 * time.Millisecond)
}

func TestOrchestrator_CancelRebuild(t *testing.T) {
	t.Parallel()
	cfg := Config{BatchSize: 1, QPSLimit: 1, MaxConcurrent: 2}
	o := New(cfg, &mockKeyBuilder{}, testLogger())

	st := mem_store.New()
	idxRef := IndexRef{
		Database:   "testdb",
		Pattern:    "test",
		TemplateID: "id",
		RawPattern: "test",
	}
	tmpl := &template.Template{Name: "test"}

	// Scanner with many docs
	docs := make([]*types.StoredDoc, 1000)
	for i := range docs {
		docs[i] = &types.StoredDoc{Id: string(rune(i)), Data: map[string]any{"id": float64(i)}}
	}
	scanner := &mockStorageScanner{docs: docs}

	ctx := context.Background()
	jobID, err := o.StartRebuild(ctx, idxRef, tmpl, st, scanner, nil)
	require.NoError(t, err)

	// Wait for job to start
	time.Sleep(50 * time.Millisecond)

	// Cancel
	err = o.CancelRebuild(jobID)
	require.NoError(t, err)

	// Wait for cancellation to complete
	time.Sleep(200 * time.Millisecond)

	progress, err := o.GetJob(jobID)
	require.NoError(t, err)
	assert.Equal(t, StatusCanceled, progress.Status)
}

func TestOrchestrator_ListJobs(t *testing.T) {
	t.Parallel()
	cfg := Config{BatchSize: 100, QPSLimit: 10000, MaxConcurrent: 2}
	o := New(cfg, &mockKeyBuilder{}, testLogger())

	st := mem_store.New()
	idxRef1 := IndexRef{
		Database:   "db1",
		Pattern:    "a",
		TemplateID: "id",
		RawPattern: "a",
	}
	idxRef2 := IndexRef{
		Database:   "db2",
		Pattern:    "b",
		TemplateID: "id",
		RawPattern: "b",
	}
	tmpl := &template.Template{Name: "test"}
	scanner := &mockStorageScanner{docs: []*types.StoredDoc{}}

	ctx := context.Background()
	_, err := o.StartRebuild(ctx, idxRef1, tmpl, st, scanner, nil)
	require.NoError(t, err)
	_, err = o.StartRebuild(ctx, idxRef2, tmpl, st, scanner, nil)
	require.NoError(t, err)

	// Wait for completion
	time.Sleep(100 * time.Millisecond)

	jobs := o.ListJobs()
	assert.Len(t, jobs, 2)
}

func TestOrchestrator_GetJob_NotFound(t *testing.T) {
	t.Parallel()
	o := New(DefaultConfig(), &mockKeyBuilder{}, testLogger())

	_, err := o.GetJob("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestOrchestrator_CancelRebuild_NotFound(t *testing.T) {
	t.Parallel()
	o := New(DefaultConfig(), &mockKeyBuilder{}, testLogger())

	err := o.CancelRebuild("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestOrchestrator_CleanupCompletedJobs(t *testing.T) {
	t.Parallel()
	cfg := Config{BatchSize: 100, QPSLimit: 10000, MaxConcurrent: 2}
	o := New(cfg, &mockKeyBuilder{}, testLogger())

	st := mem_store.New()
	idxRef := IndexRef{
		Database:   "testdb",
		Pattern:    "test",
		TemplateID: "id",
		RawPattern: "test",
	}
	tmpl := &template.Template{Name: "test"}
	scanner := &mockStorageScanner{docs: []*types.StoredDoc{}}

	ctx := context.Background()
	_, err := o.StartRebuild(ctx, idxRef, tmpl, st, scanner, nil)
	require.NoError(t, err)

	// Wait for completion
	time.Sleep(100 * time.Millisecond)

	// Should not remove (too recent)
	removed := o.CleanupCompletedJobs(time.Hour)
	assert.Equal(t, 0, removed)

	// Should remove (max age 0)
	removed = o.CleanupCompletedJobs(0)
	assert.Equal(t, 1, removed)

	// Job list should be empty
	jobs := o.ListJobs()
	assert.Len(t, jobs, 0)
}

func TestOrchestrator_WithEventReplay(t *testing.T) {
	t.Parallel()
	cfg := Config{BatchSize: 100, QPSLimit: 10000, MaxConcurrent: 2}
	o := New(cfg, &mockKeyBuilder{}, testLogger())

	st := mem_store.New()
	idxRef := IndexRef{
		Database:   "testdb",
		Pattern:    "test",
		TemplateID: "id",
		RawPattern: "test",
	}
	tmpl := &template.Template{Name: "test"}

	scanner := &mockStorageScanner{
		docs: []*types.StoredDoc{
			{Id: "doc1", Data: map[string]any{"id": "a"}},
		},
	}

	replayer := &mockEventReplayer{
		events: []*Event{
			{Database: "testdb", DocID: "doc2", Data: map[string]any{"id": "b"}},
			{Database: "testdb", DocID: "doc3", Data: map[string]any{"id": "c"}},
		},
	}

	ctx := context.Background()
	jobID, err := o.StartRebuild(ctx, idxRef, tmpl, st, scanner, replayer)
	require.NoError(t, err)

	// Wait for completion
	time.Sleep(200 * time.Millisecond)

	progress, err := o.GetJob(jobID)
	require.NoError(t, err)
	assert.Equal(t, StatusCompleted, progress.Status)

	// Should have 3 docs (1 from storage + 2 from replay)
	indexes, _ := st.ListIndexes(idxRef.Database)
	require.Len(t, indexes, 1)
	assert.Equal(t, 3, indexes[0].DocCount)
}

func TestOrchestrator_ScannerError(t *testing.T) {
	t.Parallel()
	cfg := Config{BatchSize: 100, QPSLimit: 10000, MaxConcurrent: 2}
	o := New(cfg, &mockKeyBuilder{}, testLogger())

	st := mem_store.New()
	idxRef := IndexRef{
		Database:   "testdb",
		Pattern:    "test",
		TemplateID: "id",
		RawPattern: "test",
	}
	tmpl := &template.Template{Name: "test"}

	scanner := &mockStorageScanner{
		err: errors.New("storage error"),
	}

	ctx := context.Background()
	jobID, err := o.StartRebuild(ctx, idxRef, tmpl, st, scanner, nil)
	require.NoError(t, err)

	// Wait for completion
	time.Sleep(200 * time.Millisecond)

	progress, err := o.GetJob(jobID)
	require.NoError(t, err)
	assert.Equal(t, StatusFailed, progress.Status)
	assert.Contains(t, progress.Error, "storage error")
	state, _ := st.GetState(idxRef.Database, idxRef.Pattern, idxRef.TemplateID)
	assert.Equal(t, store.IndexStateFailed, state)
}

func TestOrchestrator_MaxConcurrent(t *testing.T) {
	t.Parallel()
	cfg := Config{BatchSize: 1, QPSLimit: 1, MaxConcurrent: 1}
	o := New(cfg, &mockKeyBuilder{}, testLogger())

	// Create slow scanners
	docs := make([]*types.StoredDoc, 100)
	for i := range docs {
		docs[i] = &types.StoredDoc{Id: string(rune(i)), Data: map[string]any{"id": float64(i)}}
	}
	scanner := &mockStorageScanner{docs: docs}

	st := mem_store.New()
	idxRef1 := IndexRef{
		Database:   "db1",
		Pattern:    "a",
		TemplateID: "id",
		RawPattern: "a",
	}
	idxRef2 := IndexRef{
		Database:   "db2",
		Pattern:    "b",
		TemplateID: "id",
		RawPattern: "b",
	}
	tmpl := &template.Template{Name: "test"}

	ctx := context.Background()

	// Start first job
	jobID1, err := o.StartRebuild(ctx, idxRef1, tmpl, st, scanner, nil)
	require.NoError(t, err)

	// Wait for it to start
	time.Sleep(50 * time.Millisecond)

	// Start second job (should be queued)
	jobID2, err := o.StartRebuild(ctx, idxRef2, tmpl, st, scanner, nil)
	require.NoError(t, err)

	// Second job should be pending
	progress2, err := o.GetJob(jobID2)
	require.NoError(t, err)
	assert.Equal(t, StatusPending, progress2.Status)

	// Cancel first job to allow second to run
	err = o.CancelRebuild(jobID1)
	require.NoError(t, err)

	// Wait and cleanup
	time.Sleep(100 * time.Millisecond)
	o.CancelRebuild(jobID2)
}

func TestJob_Progress(t *testing.T) {
	t.Parallel()
	job := &Job{
		ID:        "test-job",
		Status:    StatusRunning,
		DocsTotal: 100,
		DocsAdded: 50,
		StartTime: time.Now().Add(-time.Minute),
	}

	progress := job.Progress()
	assert.Equal(t, "test-job", progress.ID)
	assert.Equal(t, StatusRunning, progress.Status)
	assert.Equal(t, int64(100), progress.DocsTotal)
	assert.Equal(t, int64(50), progress.DocsAdded)
}

func TestOrchestrator_DeletedEvents(t *testing.T) {
	t.Parallel()
	cfg := Config{BatchSize: 100, QPSLimit: 10000, MaxConcurrent: 2}
	o := New(cfg, &mockKeyBuilder{}, testLogger())

	st := mem_store.New()
	idxRef := IndexRef{
		Database:   "testdb",
		Pattern:    "test",
		TemplateID: "id",
		RawPattern: "test",
	}
	tmpl := &template.Template{Name: "test"}

	scanner := &mockStorageScanner{
		docs: []*types.StoredDoc{
			{Id: "doc1", Data: map[string]any{"id": "a"}},
			{Id: "doc2", Data: map[string]any{"id": "b"}},
		},
	}

	replayer := &mockEventReplayer{
		events: []*Event{
			{Database: "testdb", DocID: "doc1", Deleted: true}, // Delete doc1
		},
	}

	ctx := context.Background()
	jobID, err := o.StartRebuild(ctx, idxRef, tmpl, st, scanner, replayer)
	require.NoError(t, err)

	// Wait for completion
	time.Sleep(200 * time.Millisecond)

	progress, err := o.GetJob(jobID)
	require.NoError(t, err)
	assert.Equal(t, StatusCompleted, progress.Status)

	// Should have 1 doc (doc2 only, doc1 was deleted)
	indexes, _ := st.ListIndexes(idxRef.Database)
	require.Len(t, indexes, 1)
	assert.Equal(t, 1, indexes[0].DocCount)
}

func TestOrchestrator_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	cfg := Config{BatchSize: 100, QPSLimit: 10000, MaxConcurrent: 10}
	o := New(cfg, &mockKeyBuilder{}, testLogger())

	scanner := &mockStorageScanner{docs: []*types.StoredDoc{}}
	tmpl := &template.Template{Name: "test"}

	var wg sync.WaitGroup
	ctx := context.Background()

	// Start multiple rebuilds concurrently
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			st := mem_store.New()
			idxRef := IndexRef{
				Database:   "db",
				Pattern:    string(rune('a' + idx)),
				TemplateID: "id",
				RawPattern: string(rune('a' + idx)),
			}
			_, err := o.StartRebuild(ctx, idxRef, tmpl, st, scanner, nil)
			assert.NoError(t, err)
		}(i)
	}

	wg.Wait()

	// Wait for all to complete
	time.Sleep(200 * time.Millisecond)

	jobs := o.ListJobs()
	assert.Len(t, jobs, 10)

	for _, j := range jobs {
		assert.Equal(t, StatusCompleted, j.Status)
	}
}

func TestOrchestrator_CancelRunningJob(t *testing.T) {
	t.Parallel()
	cfg := Config{BatchSize: 1, QPSLimit: 1, MaxConcurrent: 1}
	o := New(cfg, &mockKeyBuilder{}, testLogger())

	// Create a slow scanner with many docs
	docs := make([]*types.StoredDoc, 1000)
	for i := range docs {
		docs[i] = &types.StoredDoc{Id: string(rune(i)), Data: map[string]any{"id": float64(i)}}
	}
	scanner := &mockStorageScanner{docs: docs}

	st := mem_store.New()
	idxRef := IndexRef{
		Database:   "testdb",
		Pattern:    "test",
		TemplateID: "id",
		RawPattern: "test",
	}
	tmpl := &template.Template{Name: "test"}

	ctx := context.Background()
	jobID, err := o.StartRebuild(ctx, idxRef, tmpl, st, scanner, nil)
	require.NoError(t, err)

	// Wait for it to start running
	time.Sleep(50 * time.Millisecond)

	// Cancel the running job
	err = o.CancelRebuild(jobID)
	require.NoError(t, err)

	// Wait for cancellation to take effect
	time.Sleep(100 * time.Millisecond)

	progress, err := o.GetJob(jobID)
	require.NoError(t, err)
	// Should be canceled or failed (due to context cancellation)
	assert.True(t, progress.Status == StatusCanceled || progress.Status == StatusFailed)
}

func TestOrchestrator_CancelCompletedJob(t *testing.T) {
	t.Parallel()
	cfg := Config{BatchSize: 100, QPSLimit: 10000, MaxConcurrent: 2}
	o := New(cfg, &mockKeyBuilder{}, testLogger())

	st := mem_store.New()
	idxRef := IndexRef{
		Database:   "testdb",
		Pattern:    "test",
		TemplateID: "id",
		RawPattern: "test",
	}
	tmpl := &template.Template{Name: "test"}
	scanner := &mockStorageScanner{docs: []*types.StoredDoc{}}

	ctx := context.Background()
	jobID, err := o.StartRebuild(ctx, idxRef, tmpl, st, scanner, nil)
	require.NoError(t, err)

	// Wait for completion
	time.Sleep(100 * time.Millisecond)

	// Try to cancel a completed job - should fail
	err = o.CancelRebuild(jobID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not pending or running")
}

func TestOrchestrator_ReplayEventsError(t *testing.T) {
	t.Parallel()
	cfg := Config{BatchSize: 100, QPSLimit: 10000, MaxConcurrent: 2}
	o := New(cfg, &mockKeyBuilder{}, testLogger())

	st := mem_store.New()
	idxRef := IndexRef{
		Database:   "testdb",
		Pattern:    "test",
		TemplateID: "id",
		RawPattern: "test",
	}
	tmpl := &template.Template{Name: "test"}
	scanner := &mockStorageScanner{docs: []*types.StoredDoc{}}

	// Create a replayer that returns an error
	replayer := &mockEventReplayer{
		err: errors.New("replay error"),
	}

	ctx := context.Background()
	jobID, err := o.StartRebuild(ctx, idxRef, tmpl, st, scanner, replayer)
	require.NoError(t, err)

	// Wait for completion
	time.Sleep(200 * time.Millisecond)

	progress, err := o.GetJob(jobID)
	require.NoError(t, err)
	assert.Equal(t, StatusFailed, progress.Status)
	assert.Contains(t, progress.Error, "replay error")
}

func TestOrchestrator_ReplayDifferentDatabase(t *testing.T) {
	t.Parallel()
	cfg := Config{BatchSize: 100, QPSLimit: 10000, MaxConcurrent: 2}
	o := New(cfg, &mockKeyBuilder{}, testLogger())

	st := mem_store.New()
	idxRef := IndexRef{
		Database:   "testdb",
		Pattern:    "test",
		TemplateID: "id",
		RawPattern: "test",
	}
	tmpl := &template.Template{Name: "test"}
	scanner := &mockStorageScanner{docs: []*types.StoredDoc{}}

	// Events from a different database should be ignored
	replayer := &mockEventReplayer{
		events: []*Event{
			{Database: "other-db", DocID: "doc1", Data: map[string]any{"id": "a"}},
		},
	}

	ctx := context.Background()
	jobID, err := o.StartRebuild(ctx, idxRef, tmpl, st, scanner, replayer)
	require.NoError(t, err)

	// Wait for completion
	time.Sleep(200 * time.Millisecond)

	progress, err := o.GetJob(jobID)
	require.NoError(t, err)
	assert.Equal(t, StatusCompleted, progress.Status)

	// Should have 0 docs (event was from different database)
	indexes, _ := st.ListIndexes(idxRef.Database)
	assert.Len(t, indexes, 1)
	assert.Equal(t, 0, indexes[0].DocCount)
}

func TestOrchestrator_ReplayBuildKeyError(t *testing.T) {
	t.Parallel()
	cfg := Config{BatchSize: 100, QPSLimit: 10000, MaxConcurrent: 2}
	// Use a key builder that fails
	o := New(cfg, &mockKeyBuilder{err: errors.New("build key error")}, testLogger())

	st := mem_store.New()
	idxRef := IndexRef{
		Database:   "testdb",
		Pattern:    "test",
		TemplateID: "id",
		RawPattern: "test",
	}
	tmpl := &template.Template{Name: "test"}
	scanner := &mockStorageScanner{docs: []*types.StoredDoc{}}

	replayer := &mockEventReplayer{
		events: []*Event{
			{Database: "testdb", DocID: "doc1", Data: map[string]any{"id": "a"}},
		},
	}

	ctx := context.Background()
	jobID, err := o.StartRebuild(ctx, idxRef, tmpl, st, scanner, replayer)
	require.NoError(t, err)

	// Wait for completion
	time.Sleep(200 * time.Millisecond)

	progress, err := o.GetJob(jobID)
	require.NoError(t, err)
	// Should complete (error is logged but not fatal)
	assert.Equal(t, StatusCompleted, progress.Status)

	// Doc should not be added due to key build error
	indexes, _ := st.ListIndexes(idxRef.Database)
	assert.Len(t, indexes, 1)
	assert.Equal(t, 0, indexes[0].DocCount)
}

// TestOrchestrator_ReplayUsesCurrentPosition verifies that rebuild uses the
// replayer's CurrentPosition to determine the startKey for event replay.
// This is critical for correctness: without recording the buffer position
// before the storage scan, rebuild would replay ALL buffered events instead
// of only the events that arrived during the rebuild.
func TestOrchestrator_ReplayUsesCurrentPosition(t *testing.T) {
	t.Parallel()
	cfg := Config{BatchSize: 100, QPSLimit: 10000, MaxConcurrent: 2}
	o := New(cfg, &mockKeyBuilder{}, testLogger())

	st := mem_store.New()
	idxRef := IndexRef{
		Database:   "testdb",
		Pattern:    "test",
		TemplateID: "id",
		RawPattern: "test",
	}
	tmpl := &template.Template{Name: "test"}

	// Storage has some documents
	scanner := &mockStorageScanner{
		docs: []*types.StoredDoc{
			{Id: "doc1", Data: map[string]any{"id": "a"}},
			{Id: "doc2", Data: map[string]any{"id": "b"}},
		},
	}

	// Replayer has a specific current position that should be used
	expectedStartKey := "buffer-position-12345"
	replayer := &mockEventReplayer{
		currentPosition: expectedStartKey,
		events: []*Event{
			{Database: "testdb", DocID: "doc3", Data: map[string]any{"id": "c"}},
		},
	}

	ctx := context.Background()
	jobID, err := o.StartRebuild(ctx, idxRef, tmpl, st, scanner, replayer)
	require.NoError(t, err)

	// Wait for completion
	time.Sleep(200 * time.Millisecond)

	progress, err := o.GetJob(jobID)
	require.NoError(t, err)
	assert.Equal(t, StatusCompleted, progress.Status)

	// CRITICAL: Verify that ReplayFrom was called with the correct startKey
	// obtained from CurrentPosition, NOT with an empty string.
	// If this fails, it means rebuild is replaying from the beginning of the
	// buffer instead of from the position recorded before the storage scan.
	assert.Equal(t, expectedStartKey, replayer.receivedStartKey,
		"ReplayFrom should be called with the startKey from CurrentPosition, not empty string")

	// Should have 3 docs (2 from storage + 1 from replay)
	indexes, _ := st.ListIndexes(idxRef.Database)
	require.Len(t, indexes, 1)
	assert.Equal(t, 3, indexes[0].DocCount)
}

// TestOrchestrator_ReplayCurrentPositionError verifies that rebuild fails
// gracefully when CurrentPosition returns an error.
func TestOrchestrator_ReplayCurrentPositionError(t *testing.T) {
	t.Parallel()
	cfg := Config{BatchSize: 100, QPSLimit: 10000, MaxConcurrent: 2}
	o := New(cfg, &mockKeyBuilder{}, testLogger())

	st := mem_store.New()
	idxRef := IndexRef{
		Database:   "testdb",
		Pattern:    "test",
		TemplateID: "id",
		RawPattern: "test",
	}
	tmpl := &template.Template{Name: "test"}
	scanner := &mockStorageScanner{docs: []*types.StoredDoc{}}

	// Replayer returns an error when getting current position
	replayer := &mockEventReplayer{
		positionErr: errors.New("buffer position unavailable"),
	}

	ctx := context.Background()
	jobID, err := o.StartRebuild(ctx, idxRef, tmpl, st, scanner, replayer)
	require.NoError(t, err)

	// Wait for completion
	time.Sleep(200 * time.Millisecond)

	progress, err := o.GetJob(jobID)
	require.NoError(t, err)

	// Should fail because we couldn't get the buffer position
	assert.Equal(t, StatusFailed, progress.Status)
	assert.Contains(t, progress.Error, "buffer position")
}
