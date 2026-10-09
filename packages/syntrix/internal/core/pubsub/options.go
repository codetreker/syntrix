package pubsub

import "time"

// StorageType defines the storage backend for streams.
type StorageType int

const (
	// MemoryStorage stores data in memory (default).
	MemoryStorage StorageType = iota
	// FileStorage stores data on disk.
	FileStorage
)

// PublisherOptions configures publisher behavior.
type PublisherOptions struct {
	// StreamName is the name of the stream to publish to.
	StreamName string

	// SubjectPrefix is prepended to all subjects.
	SubjectPrefix string

	// RetryAttempts is the number of retry attempts for publishing.
	// 0 means no retry (default).
	RetryAttempts int

	// Storage is the storage type for the stream.
	// Defaults to MemoryStorage.
	Storage StorageType

	// OnPublish is called after each publish attempt (for metrics).
	OnPublish func(subject string, err error, latency time.Duration)
}

// ConsumerOptions configures consumer behavior.
type ConsumerOptions struct {
	// StreamName is the name of the stream to consume from.
	StreamName string

	// ConsumerName is the durable consumer name.
	ConsumerName string

	// FilterSubject filters messages by subject pattern.
	FilterSubject string

	// ChannelBufSize is the buffer size for the message channel.
	ChannelBufSize int

	// Storage is the storage type for the stream.
	// Defaults to MemoryStorage.
	Storage StorageType
}

// DefaultConsumerOptions returns ConsumerOptions with sensible defaults.
func DefaultConsumerOptions() ConsumerOptions {
	return ConsumerOptions{
		ChannelBufSize: 100,
	}
}
