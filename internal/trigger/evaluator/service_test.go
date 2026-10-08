package evaluator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/puller/events"
	"github.com/syntrixbase/syntrix/internal/trigger/types"
)

// MockDocumentWatcher mocks watcher.DocumentWatcher
type MockDocumentWatcher struct {
	mock.Mock
}

func (m *MockDocumentWatcher) Watch(ctx context.Context) (<-chan events.SyntrixChangeEvent, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(<-chan events.SyntrixChangeEvent), args.Error(1)
}

func (m *MockDocumentWatcher) SaveCheckpoint(ctx context.Context, token interface{}) error {
	args := m.Called(ctx, token)
	return args.Error(0)
}

func (m *MockDocumentWatcher) Close() error {
	args := m.Called()
	return args.Error(0)
}

// MockTaskPublisher mocks TaskPublisher
type MockTaskPublisher struct {
	mock.Mock
}

type evaluatorFunc func(context.Context, *types.Trigger, events.SyntrixChangeEvent) (bool, error)

func (f evaluatorFunc) Evaluate(ctx context.Context, trigger *types.Trigger, event events.SyntrixChangeEvent) (bool, error) {
	return f(ctx, trigger, event)
}

type publisherFunc func(context.Context, *types.DeliveryTask) error

func (f publisherFunc) Publish(ctx context.Context, task *types.DeliveryTask) error {
	return f(ctx, task)
}

func (f publisherFunc) Close() error { return nil }

func (m *MockTaskPublisher) Publish(ctx context.Context, task *types.DeliveryTask) error {
	args := m.Called(ctx, task)
	return args.Error(0)
}

func (m *MockTaskPublisher) Close() error {
	args := m.Called()
	return args.Error(0)
}

// MockPuller mocks puller.Service
type MockPuller struct {
	mock.Mock
}

func (m *MockPuller) Subscribe(ctx context.Context, consumerID string, after string) <-chan *events.PullerEvent {
	args := m.Called(ctx, consumerID, after)
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(<-chan *events.PullerEvent)
}

func TestService_LoadTriggers(t *testing.T) {
	mockWatcher := new(MockDocumentWatcher)
	mockPublisher := new(MockTaskPublisher)
	eval, err := NewEvaluator()
	require.NoError(t, err)

	svc := &service{
		evaluator: eval,
		watcher:   mockWatcher,
		publisher: mockPublisher,
	}

	triggers := []*types.Trigger{
		{
			ID:         "trigger1",
			Database:   "db1",
			Collection: "users",
			Events:     []string{"create"},
			URL:        "https://example.com/webhook",
		},
	}

	err = svc.LoadTriggers(triggers)
	assert.NoError(t, err)
	assert.Len(t, svc.triggers, 1)
}

func TestService_LoadTriggers_ValidationError(t *testing.T) {
	mockWatcher := new(MockDocumentWatcher)
	mockPublisher := new(MockTaskPublisher)
	eval, err := NewEvaluator()
	require.NoError(t, err)

	svc := &service{
		evaluator: eval,
		watcher:   mockWatcher,
		publisher: mockPublisher,
	}

	// Invalid trigger - missing ID
	triggers := []*types.Trigger{
		{
			Database:   "db1",
			Collection: "users",
			Events:     []string{"create"},
			URL:        "https://example.com/webhook",
		},
	}

	err = svc.LoadTriggers(triggers)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "trigger id is required")
}

