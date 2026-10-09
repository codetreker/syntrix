package reconciler

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/storage/types"
	"github.com/codetreker/syntrix/internal/indexer/manager"
	"github.com/codetreker/syntrix/internal/indexer/mem_store"
	"github.com/codetreker/syntrix/internal/indexer/rebuild"
	"github.com/codetreker/syntrix/internal/indexer/store"
	"github.com/codetreker/syntrix/internal/indexer/template"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReconciler_New(t *testing.T) {
	cfg := DefaultConfig()
	st := mem_store.New()
	mgr := manager.New(st)
	logger := slog.Default()

	r := New(cfg, mgr, nil, logger)

	assert.NotNil(t, r)
	assert.Equal(t, cfg.Interval, r.cfg.Interval)
}

func TestReconciler_StartStop(t *testing.T) {
	cfg := Config{Interval: 100 * time.Millisecond}
	st := mem_store.New()
	mgr := manager.New(st)
	logger := slog.Default()

	r := New(cfg, mgr, nil, logger)

	ctx := context.Background()
	err := r.Start(ctx)
	require.NoError(t, err)

	// Starting again should fail
	err = r.Start(ctx)
	require.Error(t, err)

	// Stop
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = r.Stop(stopCtx)
	require.NoError(t, err)

	// Stopping again should be no-op
	err = r.Stop(stopCtx)
	require.NoError(t, err)
}

func TestReconciler_Trigger(t *testing.T) {
	cfg := Config{Interval: time.Hour} // Long interval to test trigger
	st := mem_store.New()
	mgr := manager.New(st)
	logger := slog.Default()

	r := New(cfg, mgr, nil, logger)

	// Trigger should not block even when not running
	r.Trigger()
	r.Trigger() // Multiple triggers should not block
}

func TestReconciler_ComputeDiff_CreateIndex(t *testing.T) {
	st := mem_store.New()
	mgr := manager.New(st)
	logger := slog.Default()

	// Load a template
	err := mgr.LoadTemplatesFromBytes([]byte(`
templates:
  - name: chat-ts
    collectionPattern: users/{uid}/chats
    fields:
      - field: timestamp
        order: desc
`))
	require.NoError(t, err)

	// Create a database (simulating existing data) by upserting an entry that matches the template
	// Use the same pattern/templateID so it won't be deleted
	st.Upsert("db1", "users/*/chats", "chat-ts", "doc1", []byte{0x01}, "")
	// But mark it as "rebuilding" so it needs to create a fresh one (or delete and use a separate dummy)
	// Actually, let's just use a different approach - upsert with matching template pattern
	// The computeDiff will see it exists and is healthy, so no create op.
	// Let me reconsider - we need a database without the index for the template.

	// Clear and create a db with a matching index (but different template ID) so there's a database
	// but not the right index
	st.DeleteIndex("db1", "users/*/chats", "chat-ts")
	st.Upsert("db1", "users/*/chats", "other-template", "doc1", []byte{0x01}, "")

	r := New(DefaultConfig(), mgr, nil, logger)

	ops := r.computeDiff()

	// Should have one create operation for chat-ts and one delete for other-template
	require.Len(t, ops, 2)

	// Find the create operation
	var createOp *Operation
	for i := range ops {
		if ops[i].Type == OpCreate {
			createOp = &ops[i]
			break
		}
	}
	require.NotNil(t, createOp, "expected a create operation")
	assert.Equal(t, "db1", createOp.Database)
	assert.Equal(t, "users/*/chats", createOp.Pattern)
	assert.Equal(t, "chat-ts", createOp.TemplateID)
}

func TestReconciler_ComputeDiff_DeleteIndex(t *testing.T) {
	st := mem_store.New()
	mgr := manager.New(st)
	logger := slog.Default()

	// Create an index without corresponding template
	st.Upsert("db1", "orphan/*/path", "id:asc", "doc1", []byte{0x01}, "")

	r := New(DefaultConfig(), mgr, nil, logger)

	ops := r.computeDiff()

	// Should have one delete operation
	assert.Len(t, ops, 1)
	assert.Equal(t, OpDelete, ops[0].Type)
	assert.Equal(t, "db1", ops[0].Database)
	assert.Equal(t, "orphan/*/path", ops[0].Pattern)
}

