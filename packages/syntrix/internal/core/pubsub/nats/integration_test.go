package nats

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

	deploymentconfig "github.com/codetreker/syntrix/deployment/config"
	"github.com/codetreker/syntrix/internal/core/pubsub"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

func integrationNATS(t *testing.T) (context.Context, jetstream.JetStream, string) {
	t.Helper()
	address := os.Getenv("SYNTRIX_TEST_NATS_URL")
	if address == "" {
		address = nats.DefaultURL
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	nc, err := ConnectContext(ctx, address, nats.Connect)
	if err != nil {
		if os.Getenv("SYNTRIX_TEST_NATS_URL") != "" {
			t.Fatal("configured NATS integration fixture unavailable")
		}
		t.Skip("NATS integration fixture unavailable")
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	return ctx, js, address
}
func managedIntegrationBinding(t *testing.T) deploymentconfig.BrokerBinding {
	t.Helper()
	data := make([]byte, 16)
	_, err := rand.Read(data)
	require.NoError(t, err)
	binding := deploymentconfig.DefaultOperatorBinding()
	binding.ResourceMode = "managed"
	binding.Ownership.InstanceID = hex.EncodeToString(data)
	binding.Ownership.AllocationID = binding.Ownership.InstanceID
	binding.Ownership.InstallationID = "runtime-contract-test"
	binding.StreamName = "SYNTRIX_" + binding.Ownership.InstanceID + "_TRIGGERS"
	binding.SubjectPrefix = binding.StreamName
	binding.ConsumerName = binding.StreamName + "_DELIVERY"
	binding.Policy.MaxBytes = 1 << 20
	binding.Policy.MaxMessages = 1000
	binding.Policy.MaxAge = time.Hour
	return binding
}
func allocateIntegrationBinding(t *testing.T, ctx context.Context, js jetstream.JetStream, binding deploymentconfig.BrokerBinding) {
	t.Helper()
	spec, err := binding.Specification()
	require.NoError(t, err)
	_, err = js.CreateStream(ctx, spec.Stream)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, js.DeleteStream(cleanup, spec.Stream.Name))
	})
	_, err = js.CreateConsumer(ctx, spec.Stream.Name, spec.Consumer)
	require.NoError(t, err)
}
func TestManagedSharedNATSRoutingAndAttachment(t *testing.T) {
	ctx, js, address := integrationNATS(t)
	first, second := managedIntegrationBinding(t), managedIntegrationBinding(t)
	var channels []<-chan pubsub.Message
	var publishers []pubsub.Publisher
	var states []pubsub.ConsumerState
	for _, binding := range []deploymentconfig.BrokerBinding{first, second} {
		allocateIntegrationBinding(t, ctx, js, binding)
		provider, err := NewProvider(address, binding)
		require.NoError(t, err)
		require.NoError(t, provider.Connect(ctx))
		t.Cleanup(func() { require.NoError(t, provider.Close()) })
		publisher, err := provider.NewPublisher(ctx, publisherOptions(binding))
		require.NoError(t, err)
		consumer, err := provider.NewConsumer(ctx, consumerOptions(binding))
		require.NoError(t, err)
		ch, err := consumer.Subscribe(ctx)
		require.NoError(t, err)
		channels = append(channels, ch)
		publishers = append(publishers, publisher)
		states = append(states, consumer.(pubsub.ConsumerState))
		require.True(t, consumer.(pubsub.ConsumerState).Ready())
		require.NoError(t, provider.Check(ctx))
		// Repeated attachment must preserve administrators' complete specification.
		_, err = provider.NewPublisher(ctx, publisherOptions(binding))
		require.NoError(t, err)
		stream, err := js.Stream(ctx, binding.StreamName)
		require.NoError(t, err)
		info, err := stream.Info(ctx)
		require.NoError(t, err)
		require.Equal(t, binding.Policy.MaxBytes, info.Config.MaxBytes)
		require.Equal(t, binding.Policy.MaxMessages, info.Config.MaxMsgs)
	}
	for i, publisher := range publishers {
		require.NoError(t, publisher.Publish(ctx, "db.collection.doc", []byte(fmt.Sprintf("instance-%d", i))))
	}
	for i, ch := range channels {
		select {
		case msg := <-ch:
			require.Equal(t, []byte(fmt.Sprintf("instance-%d", i)), msg.Data())
			require.Equal(t, []string{first.SubjectPrefix, second.SubjectPrefix}[i]+".db.collection.doc", msg.Subject())
			require.NoError(t, msg.Ack())
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	for i, ch := range channels {
		select {
		case msg := <-ch:
			t.Fatalf("instance %d received another instance message: %s", i, msg.Subject())
		case <-time.After(100 * time.Millisecond):
		}
	}
	require.NoError(t, js.DeleteConsumer(ctx, first.StreamName, first.ConsumerName))
	require.Eventually(t, func() bool { return !states[0].Ready() && states[0].Err() != nil }, 4*time.Second, 10*time.Millisecond)
	require.True(t, states[1].Ready())
}
func TestManagedMissingResourcesNeverCreated(t *testing.T) {
	ctx, js, address := integrationNATS(t)
	binding := managedIntegrationBinding(t)
	provider, err := NewProvider(address, binding)
	require.NoError(t, err)
	require.NoError(t, provider.Connect(ctx))
	t.Cleanup(func() { require.NoError(t, provider.Close()) })
	_, err = provider.NewPublisher(ctx, publisherOptions(binding))
	require.ErrorIs(t, err, jetstream.ErrStreamNotFound)
	_, err = js.Stream(ctx, binding.StreamName)
	require.ErrorIs(t, err, jetstream.ErrStreamNotFound)
	spec, err := binding.Specification()
	require.NoError(t, err)
	_, err = js.CreateStream(ctx, spec.Stream)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, js.DeleteStream(cleanup, spec.Stream.Name))
	})
	_, err = provider.NewConsumer(ctx, consumerOptions(binding))
	require.ErrorIs(t, err, jetstream.ErrConsumerNotFound)
	_, err = js.Consumer(ctx, binding.StreamName, binding.ConsumerName)
	require.ErrorIs(t, err, jetstream.ErrConsumerNotFound)
	_, err = js.CreateConsumer(ctx, binding.StreamName, spec.Consumer)
	require.NoError(t, err)
	_, err = provider.NewPublisher(ctx, publisherOptions(binding))
	require.NoError(t, err)
	stream, err := js.Stream(ctx, binding.StreamName)
	require.NoError(t, err)
	info, err := stream.Info(ctx)
	require.NoError(t, err)
	changed := info.Config
	changed.MaxBytes++
	_, err = js.UpdateStream(ctx, changed)
	require.NoError(t, err)
	var mismatch *SpecificationError
	require.ErrorAs(t, provider.Check(ctx), &mismatch)
	info, err = stream.Info(ctx)
	require.NoError(t, err)
	require.Equal(t, changed.MaxBytes, info.Config.MaxBytes)
}
