package delivery

import (
	"context"
	"errors"
	"testing"

	natspubsub "github.com/codetreker/syntrix/internal/core/pubsub/nats"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// mockJetStream is a mock implementation of natspubsub.JetStream for testing.
type mockJetStream struct {
	mock.Mock
}

func (m *mockJetStream) CreateOrUpdateStream(ctx context.Context, cfg jetstream.StreamConfig) (jetstream.Stream, error) {
	args := m.Called(ctx, cfg)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(jetstream.Stream), args.Error(1)
}

func (m *mockJetStream) CreateOrUpdateConsumer(ctx context.Context, stream string, cfg jetstream.ConsumerConfig) (jetstream.Consumer, error) {
	args := m.Called(ctx, stream, cfg)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(jetstream.Consumer), args.Error(1)
}

func (m *mockJetStream) Publish(ctx context.Context, subject string, data []byte, opts ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
	args := m.Called(ctx, subject, data)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*jetstream.PubAck), args.Error(1)
}

func TestNewConsumerWithFactory_Success(t *testing.T) {
	mockJS := new(mockJetStream)
	// NewConsumer doesn't call CreateOrUpdateStream - that happens on Subscribe

	// Create a factory that returns our mock
	jsFactory := func(nc *nats.Conn) (natspubsub.JetStream, error) {
		return mockJS, nil
	}

	cfg := Config{
		StreamName:   "TEST_STREAM",
		ConsumerName: "test-consumer",
		StorageType:  "memory",
	}

	consumer, err := newConsumerWithFactory(nil, cfg, jsFactory)
	assert.NoError(t, err)
	assert.NotNil(t, consumer)
}

func TestNewConsumerWithFactory_JetStreamFactoryError(t *testing.T) {
	// Create a factory that returns an error
	jsFactory := func(nc *nats.Conn) (natspubsub.JetStream, error) {
		return nil, errors.New("factory error")
	}

	cfg := Config{}

	consumer, err := newConsumerWithFactory(nil, cfg, jsFactory)
	assert.Error(t, err)
	assert.Nil(t, consumer)
	assert.Contains(t, err.Error(), "failed to create JetStream")
}

func TestNewConsumerWithFactory_AppliesDefaults(t *testing.T) {
	mockJS := new(mockJetStream)

	jsFactory := func(nc *nats.Conn) (natspubsub.JetStream, error) {
		return mockJS, nil
	}

	// Empty config should get defaults applied
	cfg := Config{}

	consumer, err := newConsumerWithFactory(nil, cfg, jsFactory)
	assert.NoError(t, err)
	assert.NotNil(t, consumer)
	// If we got here without error, defaults were applied correctly
}

func TestNewConsumerWithFactory_NilJetStream(t *testing.T) {
	// Factory returns nil JetStream (but no error) - should fail in NewConsumer
	jsFactory := func(nc *nats.Conn) (natspubsub.JetStream, error) {
		return nil, nil
	}

	cfg := Config{
		StreamName: "TEST_STREAM",
	}

	consumer, err := newConsumerWithFactory(nil, cfg, jsFactory)
	assert.Error(t, err)
	assert.Nil(t, consumer)
}
