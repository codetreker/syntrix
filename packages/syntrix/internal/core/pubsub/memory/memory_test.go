package memory

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codetreker/syntrix/internal/core/pubsub"
)

// =============================================================================
// Engine Tests
// =============================================================================

func TestEngine_New(t *testing.T) {
	engine := New()
	require.NotNil(t, engine)
	assert.False(t, engine.IsClosed())
	require.NoError(t, engine.Close())
}

func TestEngine_NewPublisherConsumer(t *testing.T) {
	engine := New()
	defer engine.Close()

	pub, err := engine.NewPublisher(pubsub.PublisherOptions{})
	require.NoError(t, err)
	require.NotNil(t, pub)

	consumer, err := engine.NewConsumer(pubsub.ConsumerOptions{})
	require.NoError(t, err)
	require.NotNil(t, consumer)
}

func TestEngine_Close(t *testing.T) {
	engine := New()
	require.NoError(t, engine.Close())
	assert.True(t, engine.IsClosed())
}

func TestEngine_DoubleClose(t *testing.T) {
	engine := New()
	require.NoError(t, engine.Close())
	require.NoError(t, engine.Close()) // Idempotent
}

func TestEngine_NewPublisherAfterClose(t *testing.T) {
	engine := New()
	require.NoError(t, engine.Close())

	_, err := engine.NewPublisher(pubsub.PublisherOptions{})
	assert.ErrorIs(t, err, ErrEngineClosed)
}

func TestEngine_NewConsumerAfterClose(t *testing.T) {
	engine := New()
	require.NoError(t, engine.Close())

	_, err := engine.NewConsumer(pubsub.ConsumerOptions{})
	assert.ErrorIs(t, err, ErrEngineClosed)
}

// =============================================================================
// Publish/Subscribe Tests
// =============================================================================

func TestBroker_PublishSubscribe(t *testing.T) {
	engine := New()
	defer engine.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	consumer, err := engine.NewConsumer(pubsub.ConsumerOptions{
		FilterSubject: "test.>",
	})
	require.NoError(t, err)

	msgCh, err := consumer.Subscribe(ctx)
	require.NoError(t, err)

	pub, err := engine.NewPublisher(pubsub.PublisherOptions{})
	require.NoError(t, err)

	// Publish
	err = pub.Publish(ctx, "test.foo", []byte("hello"))
	require.NoError(t, err)

	// Receive
	select {
	case msg := <-msgCh:
		assert.Equal(t, "test.foo", msg.Subject())
		assert.Equal(t, []byte("hello"), msg.Data())
		require.NoError(t, msg.Ack())
	case <-ctx.Done():
		t.Fatal("timeout waiting for message")
	}
}

func TestPublisher_SubjectPrefix(t *testing.T) {
	engine := New()
	defer engine.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	consumer, err := engine.NewConsumer(pubsub.ConsumerOptions{
		FilterSubject: "TRIGGERS.>",
	})
	require.NoError(t, err)

	msgCh, err := consumer.Subscribe(ctx)
	require.NoError(t, err)

	pub, err := engine.NewPublisher(pubsub.PublisherOptions{
		SubjectPrefix: "TRIGGERS",
	})
	require.NoError(t, err)

	// Publish with prefix
	err = pub.Publish(ctx, "db1.users", []byte("data"))
	require.NoError(t, err)

	// Receive
	select {
	case msg := <-msgCh:
		assert.Equal(t, "TRIGGERS.db1.users", msg.Subject())
		require.NoError(t, msg.Ack())
	case <-ctx.Done():
		t.Fatal("timeout waiting for message")
	}
}