func TestReconciler_ComputeDiff_RebuildIndex(t *testing.T) {
	st := mem_store.New()
	mgr := manager.New(st)
	logger := slog.Default()

	// Load a template
	err := mgr.LoadTemplatesFromBytes([]byte(`
templates:
  - name: chat-ts
    collectionPattern: users/{uid}/chats
    fields:
      - field: timestamp
        order: desc
`))
	require.NoError(t, err)

	// Create an index that is failed
	st.Upsert("db1", "users/*/chats", "chat-ts", "doc1", []byte{0x01}, "")
	st.SetState("db1", "users/*/chats", "chat-ts", store.IndexStateFailed)

	r := New(DefaultConfig(), mgr, nil, logger)

	ops := r.computeDiff()

	// Should have one rebuild operation
	assert.Len(t, ops, 1)
	assert.Equal(t, OpRebuild, ops[0].Type)
	assert.Equal(t, "db1", ops[0].Database)
}

func TestReconciler_ComputeDiff_NoOp(t *testing.T) {
	st := mem_store.New()
	mgr := manager.New(st)
	logger := slog.Default()

	// Load a template
	err := mgr.LoadTemplatesFromBytes([]byte(`
templates:
  - name: chat-ts
    collectionPattern: users/{uid}/chats
    fields:
      - field: timestamp
        order: desc
`))
	require.NoError(t, err)

	// Create a healthy index matching the template
	st.Upsert("db1", "users/*/chats", "chat-ts", "doc1", []byte{0x01}, "")
	st.SetState("db1", "users/*/chats", "chat-ts", store.IndexStateHealthy)

	r := New(DefaultConfig(), mgr, nil, logger)

	ops := r.computeDiff()

	// No operations needed
	assert.Len(t, ops, 0)
}

func TestReconciler_ExecuteDelete(t *testing.T) {
	st := mem_store.New()
	mgr := manager.New(st)
	logger := slog.Default()

	// Create an index
	st.Upsert("db1", "users/*/chats", "ts:desc", "doc1", []byte{0x01}, "")
	indexes, _ := st.ListIndexes("db1")
	assert.Equal(t, 1, len(indexes))

	r := New(DefaultConfig(), mgr, nil, logger)

	op := &Operation{
		Type:       OpDelete,
		Database:   "db1",
		Pattern:    "users/*/chats",
		TemplateID: "ts:desc",
	}

	err := r.executeDelete(context.Background(), op)
	require.NoError(t, err)

	// Index should be deleted
	indexes, _ = st.ListIndexes("db1")
	assert.Equal(t, 0, len(indexes))
}

func TestReconciler_ExecuteCreate_NoRebuilder(t *testing.T) {
	st := mem_store.New()
	mgr := manager.New(st)
	logger := slog.Default()

	tmpl := &template.Template{
		Name:              "chat-ts",
		CollectionPattern: "users/{uid}/chats",
		Fields: []template.Field{
			{Field: "timestamp", Order: template.Desc},
		},
	}

	r := New(DefaultConfig(), mgr, nil, logger)

	op := &Operation{
		Type:       OpCreate,
		Database:   "db1",
		Pattern:    "users/*/chats",
		TemplateID: "chat-ts",
		Template:   tmpl,
	}

	err := r.executeCreate(context.Background(), op)
	require.NoError(t, err)

	// Index should be created and healthy (no rebuilder)
	state, err := st.GetState("db1", "users/*/chats", "chat-ts")
	require.NoError(t, err)
	assert.Equal(t, store.IndexStateHealthy, state)
}

func TestReconciler_ExecuteRebuild_NoRebuilder(t *testing.T) {
	st := mem_store.New()
	mgr := manager.New(st)
	logger := slog.Default()

	// Create a failed index
	st.Upsert("db1", "users/*/chats", "ts:desc", "doc1", []byte{0x01}, "")
	st.SetState("db1", "users/*/chats", "ts:desc", store.IndexStateFailed)

	tmpl := &template.Template{
		Name:              "ts:desc",
		CollectionPattern: "users/{uid}/chats",
		Fields: []template.Field{
			{Field: "timestamp", Order: template.Desc},
		},
	}

	r := New(DefaultConfig(), mgr, nil, logger)

	op := &Operation{
		Type:       OpRebuild,
		Database:   "db1",
		Pattern:    "users/*/chats",
		TemplateID: "ts:desc",
		Template:   tmpl,
	}

	err := r.executeRebuild(context.Background(), op)
	require.NoError(t, err)

	// Index should be cleared and healthy
	state, err := st.GetState("db1", "users/*/chats", "ts:desc")
	require.NoError(t, err)
	assert.Equal(t, store.IndexStateHealthy, state)
	// Index should be empty (deleted and recreated)
	indexes, _ := st.ListIndexes("db1")
	if len(indexes) > 0 {
		assert.Equal(t, 0, indexes[0].DocCount)
	}
}