func TestService_Start(t *testing.T) {
	mockWatcher := new(MockDocumentWatcher)
	mockPublisher := new(MockTaskPublisher)
	eval, err := NewEvaluator()
	require.NoError(t, err)

	svc := &service{
		evaluator: eval,
		watcher:   mockWatcher,
		publisher: mockPublisher,
		triggers: []*types.Trigger{
			{
				ID:         "trigger1",
				Database:   "db1",
				Collection: "users",
				Events:     []string{"create"},
				URL:        "https://example.com/webhook",
			},
		},
	}

	eventCh := make(chan events.SyntrixChangeEvent)
	mockWatcher.On("Watch", mock.Anything).Return((<-chan events.SyntrixChangeEvent)(eventCh), nil)
	mockWatcher.On("SaveCheckpoint", mock.Anything, "token1").Return(nil).Maybe()
	mockPublisher.On("Publish", mock.Anything, mock.Anything).Return(nil)

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		eventCh <- events.SyntrixChangeEvent{
			Type: events.EventCreate,
			Document: &storage.StoredDoc{
				Id:         "doc1",
				Database:   "db1",
				Collection: "users",
				Data:       map[string]interface{}{"name": "test"},
			},
			Progress: "token1",
		}
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err = svc.Start(ctx)
	assert.NoError(t, err)
	mockWatcher.AssertExpectations(t)
	mockPublisher.AssertExpectations(t)
}

func TestService_Start_TaskTimeouts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	watcher := new(MockDocumentWatcher)
	publisher := new(MockTaskPublisher)
	eval, err := NewEvaluator()
	require.NoError(t, err)
	svc := &service{evaluator: eval, watcher: watcher, publisher: publisher}
	rules := []*types.Trigger{
		{ID: "short", Database: "db1", Collection: "users", Events: []string{"create"}, URL: "https://example.com/webhook", Timeout: types.Duration(2 * time.Second)},
		{ID: "long", Database: "db1", Collection: "users", Events: []string{"create"}, URL: "https://example.com/webhook", Timeout: types.Duration(time.Minute)},
		{ID: "default", Database: "db1", Collection: "users", Events: []string{"create"}, URL: "https://example.com/webhook"},
	}
	require.NoError(t, svc.LoadTriggers(rules))
	eventsCh := make(chan events.SyntrixChangeEvent, 1)
	eventsCh <- events.SyntrixChangeEvent{
		Type:     events.EventCreate,
		Document: &storage.StoredDoc{Id: "doc1", Database: "db1", Collection: "users", Data: map[string]interface{}{"name": "test"}},
		Progress: "task-timeout-progress",
	}
	watcher.On("Watch", mock.Anything).Return((<-chan events.SyntrixChangeEvent)(eventsCh), nil)
	watcher.On("SaveCheckpoint", mock.Anything, "task-timeout-progress").Return(nil).Maybe()
	captured := make(map[string]types.Duration)
	publisher.On("Publish", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		task := args.Get(1).(*types.DeliveryTask)
		captured[task.TriggerID] = task.Timeout
		if len(captured) == len(rules) {
			cancel()
		}
	}).Return(nil).Times(len(rules))

	require.NoError(t, svc.Start(ctx))
	assert.Equal(t, map[string]types.Duration{
		"short": types.Duration(2 * time.Second), "long": types.Duration(time.Minute), "default": types.Duration(30 * time.Second),
	}, captured)
	assert.Equal(t, types.Duration(2*time.Second), rules[0].Timeout)
	assert.Equal(t, types.Duration(time.Minute), rules[1].Timeout)
	assert.Zero(t, rules[2].Timeout)
	watcher.AssertExpectations(t)
	publisher.AssertExpectations(t)
}

func TestService_LoadTriggers_NegativeTimeout(t *testing.T) {
	svc := &service{}
	active := &types.Trigger{ID: "active", Database: "db1", Collection: "users", Events: []string{"create"}, URL: "https://example.com/webhook", Timeout: types.Duration(time.Second)}
	require.NoError(t, svc.LoadTriggers([]*types.Trigger{active}))
	invalid := *active
	invalid.Timeout = types.Duration(-time.Second)

	require.ErrorContains(t, svc.LoadTriggers([]*types.Trigger{&invalid}), "timeout")
	require.Len(t, svc.triggers, 1)
	assert.Same(t, active, svc.triggers[0])
	assert.Equal(t, types.Duration(time.Second), active.Timeout)
}

func TestService_Start_WatchError(t *testing.T) {
	mockWatcher := new(MockDocumentWatcher)
	mockPublisher := new(MockTaskPublisher)
	eval, err := NewEvaluator()
	require.NoError(t, err)

	svc := &service{
		evaluator: eval,
		watcher:   mockWatcher,
		publisher: mockPublisher,
	}

	mockWatcher.On("Watch", mock.Anything).Return(nil, errors.New("watch error"))

	err = svc.Start(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "watch error")
}

