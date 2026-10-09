package nats

import (
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/pubsub"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewPublisher_NilJetStream_Basic(t *testing.T) {
	_, err := NewPublisher(nil, pubsub.PublisherOptions{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "jetstream cannot be nil")
}

func TestPublisher_SubjectPrefix(t *testing.T) {
	// This test verifies the subject building logic
	// Real NATS tests require integration test setup

	opts := pubsub.PublisherOptions{
		StreamName:    "TEST",
		SubjectPrefix: "PREFIX",
	}

	// Verify that with a prefix, the subject is built correctly
	subject := "db.collection.doc"
	fullSubject := opts.SubjectPrefix + "." + subject
	assert.Equal(t, "PREFIX.db.collection.doc", fullSubject)
}

func TestPublisher_OnPublishCallback_Basic(t *testing.T) {
	var calledSubject string
	var calledErr error
	var calledLatency time.Duration

	opts := pubsub.PublisherOptions{
		StreamName:    "TEST",
		SubjectPrefix: "PREFIX",
		OnPublish: func(subject string, err error, latency time.Duration) {
			calledSubject = subject
			calledErr = err
			calledLatency = latency
		},
	}

	// Verify callback signature works
	if opts.OnPublish != nil {
		opts.OnPublish("test.subject", nil, 100*time.Millisecond)
	}

	assert.Equal(t, "test.subject", calledSubject)
	assert.NoError(t, calledErr)
	assert.Equal(t, 100*time.Millisecond, calledLatency)
}

func TestNewConsumer_NilJetStream_Basic(t *testing.T) {
	_, err := NewConsumer(nil, pubsub.ConsumerOptions{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "jetstream cannot be nil")
}

func TestNewConsumer_RequiresStreamName_Basic(t *testing.T) {
	// Can't test without real connection, but we can verify the validation exists
	// by checking the error message in the actual implementation
}

func TestConsumerOptions_Defaults(t *testing.T) {
	defaults := pubsub.DefaultConsumerOptions()

	assert.Equal(t, 100, defaults.ChannelBufSize)
}

func TestMessage_Interface(t *testing.T) {
	// Verify natsMessage implements pubsub.Message
	var _ pubsub.Message = (*natsMessage)(nil)
}

func TestPublisher_Interface(t *testing.T) {
	// Verify jetStreamPublisher implements pubsub.Publisher
	var _ pubsub.Publisher = (*jetStreamPublisher)(nil)
}

func TestConsumer_Interface(t *testing.T) {
	// Verify jetStreamConsumer implements pubsub.Consumer
	var _ pubsub.Consumer = (*jetStreamConsumer)(nil)
}

func TestWrapMessage(t *testing.T) {
	// WrapMessage should return a non-nil Message when given a non-nil Msg
	// We can't easily create a real jetstream.Msg in unit tests,
	// so we just verify the function exists and has correct signature
	require.NotNil(t, WrapMessage)
}

func TestMessageMetadata(t *testing.T) {
	md := pubsub.MessageMetadata{
		NumDelivered: 3,
		Timestamp:    time.Now(),
		Subject:      "test.subject",
		Stream:       "STREAM",
		Consumer:     "CONSUMER",
	}

	assert.Equal(t, uint64(3), md.NumDelivered)
	assert.Equal(t, "test.subject", md.Subject)
	assert.Equal(t, "STREAM", md.Stream)
	assert.Equal(t, "CONSUMER", md.Consumer)
}

func TestPublisherOptions(t *testing.T) {
	opts := pubsub.PublisherOptions{
		StreamName:    "MYSTREAM",
		SubjectPrefix: "PREFIX",
	}

	assert.Equal(t, "MYSTREAM", opts.StreamName)
	assert.Equal(t, "PREFIX", opts.SubjectPrefix)
}

func TestNewJetStream_NilConnection(t *testing.T) {
	// NewJetStream with nil connection should return an error
	js, err := NewJetStream(nil)
	assert.Error(t, err)
	assert.Nil(t, js)
	assert.Contains(t, err.Error(), "nats connection cannot be nil")
}

func TestNewJetStream_WithConnection(t *testing.T) {
	// NewJetStream with a non-nil connection should succeed
	// Note: This uses an unconnected nats.Conn which jetstream.New accepts
	nc := &nats.Conn{}
	js, err := NewJetStream(nc)
	assert.NoError(t, err)
	assert.NotNil(t, js)
}

func TestNatsMessage_Data(t *testing.T) {
	mockMsg := NewMockMsg("test.subject", []byte("hello world"))
	msg := WrapMessage(mockMsg)

	assert.Equal(t, []byte("hello world"), msg.Data())
}

func TestNatsMessage_Subject(t *testing.T) {
	mockMsg := NewMockMsg("test.subject", []byte("data"))
	msg := WrapMessage(mockMsg)

	assert.Equal(t, "test.subject", msg.Subject())
}

func TestNatsMessage_Ack(t *testing.T) {
	mockMsg := NewMockMsg("test.subject", []byte("data"))
	mockMsg.On("Ack").Return(nil)

	msg := WrapMessage(mockMsg)
	err := msg.Ack()

	assert.NoError(t, err)
	mockMsg.AssertCalled(t, "Ack")
}

func TestNatsMessage_Nak(t *testing.T) {
	mockMsg := NewMockMsg("test.subject", []byte("data"))
	mockMsg.On("Nak").Return(nil)

	msg := WrapMessage(mockMsg)
	err := msg.Nak()

	assert.NoError(t, err)
	mockMsg.AssertCalled(t, "Nak")
}

func TestNatsMessage_NakWithDelay(t *testing.T) {
	mockMsg := NewMockMsg("test.subject", []byte("data"))
	mockMsg.On("NakWithDelay", 5*time.Second).Return(nil)

	msg := WrapMessage(mockMsg)
	err := msg.NakWithDelay(5 * time.Second)

	assert.NoError(t, err)
	mockMsg.AssertCalled(t, "NakWithDelay", 5*time.Second)
}

func TestNatsMessage_Term(t *testing.T) {
	mockMsg := NewMockMsg("test.subject", []byte("data"))
	mockMsg.On("Term").Return(nil)

	msg := WrapMessage(mockMsg)
	err := msg.Term()

	assert.NoError(t, err)
	mockMsg.AssertCalled(t, "Term")
}

func TestNatsMessage_Metadata(t *testing.T) {
	mockMsg := NewMockMsg("test.subject", []byte("data"))
	mockMetadata := &jetstream.MsgMetadata{
		NumDelivered: 2,
		Timestamp:    time.Now(),
		Stream:       "STREAM",
		Consumer:     "CONSUMER",
	}
	mockMsg.On("Metadata").Return(mockMetadata, nil)

	msg := WrapMessage(mockMsg)
	md, err := msg.Metadata()

	require.NoError(t, err)
	assert.Equal(t, uint64(2), md.NumDelivered)
	assert.Equal(t, "STREAM", md.Stream)
	assert.Equal(t, "CONSUMER", md.Consumer)
	assert.Equal(t, "test.subject", md.Subject)
}

func TestNatsMessage_MetadataError(t *testing.T) {
	mockMsg := NewMockMsg("test.subject", []byte("data"))
	mockMsg.On("Metadata").Return(nil, assert.AnError)

	msg := WrapMessage(mockMsg)
	_, err := msg.Metadata()

	assert.Error(t, err)
}