func TestReconciler_PendingOperations(t *testing.T) {
	st := mem_store.New()
	mgr := manager.New(st)
	logger := slog.Default()

	r := New(DefaultConfig(), mgr, nil, logger)

	// Initially empty
	ops := r.PendingOperations()
	assert.Len(t, ops, 0)
}

func TestExtractDatabase(t *testing.T) {
	tests := []struct {
		key  string
		want string
	}{
		{"db1|users/*/chats|ts:desc", "db1"},
		{"mydb|path|id", "mydb"},
		{"single", "single"},
	}

	for _, tt := range tests {
		got := extractDatabase(tt.key)
		assert.Equal(t, tt.want, got)
	}
}

func TestOpType_String(t *testing.T) {
	assert.Equal(t, OpType("create"), OpCreate)
	assert.Equal(t, OpType("delete"), OpDelete)
	assert.Equal(t, OpType("rebuild"), OpRebuild)
}

func TestOpStatus_String(t *testing.T) {
	assert.Equal(t, OpStatus("pending"), StatusPending)
	assert.Equal(t, OpStatus("running"), StatusRunning)
	assert.Equal(t, OpStatus("done"), StatusDone)
	assert.Equal(t, OpStatus("failed"), StatusFailed)
}

func TestReconciler_ExecuteOperation_Create(t *testing.T) {
	st := mem_store.New()
	mgr := manager.New(st)
	logger := slog.Default()

	tmpl := &template.Template{
		Name:              "chat-ts",
		CollectionPattern: "users/{uid}/chats",
		Fields: []template.Field{
			{Field: "timestamp", Order: template.Desc},
		},
	}

	r := New(DefaultConfig(), mgr, nil, logger)

	op := &Operation{
		Type:       OpCreate,
		Database:   "db1",
		Pattern:    "users/*/chats",
		TemplateID: "chat-ts",
		Template:   tmpl,
		Status:     StatusPending,
	}

	r.executeOperation(context.Background(), op)

	// Operation should be marked as done
	assert.Equal(t, StatusDone, op.Status)
	assert.Empty(t, op.Error)
	assert.False(t, op.StartedAt.IsZero())

	// Index should exist (state set to healthy)
	state, err := st.GetState("db1", "users/*/chats", "chat-ts")
	require.NoError(t, err)
	assert.Equal(t, store.IndexStateHealthy, state)
}

func TestReconciler_ExecuteOperation_Delete(t *testing.T) {
	st := mem_store.New()
	mgr := manager.New(st)
	logger := slog.Default()

	// Create an index first
	st.Upsert("db1", "users/*/chats", "ts:desc", "doc1", []byte{0x01}, "")

	r := New(DefaultConfig(), mgr, nil, logger)

	op := &Operation{
		Type:       OpDelete,
		Database:   "db1",
		Pattern:    "users/*/chats",
		TemplateID: "ts:desc",
		Status:     StatusPending,
	}

	r.executeOperation(context.Background(), op)

	// Operation should be marked as done
	assert.Equal(t, StatusDone, op.Status)
	assert.Empty(t, op.Error)

	// Index should be deleted
	indexes, _ := st.ListIndexes("db1")
	assert.Equal(t, 0, len(indexes))
}

func TestReconciler_ExecuteOperation_Rebuild(t *testing.T) {
	st := mem_store.New()
	mgr := manager.New(st)
	logger := slog.Default()

	// Create a failed index
	st.Upsert("db1", "users/*/chats", "ts:desc", "doc1", []byte{0x01}, "")
	st.SetState("db1", "users/*/chats", "ts:desc", store.IndexStateFailed)

	tmpl := &template.Template{
		Name:              "ts:desc",
		CollectionPattern: "users/{uid}/chats",
		Fields: []template.Field{
			{Field: "timestamp", Order: template.Desc},
		},
	}

	r := New(DefaultConfig(), mgr, nil, logger)

	op := &Operation{
		Type:       OpRebuild,
		Database:   "db1",
		Pattern:    "users/*/chats",
		TemplateID: "ts:desc",
		Template:   tmpl,
		Status:     StatusPending,
	}

	r.executeOperation(context.Background(), op)

	// Operation should be marked as done
	assert.Equal(t, StatusDone, op.Status)
	assert.Empty(t, op.Error)
}

