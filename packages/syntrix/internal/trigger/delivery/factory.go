package delivery

import (
	"context"
	"fmt"

	"github.com/codetreker/syntrix/internal/core/identity"
	"github.com/codetreker/syntrix/internal/core/pubsub"
	natspubsub "github.com/codetreker/syntrix/internal/core/pubsub/nats"
	"github.com/codetreker/syntrix/internal/trigger/delivery/worker"
	"github.com/codetreker/syntrix/internal/trigger/types"
	"github.com/nats-io/nats.go"
)

// Dependencies contains external dependencies for the delivery service.
type Dependencies struct {
	Consumer pubsub.Consumer
	Auth     identity.AuthN
	Secrets  types.SecretProvider
	Metrics  types.Metrics
}

// service implements the Service interface.
type service struct {
	consumer TaskConsumer
}

// NewService creates a new delivery Service.
func NewService(deps Dependencies, cfg Config) (Service, error) {
	if deps.Consumer == nil {
		return nil, fmt.Errorf("consumer is required for delivery service")
	}

	// Apply defaults
	cfg.ApplyDefaults()
	if deps.Metrics == nil {
		deps.Metrics = &types.NoopMetrics{}
	}

	// Create HTTP delivery worker
	w := worker.NewDeliveryWorker(deps.Auth, deps.Secrets, worker.HTTPClientOptions{}, deps.Metrics)

	// Build consumer options
	var consumerOpts []ConsumerOption
	if cfg.ChannelBufSize > 0 {
		consumerOpts = append(consumerOpts, WithChannelBufferSize(cfg.ChannelBufSize))
	}
	if cfg.DrainTimeout > 0 {
		consumerOpts = append(consumerOpts, WithDrainTimeout(cfg.DrainTimeout))
	}
	if cfg.ShutdownTimeout > 0 {
		consumerOpts = append(consumerOpts, WithShutdownTimeout(cfg.ShutdownTimeout))
	}

	// Create consumer wrapping pubsub.Consumer
	taskConsumer := NewTaskConsumer(deps.Consumer, w, cfg.NumWorkers, deps.Metrics, consumerOpts...)

	return &service{
		consumer: taskConsumer,
	}, nil
}

// Start begins consuming and processing delivery tasks.
func (s *service) Start(ctx context.Context) error {
	return s.consumer.Start(ctx)
}

// NewConsumer creates a pubsub.Consumer from delivery config.
func NewConsumer(nc *nats.Conn, cfg Config) (pubsub.Consumer, error) {
	return newConsumerWithFactory(nc, cfg, natspubsub.NewJetStream)
}

// newConsumerWithFactory is the internal implementation that accepts a JetStream factory for testing.
func newConsumerWithFactory(nc *nats.Conn, cfg Config, jsFactory func(*nats.Conn) (natspubsub.JetStream, error)) (pubsub.Consumer, error) {
	js, err := jsFactory(nc)
	if err != nil {
		return nil, fmt.Errorf("failed to create JetStream: %w", err)
	}

	cfg.ApplyDefaults()

	return natspubsub.NewConsumer(js, pubsub.ConsumerOptions{
		StreamName:   cfg.StreamName,
		ConsumerName: cfg.ConsumerName,
		Storage:      cfg.StorageTypeValue(),
	})
}