func TestService_Start_EvaluateError(t *testing.T) {
	mockWatcher := new(MockDocumentWatcher)
	mockPublisher := new(MockTaskPublisher)
	eval, err := NewEvaluator()
	require.NoError(t, err)

	svc := &service{
		evaluator: eval,
		watcher:   mockWatcher,
		publisher: mockPublisher,
		triggers: []*types.Trigger{
			{
				ID:         "trigger1",
				Database:   "db1",
				Collection: "users",
				Events:     []string{"create"},
				Condition:  "invalid.cel.expression[",
				URL:        "https://example.com/webhook",
			},
		},
	}

	eventCh := make(chan events.SyntrixChangeEvent)
	mockWatcher.On("Watch", mock.Anything).Return((<-chan events.SyntrixChangeEvent)(eventCh), nil)
	mockWatcher.On("SaveCheckpoint", mock.Anything, "evaluation-progress").Return(nil).Maybe()

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		eventCh <- events.SyntrixChangeEvent{
			Type: events.EventCreate,
			Document: &storage.StoredDoc{
				Id:         "doc1",
				Database:   "db1",
				Collection: "users",
				Data:       map[string]interface{}{"name": "test"},
			},
			Progress: "evaluation-progress",
		}
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err = svc.Start(ctx)
	assert.NoError(t, err)
}

func TestService_Start_NilDocumentAndBefore(t *testing.T) {
	mockWatcher := new(MockDocumentWatcher)
	mockPublisher := new(MockTaskPublisher)
	eval, err := NewEvaluator()
	require.NoError(t, err)

	svc := &service{
		evaluator: eval,
		watcher:   mockWatcher,
		publisher: mockPublisher,
		triggers: []*types.Trigger{
			{
				ID:         "trigger1",
				Database:   "db1",
				Collection: "users",
				Events:     []string{"create"},
				URL:        "https://example.com/webhook",
			},
		},
	}

	eventCh := make(chan events.SyntrixChangeEvent)
	mockWatcher.On("Watch", mock.Anything).Return((<-chan events.SyntrixChangeEvent)(eventCh), nil)

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		// Send event with nil Document and nil Before
		eventCh <- events.SyntrixChangeEvent{
			Type: events.EventCreate,
		}
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err = svc.Start(ctx)
	assert.NoError(t, err)
}

func TestService_Start_PublishError(t *testing.T) {
	mockWatcher := new(MockDocumentWatcher)
	mockPublisher := new(MockTaskPublisher)
	eval, err := NewEvaluator()
	require.NoError(t, err)

	svc := &service{
		evaluator: eval,
		watcher:   mockWatcher,
		publisher: mockPublisher,
		triggers: []*types.Trigger{
			{
				ID:         "trigger1",
				Database:   "db1",
				Collection: "users",
				Events:     []string{"create"},
				URL:        "https://example.com/webhook",
			},
		},
	}

	eventCh := make(chan events.SyntrixChangeEvent)
	mockWatcher.On("Watch", mock.Anything).Return((<-chan events.SyntrixChangeEvent)(eventCh), nil)
	mockWatcher.On("SaveCheckpoint", mock.Anything, mock.Anything).Return(nil).Maybe()

	ctx, cancel := context.WithCancel(context.Background())
	mockPublisher.On("Publish", mock.Anything, mock.Anything).Run(func(mock.Arguments) { cancel() }).Return(errors.New("publish error"))

	go func() {
		eventCh <- events.SyntrixChangeEvent{
			Type: events.EventCreate,
			Document: &storage.StoredDoc{
				Id:         "doc1",
				Database:   "db1",
				Collection: "users",
				Data:       map[string]interface{}{"name": "test"},
			},
			Progress: "failed-progress",
		}
	}()

	err = svc.Start(ctx)
	assert.NoError(t, err)
	mockPublisher.AssertExpectations(t)
	mockWatcher.AssertNotCalled(t, "SaveCheckpoint", mock.Anything, mock.Anything)
}

