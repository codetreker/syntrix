package nats

import (
	"context"
	"sync"
	"time"

	"github.com/codetreker/syntrix/internal/core/pubsub"
	pubsubconfig "github.com/codetreker/syntrix/internal/core/pubsub/config"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/mock"
)

type fakeJetStream struct {
	mu                                                sync.Mutex
	stream                                            *jetstream.StreamConfig
	consumer                                          *jetstream.ConsumerConfig
	calls                                             []string
	streamLookup                                      func(context.Context) error
	consumerLookupErr, streamInfoErr, consumerInfoErr error
	createStreamErr, createConsumerErr                error
	applyOnCreateError                                bool
	consumeErr                                        error
	consumeHook                                       func(jetstream.MessageHandler, jetstream.ConsumeErrHandler)
	loop                                              *fakeConsumeContext
	handler                                           jetstream.MessageHandler
	errHandler                                        jetstream.ConsumeErrHandler
	publish                                           func(context.Context, string, []byte, []jetstream.PublishOpt) error
}

func allocatedFake(binding pubsubconfig.BrokerBinding) *fakeJetStream {
	spec, err := binding.Specification()
	if err != nil {
		panic(err)
	}
	return &fakeJetStream{stream: &spec.Stream, consumer: &spec.Consumer}
}

func (f *fakeJetStream) Stream(ctx context.Context, name string) (jetstream.Stream, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "stream.lookup")
	hook := f.streamLookup
	f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if hook != nil {
		if err := hook(ctx); err != nil {
			return nil, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stream == nil || f.stream.Name != name {
		return nil, jetstream.ErrStreamNotFound
	}
	return &fakeStream{owner: f}, nil
}

func (f *fakeJetStream) Consumer(ctx context.Context, stream, name string) (jetstream.Consumer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "consumer.lookup")
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.consumerLookupErr != nil {
		return nil, f.consumerLookupErr
	}
	if f.stream == nil || f.stream.Name != stream {
		return nil, jetstream.ErrStreamNotFound
	}
	if f.consumer == nil || f.consumer.Name != name {
		return nil, jetstream.ErrConsumerNotFound
	}
	return &fakeConsumer{owner: f}, nil
}

func (f *fakeJetStream) CreateStream(ctx context.Context, config jetstream.StreamConfig) (jetstream.Stream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "stream.create")
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.createStreamErr == nil || f.applyOnCreateError {
		f.stream = &config
	}
	return &fakeStream{owner: f}, f.createStreamErr
}

func (f *fakeJetStream) CreateConsumer(ctx context.Context, stream string, config jetstream.ConsumerConfig) (jetstream.Consumer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "consumer.create")
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.stream == nil || f.stream.Name != stream {
		return nil, jetstream.ErrStreamNotFound
	}
	if f.createConsumerErr == nil || f.applyOnCreateError {
		f.consumer = &config
	}
	return &fakeConsumer{owner: f}, f.createConsumerErr
}

func (f *fakeJetStream) Publish(ctx context.Context, subject string, data []byte, options ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.publish != nil {
		if err := f.publish(ctx, subject, data, options); err != nil {
			return nil, err
		}
	}
	return &jetstream.PubAck{Stream: "TRIGGERS", Sequence: 1}, nil
}

type fakeStream struct {
	jetstream.Stream
	owner *fakeJetStream
}

func (s *fakeStream) Info(ctx context.Context, _ ...jetstream.StreamInfoOpt) (*jetstream.StreamInfo, error) {
	f := s.owner
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "stream.info")
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.streamInfoErr != nil {
		return nil, f.streamInfoErr
	}
	return &jetstream.StreamInfo{Config: *f.stream}, nil
}

type fakeConsumer struct {
	jetstream.Consumer
	owner *fakeJetStream
}

func (c *fakeConsumer) Info(ctx context.Context) (*jetstream.ConsumerInfo, error) {
	f := c.owner
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "consumer.info")
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.consumerInfoErr != nil {
		return nil, f.consumerInfoErr
	}
	return &jetstream.ConsumerInfo{Config: *f.consumer}, nil
}

func (c *fakeConsumer) Consume(handler jetstream.MessageHandler, opts ...jetstream.PullConsumeOpt) (jetstream.ConsumeContext, error) {
	f := c.owner
	f.mu.Lock()
	if f.consumeErr != nil {
		f.mu.Unlock()
		return nil, f.consumeErr
	}
	f.handler = handler
	for _, option := range opts {
		if errHandler, ok := option.(jetstream.ConsumeErrHandler); ok {
			f.errHandler = errHandler
		}
	}
	if f.loop == nil {
		f.loop = newFakeConsumeContext()
	}
	loop, hook, errHandler := f.loop, f.consumeHook, f.errHandler
	f.mu.Unlock()
	if hook != nil {
		hook(handler, errHandler)
	}
	return loop, nil
}

type fakeConsumeContext struct {
	stopped             chan struct{}
	closed              chan struct{}
	stopOnce, closeOnce sync.Once
	manualClose         bool
}

func newFakeConsumeContext() *fakeConsumeContext {
	return &fakeConsumeContext{stopped: make(chan struct{}), closed: make(chan struct{})}
}
func (c *fakeConsumeContext) Stop() {
	c.stopOnce.Do(func() { close(c.stopped) })
	if !c.manualClose {
		c.finish()
	}
}
func (c *fakeConsumeContext) Drain()                  { c.Stop() }
func (c *fakeConsumeContext) Closed() <-chan struct{} { return c.closed }
func (c *fakeConsumeContext) finish()                 { c.closeOnce.Do(func() { close(c.closed) }) }

func publisherOptions(b pubsubconfig.BrokerBinding) pubsub.PublisherOptions {
	return pubsub.PublisherOptions{StreamName: b.StreamName, SubjectPrefix: b.SubjectPrefix, Storage: bindingStorage(b)}
}
func consumerOptions(b pubsubconfig.BrokerBinding) pubsub.ConsumerOptions {
	return pubsub.ConsumerOptions{StreamName: b.StreamName, ConsumerName: b.ConsumerName, FilterSubject: b.SubjectPrefix + ".>", Storage: bindingStorage(b), ChannelBufSize: 1}
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