func TestConsumer_FilterSubject(t *testing.T) {
	engine := New()
	defer engine.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Subscribe to specific pattern
	consumer, err := engine.NewConsumer(pubsub.ConsumerOptions{
		FilterSubject: "foo.bar.*",
	})
	require.NoError(t, err)

	msgCh, err := consumer.Subscribe(ctx)
	require.NoError(t, err)

	pub, err := engine.NewPublisher(pubsub.PublisherOptions{})
	require.NoError(t, err)

	// Matching message
	err = pub.Publish(ctx, "foo.bar.baz", []byte("match"))
	require.NoError(t, err)

	// Non-matching message (should not receive)
	err = pub.Publish(ctx, "foo.baz.qux", []byte("no-match"))
	require.NoError(t, err)

	// Receive only matching
	select {
	case msg := <-msgCh:
		assert.Equal(t, "foo.bar.baz", msg.Subject())
		assert.Equal(t, []byte("match"), msg.Data())
		require.NoError(t, msg.Ack())
	case <-ctx.Done():
		t.Fatal("timeout waiting for message")
	}

	// Verify no more messages
	select {
	case msg := <-msgCh:
		t.Fatalf("unexpected message: %s", msg.Subject())
	case <-time.After(100 * time.Millisecond):
		// Expected
	}
}

func TestConsumer_DefaultPattern(t *testing.T) {
	engine := New()
	defer engine.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// No FilterSubject, use StreamName
	consumer, err := engine.NewConsumer(pubsub.ConsumerOptions{
		StreamName: "STREAM",
	})
	require.NoError(t, err)

	msgCh, err := consumer.Subscribe(ctx)
	require.NoError(t, err)

	pub, err := engine.NewPublisher(pubsub.PublisherOptions{})
	require.NoError(t, err)

	err = pub.Publish(ctx, "STREAM.foo.bar", []byte("data"))
	require.NoError(t, err)

	select {
	case msg := <-msgCh:
		assert.Equal(t, "STREAM.foo.bar", msg.Subject())
	case <-ctx.Done():
		t.Fatal("timeout waiting for message")
	}
}

func TestBroker_PublishNoSubscribers(t *testing.T) {
	engine := New()
	defer engine.Close()

	ctx := context.Background()
	pub, err := engine.NewPublisher(pubsub.PublisherOptions{})
	require.NoError(t, err)

	// Should not error, just drop
	err = pub.Publish(ctx, "no.subscribers", []byte("data"))
	require.NoError(t, err)
}

func TestBroker_PublishAfterClose(t *testing.T) {
	engine := New()
	pub, err := engine.NewPublisher(pubsub.PublisherOptions{})
	require.NoError(t, err)

	require.NoError(t, engine.Close())

	err = pub.Publish(context.Background(), "test", []byte("data"))
	assert.ErrorIs(t, err, ErrEngineClosed)
}

func TestBroker_SubscribeAfterClose(t *testing.T) {
	engine := New()
	consumer, err := engine.NewConsumer(pubsub.ConsumerOptions{})
	require.NoError(t, err)

	require.NoError(t, engine.Close())

	_, err = consumer.Subscribe(context.Background())
	assert.ErrorIs(t, err, ErrEngineClosed)
}

func TestBroker_DuplicateSubscribe(t *testing.T) {
	engine := New()
	defer engine.Close()

	ctx := context.Background()

	consumer1, err := engine.NewConsumer(pubsub.ConsumerOptions{
		FilterSubject: "test.>",
	})
	require.NoError(t, err)

	_, err = consumer1.Subscribe(ctx)
	require.NoError(t, err)

	consumer2, err := engine.NewConsumer(pubsub.ConsumerOptions{
		FilterSubject: "test.>",
	})
	require.NoError(t, err)

	_, err = consumer2.Subscribe(ctx)
	assert.ErrorIs(t, err, ErrPatternSubscribed)
}

// =============================================================================
// Message Acknowledgment Tests
// =============================================================================

func TestMessage_Ack(t *testing.T) {
	engine := New()
	defer engine.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	consumer, _ := engine.NewConsumer(pubsub.ConsumerOptions{FilterSubject: ">"})
	msgCh, _ := consumer.Subscribe(ctx)

	pub, _ := engine.NewPublisher(pubsub.PublisherOptions{})
	pub.Publish(ctx, "test", []byte("data"))

	msg := <-msgCh
	require.NoError(t, msg.Ack())
}

