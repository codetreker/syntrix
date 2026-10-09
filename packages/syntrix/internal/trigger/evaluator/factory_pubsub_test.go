package evaluator

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

// mockStream is a minimal mock for jetstream.Stream.
type mockStream struct {
	jetstream.Stream
}

func TestNewPublisherWithFactory_Success(t *testing.T) {
	mockJS := new(mockJetStream)
	mockJS.On("CreateOrUpdateStream", mock.Anything, mock.Anything).Return(&mockStream{}, nil)

	// Create a factory that returns our mock
	jsFactory := func(nc *nats.Conn) (natspubsub.JetStream, error) {
		return mockJS, nil
	}

	cfg := Config{
		StreamName:    "TEST_STREAM",
		RetryAttempts: 3,
		StorageType:   "memory",
	}

	pub, err := newPublisherWithFactory(nil, cfg, jsFactory)
	assert.NoError(t, err)
	assert.NotNil(t, pub)
	mockJS.AssertExpectations(t)
}

func TestNewPublisherWithFactory_StreamError(t *testing.T) {
	mockJS := new(mockJetStream)
	mockJS.On("CreateOrUpdateStream", mock.Anything, mock.Anything).Return(nil, errors.New("stream error"))

	// Create a factory that returns our mock
	jsFactory := func(nc *nats.Conn) (natspubsub.JetStream, error) {
		return mockJS, nil
	}

	cfg := Config{
		StreamName: "TEST_STREAM",
	}

	pub, err := newPublisherWithFactory(nil, cfg, jsFactory)
	assert.Error(t, err)
	assert.Nil(t, pub)
	assert.Contains(t, err.Error(), "stream error")
	mockJS.AssertExpectations(t)
}

func TestNewPublisherWithFactory_JetStreamFactoryError(t *testing.T) {
	// Create a factory that returns an error
	jsFactory := func(nc *nats.Conn) (natspubsub.JetStream, error) {
		return nil, errors.New("factory error")
	}

	cfg := Config{}

	pub, err := newPublisherWithFactory(nil, cfg, jsFactory)
	assert.Error(t, err)
	assert.Nil(t, pub)
	assert.Contains(t, err.Error(), "failed to create JetStream")
}

func TestNewPublisherWithFactory_AppliesDefaults(t *testing.T) {
	mockJS := new(mockJetStream)
	// Capture the stream config to verify defaults are applied
	var capturedCfg jetstream.StreamConfig
	mockJS.On("CreateOrUpdateStream", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		capturedCfg = args.Get(1).(jetstream.StreamConfig)
	}).Return(&mockStream{}, nil)

	jsFactory := func(nc *nats.Conn) (natspubsub.JetStream, error) {
		return mockJS, nil
	}

	// Empty config should get defaults applied
	cfg := Config{}

	pub, err := newPublisherWithFactory(nil, cfg, jsFactory)
	assert.NoError(t, err)
	assert.NotNil(t, pub)

	// Verify that defaults were applied (default stream name is "TRIGGERS")
	assert.Equal(t, "TRIGGERS", capturedCfg.Name)
	mockJS.AssertExpectations(t)
}