func TestReconciler_ExecuteOperation_Failed(t *testing.T) {
	st := mem_store.New()
	mgr := manager.New(st)
	logger := slog.Default()

	r := New(DefaultConfig(), mgr, nil, logger)

	// Create without template - should fail
	op := &Operation{
		Type:       OpCreate,
		Database:   "db1",
		Pattern:    "users/*/chats",
		TemplateID: "missing",
		Template:   nil, // No template
		Status:     StatusPending,
	}

	r.executeOperation(context.Background(), op)

	// Operation should be marked as failed
	assert.Equal(t, StatusFailed, op.Status)
	assert.NotEmpty(t, op.Error)
}

func TestReconciler_ExecuteRebuild_IndexNotFound(t *testing.T) {
	st := mem_store.New()
	mgr := manager.New(st)
	logger := slog.Default()

	r := New(DefaultConfig(), mgr, nil, logger)

	op := &Operation{
		Type:       OpRebuild,
		Database:   "db1",
		Pattern:    "nonexistent/*/path",
		TemplateID: "ts:desc",
		Status:     StatusPending,
	}

	// executeRebuild no longer returns "index not found" error since it works with store interface
	// which just clears and recreates. The operation should complete successfully.
	err := r.executeRebuild(context.Background(), op)
	require.NoError(t, err)
}

func TestReconciler_Reconcile(t *testing.T) {
	st := mem_store.New()
	mgr := manager.New(st)
	logger := slog.Default()

	// Load a template
	err := mgr.LoadTemplatesFromBytes([]byte(`
templates:
  - name: chat-ts
    collectionPattern: users/{uid}/chats
    fields:
      - field: timestamp
        order: desc
`))
	require.NoError(t, err)

	// Create a database by upserting an entry
	st.Upsert("db1", "dummy/*/path", "dummy-id", "doc1", []byte{0x01}, "")

	cfg := Config{Interval: 100 * time.Millisecond}
	r := New(cfg, mgr, nil, logger)

	// Run reconcile - should create the index
	r.reconcile(context.Background())

	// Index should now exist and be healthy
	state, err := st.GetState("db1", "users/*/chats", "chat-ts")
	require.NoError(t, err)
	assert.Equal(t, store.IndexStateHealthy, state)
}

func TestReconciler_ReconcileWithContextCancellation(t *testing.T) {
	st := mem_store.New()
	mgr := manager.New(st)
	logger := slog.Default()

	// Load multiple templates
	err := mgr.LoadTemplatesFromBytes([]byte(`
templates:
  - name: chat-ts
    collectionPattern: users/{uid}/chats
    fields:
      - field: timestamp
        order: desc
  - name: rooms-ts
    collectionPattern: rooms/{rid}/messages
    fields:
      - field: timestamp
        order: desc
`))
	require.NoError(t, err)

	// Create a database by upserting an entry
	st.Upsert("db1", "dummy/*/path", "dummy-id", "doc1", []byte{0x01}, "")

	cfg := Config{Interval: 100 * time.Millisecond}
	r := New(cfg, mgr, nil, logger)

	// Cancel context immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Run reconcile - should return early
	r.reconcile(ctx)

	// Some operations may or may not have completed depending on timing
}

func TestReconciler_SetStorageScanner(t *testing.T) {
	st := mem_store.New()
	mgr := manager.New(st)
	logger := slog.Default()

	r := New(DefaultConfig(), mgr, nil, logger)

	// Set scanner and replayer (nil for this test)
	r.SetStorageScanner(nil, nil)

	// Verify they're set (internal state)
	assert.Nil(t, r.scanner)
	assert.Nil(t, r.replayer)
}

