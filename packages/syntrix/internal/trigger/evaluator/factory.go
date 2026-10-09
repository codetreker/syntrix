package evaluator

import (
	"fmt"

	"github.com/codetreker/syntrix/internal/core/pubsub"
	natspubsub "github.com/codetreker/syntrix/internal/core/pubsub/nats"
	"github.com/codetreker/syntrix/internal/core/storage"
	"github.com/codetreker/syntrix/internal/puller"
	"github.com/codetreker/syntrix/internal/trigger/evaluator/watcher"
	"github.com/codetreker/syntrix/internal/trigger/types"
	"github.com/nats-io/nats.go"
)

// Dependencies contains external dependencies for the evaluator service.
type Dependencies struct {
	Store     storage.DocumentStore
	Puller    puller.Service
	Publisher pubsub.Publisher
	Metrics   types.Metrics
}

// NewService creates a new evaluator Service.
func NewService(deps Dependencies, cfg Config) (Service, error) {
	if deps.Puller == nil {
		return nil, fmt.Errorf("puller service is required for evaluator service")
	}

	// Apply defaults
	cfg.ApplyDefaults()
	if deps.Metrics == nil {
		deps.Metrics = &types.NoopMetrics{}
	}

	// Create CEL evaluator
	eval, err := NewEvaluator()
	if err != nil {
		return nil, fmt.Errorf("failed to create evaluator: %w", err)
	}

	// Create watcher (receives all events; database filtering is done by Evaluator per trigger)
	w := watcher.NewWatcher(deps.Puller, deps.Store, watcher.WatcherOptions{
		StartFromNow:       cfg.StartFromNow,
		CheckpointDatabase: cfg.CheckpointDatabase,
	})

	// Create publisher wrapping pubsub.Publisher
	var pub TaskPublisher
	if deps.Publisher != nil {
		pub = NewTaskPublisher(deps.Publisher, cfg.StreamName, deps.Metrics)
	}

	svc := &service{
		evaluator: eval,
		watcher:   w,
		publisher: pub,
	}

	// Load trigger rules from directory if configured
	if cfg.RulesPath != "" {
		triggersByDB, err := types.LoadTriggersFromDir(cfg.RulesPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load trigger rules from %s: %w", cfg.RulesPath, err)
		}
		// Flatten all triggers for loading
		var allTriggers []*types.Trigger
		for _, triggers := range triggersByDB {
			allTriggers = append(allTriggers, triggers...)
		}
		if err := svc.LoadTriggers(allTriggers); err != nil {
			return nil, fmt.Errorf("failed to load triggers: %w", err)
		}
	}

	return svc, nil
}

// NewPublisher creates a pubsub.Publisher from evaluator config.
func NewPublisher(nc *nats.Conn, cfg Config) (pubsub.Publisher, error) {
	return newPublisherWithFactory(nc, cfg, natspubsub.NewJetStream)
}

// newPublisherWithFactory is the internal implementation that accepts a JetStream factory for testing.
func newPublisherWithFactory(nc *nats.Conn, cfg Config, jsFactory func(*nats.Conn) (natspubsub.JetStream, error)) (pubsub.Publisher, error) {
	js, err := jsFactory(nc)
	if err != nil {
		return nil, fmt.Errorf("failed to create JetStream: %w", err)
	}

	cfg.ApplyDefaults()

	return natspubsub.NewPublisher(js, pubsub.PublisherOptions{
		StreamName:    cfg.StreamName,
		SubjectPrefix: cfg.StreamName,
		RetryAttempts: cfg.RetryAttempts,
		Storage:       cfg.StorageTypeValue(),
	})
}
