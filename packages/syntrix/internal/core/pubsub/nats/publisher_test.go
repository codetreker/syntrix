package nats

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/pubsub"
	pubsubconfig "github.com/codetreker/syntrix/internal/core/pubsub/config"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestPublisherUsesCallerContextAndPrefixesOnce(t *testing.T) {
	b := managedTestBinding()
	f := allocatedFake(b)
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "caller")
	calls := 0
	f.publish = func(actual context.Context, subject string, data []byte, opts []jetstream.PublishOpt) error {
		assert.Equal(t, "caller", actual.Value(contextKey{}))
		assert.Equal(t, b.SubjectPrefix+".database.collection.document", subject)
		assert.Equal(t, []byte("payload"), data)
		assert.Len(t, opts, 1)
		calls++
		return nil
	}
	opts := publisherOptions(b)
	opts.RetryAttempts = 3
	opts.OnPublish = func(subject string, err error, latency time.Duration) {
		assert.Equal(t, b.SubjectPrefix+".database.collection.document", subject)
		assert.NoError(t, err)
		assert.GreaterOrEqual(t, latency, time.Duration(0))
		calls++
	}
	publisher, err := NewPublisher(ctx, f, b, opts)
	require.NoError(t, err)
	require.NoError(t, publisher.Publish(ctx, "database.collection.document", []byte("payload")))
	assert.Equal(t, 2, calls)
	require.NoError(t, publisher.Close())
}

func TestPublisherErrorsAreSafeForCallbacks(t *testing.T) {
	b := pubsubconfig.DefaultOperatorBinding()
	f := allocatedFake(b)
	cause := errors.New("nats://root:password@private-host secret-value")
	f.publish = func(context.Context, string, []byte, []jetstream.PublishOpt) error { return cause }
	opts := publisherOptions(b)
	var callbackErr error
	opts.OnPublish = func(_ string, err error, _ time.Duration) { callbackErr = err }
	publisher, err := NewPublisher(context.Background(), f, b, opts)
	require.NoError(t, err)
	err = publisher.Publish(context.Background(), "db.col.id", nil)
	require.EqualError(t, err, "NATS publish failed")
	assert.ErrorIs(t, err, cause)
	assert.Same(t, err, callbackErr)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, publisher.Publish(ctx, "db.col.id", nil), context.Canceled)
}

func TestPublisherRejectsContradictoryOptions(t *testing.T) {
	b := pubsubconfig.DefaultOperatorBinding()
	edits := []func(*pubsub.PublisherOptions){
		func(o *pubsub.PublisherOptions) { o.StreamName = "foreign" },
		func(o *pubsub.PublisherOptions) { o.SubjectPrefix = "foreign" },
		func(o *pubsub.PublisherOptions) { o.Storage = pubsub.MemoryStorage },
		func(o *pubsub.PublisherOptions) { o.RetryAttempts = -1 },
	}
	for _, edit := range edits {
		f := allocatedFake(b)
		opts := publisherOptions(b)
		edit(&opts)
		_, err := NewPublisher(context.Background(), f, b, opts)
		require.Error(t, err)
		assert.Empty(t, f.calls)
	}
	_, err := NewPublisher(context.Background(), nil, b, publisherOptions(b))
	require.Error(t, err)
	invalid := b
	invalid.Policy.MaxBytes = 0
	_, err = NewPublisher(context.Background(), allocatedFake(b), invalid, publisherOptions(b))
	require.Error(t, err)
}

func TestConsumerRejectsContradictoryOptions(t *testing.T) {
	b := pubsubconfig.DefaultOperatorBinding()
	edits := []func(*pubsub.ConsumerOptions){
		func(o *pubsub.ConsumerOptions) { o.StreamName = "foreign" },
		func(o *pubsub.ConsumerOptions) { o.ConsumerName = "foreign" },
		func(o *pubsub.ConsumerOptions) { o.FilterSubject = "foreign.>" },
		func(o *pubsub.ConsumerOptions) { o.Storage = pubsub.MemoryStorage },
		func(o *pubsub.ConsumerOptions) { o.ChannelBufSize = 0 },
	}
	for _, edit := range edits {
		f := allocatedFake(b)
		opts := consumerOptions(b)
		edit(&opts)
		_, err := NewConsumer(context.Background(), f, b, opts)
		require.Error(t, err)
		assert.Empty(t, f.calls)
	}
	_, err := NewConsumer(context.Background(), nil, b, consumerOptions(b))
	require.Error(t, err)
}

func TestConsumerDeliversAndDrainsBeforeChannelClose(t *testing.T) {
	b := pubsubconfig.DefaultOperatorBinding()
	f := allocatedFake(b)
	f.loop = newFakeConsumeContext()
	f.loop.manualClose = true
	consumer, err := NewConsumer(context.Background(), f, b, consumerOptions(b))
	require.NoError(t, err)
	runtime := consumer.(*jetStreamConsumer)
	assert.False(t, runtime.Ready())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	messages, err := consumer.Subscribe(ctx)
	require.NoError(t, err)
	assert.True(t, runtime.Ready())
	_, err = consumer.Subscribe(ctx)
	require.Error(t, err)
	first := NewMockMsg("TRIGGERS.db.col.first", []byte("first"))
	f.handler(first)
	second := NewMockMsg("TRIGGERS.db.col.second", []byte("second"))
	second.On("Nak").Return(nil).Once()
	callbackDone := make(chan struct{})
	go func() { f.handler(second); close(callbackDone) }()
	cancel()
	select {
	case <-callbackDone:
	case <-time.After(time.Second):
		t.Fatal("full-buffer callback did not stop")
	}
	select {
	case <-f.loop.stopped:
	case <-time.After(time.Second):
		t.Fatal("consume Stop was not called")
	}
	assert.False(t, runtime.Ready())
	delivered := <-messages
	assert.Equal(t, first.Subject(), delivered.Subject())
	assert.Equal(t, first.Data(), delivered.Data())
	select {
	case _, open := <-messages:
		if !open {
			t.Fatal("message channel closed before consume Closed")
		}
	default:
	}
	f.loop.finish()
	requireChannelClosed(t, messages)
	assert.NoError(t, runtime.Err())
	second.AssertExpectations(t)
}