func TestMessage_DoubleAck(t *testing.T) {
	engine := New()
	defer engine.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	consumer, _ := engine.NewConsumer(pubsub.ConsumerOptions{FilterSubject: ">"})
	msgCh, _ := consumer.Subscribe(ctx)

	pub, _ := engine.NewPublisher(pubsub.PublisherOptions{})
	pub.Publish(ctx, "test", []byte("data"))

	msg := <-msgCh
	require.NoError(t, msg.Ack())
	require.NoError(t, msg.Ack()) // Idempotent
}

func TestMessage_Nak(t *testing.T) {
	engine := New()
	defer engine.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	consumer, _ := engine.NewConsumer(pubsub.ConsumerOptions{FilterSubject: ">"})
	msgCh, _ := consumer.Subscribe(ctx)

	pub, _ := engine.NewPublisher(pubsub.PublisherOptions{})
	pub.Publish(ctx, "test", []byte("data"))

	// First delivery
	msg := <-msgCh
	md, _ := msg.Metadata()
	assert.Equal(t, uint64(1), md.NumDelivered)

	// Nak to requeue
	require.NoError(t, msg.Nak())

	// Second delivery
	msg2 := <-msgCh
	md2, _ := msg2.Metadata()
	assert.Equal(t, uint64(2), md2.NumDelivered)
	require.NoError(t, msg2.Ack())
}

func TestMessage_NakWithDelay(t *testing.T) {
	engine := New()
	defer engine.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	consumer, _ := engine.NewConsumer(pubsub.ConsumerOptions{FilterSubject: ">"})
	msgCh, _ := consumer.Subscribe(ctx)

	pub, _ := engine.NewPublisher(pubsub.PublisherOptions{})
	pub.Publish(ctx, "test", []byte("data"))

	msg := <-msgCh
	start := time.Now()
	require.NoError(t, msg.NakWithDelay(100*time.Millisecond))

	// Should receive after delay
	msg2 := <-msgCh
	elapsed := time.Since(start)
	assert.GreaterOrEqual(t, elapsed, 100*time.Millisecond)

	md, _ := msg2.Metadata()
	assert.Equal(t, uint64(2), md.NumDelivered)
	require.NoError(t, msg2.Ack())
}

func TestMessage_Term(t *testing.T) {
	engine := New()
	defer engine.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	consumer, _ := engine.NewConsumer(pubsub.ConsumerOptions{FilterSubject: ">"})
	msgCh, _ := consumer.Subscribe(ctx)

	pub, _ := engine.NewPublisher(pubsub.PublisherOptions{})
	pub.Publish(ctx, "test", []byte("data"))

	msg := <-msgCh
	require.NoError(t, msg.Term())

	// Should not receive again (no redelivery)
	select {
	case <-msgCh:
		t.Fatal("should not receive terminated message")
	case <-time.After(100 * time.Millisecond):
		// Expected
	}
}

func TestMessage_AckAfterNak(t *testing.T) {
	engine := New()
	defer engine.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	consumer, _ := engine.NewConsumer(pubsub.ConsumerOptions{FilterSubject: ">"})
	msgCh, _ := consumer.Subscribe(ctx)

	pub, _ := engine.NewPublisher(pubsub.PublisherOptions{})
	pub.Publish(ctx, "test", []byte("data"))

	msg := <-msgCh
	require.NoError(t, msg.Nak())
	require.NoError(t, msg.Ack()) // Idempotent, no effect after Nak
}

func TestMessage_Metadata(t *testing.T) {
	engine := New()
	defer engine.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	consumer, _ := engine.NewConsumer(pubsub.ConsumerOptions{FilterSubject: ">"})
	msgCh, _ := consumer.Subscribe(ctx)

	pub, _ := engine.NewPublisher(pubsub.PublisherOptions{})
	pub.Publish(ctx, "test.subject", []byte("data"))

	msg := <-msgCh
	md, err := msg.Metadata()
	require.NoError(t, err)

	assert.Equal(t, uint64(1), md.NumDelivered)
	assert.Equal(t, "test.subject", md.Subject)
	assert.False(t, md.Timestamp.IsZero())
}

