package pubsub

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDefaultConsumerOptions(t *testing.T) {
	defaults := DefaultConsumerOptions()

	assert.Equal(t, 100, defaults.ChannelBufSize)
}

func TestConsumerOptionsFields(t *testing.T) {
	opts := ConsumerOptions{
		StreamName:     "TEST_STREAM",
		ConsumerName:   "test-consumer",
		FilterSubject:  "events.>",
		ChannelBufSize: 50,
	}

	assert.Equal(t, "TEST_STREAM", opts.StreamName)
	assert.Equal(t, "test-consumer", opts.ConsumerName)
	assert.Equal(t, "events.>", opts.FilterSubject)
	assert.Equal(t, 50, opts.ChannelBufSize)
}

func TestPublisherOptionsFields(t *testing.T) {
	opts := PublisherOptions{
		StreamName:    "TEST_STREAM",
		SubjectPrefix: "PREFIX",
		OnPublish:     func(subject string, err error, latency time.Duration) {},
	}

	assert.Equal(t, "TEST_STREAM", opts.StreamName)
	assert.Equal(t, "PREFIX", opts.SubjectPrefix)
	assert.NotNil(t, opts.OnPublish)
}