func TestService_Start_DoesNotCheckpointFailedEvaluation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	watcher := new(MockDocumentWatcher)
	eventsCh := make(chan events.SyntrixChangeEvent, 1)
	eventsCh <- events.SyntrixChangeEvent{
		Id:       "event-1",
		Type:     events.EventCreate,
		Document: &storage.StoredDoc{Id: "doc1", Database: "db1", Collection: "users"},
		Progress: "failed-progress",
	}
	watcher.On("Watch", mock.Anything).Return((<-chan events.SyntrixChangeEvent)(eventsCh), nil)
	watcher.On("SaveCheckpoint", mock.Anything, mock.Anything).Return(nil).Maybe()
	eval := evaluatorFunc(func(context.Context, *types.Trigger, events.SyntrixChangeEvent) (bool, error) {
		cancel()
		return false, errors.New("evaluation failed")
	})
	svc := &service{
		evaluator: eval,
		watcher:   watcher,
		publisher: publisherFunc(func(context.Context, *types.DeliveryTask) error {
			t.Error("publisher called after evaluation failure")
			return nil
		}),
		triggers: []*types.Trigger{{ID: "trigger-1"}},
	}

	require.NoError(t, svc.Start(ctx))
	watcher.AssertNotCalled(t, "SaveCheckpoint", mock.Anything, mock.Anything)
}

func TestService_Start_RetriesFailedPublishBeforeCheckpoint(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	watcher := new(MockDocumentWatcher)
	eventsCh := make(chan events.SyntrixChangeEvent, 2)
	eventsCh <- events.SyntrixChangeEvent{
		Id:       "event-0",
		Type:     events.EventCreate,
		Document: &storage.StoredDoc{Id: "doc0", Database: "db1", Collection: "users"},
		Progress: "safe-progress",
	}
	eventsCh <- events.SyntrixChangeEvent{
		Id:       "event-1",
		Type:     events.EventCreate,
		Document: &storage.StoredDoc{Id: "doc1", Database: "db1", Collection: "users"},
		Progress: "completed-progress",
	}
	watcher.On("Watch", mock.Anything).Return((<-chan events.SyntrixChangeEvent)(eventsCh), nil)
	var firstPublishes, secondPublishes atomic.Int32
	var checkpointBeforeCompletion atomic.Bool
	watcher.On("SaveCheckpoint", mock.Anything, "safe-progress").Return(nil).Maybe()
	watcher.On("SaveCheckpoint", mock.Anything, "completed-progress").Run(func(mock.Arguments) {
		if secondPublishes.Load() != 2 {
			checkpointBeforeCompletion.Store(true)
		}
		cancel()
	}).Return(nil).Maybe()
	svc := &service{
		evaluator: evaluatorFunc(func(_ context.Context, _ *types.Trigger, event events.SyntrixChangeEvent) (bool, error) {
			return event.Id == "event-1", nil
		}),
		watcher: watcher,
		publisher: publisherFunc(func(_ context.Context, task *types.DeliveryTask) error {
			if task.TriggerID == "first" {
				firstPublishes.Add(1)
				return nil
			}
			if secondPublishes.Add(1) == 1 {
				return errors.New("temporary publish failure")
			}
			return nil
		}),
		triggers: []*types.Trigger{{ID: "first"}, {ID: "second"}},
	}

	require.NoError(t, svc.Start(ctx))
	assert.EqualValues(t, 1, firstPublishes.Load())
	assert.EqualValues(t, 2, secondPublishes.Load())
	assert.False(t, checkpointBeforeCompletion.Load())
	watcher.AssertCalled(t, "SaveCheckpoint", mock.Anything, "completed-progress")
}