func TestConsumerUnexpectedCloseAndTerminalErrors(t *testing.T) {
	for _, terminal := range []error{nil, jetstream.ErrConsumerDeleted, jetstream.ErrBadRequest, jetstream.ErrConnectionClosed} {
		t.Run(errorName(terminal), func(t *testing.T) {
			b := pubsubconfig.DefaultOperatorBinding()
			f := allocatedFake(b)
			consumer, err := NewConsumer(context.Background(), f, b, consumerOptions(b))
			require.NoError(t, err)
			runtime := consumer.(*jetStreamConsumer)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			messages, err := consumer.Subscribe(ctx)
			require.NoError(t, err)
			if terminal == nil {
				f.loop.finish()
			} else {
				f.errHandler(f.loop, terminal)
			}
			requireChannelClosed(t, messages)
			assert.False(t, runtime.Ready())
			if terminal == nil {
				assert.EqualError(t, runtime.Err(), "NATS consume loop stopped unexpectedly")
			} else {
				assert.EqualError(t, runtime.Err(), "NATS consume failed")
				assert.ErrorIs(t, runtime.Err(), terminal)
			}
		})
	}
}

func TestConsumerConnectionClosesDuringSubscribeWaitsForCallback(t *testing.T) {
	b := pubsubconfig.DefaultOperatorBinding()
	f := allocatedFake(b)
	f.loop = newFakeConsumeContext()
	consumer, err := NewConsumer(context.Background(), f, b, consumerOptions(b))
	require.NoError(t, err)
	naking, release, callbackDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseCallback := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseCallback()
	first := NewMockMsg("TRIGGERS.db.col.first", []byte("first"))
	second := NewMockMsg("TRIGGERS.db.col.second", []byte("second"))
	second.On("Nak").Run(func(mock.Arguments) { close(naking); <-release }).Return(nil).Once()
	f.consumeHook = func(handler jetstream.MessageHandler, errHandler jetstream.ConsumeErrHandler) {
		handler(first)
		go func() { handler(second); close(callbackDone) }()
		errHandler(f.loop, jetstream.ErrConnectionClosed)
		select {
		case <-naking:
		case <-time.After(time.Second):
			t.Fatal("cancelled callback did not begin settlement")
		}
		// Closed can already be closed when first captured after connection loss.
		f.loop.finish()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	messages, err := consumer.Subscribe(ctx)
	require.NoError(t, err)
	require.Equal(t, first.Data(), (<-messages).Data())
	select {
	case _, open := <-messages:
		require.True(t, open, "source closed before the active callback settled")
	case <-time.After(20 * time.Millisecond):
	}
	releaseCallback()
	select {
	case <-callbackDone:
	case <-time.After(time.Second):
		t.Fatal("callback did not settle")
	}
	requireChannelClosed(t, messages)
	require.ErrorIs(t, consumer.(*jetStreamConsumer).Err(), jetstream.ErrConnectionClosed)
	second.AssertExpectations(t)
}

func TestConsumerStartupFailureCanRetry(t *testing.T) {
	b := pubsubconfig.DefaultOperatorBinding()
	f := allocatedFake(b)
	consumer, err := NewConsumer(context.Background(), f, b, consumerOptions(b))
	require.NoError(t, err)
	runtime := consumer.(*jetStreamConsumer)
	cause := errors.New("credential-bearing startup error")
	f.consumeErr = cause
	_, err = consumer.Subscribe(context.Background())
	require.EqualError(t, err, "NATS consume startup failed")
	assert.ErrorIs(t, err, cause)
	assert.False(t, runtime.Ready())
	f.consumeErr = nil
	f.consumerInfoErr = cause
	_, err = consumer.Subscribe(context.Background())
	require.ErrorIs(t, err, cause)
	f.consumerInfoErr = nil
	ctx, cancel := context.WithCancel(context.Background())
	messages, err := consumer.Subscribe(ctx)
	require.NoError(t, err)
	f.errHandler(f.loop, jetstream.ErrNoHeartbeat)
	assert.True(t, runtime.Ready())
	assert.NoError(t, runtime.Err())
	cancel()
	requireChannelClosed(t, messages)
}

func TestMemoryBrokerOptions(t *testing.T) {
	b := pubsubconfig.DefaultOperatorBinding()
	b.Policy.Storage = "memory"
	f := allocatedFake(b)
	_, err := NewPublisher(context.Background(), f, b, publisherOptions(b))
	require.NoError(t, err)
	_, err = NewConsumer(context.Background(), f, b, consumerOptions(b))
	require.NoError(t, err)
}

func requireChannelClosed(t *testing.T, messages <-chan pubsub.Message) {
	t.Helper()
	select {
	case _, open := <-messages:
		require.False(t, open)
	case <-time.After(time.Second):
		t.Fatal("message channel did not close")
	}
}
