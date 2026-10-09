// Package testing provides mock implementations of pubsub interfaces for testing.
package testing

import (
	"context"
	"sync"
	"time"

	"github.com/codetreker/syntrix/internal/core/pubsub"
)

// PublishedMessage represents a message that was published.
type PublishedMessage struct {
	Subject string
	Data    []byte
}

// MockPublisher is a mock implementation of pubsub.Publisher.
type MockPublisher struct {
	mu       sync.Mutex
	messages []PublishedMessage
	err      error
	closed   bool
}

// NewMockPublisher creates a new MockPublisher.
func NewMockPublisher() *MockPublisher {
	return &MockPublisher{}
}

// Publish records the message.
func (m *MockPublisher) Publish(ctx context.Context, subject string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.err != nil {
		return m.err
	}

	m.messages = append(m.messages, PublishedMessage{
		Subject: subject,
		Data:    append([]byte(nil), data...), // Copy to avoid mutation
	})
	return nil
}

// Close marks the publisher as closed.
func (m *MockPublisher) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

// Messages returns all published messages.
func (m *MockPublisher) Messages() []PublishedMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]PublishedMessage(nil), m.messages...)
}

// SetError sets an error to return on Publish.
func (m *MockPublisher) SetError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

// Reset clears all messages and errors.
func (m *MockPublisher) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = nil
	m.err = nil
	m.closed = false
}

// IsClosed returns whether Close was called.
func (m *MockPublisher) IsClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

// MockMessage is a mock implementation of pubsub.Message.
type MockMessage struct {
	mu          sync.Mutex
	data        []byte
	subject     string
	metadata    pubsub.MessageMetadata
	acked       bool
	naked       bool
	nakDelay    time.Duration
	termed      bool
	ackErr      error
	nakErr      error
	termErr     error
	metadataErr error
}

// NewMockMessage creates a new MockMessage.
func NewMockMessage(subject string, data []byte) *MockMessage {
	return &MockMessage{
		subject: subject,
		data:    data,
		metadata: pubsub.MessageMetadata{
			NumDelivered: 1,
			Timestamp:    time.Now(),
			Subject:      subject,
		},
	}
}

// Data returns the message data.
func (m *MockMessage) Data() []byte {
	return m.data
}

// Subject returns the message subject.
func (m *MockMessage) Subject() string {
	return m.subject
}

// Ack acknowledges the message.
func (m *MockMessage) Ack() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ackErr != nil {
		return m.ackErr
	}
	m.acked = true
	return nil
}

// Nak signals processing failure.
func (m *MockMessage) Nak() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.nakErr != nil {
		return m.nakErr
	}
	m.naked = true
	return nil
}

// NakWithDelay signals processing failure with delay.
func (m *MockMessage) NakWithDelay(delay time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.nakErr != nil {
		return m.nakErr
	}
	m.naked = true
	m.nakDelay = delay
	return nil
}

// Term terminates the message.
func (m *MockMessage) Term() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.termErr != nil {
		return m.termErr
	}
	m.termed = true
	return nil
}

// Metadata returns message metadata.
func (m *MockMessage) Metadata() (pubsub.MessageMetadata, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.metadataErr != nil {
		return pubsub.MessageMetadata{}, m.metadataErr
	}
	return m.metadata, nil
}

// IsAcked returns whether Ack was called.
func (m *MockMessage) IsAcked() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.acked
}

// IsNaked returns whether Nak was called.
func (m *MockMessage) IsNaked() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.naked
}

// IsTermed returns whether Term was called.
func (m *MockMessage) IsTermed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.termed
}

// NakDelay returns the delay passed to NakWithDelay.
func (m *MockMessage) NakDelay() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.nakDelay
}

// SetMetadata sets the metadata to return.
func (m *MockMessage) SetMetadata(md pubsub.MessageMetadata) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.metadata = md
}

// SetErrors sets errors to return from various methods.
func (m *MockMessage) SetErrors(ack, nak, term, metadata error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ackErr = ack
	m.nakErr = nak
	m.termErr = term
	m.metadataErr = metadata
}

