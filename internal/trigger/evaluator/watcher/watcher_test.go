package watcher

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/puller/events"
	"github.com/syntrixbase/syntrix/pkg/model"
)

// MockDocumentStore is a mock implementation of storage.DocumentStore
type MockDocumentStore struct {
	mock.Mock
}

func (m *MockDocumentStore) Create(ctx context.Context, database string, doc storage.StoredDoc) error {
	args := m.Called(ctx, database, doc)
	return args.Error(0)
}

func (m *MockDocumentStore) Get(ctx context.Context, database, id string, _ ...storage.ReadOptions) (*storage.StoredDoc, error) {
	args := m.Called(ctx, database, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.StoredDoc), args.Error(1)
}

func (m *MockDocumentStore) Update(ctx context.Context, database, id string, data map[string]interface{}, filters model.Filters) error {
	args := m.Called(ctx, database, id, data, filters)
	return args.Error(0)
}

func (m *MockDocumentStore) Patch(ctx context.Context, database, id string, data map[string]interface{}, pred model.Filters) error {
	args := m.Called(ctx, database, id, data, pred)
	return args.Error(0)
}

func (m *MockDocumentStore) Delete(ctx context.Context, database, id string, pred model.Filters) error {
	args := m.Called(ctx, database, id, pred)
	return args.Error(0)
}

func (m *MockDocumentStore) DeleteByDatabase(ctx context.Context, database string, limit int) (int, error) {
	args := m.Called(ctx, database, limit)
	return args.Int(0), args.Error(1)
}

func (m *MockDocumentStore) Query(ctx context.Context, database string, q model.Query) ([]*storage.StoredDoc, error) {
	args := m.Called(ctx, database, q)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*storage.StoredDoc), args.Error(1)
}

func (m *MockDocumentStore) GetMany(ctx context.Context, database string, paths []string, _ ...storage.ReadOptions) ([]*storage.StoredDoc, error) {
	args := m.Called(ctx, database, paths)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*storage.StoredDoc), args.Error(1)
}

