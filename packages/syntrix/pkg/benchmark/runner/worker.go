package runner

import (
	"context"
	"fmt"
	"sync"

	"github.com/codetreker/syntrix/pkg/benchmark/types"
)

// Worker executes operations concurrently.
type Worker struct {
	id      int
	client  types.Client
	running bool
	mu      sync.RWMutex
}

// NewWorker creates a new worker instance.
func NewWorker(id int, client types.Client) *Worker {
	return &Worker{
		id:      id,
		client:  client,
		running: false,
	}
}

// Start begins worker execution.
func (w *Worker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.running {
		return fmt.Errorf("worker %d is already running", w.id)
	}
	w.running = true

	return nil
}

// Execute performs a single operation.
func (w *Worker) Execute(ctx context.Context, op types.Operation) (*types.OperationResult, error) {
	w.mu.RLock()
	if !w.running {
		w.mu.RUnlock()
		return nil, fmt.Errorf("worker %d is not running", w.id)
	}
	client := w.client
	w.mu.RUnlock()

	if op == nil {
		return nil, fmt.Errorf("operation cannot be nil")
	}

	// Execute the operation
	result, err := op.Execute(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("operation execution failed: %w", err)
	}

	return result, nil
}

// Stop gracefully stops the worker.
func (w *Worker) Stop(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.running {
		return nil
	}

	w.running = false
	return nil
}

// ID returns the worker identifier.
func (w *Worker) ID() int {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.id
}

// IsRunning returns whether the worker is currently running.
func (w *Worker) IsRunning() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.running
}
