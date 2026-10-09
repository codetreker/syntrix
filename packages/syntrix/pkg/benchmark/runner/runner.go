// Package runner provides the core benchmark execution engine.
package runner

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/codetreker/syntrix/pkg/benchmark/client"
	"github.com/codetreker/syntrix/pkg/benchmark/types"
	"github.com/codetreker/syntrix/pkg/benchmark/utils"
)

// BasicRunner implements the Runner interface for executing benchmarks.
type BasicRunner struct {
	config    *types.Config
	client    types.Client
	scenario  types.Scenario
	metrics   types.MetricsCollector
	workers   []*Worker
	startTime time.Time
	endTime   time.Time
	mu        sync.RWMutex
}

// NewBasicRunner creates a new basic runner instance.
func NewBasicRunner() *BasicRunner {
	return &BasicRunner{
		workers: make([]*Worker, 0),
	}
}

// Initialize prepares the benchmark environment.
func (r *BasicRunner) Initialize(ctx context.Context, config *types.Config) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if config == nil {
		return fmt.Errorf("config cannot be nil")
	}

	r.config = config

	// Load authentication token
	token, err := utils.LoadToken(config.Auth.Token)
	if err != nil {
		return fmt.Errorf("failed to load auth token: %w", err)
	}

	// Initialize HTTP client
	httpClient, err := client.NewHTTPClient(config.Target, config.Auth.Database, token)
	if err != nil {
		return fmt.Errorf("failed to create HTTP client: %w", err)
	}
	r.client = httpClient

	// Initialize metrics collector (placeholder - will implement in P1-40)
	// For now, we'll set it to nil and check before use
	r.metrics = nil

	// Initialize scenario (placeholder - will implement in P1-30)
	// For now, we'll set it to nil and check before use
	r.scenario = nil

	// Initialize workers
	r.workers = make([]*Worker, config.Workers)
	for i := 0; i < config.Workers; i++ {
		r.workers[i] = NewWorker(i, r.client)
	}

	return nil
}

// Run executes the benchmark scenario.
func (r *BasicRunner) Run(ctx context.Context) (*types.Result, error) {
	r.mu.Lock()
	if r.config == nil {
		r.mu.Unlock()
		return nil, fmt.Errorf("runner not initialized")
	}
	config := r.config
	r.mu.Unlock()

	// Record start time
	r.startTime = time.Now()

	// Create test environment
	token, _ := utils.LoadToken(config.Auth.Token)
	env := &types.TestEnv{
		BaseURL: config.Target,
		Token:   token,
		Config:  config,
		Client:  r.client,
	}

	// Setup scenario if available
	if r.scenario != nil {
		if err := r.scenario.Setup(ctx, env); err != nil {
			return nil, fmt.Errorf("scenario setup failed: %w", err)
		}
	}

	// Start workers with concurrent execution
	var wg sync.WaitGroup
	var opsCompleted atomic.Int64
	var opsErrors atomic.Int64

	// Use a stop channel for graceful shutdown instead of context cancellation
	stopCh := make(chan struct{})

	// Timer to signal workers to stop accepting new operations
	timer := time.AfterFunc(config.Duration, func() {
		close(stopCh)
	})
	defer timer.Stop()

	// Start each worker in a goroutine that continuously executes operations
	for _, w := range r.workers {
		wg.Add(1)
		go func(worker *Worker) {
			defer wg.Done()

			// Mark worker as running
			if err := worker.Start(ctx); err != nil {
				return
			}

			// Continuously execute operations until stop signal
			for {
				select {
				case <-stopCh:
					// Stop accepting new operations, but let current ones complete
					return
				case <-ctx.Done():
					return
				default:
					// Get next operation from scenario if available
					if r.scenario != nil {
						op, err := r.scenario.NextOperation()
						if err != nil {
							// No more operations or error, continue to allow other workers to work
							continue
						}

						// Check if operation is nil (scenario may return nil to indicate no more operations)
						if op == nil {
							continue
						}

						// Execute operation with parent context (not cancelled on duration end)
						result, err := op.Execute(ctx, worker.client)
						if err != nil {
							opsErrors.Add(1)
						} else {
							opsCompleted.Add(1)
						}

						// Record metrics if collector available
						if r.metrics != nil && result != nil {
							r.metrics.RecordOperation(result)
						}
					} else {
						// No scenario available, wait for stop signal
						<-stopCh
						return
					}
				}
			}
		}(w)
	}

	// Wait for workers to finish
	wg.Wait()

	// Stop all workers
	for _, w := range r.workers {
		if err := w.Stop(ctx); err != nil {
			// Log error but continue cleanup
		}
	}

	// Teardown scenario if available
	if r.scenario != nil {
		if err := r.scenario.Teardown(ctx, env); err != nil {
			// Log error but don't fail the benchmark
		}
	}

	// Record end time
	r.endTime = time.Now()

	// Build result
	result := &types.Result{
		StartTime: r.startTime,
		EndTime:   r.endTime,
		Duration:  r.endTime.Sub(r.startTime).Seconds(),
		Config:    r.config,
	}

	// Add summary metrics if collector available
	if r.metrics != nil {
		result.Summary = r.metrics.GetSnapshot()
	} else {
		// Create basic summary
		result.Summary = &types.AggregatedMetrics{
			TotalOperations: opsCompleted.Load(),
			TotalErrors:     opsErrors.Load(),
			SuccessRate:     float64(opsCompleted.Load()) / float64(opsCompleted.Load()+opsErrors.Load()) * 100,
		}
	}

	return result, nil
}

// Cleanup performs post-benchmark cleanup.
func (r *BasicRunner) Cleanup(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	var errs []error

	// Close client
	if r.client != nil {
		if err := r.client.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close client: %w", err))
		}
	}

	// Reset state
	r.config = nil
	r.client = nil
	r.scenario = nil
	r.metrics = nil
	r.workers = nil

	if len(errs) > 0 {
		return fmt.Errorf("cleanup errors: %v", errs)
	}

	return nil
}

// SetScenario sets the scenario for the runner (for testing and custom scenarios).
func (r *BasicRunner) SetScenario(scenario types.Scenario) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scenario = scenario
}

// SetMetricsCollector sets the metrics collector for the runner.
func (r *BasicRunner) SetMetricsCollector(collector types.MetricsCollector) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.metrics = collector
}