func TestService_Start_FailedEventBlocksLaterProgress(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	watcher := new(MockDocumentWatcher)
	eventsCh := make(chan events.SyntrixChangeEvent, 3)
	for _, event := range []struct{ id, progress string }{
		{"first", "first-progress"},
		{"failed", "failed-progress"},
		{"later", "later-progress"},
	} {
		eventsCh <- events.SyntrixChangeEvent{
			Id:       event.id,
			Type:     events.EventCreate,
			Document: &storage.StoredDoc{Id: event.id, Database: "db1", Collection: "users"},
			Progress: event.progress,
		}
	}
	watcher.On("Watch", mock.Anything).Return((<-chan events.SyntrixChangeEvent)(eventsCh), nil)
	watcher.On("SaveCheckpoint", mock.Anything, "first-progress").Return(nil).Maybe()
	svc := &service{
		evaluator: evaluatorFunc(func(_ context.Context, _ *types.Trigger, event events.SyntrixChangeEvent) (bool, error) {
			return event.Id != "first", nil
		}),
		watcher: watcher,
		publisher: publisherFunc(func(context.Context, *types.DeliveryTask) error {
			cancel()
			return errors.New("publish failed")
		}),
		triggers: []*types.Trigger{{ID: "trigger-1"}},
	}

	require.NoError(t, svc.Start(ctx))
	watcher.AssertNotCalled(t, "SaveCheckpoint", mock.Anything, "failed-progress")
	watcher.AssertNotCalled(t, "SaveCheckpoint", mock.Anything, "later-progress")
}

func TestService_Start_SaveCheckpointError(t *testing.T) {
	mockWatcher := new(MockDocumentWatcher)
	mockPublisher := new(MockTaskPublisher)
	eval, err := NewEvaluator()
	require.NoError(t, err)

	svc := &service{
		evaluator: eval,
		watcher:   mockWatcher,
		publisher: mockPublisher,
		triggers: []*types.Trigger{
			{
				ID:         "trigger1",
				Database:   "db1",
				Collection: "users",
				Events:     []string{"create"},
				URL:        "https://example.com/webhook",
			},
		},
	}

	eventCh := make(chan events.SyntrixChangeEvent)
	mockWatcher.On("Watch", mock.Anything).Return((<-chan events.SyntrixChangeEvent)(eventCh), nil)
	mockWatcher.On("SaveCheckpoint", mock.Anything, "token1").Return(errors.New("checkpoint error"))
	mockPublisher.On("Publish", mock.Anything, mock.Anything).Return(nil)

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		eventCh <- events.SyntrixChangeEvent{
			Type: events.EventCreate,
			Document: &storage.StoredDoc{
				Id:         "doc1",
				Database:   "db1",
				Collection: "users",
				Data:       map[string]interface{}{"name": "test"},
			},
			Progress: "token1",
		}
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err = svc.Start(ctx)
	assert.NoError(t, err)
}

func TestService_Start_BeforeOnlyEvent(t *testing.T) {
	mockWatcher := new(MockDocumentWatcher)
	mockPublisher := new(MockTaskPublisher)
	eval, err := NewEvaluator()
	require.NoError(t, err)

	svc := &service{
		evaluator: eval,
		watcher:   mockWatcher,
		publisher: mockPublisher,
		triggers: []*types.Trigger{
			{
				ID:         "trigger1",
				Database:   "db1",
				Collection: "users",
				Events:     []string{"delete"},
				URL:        "https://example.com/webhook",
			},
		},
	}

	eventCh := make(chan events.SyntrixChangeEvent)
	mockWatcher.On("Watch", mock.Anything).Return((<-chan events.SyntrixChangeEvent)(eventCh), nil)
	mockWatcher.On("SaveCheckpoint", mock.Anything, "before-progress").Return(nil)
	mockPublisher.On("Publish", mock.Anything, mock.Anything).Return(nil)

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		eventCh <- events.SyntrixChangeEvent{
			Type: events.EventDelete,
			Before: &storage.StoredDoc{
				Id:         "doc1",
				Database:   "db1",
				Collection: "users",
				Data:       map[string]interface{}{"name": "test"},
			},
			Progress: "before-progress",
		}
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err = svc.Start(ctx)
	assert.NoError(t, err)
	mockPublisher.AssertExpectations(t)
}

