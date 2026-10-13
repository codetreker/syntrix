package nats

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	pubsubconfig "github.com/codetreker/syntrix/internal/core/pubsub/config"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func managedTestBinding() pubsubconfig.BrokerBinding {
	b := pubsubconfig.DefaultOperatorBinding()
	b.ResourceMode = "managed"
	b.StreamName = "SYNTRIX_" + b.Ownership.InstanceID + "_TRIGGERS"
	b.SubjectPrefix = b.StreamName
	b.ConsumerName = b.StreamName + "_DELIVERY"
	return b
}

func TestManagedAttachmentNeverMutates(t *testing.T) {
	for _, missing := range []string{"none", "stream", "consumer"} {
		t.Run(missing, func(t *testing.T) {
			b := managedTestBinding()
			f := allocatedFake(b)
			if missing == "stream" {
				f.stream = nil
			}
			if missing == "consumer" {
				f.consumer = nil
			}
			p, err := NewProvider("nats://unused", b)
			require.NoError(t, err)
			p.js = f
			_, publisherErr := p.NewPublisher(context.Background(), publisherOptions(b))
			_, consumerErr := p.NewConsumer(context.Background(), consumerOptions(b))
			checkErr := p.Check(context.Background())
			if missing == "none" {
				require.NoError(t, publisherErr)
				require.NoError(t, consumerErr)
				require.NoError(t, checkErr)
			} else {
				require.Error(t, publisherErr)
				require.Error(t, consumerErr)
				require.Error(t, checkErr)
			}
			assert.NotContains(t, f.calls, "stream.create")
			assert.NotContains(t, f.calls, "consumer.create")
		})
	}
}

func TestOperatorAttachmentCreatesCompleteSpecification(t *testing.T) {
	for _, createErr := range []error{nil, jetstream.ErrStreamNameAlreadyInUse, context.DeadlineExceeded} {
		t.Run(errorName(createErr), func(t *testing.T) {
			b := pubsubconfig.DefaultOperatorBinding()
			f := &fakeJetStream{createStreamErr: createErr, createConsumerErr: createErr, applyOnCreateError: true}
			_, err := NewPublisher(context.Background(), f, b, publisherOptions(b))
			require.NoError(t, err)
			spec, err := b.Specification()
			require.NoError(t, err)
			assert.Equal(t, spec.Stream, *f.stream)
			assert.Equal(t, spec.Consumer, *f.consumer)
			assert.Equal(t, []string{"stream.lookup", "stream.create", "stream.lookup", "stream.info", "consumer.lookup", "consumer.create", "consumer.lookup", "consumer.info"}, f.calls)
		})
	}
}

func errorName(err error) string {
	if err == nil {
		return "success"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "lost_response"
	}
	return "concurrent_creation"
}

func TestOperatorAttachmentConflictingStreamStopsBeforeConsumer(t *testing.T) {
	b := pubsubconfig.DefaultOperatorBinding()
	f := allocatedFake(b)
	f.stream.MaxBytes++
	f.consumer = nil
	_, err := NewConsumer(context.Background(), f, b, consumerOptions(b))
	require.Error(t, err)
	assert.Equal(t, []string{"stream.lookup", "stream.info"}, f.calls)
}

func TestAttachmentErrorsPreserveCauseAndCanRetry(t *testing.T) {
	for _, phase := range []string{"stream.lookup", "stream.info", "consumer.lookup", "consumer.info", "stream.create", "consumer.create"} {
		t.Run(phase, func(t *testing.T) {
			b := pubsubconfig.DefaultOperatorBinding()
			f := allocatedFake(b)
			cause := errors.New("nats://admin:password@private-host:4222 secret-value")
			switch phase {
			case "stream.lookup":
				f.streamLookup = func(context.Context) error { return cause }
			case "stream.info":
				f.streamInfoErr = cause
			case "consumer.lookup":
				f.consumerLookupErr = cause
			case "consumer.info":
				f.consumerInfoErr = cause
			case "stream.create":
				f.stream = nil
				f.createStreamErr = cause
			case "consumer.create":
				f.consumer = nil
				f.createConsumerErr = cause
			}
			_, err := NewPublisher(context.Background(), f, b, publisherOptions(b))
			require.Error(t, err)
			assert.ErrorIs(t, err, cause)
			assert.NotContains(t, err.Error(), "password")
			assert.NotContains(t, err.Error(), "private-host")
			assert.NotContains(t, err.Error(), "secret-value")
			f.streamLookup, f.streamInfoErr, f.consumerLookupErr, f.consumerInfoErr = nil, nil, nil, nil
			f.createStreamErr, f.createConsumerErr = nil, nil
			_, err = NewPublisher(context.Background(), f, b, publisherOptions(b))
			require.NoError(t, err)
		})
	}
}

