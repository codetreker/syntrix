package nats

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/pubsub"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestNewPublisher_NilJetStream(t *testing.T) {
	_, err := NewPublisher(nil, pubsub.PublisherOptions{
		StreamName: "TEST",
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "jetstream cannot be nil")
}

func TestNewPublisher_WithMock(t *testing.T) {
	mockJS := new(MockJetStream)

	mockJS.On("CreateOrUpdateStream", mock.Anything, mock.MatchedBy(func(cfg jetstream.StreamConfig) bool {
		return cfg.Name == "TEST" && len(cfg.Subjects) > 0 && cfg.Subjects[0] == "PREFIX.>"
	})).Return(nil, nil)

	pub, err := NewPublisher(mockJS, pubsub.PublisherOptions{
		StreamName:    "TEST",
		SubjectPrefix: "PREFIX",
	})

	require.NoError(t, err)
	assert.NotNil(t, pub)
	mockJS.AssertExpectations(t)
}

func TestNewPublisher_StreamCreationError(t *testing.T) {
	mockJS := new(MockJetStream)

	mockJS.On("CreateOrUpdateStream", mock.Anything, mock.Anything).Return(nil, errors.New("stream error"))

	_, err := NewPublisher(mockJS, pubsub.PublisherOptions{
		StreamName: "TEST",
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "stream error")
}

func TestPublisher_Publish(t *testing.T) {
	mockJS := new(MockJetStream)

	mockJS.On("CreateOrUpdateStream", mock.Anything, mock.Anything).Return(nil, nil)
	mockJS.On("Publish", mock.Anything, "PREFIX.test.subject", []byte("hello")).Return(&jetstream.PubAck{}, nil)

	pub, err := NewPublisher(mockJS, pubsub.PublisherOptions{
		StreamName:    "TEST",
		SubjectPrefix: "PREFIX",
	})
	require.NoError(t, err)

	err = pub.Publish(context.Background(), "test.subject", []byte("hello"))
	assert.NoError(t, err)
	mockJS.AssertExpectations(t)
}

func TestPublisher_PublishError(t *testing.T) {
	mockJS := new(MockJetStream)

	mockJS.On("CreateOrUpdateStream", mock.Anything, mock.Anything).Return(nil, nil)
	mockJS.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("publish failed"))

	pub, err := NewPublisher(mockJS, pubsub.PublisherOptions{
		StreamName:    "TEST",
		SubjectPrefix: "PREFIX",
	})
	require.NoError(t, err)

	err = pub.Publish(context.Background(), "test.subject", []byte("hello"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "publish failed")
}

func TestPublisher_OnPublishCallback_WithMock(t *testing.T) {
	mockJS := new(MockJetStream)

	mockJS.On("CreateOrUpdateStream", mock.Anything, mock.Anything).Return(nil, nil)
	mockJS.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(&jetstream.PubAck{}, nil)

	var calledSubject string
	var calledErr error
	var calledLatency time.Duration

	pub, err := NewPublisher(mockJS, pubsub.PublisherOptions{
		StreamName:    "TEST",
		SubjectPrefix: "PREFIX",
		OnPublish: func(subject string, err error, latency time.Duration) {
			calledSubject = subject
			calledErr = err
			calledLatency = latency
		},
	})
	require.NoError(t, err)

	err = pub.Publish(context.Background(), "test.subject", []byte("hello"))
	require.NoError(t, err)

	assert.Equal(t, "PREFIX.test.subject", calledSubject)
	assert.NoError(t, calledErr)
	assert.Greater(t, calledLatency, time.Duration(0))
}

func TestPublisher_Close(t *testing.T) {
	mockJS := new(MockJetStream)

	mockJS.On("CreateOrUpdateStream", mock.Anything, mock.Anything).Return(nil, nil)

	pub, err := NewPublisher(mockJS, pubsub.PublisherOptions{
		StreamName: "TEST",
	})
	require.NoError(t, err)

	err = pub.Close()
	assert.NoError(t, err)
}

func TestNewConsumer_NilJetStream(t *testing.T) {
	_, err := NewConsumer(nil, pubsub.ConsumerOptions{
		StreamName: "TEST",
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "jetstream cannot be nil")
}

func TestNewConsumer_WithMock(t *testing.T) {
	mockJS := new(MockJetStream)

	consumer, err := NewConsumer(mockJS, pubsub.ConsumerOptions{
		StreamName:   "TEST",
		ConsumerName: "test-consumer",
	})

	require.NoError(t, err)
	assert.NotNil(t, consumer)
}

func TestNewConsumer_RequiresStreamName(t *testing.T) {
	mockJS := new(MockJetStream)

	_, err := NewConsumer(mockJS, pubsub.ConsumerOptions{
		StreamName: "", // Empty stream name
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "stream name is required")
}

func TestNewConsumer_DefaultOptions(t *testing.T) {
	mockJS := new(MockJetStream)

	consumer, err := NewConsumer(mockJS, pubsub.ConsumerOptions{
		StreamName: "TEST",
		// All other options left at zero values
	})

	require.NoError(t, err)
	assert.NotNil(t, consumer)

	// Verify defaults were applied
	jsc := consumer.(*jetStreamConsumer)
	assert.Equal(t, 100, jsc.opts.ChannelBufSize)
}

func TestPublisher_NoPrefix(t *testing.T) {
	mockJS := new(MockJetStream)

	mockJS.On("CreateOrUpdateStream", mock.Anything, mock.Anything).Return(nil, nil)
	// Without prefix, subject should not be prefixed
	mockJS.On("Publish", mock.Anything, "test.subject", []byte("hello")).Return(&jetstream.PubAck{}, nil)

	pub, err := NewPublisher(mockJS, pubsub.PublisherOptions{
		StreamName:    "TEST",
		SubjectPrefix: "", // No prefix
	})
	require.NoError(t, err)

	err = pub.Publish(context.Background(), "test.subject", []byte("hello"))
	assert.NoError(t, err)
	mockJS.AssertExpectations(t)
}

func TestPublisher_NoStreamName(t *testing.T) {
	mockJS := new(MockJetStream)

	// Without stream name, CreateOrUpdateStream should not be called
	pub, err := NewPublisher(mockJS, pubsub.PublisherOptions{
		StreamName: "", // No stream name
	})

	require.NoError(t, err)
	assert.NotNil(t, pub)
	mockJS.AssertNotCalled(t, "CreateOrUpdateStream")
}

func TestConsumer_Subscribe_StreamError(t *testing.T) {
	mockJS := new(MockJetStream)

	mockJS.On("CreateOrUpdateStream", mock.Anything, mock.Anything).Return(nil, errors.New("stream error"))

	consumer, err := NewConsumer(mockJS, pubsub.ConsumerOptions{
		StreamName:   "TEST",
		ConsumerName: "test-consumer",
	})
	require.NoError(t, err)

	_, err = consumer.Subscribe(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "stream error")
}

func TestConsumer_Subscribe_ConsumerCreationError(t *testing.T) {
	mockJS := new(MockJetStream)

	mockJS.On("CreateOrUpdateStream", mock.Anything, mock.Anything).Return(nil, nil)
	mockJS.On("CreateOrUpdateConsumer", mock.Anything, "TEST", mock.Anything).Return(nil, errors.New("consumer error"))

	consumer, err := NewConsumer(mockJS, pubsub.ConsumerOptions{
		StreamName:   "TEST",
		ConsumerName: "test-consumer",
	})
	require.NoError(t, err)

	_, err = consumer.Subscribe(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "consumer error")
}

func TestConsumer_Subscribe_ConsumeError(t *testing.T) {
	mockJS := new(MockJetStream)
	mockConsumer := NewMockConsumer()

	mockJS.On("CreateOrUpdateStream", mock.Anything, mock.Anything).Return(nil, nil)
	mockJS.On("CreateOrUpdateConsumer", mock.Anything, "TEST", mock.Anything).Return(mockConsumer, nil)
	mockConsumer.On("Consume", mock.Anything).Return(nil, errors.New("consume error"))

	consumer, err := NewConsumer(mockJS, pubsub.ConsumerOptions{
		StreamName:   "TEST",
		ConsumerName: "test-consumer",
	})
	require.NoError(t, err)

	_, err = consumer.Subscribe(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "consume error")
}

func TestConsumer_Subscribe_Success(t *testing.T) {
	mockJS := new(MockJetStream)
	mockConsumer := NewMockConsumer()
	mockCC := NewMockConsumeContext()

	mockJS.On("CreateOrUpdateStream", mock.Anything, mock.Anything).Return(nil, nil)
	mockJS.On("CreateOrUpdateConsumer", mock.Anything, "TEST", mock.Anything).Return(mockConsumer, nil)
	mockConsumer.On("Consume", mock.Anything).Return(mockCC, nil)
	mockCC.On("Stop").Return()

	consumer, err := NewConsumer(mockJS, pubsub.ConsumerOptions{
		StreamName:   "TEST",
		ConsumerName: "test-consumer",
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	msgCh, err := consumer.Subscribe(ctx)
	require.NoError(t, err)
	assert.NotNil(t, msgCh)

	// Cancel to stop consumer
	cancel()

	// Wait for channel to close
	select {
	case _, ok := <-msgCh:
		if ok {
			t.Log("Received message or channel still open")
		}
	case <-time.After(100 * time.Millisecond):
	}
}

func TestConsumer_Subscribe_WithFilterSubject(t *testing.T) {
	mockJS := new(MockJetStream)
	mockConsumer := NewMockConsumer()
	mockCC := NewMockConsumeContext()

	mockJS.On("CreateOrUpdateStream", mock.Anything, mock.MatchedBy(func(cfg jetstream.StreamConfig) bool {
		return cfg.Name == "TEST" && len(cfg.Subjects) > 0 && cfg.Subjects[0] == "custom.subject.>"
	})).Return(nil, nil)
	mockJS.On("CreateOrUpdateConsumer", mock.Anything, "TEST", mock.MatchedBy(func(cfg jetstream.ConsumerConfig) bool {
		return cfg.FilterSubject == "custom.subject.>"
	})).Return(mockConsumer, nil)
	mockConsumer.On("Consume", mock.Anything).Return(mockCC, nil)
	mockCC.On("Stop").Return()

	consumer, err := NewConsumer(mockJS, pubsub.ConsumerOptions{
		StreamName:    "TEST",
		ConsumerName:  "test-consumer",
		FilterSubject: "custom.subject.>",
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	msgCh, err := consumer.Subscribe(ctx)
	require.NoError(t, err)
	assert.NotNil(t, msgCh)

	mockJS.AssertExpectations(t)
}

func TestConsumer_Subscribe_DefaultConsumerName(t *testing.T) {
	mockJS := new(MockJetStream)
	mockConsumer := NewMockConsumer()
	mockCC := NewMockConsumeContext()

	mockJS.On("CreateOrUpdateStream", mock.Anything, mock.Anything).Return(nil, nil)
	mockJS.On("CreateOrUpdateConsumer", mock.Anything, "TEST", mock.MatchedBy(func(cfg jetstream.ConsumerConfig) bool {
		return cfg.Durable == "consumer" // Default name
	})).Return(mockConsumer, nil)
	mockConsumer.On("Consume", mock.Anything).Return(mockCC, nil)
	mockCC.On("Stop").Return()

	consumer, err := NewConsumer(mockJS, pubsub.ConsumerOptions{
		StreamName:   "TEST",
		ConsumerName: "", // Empty, should use default
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err = consumer.Subscribe(ctx)
	require.NoError(t, err)

	mockJS.AssertExpectations(t)
}

func TestConsumer_Subscribe_ReceivesMessages(t *testing.T) {
	mockJS := new(MockJetStream)
	mockConsumer := NewMockConsumer()
	mockCC := NewMockConsumeContext()

	mockJS.On("CreateOrUpdateStream", mock.Anything, mock.Anything).Return(nil, nil)
	mockJS.On("CreateOrUpdateConsumer", mock.Anything, "TEST", mock.Anything).Return(mockConsumer, nil)
	mockConsumer.On("Consume", mock.Anything).Return(mockCC, nil)
	mockCC.On("Stop").Return()

	consumer, err := NewConsumer(mockJS, pubsub.ConsumerOptions{
		StreamName:   "TEST",
		ConsumerName: "test-consumer",
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	msgCh, err := consumer.Subscribe(ctx)
	require.NoError(t, err)

	// Get the handler from mock and send a message
	var handler jetstream.MessageHandler
	select {
	case handler = <-mockConsumer.HandlerCh():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Handler not registered")
	}

	// Send a mock message
	mockMsg := NewMockMsg("test.subject", []byte("hello"))
	mockMsg.On("Ack").Return(nil)
	handler(mockMsg)

	// Receive from channel
	select {
	case msg := <-msgCh:
		assert.Equal(t, "test.subject", msg.Subject())
		assert.Equal(t, []byte("hello"), msg.Data())
		msg.Ack()
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Did not receive message")
	}
}

func TestConsumer_Subscribe_ChannelClosedOnCancel(t *testing.T) {
	mockJS := new(MockJetStream)
	mockConsumer := NewMockConsumer()
	mockCC := NewMockConsumeContext()

	mockJS.On("CreateOrUpdateStream", mock.Anything, mock.Anything).Return(nil, nil)
	mockJS.On("CreateOrUpdateConsumer", mock.Anything, "TEST", mock.Anything).Return(mockConsumer, nil)
	mockConsumer.On("Consume", mock.Anything).Return(mockCC, nil)
	mockCC.On("Stop").Return()

	consumer, err := NewConsumer(mockJS, pubsub.ConsumerOptions{
		StreamName:   "TEST",
		ConsumerName: "test-consumer",
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	msgCh, err := consumer.Subscribe(ctx)
	require.NoError(t, err)

	// Cancel context
	cancel()

	// Channel should close
	select {
	case _, ok := <-msgCh:
		assert.False(t, ok, "Channel should be closed")
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Channel did not close in time")
	}
}

func TestConsumer_Subscribe_NakOnContextDone(t *testing.T) {
	mockJS := new(MockJetStream)
	mockConsumer := NewMockConsumer()
	mockCC := NewMockConsumeContext()

	mockJS.On("CreateOrUpdateStream", mock.Anything, mock.Anything).Return(nil, nil)
	mockJS.On("CreateOrUpdateConsumer", mock.Anything, "TEST", mock.Anything).Return(mockConsumer, nil)
	mockConsumer.On("Consume", mock.Anything).Return(mockCC, nil)
	mockCC.On("Stop").Return()

	consumer, err := NewConsumer(mockJS, pubsub.ConsumerOptions{
		StreamName:     "TEST",
		ConsumerName:   "test-consumer",
		ChannelBufSize: 1, // Small buffer
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	_, err = consumer.Subscribe(ctx)
	require.NoError(t, err)

	// Get the handler
	var handler jetstream.MessageHandler
	select {
	case handler = <-mockConsumer.HandlerCh():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Handler not registered")
	}

	// Cancel first
	cancel()
	time.Sleep(50 * time.Millisecond)

	// Message arriving after cancel should be NAK'd
	mockMsg := NewMockMsg("test.subject", []byte("hello"))
	mockMsg.On("Nak").Return(nil)
	handler(mockMsg)

	time.Sleep(50 * time.Millisecond)
	mockMsg.AssertCalled(t, "Nak")
}