func TestService_Close(t *testing.T) {
	mockWatcher := new(MockDocumentWatcher)
	mockPublisher := new(MockTaskPublisher)
	eval, err := NewEvaluator()
	require.NoError(t, err)

	svc := &service{
		evaluator: eval,
		watcher:   mockWatcher,
		publisher: mockPublisher,
	}

	mockWatcher.On("Close").Return(nil)
	mockPublisher.On("Close").Return(nil)

	err = svc.Close()
	assert.NoError(t, err)
	mockWatcher.AssertExpectations(t)
	mockPublisher.AssertExpectations(t)
}

func TestService_Close_WithWatcherError(t *testing.T) {
	mockWatcher := new(MockDocumentWatcher)
	mockPublisher := new(MockTaskPublisher)
	eval, err := NewEvaluator()
	require.NoError(t, err)

	svc := &service{
		evaluator: eval,
		watcher:   mockWatcher,
		publisher: mockPublisher,
	}

	mockWatcher.On("Close").Return(errors.New("watcher close error"))
	mockPublisher.On("Close").Return(nil)

	err = svc.Close()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "watcher close error")
}

func TestService_Close_WithPublisherError(t *testing.T) {
	mockWatcher := new(MockDocumentWatcher)
	mockPublisher := new(MockTaskPublisher)
	eval, err := NewEvaluator()
	require.NoError(t, err)

	svc := &service{
		evaluator: eval,
		watcher:   mockWatcher,
		publisher: mockPublisher,
	}

	mockWatcher.On("Close").Return(nil)
	mockPublisher.On("Close").Return(errors.New("publisher close error"))

	err = svc.Close()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "publisher close error")
}

func TestService_Close_WithBothErrors(t *testing.T) {
	mockWatcher := new(MockDocumentWatcher)
	mockPublisher := new(MockTaskPublisher)
	eval, err := NewEvaluator()
	require.NoError(t, err)

	svc := &service{
		evaluator: eval,
		watcher:   mockWatcher,
		publisher: mockPublisher,
	}

	mockWatcher.On("Close").Return(errors.New("watcher close error"))
	mockPublisher.On("Close").Return(errors.New("publisher close error"))

	err = svc.Close()
	assert.Error(t, err)
	// First error wins
	assert.Contains(t, err.Error(), "watcher close error")
}

func TestService_Close_Success(t *testing.T) {
	eval, err := NewEvaluator()
	require.NoError(t, err)

	svc := &service{
		evaluator: eval,
		watcher:   nil,
		publisher: nil,
	}

	err = svc.Close()
	assert.NoError(t, err)
}

func TestNewService_NoPuller(t *testing.T) {
	deps := Dependencies{
		Puller: nil,
	}
	cfg := Config{}

	_, err := NewService(deps, cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "puller service is required")
}

func TestNewService_Success(t *testing.T) {
	mockPuller := new(MockPuller)

	deps := Dependencies{
		Puller: mockPuller,
	}
	cfg := Config{}

	svc, err := NewService(deps, cfg)
	assert.NoError(t, err)
	assert.NotNil(t, svc)
}

func TestNewService_WithRulesPath(t *testing.T) {
	mockPuller := new(MockPuller)

	// Create directory with trigger file in new format
	tmpDir := t.TempDir()
	content := `database: db1
triggers:
  trigger1:
    collection: users
    events:
      - create
    url: https://example.com/webhook
`

	err := os.WriteFile(filepath.Join(tmpDir, "db1.yml"), []byte(content), 0644)
	require.NoError(t, err)

	deps := Dependencies{
		Puller: mockPuller,
	}
	cfg := Config{
		RulesPath: tmpDir,
	}

	svc, err := NewService(deps, cfg)
	assert.NoError(t, err)
	assert.NotNil(t, svc)
}