// MockConsumer is a mock implementation of pubsub.Consumer.
type MockConsumer struct {
	mu      sync.Mutex
	msgCh   chan pubsub.Message
	started bool
	err     error
}

// NewMockConsumer creates a new MockConsumer.
func NewMockConsumer() *MockConsumer {
	return &MockConsumer{}
}

// Subscribe returns a channel for receiving messages.
func (c *MockConsumer) Subscribe(ctx context.Context) (<-chan pubsub.Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.err != nil {
		return nil, c.err
	}

	c.msgCh = make(chan pubsub.Message, 100)
	c.started = true

	// Close channel when context is done
	go func() {
		<-ctx.Done()
		c.mu.Lock()
		if c.msgCh != nil {
			close(c.msgCh)
			c.msgCh = nil
		}
		c.mu.Unlock()
	}()

	return c.msgCh, nil
}

// Send sends a message to the consumer channel.
func (c *MockConsumer) Send(msg pubsub.Message) {
	c.mu.Lock()
	ch := c.msgCh
	c.mu.Unlock()

	if ch != nil {
		ch <- msg
	}
}

// IsStarted returns whether Subscribe was called.
func (c *MockConsumer) IsStarted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.started
}

// SetError sets an error to return from Subscribe.
func (c *MockConsumer) SetError(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.err = err
}

// MockProvider is a mock implementation of pubsub.Provider.
type MockProvider struct {
	mu            sync.Mutex
	publisher     pubsub.Publisher
	consumer      pubsub.Consumer
	publisherErr  error
	consumerErr   error
	closeErr      error
	closed        bool
	publisherOpts []pubsub.PublisherOptions
	consumerOpts  []pubsub.ConsumerOptions
}

// NewMockProvider creates a new MockProvider with default mock publisher and consumer.
func NewMockProvider() *MockProvider {
	return &MockProvider{
		publisher: NewMockPublisher(),
		consumer:  NewMockConsumer(),
	}
}

// NewPublisher returns the configured publisher or error.
func (p *MockProvider) NewPublisher(opts pubsub.PublisherOptions) (pubsub.Publisher, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.publisherOpts = append(p.publisherOpts, opts)
	if p.publisherErr != nil {
		return nil, p.publisherErr
	}
	return p.publisher, nil
}

// NewConsumer returns the configured consumer or error.
func (p *MockProvider) NewConsumer(opts pubsub.ConsumerOptions) (pubsub.Consumer, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.consumerOpts = append(p.consumerOpts, opts)
	if p.consumerErr != nil {
		return nil, p.consumerErr
	}
	return p.consumer, nil
}

// Close marks the provider as closed.
func (p *MockProvider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	return p.closeErr
}

// SetPublisher sets the publisher to return.
func (p *MockProvider) SetPublisher(pub pubsub.Publisher) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.publisher = pub
}

// SetConsumer sets the consumer to return.
func (p *MockProvider) SetConsumer(cons pubsub.Consumer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.consumer = cons
}

// SetPublisherError sets an error to return from NewPublisher.
func (p *MockProvider) SetPublisherError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.publisherErr = err
}

// SetConsumerError sets an error to return from NewConsumer.
func (p *MockProvider) SetConsumerError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.consumerErr = err
}

// SetCloseError sets an error to return from Close.
func (p *MockProvider) SetCloseError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closeErr = err
}

// IsClosed returns whether Close was called.
func (p *MockProvider) IsClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

// PublisherOpts returns all options passed to NewPublisher.
func (p *MockProvider) PublisherOpts() []pubsub.PublisherOptions {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]pubsub.PublisherOptions(nil), p.publisherOpts...)
}

// ConsumerOpts returns all options passed to NewConsumer.
func (p *MockProvider) ConsumerOpts() []pubsub.ConsumerOptions {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]pubsub.ConsumerOptions(nil), p.consumerOpts...)
}