// =============================================================================
// Context Cancellation Tests
// =============================================================================

func TestConsumer_ContextCancel(t *testing.T) {
	engine := New()
	defer engine.Close()

	ctx, cancel := context.WithCancel(context.Background())

	consumer, _ := engine.NewConsumer(pubsub.ConsumerOptions{FilterSubject: ">"})
	msgCh, err := consumer.Subscribe(ctx)
	require.NoError(t, err)

	// Cancel context
	cancel()

	// Channel should be closed eventually
	select {
	case _, ok := <-msgCh:
		assert.False(t, ok, "channel should be closed")
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for channel close")
	}
}

func TestPublisher_ContextCancel(t *testing.T) {
	engine := New()
	defer engine.Close()

	// Use separate contexts for consumer and publisher
	consumerCtx := context.Background()
	pubCtx, cancel := context.WithCancel(context.Background())

	// Create a consumer with small buffer to cause blocking
	consumer, _ := engine.NewConsumer(pubsub.ConsumerOptions{
		FilterSubject:  ">",
		ChannelBufSize: 1,
	})
	_, _ = consumer.Subscribe(consumerCtx)

	pub, _ := engine.NewPublisher(pubsub.PublisherOptions{})

	// Fill buffer
	pub.Publish(pubCtx, "test", []byte("1"))

	// Cancel publisher context and try to publish (should block then fail)
	cancel()

	err := pub.Publish(pubCtx, "test", []byte("2"))
	assert.ErrorIs(t, err, context.Canceled)
}

// =============================================================================
// Metrics Tests
// =============================================================================

func TestPublisher_OnPublishCallback(t *testing.T) {
	engine := New()
	defer engine.Close()

	ctx := context.Background()

	var called bool
	var capturedSubject string
	var capturedErr error
	var capturedLatency time.Duration

	pub, _ := engine.NewPublisher(pubsub.PublisherOptions{
		SubjectPrefix: "PREFIX",
		OnPublish: func(subject string, err error, latency time.Duration) {
			called = true
			capturedSubject = subject
			capturedErr = err
			capturedLatency = latency
		},
	})

	err := pub.Publish(ctx, "test", []byte("data"))
	require.NoError(t, err)

	assert.True(t, called)
	assert.Equal(t, "PREFIX.test", capturedSubject)
	assert.NoError(t, capturedErr)
	assert.Greater(t, capturedLatency, time.Duration(0))
}

func TestPublisher_OnPublishError(t *testing.T) {
	engine := New()

	var capturedErr error
	pub, _ := engine.NewPublisher(pubsub.PublisherOptions{
		OnPublish: func(subject string, err error, latency time.Duration) {
			capturedErr = err
		},
	})

	// Close engine to trigger error
	engine.Close()

	pub.Publish(context.Background(), "test", []byte("data"))
	assert.ErrorIs(t, capturedErr, ErrEngineClosed)
}

// =============================================================================
// Concurrency Tests
// =============================================================================

func TestConcurrent_PublishSubscribe(t *testing.T) {
	engine := New()
	defer engine.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	consumer, _ := engine.NewConsumer(pubsub.ConsumerOptions{
		FilterSubject:  ">",
		ChannelBufSize: 1000,
	})
	msgCh, _ := consumer.Subscribe(ctx)

	pub, _ := engine.NewPublisher(pubsub.PublisherOptions{})

	const numMessages = 100
	var wg sync.WaitGroup

	// Concurrent publishers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numMessages/10; j++ {
				pub.Publish(ctx, "test", []byte("data"))
			}
		}(i)
	}

	// Collect messages
	received := 0
	done := make(chan struct{})
	go func() {
		for range msgCh {
			received++
			if received >= numMessages {
				close(done)
				return
			}
		}
	}()

	wg.Wait()

	select {
	case <-done:
		assert.Equal(t, numMessages, received)
	case <-ctx.Done():
		t.Fatalf("timeout, received %d/%d messages", received, numMessages)
	}
}