func TestReconciler_ExecuteCreateWithRebuild(t *testing.T) {
	st := mem_store.New()
	mgr := manager.New(st)
	logger := slog.Default()

	// Create a mock orchestrator
	rebuildCfg := rebuild.DefaultConfig()
	rebuilder := rebuild.New(rebuildCfg, &mockKeyBuilder{}, logger)

	tmpl := &template.Template{
		Name:              "chat-ts",
		CollectionPattern: "users/{uid}/chats",
		Fields: []template.Field{
			{Field: "timestamp", Order: template.Desc},
		},
	}

	r := New(DefaultConfig(), mgr, rebuilder, logger)

	// Set a mock scanner
	r.SetStorageScanner(&mockScanner{}, nil)

	op := &Operation{
		Type:       OpCreate,
		Database:   "db1",
		Pattern:    "users/*/chats",
		TemplateID: "chat-ts",
		Template:   tmpl,
		Status:     StatusPending,
	}

	err := r.executeCreate(context.Background(), op)
	require.NoError(t, err)

	// Index should be created (state managed by rebuilder)
	indexes, _ := st.ListIndexes("db1")
	assert.Greater(t, len(indexes), 0)
}

func TestReconciler_ExecuteRebuildWithRebuild(t *testing.T) {
	st := mem_store.New()
	mgr := manager.New(st)
	logger := slog.Default()

	// Create a mock orchestrator
	rebuildCfg := rebuild.DefaultConfig()
	rebuilder := rebuild.New(rebuildCfg, &mockKeyBuilder{}, logger)

	// Create an existing index
	st.Upsert("db1", "users/*/chats", "ts:desc", "doc1", []byte{0x01}, "")
	st.SetState("db1", "users/*/chats", "ts:desc", store.IndexStateFailed)

	tmpl := &template.Template{
		Name:              "ts:desc",
		CollectionPattern: "users/{uid}/chats",
		Fields: []template.Field{
			{Field: "timestamp", Order: template.Desc},
		},
	}

	r := New(DefaultConfig(), mgr, rebuilder, logger)

	// Set a mock scanner
	r.SetStorageScanner(&mockScanner{}, nil)

	op := &Operation{
		Type:       OpRebuild,
		Database:   "db1",
		Pattern:    "users/*/chats",
		TemplateID: "ts:desc",
		Template:   tmpl,
		Status:     StatusPending,
	}

	err := r.executeRebuild(context.Background(), op)
	require.NoError(t, err)
}

func TestReconciler_Loop(t *testing.T) {
	st := mem_store.New()
	mgr := manager.New(st)
	logger := slog.Default()

	// Load a template
	err := mgr.LoadTemplatesFromBytes([]byte(`
templates:
  - name: chat-ts
    collectionPattern: users/{uid}/chats
    fields:
      - field: timestamp
        order: desc
`))
	require.NoError(t, err)

	// Create a database by upserting an entry
	st.Upsert("db1", "dummy/*/path", "dummy-id", "doc1", []byte{0x01}, "")

	cfg := Config{Interval: 50 * time.Millisecond}
	r := New(cfg, mgr, nil, logger)

	ctx := context.Background()
	err = r.Start(ctx)
	require.NoError(t, err)

	// Wait for loop to run and reconcile
	time.Sleep(100 * time.Millisecond)

	// Index should exist and be healthy
	state, err := st.GetState("db1", "users/*/chats", "chat-ts")
	require.NoError(t, err)
	assert.Equal(t, store.IndexStateHealthy, state)

	// Trigger explicit reconciliation
	r.Trigger()
	time.Sleep(50 * time.Millisecond)

	// Stop
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = r.Stop(stopCtx)
	require.NoError(t, err)
}

// mockScanner implements rebuild.StorageScanner for testing
type mockScanner struct{}

func (m *mockScanner) ScanCollection(ctx context.Context, database, collectionPattern string, batchSize int, startAfter string) (rebuild.DocIterator, error) {
	return &mockDocIterator{}, nil
}

// mockDocIterator implements rebuild.DocIterator
type mockDocIterator struct{}

func (m *mockDocIterator) Next() bool            { return false }
func (m *mockDocIterator) Doc() *types.StoredDoc { return nil }
func (m *mockDocIterator) Err() error            { return nil }
func (m *mockDocIterator) Close() error          { return nil }

// mockKeyBuilder implements rebuild.OrderKeyBuilder
type mockKeyBuilder struct{}

func (m *mockKeyBuilder) BuildOrderKey(data map[string]any, tmpl *template.Template) ([]byte, error) {
	return []byte{0x01}, nil
}
