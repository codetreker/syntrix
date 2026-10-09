package testing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/pubsub"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMockPublisher_Publish(t *testing.T) {
	pub := NewMockPublisher()

	err := pub.Publish(context.Background(), "test.subject", []byte("hello"))
	require.NoError(t, err)

	msgs := pub.Messages()
	require.Len(t, msgs, 1)
	assert.Equal(t, "test.subject", msgs[0].Subject)
	assert.Equal(t, []byte("hello"), msgs[0].Data)
}

func TestMockPublisher_PublishMultiple(t *testing.T) {
	pub := NewMockPublisher()

	pub.Publish(context.Background(), "s1", []byte("d1"))
	pub.Publish(context.Background(), "s2", []byte("d2"))

	msgs := pub.Messages()
	require.Len(t, msgs, 2)
	assert.Equal(t, "s1", msgs[0].Subject)
	assert.Equal(t, "s2", msgs[1].Subject)
}

func TestMockPublisher_Error(t *testing.T) {
	pub := NewMockPublisher()
	expectedErr := errors.New("publish failed")
	pub.SetError(expectedErr)

	err := pub.Publish(context.Background(), "test", []byte("data"))
	assert.Equal(t, expectedErr, err)
	assert.Empty(t, pub.Messages())
}

func TestMockPublisher_Close(t *testing.T) {
	pub := NewMockPublisher()
	assert.False(t, pub.IsClosed())

	err := pub.Close()
	require.NoError(t, err)
	assert.True(t, pub.IsClosed())
}

func TestMockPublisher_Reset(t *testing.T) {
	pub := NewMockPublisher()
	pub.Publish(context.Background(), "test", []byte("data"))
	pub.SetError(errors.New("err"))
	pub.Close()

	pub.Reset()

	assert.Empty(t, pub.Messages())
	assert.False(t, pub.IsClosed())
	err := pub.Publish(context.Background(), "test", []byte("data"))
	require.NoError(t, err)
}

func TestMockMessage_Data(t *testing.T) {
	msg := NewMockMessage("test.subject", []byte("payload"))

	assert.Equal(t, []byte("payload"), msg.Data())
	assert.Equal(t, "test.subject", msg.Subject())
}

func TestMockMessage_Ack(t *testing.T) {
	msg := NewMockMessage("test", []byte("data"))
	assert.False(t, msg.IsAcked())

	err := msg.Ack()
	require.NoError(t, err)
	assert.True(t, msg.IsAcked())
}

func TestMockMessage_Nak(t *testing.T) {
	msg := NewMockMessage("test", []byte("data"))
	assert.False(t, msg.IsNaked())

	err := msg.Nak()
	require.NoError(t, err)
	assert.True(t, msg.IsNaked())
}

func TestMockMessage_NakWithDelay(t *testing.T) {
	msg := NewMockMessage("test", []byte("data"))

	err := msg.NakWithDelay(5 * time.Second)
	require.NoError(t, err)
	assert.True(t, msg.IsNaked())
	assert.Equal(t, 5*time.Second, msg.NakDelay())
}

func TestMockMessage_Term(t *testing.T) {
	msg := NewMockMessage("test", []byte("data"))
	assert.False(t, msg.IsTermed())

	err := msg.Term()
	require.NoError(t, err)
	assert.True(t, msg.IsTermed())
}

func TestMockMessage_Metadata(t *testing.T) {
	msg := NewMockMessage("test", []byte("data"))

	md, err := msg.Metadata()
	require.NoError(t, err)
	assert.Equal(t, uint64(1), md.NumDelivered)
	assert.Equal(t, "test", md.Subject)
}

func TestMockMessage_SetMetadata(t *testing.T) {
	msg := NewMockMessage("test", []byte("data"))
	msg.SetMetadata(pubsub.MessageMetadata{
		NumDelivered: 5,
		Subject:      "custom",
		Stream:       "mystream",
	})

	md, err := msg.Metadata()
	require.NoError(t, err)
	assert.Equal(t, uint64(5), md.NumDelivered)
	assert.Equal(t, "custom", md.Subject)
	assert.Equal(t, "mystream", md.Stream)
}

func TestMockMessage_Errors(t *testing.T) {
	msg := NewMockMessage("test", []byte("data"))
	msg.SetErrors(
		errors.New("ack err"),
		errors.New("nak err"),
		errors.New("term err"),
		errors.New("metadata err"),
	)

	err := msg.Ack()
	assert.EqualError(t, err, "ack err")

	err = msg.Nak()
	assert.EqualError(t, err, "nak err")

	err = msg.Term()
	assert.EqualError(t, err, "term err")

	_, err = msg.Metadata()
	assert.EqualError(t, err, "metadata err")
}

