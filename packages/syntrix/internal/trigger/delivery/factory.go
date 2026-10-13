package delivery

import (
	"context"
	"fmt"

	deploymentconfig "github.com/codetreker/syntrix/deployment/config"
	"github.com/codetreker/syntrix/internal/core/pubsub"
	natspubsub "github.com/codetreker/syntrix/internal/core/pubsub/nats"
	"github.com/codetreker/syntrix/internal/identity"
	"github.com/codetreker/syntrix/internal/trigger/delivery/worker"
	"github.com/codetreker/syntrix/internal/trigger/types"
	"github.com/nats-io/nats.go"
)

// Dependencies contains external dependencies for the delivery service.
type Dependencies struct {
	Consumer pubsub.Consumer
	Auth     identity.SystemTokenIssuer
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
func NewConsumer(ctx context.Context, nc *nats.Conn, cfg Config, binding deploymentconfig.BrokerBinding) (pubsub.Consumer, error) {
	return newConsumerWithFactory(ctx, nc, cfg, binding, natspubsub.NewJetStream)
}

func newConsumerWithFactory(ctx context.Context, nc *nats.Conn, cfg Config, binding deploymentconfig.BrokerBinding, jsFactory func(*nats.Conn) (natspubsub.JetStream, error)) (pubsub.Consumer, error) {
	js, err := jsFactory(nc)
	if err != nil {
		return nil, fmt.Errorf("failed to create JetStream: %w", err)
	}

	if cfg.StreamName == "" {
		cfg.StreamName = binding.StreamName
	}
	if cfg.ConsumerName == "" {
		cfg.ConsumerName = binding.ConsumerName
	}
	if cfg.StorageType == "" {
		cfg.StorageType = binding.Policy.Storage
	}
	cfg.ApplyDefaults()
	if cfg.StreamName != binding.StreamName || cfg.ConsumerName != binding.ConsumerName || cfg.StorageType != binding.Policy.Storage {
		return nil, fmt.Errorf("delivery stream_name, consumer_name, and storage_type must match broker binding")
	}
	return natspubsub.NewConsumer(ctx, js, binding, pubsub.ConsumerOptions{
		StreamName:     cfg.StreamName,
		ConsumerName:   cfg.ConsumerName,
		FilterSubject:  binding.SubjectPrefix + ".>",
		ChannelBufSize: cfg.ChannelBufSize,
		Storage:        cfg.StorageTypeValue(),
	})
}
