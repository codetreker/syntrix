package delivery

import (
	"context"
	"errors"
	"testing"

	pubsubconfig "github.com/codetreker/syntrix/internal/core/pubsub/config"
	natspubsub "github.com/codetreker/syntrix/internal/core/pubsub/nats"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type mockJetStream struct{ mock.Mock }

func (m *mockJetStream) Stream(ctx context.Context, name string) (jetstream.Stream, error) {
	args := m.Called(ctx, name)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(jetstream.Stream), args.Error(1)
}

func (m *mockJetStream) Consumer(ctx context.Context, stream, name string) (jetstream.Consumer, error) {
	args := m.Called(ctx, stream, name)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(jetstream.Consumer), args.Error(1)
}

func (m *mockJetStream) CreateStream(ctx context.Context, cfg jetstream.StreamConfig) (jetstream.Stream, error) {
	args := m.Called(ctx, cfg)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(jetstream.Stream), args.Error(1)
}

func (m *mockJetStream) CreateConsumer(ctx context.Context, stream string, cfg jetstream.ConsumerConfig) (jetstream.Consumer, error) {
	args := m.Called(ctx, stream, cfg)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(jetstream.Consumer), args.Error(1)
}

func (m *mockJetStream) Publish(ctx context.Context, subject string, data []byte, opts ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
	args := m.Called(ctx, subject, data, len(opts))
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*jetstream.PubAck), args.Error(1)
}

type mockStream struct {
	jetstream.Stream
	cfg jetstream.StreamConfig
}

func (m *mockStream) Info(context.Context, ...jetstream.StreamInfoOpt) (*jetstream.StreamInfo, error) {
	return &jetstream.StreamInfo{Config: m.cfg}, nil
}

type mockBrokerConsumer struct {
	jetstream.Consumer
	cfg jetstream.ConsumerConfig
}

func (m *mockBrokerConsumer) Info(context.Context) (*jetstream.ConsumerInfo, error) {
	return &jetstream.ConsumerInfo{Config: m.cfg}, nil
}

func factoryBinding() pubsubconfig.BrokerBinding {
	binding := pubsubconfig.DefaultOperatorBinding()
	binding.StreamName = "INSTANCE_TRIGGERS"
	binding.SubjectPrefix = "instance.triggers"
	binding.ConsumerName = "instance-delivery"
	binding.Policy.Storage = "memory"
	return binding
}

func expectAttachment(t *testing.T, js *mockJetStream, ctx context.Context, binding pubsubconfig.BrokerBinding) {
	spec, err := binding.Specification()
	require.NoError(t, err)
	js.On("Stream", ctx, binding.StreamName).Return(&mockStream{cfg: spec.Stream}, nil)
	js.On("Consumer", ctx, binding.StreamName, binding.ConsumerName).Return(&mockBrokerConsumer{cfg: spec.Consumer}, nil)
}

func TestNewConsumerWithFactory_AttachesBinding(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	binding := factoryBinding()
	js := new(mockJetStream)
	expectAttachment(t, js, ctx, binding)
	consumer, err := newConsumerWithFactory(ctx, nil, Config{ChannelBufSize: 24}, binding, func(*nats.Conn) (natspubsub.JetStream, error) { return js, nil })
	require.NoError(t, err)
	require.NotNil(t, consumer)
	js.AssertExpectations(t)
}

func TestNewConsumerWithFactory_Failures(t *testing.T) {
	binding := factoryBinding()
	t.Run("factory", func(t *testing.T) {
		cause := errors.New("factory error")
		consumer, err := newConsumerWithFactory(context.Background(), nil, Config{}, binding, func(*nats.Conn) (natspubsub.JetStream, error) { return nil, cause })
		assert.Nil(t, consumer)
		assert.ErrorIs(t, err, cause)
	})
	t.Run("nil JetStream", func(t *testing.T) {
		consumer, err := newConsumerWithFactory(context.Background(), nil, Config{}, binding, func(*nats.Conn) (natspubsub.JetStream, error) { return nil, nil })
		assert.Nil(t, consumer)
		assert.Error(t, err)
	})
	t.Run("consumer lookup", func(t *testing.T) {
		cause := errors.New("consumer error")
		js := new(mockJetStream)
		spec, err := binding.Specification()
		require.NoError(t, err)
		js.On("Stream", mock.Anything, binding.StreamName).Return(&mockStream{cfg: spec.Stream}, nil)
		js.On("Consumer", mock.Anything, binding.StreamName, binding.ConsumerName).Return(nil, cause)
		consumer, err := newConsumerWithFactory(context.Background(), nil, Config{}, binding, func(*nats.Conn) (natspubsub.JetStream, error) { return js, nil })
		assert.Nil(t, consumer)
		assert.ErrorIs(t, err, cause)
		js.AssertExpectations(t)
	})
	for _, cfg := range []Config{{StreamName: "FOREIGN"}, {ConsumerName: "foreign"}, {StorageType: "file"}} {
		t.Run("contradictory config", func(t *testing.T) {
			js := new(mockJetStream)
			consumer, err := newConsumerWithFactory(context.Background(), nil, cfg, binding, func(*nats.Conn) (natspubsub.JetStream, error) { return js, nil })
			assert.Nil(t, consumer)
			assert.ErrorContains(t, err, "must match broker binding")
			js.AssertExpectations(t)
		})
	}
}