func TestConcurrentAttachmentKeepsCallerCancellation(t *testing.T) {
	b := pubsubconfig.DefaultOperatorBinding()
	f := allocatedFake(b)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	f.streamLookup = func(ctx context.Context) error {
		entered <- struct{}{}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			return nil
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	results := make(chan error, 2)
	go func() { _, err := NewPublisher(canceled, f, b, publisherOptions(b)); results <- err }()
	go func() { _, err := NewPublisher(context.Background(), f, b, publisherOptions(b)); results <- err }()
	for range 2 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("attachment blocked another caller")
		}
	}
	cancel()
	select {
	case err := <-results:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("canceled attachment did not return")
	}
	close(release)
	select {
	case err := <-results:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("other attachment did not succeed")
	}
	f.mu.Lock()
	f.streamLookup = nil
	f.mu.Unlock()
	_, err := NewPublisher(context.Background(), f, b, publisherOptions(b))
	require.NoError(t, err)
}

func TestProviderChecksFreshBrokerState(t *testing.T) {
	b := managedTestBinding()
	f := allocatedFake(b)
	p, err := NewProvider("nats://unused", b)
	require.NoError(t, err)
	p.js = f
	_, err = p.NewPublisher(context.Background(), publisherOptions(b))
	require.NoError(t, err)
	require.NoError(t, p.Check(context.Background()))
	f.consumer.HeadersOnly = true
	require.Error(t, p.Check(context.Background()))
	f.consumer.HeadersOnly = false
	f.stream.Metadata["syntrix.spec_revision"] = "2"
	require.Error(t, p.Check(context.Background()))
	f.stream.Metadata["syntrix.spec_revision"] = "1"
	require.NoError(t, p.Check(context.Background()))
	f.consumer = nil
	require.Error(t, p.Check(context.Background()))
	assert.NotContains(t, f.calls, "consumer.create")
}

func TestProviderLifecycleAndValidation(t *testing.T) {
	b := pubsubconfig.DefaultOperatorBinding()
	_, err := NewProvider("", b)
	require.Error(t, err)
	bad := b
	bad.SpecRevision = 0
	_, err = NewProvider("nats://unused", bad)
	require.Error(t, err)
	p, err := NewProvider("nats://unused", b)
	require.NoError(t, err)
	_, err = p.NewPublisher(context.Background(), publisherOptions(b))
	require.Error(t, err)
	_, err = p.NewConsumer(context.Background(), consumerOptions(b))
	require.Error(t, err)
	require.Error(t, p.Check(context.Background()))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, p.Connect(ctx), context.Canceled)
	p.js = allocatedFake(b)
	require.NoError(t, p.Close())
	require.NoError(t, p.Close())
	_, err = p.NewPublisher(context.Background(), publisherOptions(b))
	require.Error(t, err)
	_, err = p.NewConsumer(context.Background(), consumerOptions(b))
	require.Error(t, err)
	require.Error(t, p.Check(context.Background()))
}

func TestAttachmentCanceledBeforeBrokerCalls(t *testing.T) {
	b := pubsubconfig.DefaultOperatorBinding()
	f := allocatedFake(b)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewPublisher(ctx, f, b, publisherOptions(b))
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, f.calls)
	_, err = NewPublisher(context.Background(), f, b, publisherOptions(b))
	require.NoError(t, err)
}

func TestConcurrentSuccessfulAttachments(t *testing.T) {
	b := pubsubconfig.DefaultOperatorBinding()
	f := allocatedFake(b)
	var callers sync.WaitGroup
	for range 8 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			_, err := NewPublisher(context.Background(), f, b, publisherOptions(b))
			assert.NoError(t, err)
		}()
	}
	callers.Wait()
	assert.NotContains(t, f.calls, "stream.create")
	assert.NotContains(t, f.calls, "consumer.create")
}