func TestNewService_WithInvalidRulesPath(t *testing.T) {
	mockPuller := new(MockPuller)

	deps := Dependencies{
		Puller: mockPuller,
	}
	cfg := Config{
		RulesPath: "/nonexistent/directory",
	}

	_, err := NewService(deps, cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load trigger rules")
}

func TestNewService_WithInvalidTrigger(t *testing.T) {
	mockPuller := new(MockPuller)

	// Invalid trigger - missing collection
	tmpDir := t.TempDir()
	content := `database: db1
triggers:
  trigger1:
    events:
      - create
    url: https://example.com/webhook
`

	err := os.WriteFile(filepath.Join(tmpDir, "db1.yml"), []byte(content), 0644)
	require.NoError(t, err)

	deps := Dependencies{
		Puller: mockPuller,
	}
	cfg := Config{
		RulesPath: tmpDir,
	}

	_, err = NewService(deps, cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load triggers")
}

func TestValidateTrigger(t *testing.T) {
	tests := []struct {
		name      string
		trigger   *types.Trigger
		wantError string
	}{
		{
			name: "valid trigger",
			trigger: &types.Trigger{
				ID:         "trigger1",
				Database:   "db1",
				Collection: "users",
				Events:     []string{"create"},
				URL:        "https://example.com/webhook",
			},
			wantError: "",
		},
		{
			name: "missing id",
			trigger: &types.Trigger{
				Database:   "db1",
				Collection: "users",
				Events:     []string{"create"},
				URL:        "https://example.com/webhook",
			},
			wantError: "trigger id is required",
		},
		{
			name: "invalid id",
			trigger: &types.Trigger{
				ID:         "trigger.1",
				Database:   "db1",
				Collection: "users",
				Events:     []string{"create"},
				URL:        "https://example.com/webhook",
			},
			wantError: "invalid trigger id",
		},
		{
			name: "missing database",
			trigger: &types.Trigger{
				ID:         "trigger1",
				Collection: "users",
				Events:     []string{"create"},
				URL:        "https://example.com/webhook",
			},
			wantError: "database is required",
		},
		{
			name: "invalid database",
			trigger: &types.Trigger{
				ID:         "trigger1",
				Database:   "db.1",
				Collection: "users",
				Events:     []string{"create"},
				URL:        "https://example.com/webhook",
			},
			wantError: "invalid database",
		},
		{
			name: "database too long",
			trigger: &types.Trigger{
				ID:         "trigger1",
				Database:   "abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz",
				Collection: "users",
				Events:     []string{"create"},
				URL:        "https://example.com/webhook",
			},
			wantError: "database name too long",
		},
		{
			name: "missing collection",
			trigger: &types.Trigger{
				ID:       "trigger1",
				Database: "db1",
				Events:   []string{"create"},
				URL:      "https://example.com/webhook",
			},
			wantError: "collection is required",
		},
		{
			name: "collection too long",
			trigger: &types.Trigger{
				ID:         "trigger1",
				Database:   "db1",
				Collection: "abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz",
				Events:     []string{"create"},
				URL:        "https://example.com/webhook",
			},
			wantError: "collection name too long",
		},
		{
			name: "missing events",
			trigger: &types.Trigger{
				ID:         "trigger1",
				Database:   "db1",
				Collection: "users",
				URL:        "https://example.com/webhook",
			},
			wantError: "at least one event is required",
		},
		{
			name: "invalid event",
			trigger: &types.Trigger{
				ID:         "trigger1",
				Database:   "db1",
				Collection: "users",
				Events:     []string{"invalid"},
				URL:        "https://example.com/webhook",
			},
			wantError: "invalid event type",
		},
		{
			name: "missing url",
			trigger: &types.Trigger{
				ID:         "trigger1",
				Database:   "db1",
				Collection: "users",
				Events:     []string{"create"},
			},
			wantError: "url is required",
		},
		{
			name: "invalid url scheme",
			trigger: &types.Trigger{
				ID:         "trigger1",
				Database:   "db1",
				Collection: "users",
				Events:     []string{"create"},
				URL:        "ftp://example.com/webhook",
			},
			wantError: "url must use http or https scheme",
		},
		{
			name: "url missing host",
			trigger: &types.Trigger{
				ID:         "trigger1",
				Database:   "db1",
				Collection: "users",
				Events:     []string{"create"},
				URL:        "https:///webhook",
			},
			wantError: "url must have a host",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateTrigger(tt.trigger)
			if tt.wantError == "" {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantError)
			}
		})
	}
}
