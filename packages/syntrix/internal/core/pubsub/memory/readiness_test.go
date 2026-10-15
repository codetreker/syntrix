package memory

import (
	"context"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/pubsub"
	"github.com/stretchr/testify/require"
)

func TestMemoryConsumerReadinessLifecycle(t *testing.T) {
	for _, stopEngine := range []bool{false, true} {
		name := "context cancellation"
		if stopEngine {
			name = "engine closure"
		}
		t.Run(name, func(t *testing.T) {
			engine := New()
			defer engine.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			consumer, err := engine.NewConsumer(ctx, pubsub.ConsumerOptions{FilterSubject: "test.>", ChannelBufSize: 1})
			require.NoError(t, err)
			state := consumer.(pubsub.ConsumerState)
			require.False(t, state.Ready())
			require.NoError(t, state.Err())
			messages, err := consumer.Subscribe(ctx)
			require.NoError(t, err)
			require.True(t, state.Ready())
			_, err = consumer.Subscribe(ctx)
			require.ErrorIs(t, err, ErrPatternSubscribed)
			require.True(t, state.Ready())
			if stopEngine {
				require.NoError(t, engine.Close())
			} else {
				cancel()
			}
			require.Eventually(t, func() bool { return !state.Ready() }, time.Second, time.Millisecond)
			select {
			case _, open := <-messages:
				require.False(t, open)
			case <-time.After(time.Second):
				t.Fatal("subscription did not close")
			}
			if stopEngine {
				require.ErrorIs(t, state.Err(), ErrEngineClosed)
			} else {
				require.NoError(t, state.Err())
			}
		})
	}
}
func TestMemoryConsumerCanceledSubscribe(t *testing.T) {
	engine := New()
	defer engine.Close()
	consumer, err := engine.NewConsumer(context.Background(), pubsub.ConsumerOptions{})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = consumer.Subscribe(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, consumer.(pubsub.ConsumerState).Ready())
}
func TestEngineCloseUnblocksFullPublisher(t *testing.T) {
	engine := New()
	defer engine.Close()
	consumer, err := engine.NewConsumer(context.Background(), pubsub.ConsumerOptions{ChannelBufSize: 1})
	require.NoError(t, err)
	_, err = consumer.Subscribe(context.Background())
	require.NoError(t, err)
	publisher, err := engine.NewPublisher(context.Background(), pubsub.PublisherOptions{})
	require.NoError(t, err)
	require.NoError(t, publisher.Publish(context.Background(), "test.subject", []byte("first")))
	result := make(chan error, 1)
	go func() { result <- publisher.Publish(context.Background(), "test.subject", []byte("second")) }()
	select {
	case <-result:
		t.Fatal("publisher did not block")
	case <-time.After(20 * time.Millisecond):
	}
	closed := make(chan struct{})
	go func() { _ = engine.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("engine close blocked")
	}
	select {
	case err := <-result:
		require.ErrorIs(t, err, ErrEngineClosed)
	case <-time.After(time.Second):
		t.Fatal("publisher did not unblock")
	}
}