func (m *MockDocumentStore) Watch(ctx context.Context, database, collection string, after storage.WatchCheckpoint, opts storage.WatchOptions) (storage.WatchStream, error) {
	args := m.Called(ctx, database, collection, after, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(storage.WatchStream), args.Error(1)
}

func (m *MockDocumentStore) Close(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

// MockPullerService
type MockPullerService struct {
	mock.Mock
}

func (m *MockPullerService) Subscribe(ctx context.Context, consumerID string, after string) <-chan *events.PullerEvent {
	args := m.Called(ctx, consumerID, after)
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(<-chan *events.PullerEvent)
}

func TestNewWatcher(t *testing.T) {
	mockStore := new(MockDocumentStore)
	mockPuller := new(MockPullerService)
	w := NewWatcher(mockPuller, mockStore, WatcherOptions{})
	assert.NotNil(t, w)
}

func TestWatch_WithCheckpoint(t *testing.T) {
	mockStore := new(MockDocumentStore)
	mockPuller := new(MockPullerService)
	w := NewWatcher(mockPuller, mockStore, WatcherOptions{})

	checkpointDoc := &storage.StoredDoc{
		Data: map[string]interface{}{"token": "resume-token"},
	}
	mockStore.On("Get", mock.Anything, "default", "sys/checkpoints/trigger_evaluator").Return(checkpointDoc, nil)

	ch := make(chan *events.PullerEvent)
	mockPuller.On("Subscribe", mock.Anything, "trigger-evaluator", "resume-token").Return((<-chan *events.PullerEvent)(ch))

	eventCh, err := w.Watch(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, eventCh)

	// Verify event conversion
	go func() {
		ch <- &events.PullerEvent{
			Change: &events.StoreChangeEvent{
				EventID:      "evt-doc1",
				Database:     "database1",
				OpType:       events.StoreOperationInsert,
				MgoDocID:     "doc1",
				FullDocument: &storage.StoredDoc{Id: "doc1"},
			},
			Progress: "next-token",
		}
		close(ch)
	}()

	evt := <-eventCh
	assert.Equal(t, events.EventCreate, evt.Type)
	assert.Equal(t, "evt-doc1", evt.Id) // Id comes from EventID, not MgoDocID
	assert.Equal(t, "next-token", evt.Progress)

	mockStore.AssertExpectations(t)
	mockPuller.AssertExpectations(t)
}

func TestWatch_NoCheckpoint_StartFromNow(t *testing.T) {
	mockStore := new(MockDocumentStore)
	mockPuller := new(MockPullerService)
	w := NewWatcher(mockPuller, mockStore, WatcherOptions{StartFromNow: true})

	mockStore.On("Get", mock.Anything, "default", "sys/checkpoints/trigger_evaluator").Return(nil, model.ErrNotFound)

	ch := make(chan *events.PullerEvent)
	// Expect empty string for resume token
	mockPuller.On("Subscribe", mock.Anything, "trigger-evaluator", "").Return((<-chan *events.PullerEvent)(ch))

	eventCh, err := w.Watch(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, eventCh)

	mockStore.AssertExpectations(t)
	mockPuller.AssertExpectations(t)
}

func TestWatch_AllDatabaseEvents(t *testing.T) {
	// Watcher now receives all events regardless of database
	// Database filtering is done by Evaluator per trigger
	mockStore := new(MockDocumentStore)
	mockPuller := new(MockPullerService)
	w := NewWatcher(mockPuller, mockStore, WatcherOptions{StartFromNow: true})

	mockStore.On("Get", mock.Anything, "default", "sys/checkpoints/trigger_evaluator").Return(nil, model.ErrNotFound)

	ch := make(chan *events.PullerEvent)
	mockPuller.On("Subscribe", mock.Anything, "trigger-evaluator", "").Return((<-chan *events.PullerEvent)(ch))

	eventCh, err := w.Watch(context.Background())
	assert.NoError(t, err)

	go func() {
		// Event from database1
		ch <- &events.PullerEvent{
			Change: &events.StoreChangeEvent{
				EventID:      "evt-db1",
				Database:     "database1",
				OpType:       events.StoreOperationInsert,
				FullDocument: &storage.StoredDoc{Id: "doc1"},
			},
		}
		// Event from database2
		ch <- &events.PullerEvent{
			Change: &events.StoreChangeEvent{
				EventID:      "evt-db2",
				Database:     "database2",
				OpType:       events.StoreOperationInsert,
				FullDocument: &storage.StoredDoc{Id: "doc2"},
			},
		}
		close(ch)
	}()

	// Both events should be received (no filtering)
	evt1 := <-eventCh
	assert.Equal(t, "evt-db1", evt1.Id)

	evt2 := <-eventCh
	assert.Equal(t, "evt-db2", evt2.Id)

	_, ok := <-eventCh
	assert.False(t, ok)
}

func TestWatch_NoCheckpoint_Fail(t *testing.T) {
	mockStore := new(MockDocumentStore)
	mockPuller := new(MockPullerService)
	w := NewWatcher(mockPuller, mockStore, WatcherOptions{StartFromNow: false})

	mockStore.On("Get", mock.Anything, "default", "sys/checkpoints/trigger_evaluator").Return(nil, model.ErrNotFound)

	_, err := w.Watch(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "checkpoint not found")
	mockStore.AssertExpectations(t)
}

func TestSaveCheckpoint_Update(t *testing.T) {
	mockStore := new(MockDocumentStore)
	mockPuller := new(MockPullerService)
	w := NewWatcher(mockPuller, mockStore, WatcherOptions{})

	mockStore.On("Update", mock.Anything, "default", "sys/checkpoints/trigger_evaluator", mock.Anything, mock.Anything).Return(nil)

	err := w.SaveCheckpoint(context.Background(), "new-token")
	assert.NoError(t, err)
	mockStore.AssertExpectations(t)
}

func TestSaveCheckpoint_Create(t *testing.T) {
	mockStore := new(MockDocumentStore)
	mockPuller := new(MockPullerService)
	w := NewWatcher(mockPuller, mockStore, WatcherOptions{})

	mockStore.On("Update", mock.Anything, "default", "sys/checkpoints/trigger_evaluator", mock.Anything, mock.Anything).Return(model.ErrNotFound)
	mockStore.On("Create", mock.Anything, "default", mock.Anything).Return(nil)

	err := w.SaveCheckpoint(context.Background(), "new-token")
	assert.NoError(t, err)
	mockStore.AssertExpectations(t)
}

func TestClose(t *testing.T) {
	mockStore := new(MockDocumentStore)
	mockPuller := new(MockPullerService)
	w := NewWatcher(mockPuller, mockStore, WatcherOptions{})

	// Close should be a no-op and return nil
	err := w.Close()
	assert.NoError(t, err)
}

func TestWatch_GetError(t *testing.T) {
	mockStore := new(MockDocumentStore)
	mockPuller := new(MockPullerService)
	w := NewWatcher(mockPuller, mockStore, WatcherOptions{})

	// Get returns unexpected error
	mockStore.On("Get", mock.Anything, "default", "sys/checkpoints/trigger_evaluator").Return(nil, assert.AnError)

	_, err := w.Watch(context.Background())
	assert.Error(t, err)
	mockStore.AssertExpectations(t)
}

func TestSaveCheckpoint_UpdateError(t *testing.T) {
	mockStore := new(MockDocumentStore)
	mockPuller := new(MockPullerService)
	w := NewWatcher(mockPuller, mockStore, WatcherOptions{})

	mockStore.On("Update", mock.Anything, "default", "sys/checkpoints/trigger_evaluator", mock.Anything, mock.Anything).Return(assert.AnError)

	err := w.SaveCheckpoint(context.Background(), "new-token")
	assert.Error(t, err)
	mockStore.AssertExpectations(t)
}

func TestWatch_EventTypes(t *testing.T) {
	mockStore := new(MockDocumentStore)
	mockPuller := new(MockPullerService)
	w := NewWatcher(mockPuller, mockStore, WatcherOptions{StartFromNow: true})

	mockStore.On("Get", mock.Anything, "default", "sys/checkpoints/trigger_evaluator").Return(nil, model.ErrNotFound)

	pullerCh := make(chan *events.PullerEvent)
	mockPuller.On("Subscribe", mock.Anything, "trigger-evaluator", "").Return((<-chan *events.PullerEvent)(pullerCh))

	eventCh, err := w.Watch(context.Background())
	if err != nil {
		t.Fatalf("Watch failed: %v", err)
	}

	go func() {
		// Insert
		pullerCh <- &events.PullerEvent{
			Change: &events.StoreChangeEvent{
				EventID: "evt-doc1", MgoDocID: "doc1", Database: "database1", OpType: events.StoreOperationInsert,
				FullDocument: &storage.StoredDoc{Id: "doc1", Data: map[string]interface{}{"a": 1}},
			},
			Progress: "t1",
		}
		// Update
		pullerCh <- &events.PullerEvent{
			Change: &events.StoreChangeEvent{
				EventID: "evt-doc2", MgoDocID: "doc2", Database: "database1", OpType: events.StoreOperationUpdate,
				FullDocument: &storage.StoredDoc{Id: "doc2", Data: map[string]interface{}{"a": 2}},
			},
			Progress: "t2",
		}
		// Hard Delete - will be ignored by Transform (returns ErrDeleteOPIgnored)
		// So we don't expect to receive this event
		pullerCh <- &events.PullerEvent{
			Change: &events.StoreChangeEvent{
				EventID: "evt-doc3", MgoDocID: "doc3", Database: "database1", OpType: events.StoreOperationDelete,
				MgoColl: "users",
			},
			Progress: "t3",
		}
		// Soft Delete (Update with Deleted=true) - transforms to EventDelete
		pullerCh <- &events.PullerEvent{
			Change: &events.StoreChangeEvent{
				EventID: "evt-doc4", MgoDocID: "doc4", Database: "database1", OpType: events.StoreOperationUpdate,
				FullDocument: &storage.StoredDoc{Id: "doc4", Deleted: true},
			},
			Progress: "t4",
		}
		// Replace - transforms to EventCreate (recreate after soft delete)
		pullerCh <- &events.PullerEvent{
			Change: &events.StoreChangeEvent{
				EventID: "evt-doc5", MgoDocID: "doc5", Database: "database1", OpType: events.StoreOperationReplace,
				FullDocument: &storage.StoredDoc{Id: "doc5", Data: map[string]interface{}{"b": 1}},
			},
			Progress: "t5",
		}
		close(pullerCh)
	}()

	// Verify Insert
	evt1 := <-eventCh
	assert.Equal(t, events.EventCreate, evt1.Type)
	assert.Equal(t, "evt-doc1", evt1.Id)
	assert.NotNil(t, evt1.Document)

	// Verify Update
	evt2 := <-eventCh
	assert.Equal(t, events.EventUpdate, evt2.Type)
	assert.Equal(t, "evt-doc2", evt2.Id)
	assert.NotNil(t, evt2.Document)

	// Note: Hard DELETE (StoreOperationDelete) is ignored by Transform
	// Syntrix uses soft delete, so hard deletes are not exposed to triggers

	// Verify Soft Delete (UPDATE with Deleted=true -> EventDelete)
	evt4 := <-eventCh
	assert.Equal(t, events.EventDelete, evt4.Type)
	assert.Equal(t, "evt-doc4", evt4.Id)
	assert.NotNil(t, evt4.Document)
	assert.True(t, evt4.Document.Deleted)

	// Verify Replace -> EventCreate
	evt5 := <-eventCh
	assert.Equal(t, events.EventCreate, evt5.Type)
	assert.Equal(t, "evt-doc5", evt5.Id)
	assert.NotNil(t, evt5.Document)
}