func TestMockConsumer_Subscribe(t *testing.T) {
	consumer := NewMockConsumer()
	assert.False(t, consumer.IsStarted())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	msgCh, err := consumer.Subscribe(ctx)
	require.NoError(t, err)
	assert.NotNil(t, msgCh)
	assert.True(t, consumer.IsStarted())
}

func TestMockConsumer_Send(t *testing.T) {
	consumer := NewMockConsumer()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	msgCh, err := consumer.Subscribe(ctx)
	require.NoError(t, err)

	msg := NewMockMessage("test", []byte("hello"))
	consumer.Send(msg)

	select {
	case received := <-msgCh:
		assert.Equal(t, []byte("hello"), received.Data())
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Did not receive message")
	}
}

func TestMockConsumer_Error(t *testing.T) {
	consumer := NewMockConsumer()
	expectedErr := errors.New("subscribe failed")
	consumer.SetError(expectedErr)

	_, err := consumer.Subscribe(context.Background())
	assert.Equal(t, expectedErr, err)
}

func TestMockConsumer_ChannelClosedOnCancel(t *testing.T) {
	consumer := NewMockConsumer()
	ctx, cancel := context.WithCancel(context.Background())

	msgCh, err := consumer.Subscribe(ctx)
	require.NoError(t, err)

	cancel()

	select {
	case _, ok := <-msgCh:
		assert.False(t, ok, "Channel should be closed")
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Channel did not close in time")
	}
}

func TestMockProvider_NewPublisher(t *testing.T) {
	provider := NewMockProvider()

	opts := pubsub.PublisherOptions{
		StreamName:    "test-stream",
		SubjectPrefix: "test.prefix",
	}

	pub, err := provider.NewPublisher(opts)
	require.NoError(t, err)
	assert.NotNil(t, pub)

	// Verify opts were recorded
	recordedOpts := provider.PublisherOpts()
	require.Len(t, recordedOpts, 1)
	assert.Equal(t, "test-stream", recordedOpts[0].StreamName)
	assert.Equal(t, "test.prefix", recordedOpts[0].SubjectPrefix)
}

func TestMockProvider_NewConsumer(t *testing.T) {
	provider := NewMockProvider()

	opts := pubsub.ConsumerOptions{
		StreamName:    "test-stream",
		FilterSubject: "test.>",
	}

	cons, err := provider.NewConsumer(opts)
	require.NoError(t, err)
	assert.NotNil(t, cons)

	// Verify opts were recorded
	recordedOpts := provider.ConsumerOpts()
	require.Len(t, recordedOpts, 1)
	assert.Equal(t, "test-stream", recordedOpts[0].StreamName)
	assert.Equal(t, "test.>", recordedOpts[0].FilterSubject)
}

func TestMockProvider_PublisherError(t *testing.T) {
	provider := NewMockProvider()
	expectedErr := errors.New("publisher creation failed")
	provider.SetPublisherError(expectedErr)

	_, err := provider.NewPublisher(pubsub.PublisherOptions{})
	assert.Equal(t, expectedErr, err)
}

func TestMockProvider_ConsumerError(t *testing.T) {
	provider := NewMockProvider()
	expectedErr := errors.New("consumer creation failed")
	provider.SetConsumerError(expectedErr)

	_, err := provider.NewConsumer(pubsub.ConsumerOptions{})
	assert.Equal(t, expectedErr, err)
}

func TestMockProvider_Close(t *testing.T) {
	provider := NewMockProvider()
	assert.False(t, provider.IsClosed())

	err := provider.Close()
	require.NoError(t, err)
	assert.True(t, provider.IsClosed())
}

func TestMockProvider_CloseError(t *testing.T) {
	provider := NewMockProvider()
	expectedErr := errors.New("close failed")
	provider.SetCloseError(expectedErr)

	err := provider.Close()
	assert.Equal(t, expectedErr, err)
	assert.True(t, provider.IsClosed())
}

func TestMockProvider_SetPublisher(t *testing.T) {
	provider := NewMockProvider()
	customPub := NewMockPublisher()
	customPub.Publish(context.Background(), "preset", []byte("data"))

	provider.SetPublisher(customPub)

	pub, err := provider.NewPublisher(pubsub.PublisherOptions{})
	require.NoError(t, err)

	// Verify it's the custom publisher
	mockPub := pub.(*MockPublisher)
	msgs := mockPub.Messages()
	require.Len(t, msgs, 1)
	assert.Equal(t, "preset", msgs[0].Subject)
}

func TestMockProvider_SetConsumer(t *testing.T) {
	provider := NewMockProvider()
	customCons := NewMockConsumer()

	provider.SetConsumer(customCons)

	cons, err := provider.NewConsumer(pubsub.ConsumerOptions{})
	require.NoError(t, err)
	assert.Equal(t, customCons, cons)
}
