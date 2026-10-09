package nats

import (
	"context"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/mock"
)

// MockJetStream is a mock implementation of the JetStream interface for testing.
type MockJetStream struct {
	mock.Mock
}

func (m *MockJetStream) CreateOrUpdateStream(ctx context.Context, cfg jetstream.StreamConfig) (jetstream.Stream, error) {
	args := m.Called(ctx, cfg)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(jetstream.Stream), args.Error(1)
}

func (m *MockJetStream) CreateOrUpdateConsumer(ctx context.Context, stream string, cfg jetstream.ConsumerConfig) (jetstream.Consumer, error) {
	args := m.Called(ctx, stream, cfg)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(jetstream.Consumer), args.Error(1)
}

func (m *MockJetStream) Publish(ctx context.Context, subject string, data []byte, opts ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
	args := m.Called(ctx, subject, data)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*jetstream.PubAck), args.Error(1)
}

// MockConsumer is a mock implementation of jetstream.Consumer for testing.
type MockConsumer struct {
	mock.Mock
	jetstream.Consumer
	handlerCh chan jetstream.MessageHandler
}

// NewMockConsumer creates a new MockConsumer with handler channel.
func NewMockConsumer() *MockConsumer {
	return &MockConsumer{
		handlerCh: make(chan jetstream.MessageHandler, 1),
	}
}

func (m *MockConsumer) Consume(handler jetstream.MessageHandler, opts ...jetstream.PullConsumeOpt) (jetstream.ConsumeContext, error) {
	args := m.Called(handler)
	if m.handlerCh != nil {
		select {
		case m.handlerCh <- handler:
		default:
		}
	}
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(jetstream.ConsumeContext), args.Error(1)
}

// HandlerCh returns a channel that receives the message handler when Consume is called.
func (m *MockConsumer) HandlerCh() <-chan jetstream.MessageHandler {
	return m.handlerCh
}

// MockConsumeContext is a mock implementation of jetstream.ConsumeContext for testing.
type MockConsumeContext struct {
	mock.Mock
	jetstream.ConsumeContext // Embed to avoid implementing all methods
	stopCh                   chan struct{}
}

func NewMockConsumeContext() *MockConsumeContext {
	return &MockConsumeContext{
		stopCh: make(chan struct{}),
	}
}

func (m *MockConsumeContext) Stop() {
	select {
	case <-m.stopCh:
	default:
		close(m.stopCh)
	}
	m.Called()
}

func (m *MockConsumeContext) Drain() {
	m.Called()
}

// MockMsg is a mock implementation of jetstream.Msg for testing.
type MockMsg struct {
	mock.Mock
	data    []byte
	subject string
}

func NewMockMsg(subject string, data []byte) *MockMsg {
	return &MockMsg{subject: subject, data: data}
}

func (m *MockMsg) Data() []byte         { return m.data }
func (m *MockMsg) Subject() string      { return m.subject }
func (m *MockMsg) Reply() string        { return "" }
func (m *MockMsg) Headers() nats.Header { return nil }

func (m *MockMsg) Ack() error {
	args := m.Called()
	return args.Error(0)
}

func (m *MockMsg) Nak() error {
	args := m.Called()
	return args.Error(0)
}

func (m *MockMsg) NakWithDelay(d time.Duration) error {
	args := m.Called(d)
	return args.Error(0)
}

func (m *MockMsg) Term() error {
	args := m.Called()
	return args.Error(0)
}

func (m *MockMsg) TermWithReason(reason string) error {
	args := m.Called(reason)
	return args.Error(0)
}

func (m *MockMsg) InProgress() error {
	args := m.Called()
	return args.Error(0)
}

func (m *MockMsg) DoubleAck(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func (m *MockMsg) Metadata() (*jetstream.MsgMetadata, error) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*jetstream.MsgMetadata), args.Error(1)
}