func TestConcurrent_MultiplePublishers(t *testing.T) {
	engine := New()
	defer engine.Close()

	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pub, err := engine.NewPublisher(pubsub.PublisherOptions{})
			require.NoError(t, err)
			for j := 0; j < 10; j++ {
				pub.Publish(ctx, "test", []byte("data"))
			}
		}()
	}
	wg.Wait()
}

func TestConcurrent_CloseWhilePublishing(t *testing.T) {
	engine := New()

	ctx := context.Background()
	pub, _ := engine.NewPublisher(pubsub.PublisherOptions{})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			pub.Publish(ctx, "test", []byte("data"))
		}
	}()

	// Close while publishing
	time.Sleep(time.Millisecond)
	engine.Close()

	wg.Wait()
}

// =============================================================================
// NakWithDelay Edge Cases
// =============================================================================

func TestNakWithDelay_EngineClosed(t *testing.T) {
	engine := New()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	consumer, _ := engine.NewConsumer(pubsub.ConsumerOptions{FilterSubject: ">"})
	msgCh, _ := consumer.Subscribe(ctx)

	pub, _ := engine.NewPublisher(pubsub.PublisherOptions{})
	pub.Publish(ctx, "test", []byte("data"))

	msg := <-msgCh

	// Close engine before delay fires
	engine.Close()

	// Should not panic
	require.NoError(t, msg.NakWithDelay(10*time.Millisecond))

	// Wait for AfterFunc to fire
	time.Sleep(50 * time.Millisecond)
}

func TestNakWithDelay_ContextCancelled(t *testing.T) {
	engine := New()
	defer engine.Close()

	ctx, cancel := context.WithCancel(context.Background())

	consumer, _ := engine.NewConsumer(pubsub.ConsumerOptions{FilterSubject: ">"})
	msgCh, _ := consumer.Subscribe(ctx)

	pub, _ := engine.NewPublisher(pubsub.PublisherOptions{})
	pub.Publish(ctx, "test", []byte("data"))

	msg := <-msgCh

	// Cancel context before delay fires
	cancel()

	// Should not panic
	require.NoError(t, msg.NakWithDelay(10*time.Millisecond))

	// Wait for AfterFunc to fire
	time.Sleep(50 * time.Millisecond)
}

// =============================================================================
// Publisher Close Test
// =============================================================================

func TestPublisher_Close(t *testing.T) {
	engine := New()
	defer engine.Close()

	pub, err := engine.NewPublisher(pubsub.PublisherOptions{})
	require.NoError(t, err)

	// Close the publisher
	err = pub.Close()
	require.NoError(t, err)

	// After close, publish should fail
	ctx := context.Background()
	err = pub.Publish(ctx, "test", []byte("data"))
	assert.Equal(t, ErrEngineClosed, err)
}

// =============================================================================
// Nak Channel Full Test
// =============================================================================

func TestNak_ChannelFull(t *testing.T) {
	engine := New()
	defer engine.Close()

	// Use a context that won't timeout during the test
	ctx := context.Background()

	// Create consumer with buffer size of 2
	consumer, _ := engine.NewConsumer(pubsub.ConsumerOptions{
		FilterSubject:  ">",
		ChannelBufSize: 2,
	})
	msgCh, _ := consumer.Subscribe(ctx)

	pub, _ := engine.NewPublisher(pubsub.PublisherOptions{})

	// Publish 2 messages to fill the buffer
	pub.Publish(ctx, "test", []byte("msg1"))
	pub.Publish(ctx, "test", []byte("msg2"))

	// Receive one message
	msg := <-msgCh

	// Now buffer has 1 slot. Publish another to fill it again
	pub.Publish(ctx, "test", []byte("msg3"))

	// Now buffer is full (msg2, msg3). Try to Nak msg1.
	// This should not block - it should drop if channel is full.
	done := make(chan struct{})
	go func() {
		msg.Nak()
		close(done)
	}()

	// If Nak blocks, this will timeout
	select {
	case <-done:
		// Success - Nak completed without blocking
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Nak blocked when channel was full")
	}

	// Verify we can still receive remaining messages
	<-msgCh // msg2
	<-msgCh // msg3
}
