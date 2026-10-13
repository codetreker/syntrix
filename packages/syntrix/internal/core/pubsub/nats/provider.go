package nats

import (
	"context"
	"errors"
	"sync"

	deploymentconfig "github.com/codetreker/syntrix/deployment/config"
	"github.com/codetreker/syntrix/internal/core/pubsub"
	"github.com/nats-io/nats.go"
)

// OperationError preserves the cause without rendering broker addresses or credentials.
type OperationError struct {
	Operation string
	Cause     error
}

func (e *OperationError) Error() string { return "NATS " + e.Operation + " failed" }
func (e *OperationError) Unwrap() error { return e.Cause }

type Provider struct {
	mu      sync.RWMutex
	url     string
	binding deploymentconfig.BrokerBinding
	nc      *nats.Conn
	js      JetStream
	closed  bool
}

var _ pubsub.Provider = (*Provider)(nil)

func NewProvider(url string, binding deploymentconfig.BrokerBinding) (*Provider, error) {
	if url == "" {
		return nil, errors.New("NATS URL is required")
	}
	if _, err := binding.Specification(); err != nil {
		return nil, err
	}
	return &Provider{url: url, binding: binding}, nil
}
func (p *Provider) Connect(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	nc, err := ConnectContext(ctx, p.url, nats.Connect)
	if err != nil {
		return &OperationError{Operation: "connection", Cause: err}
	}
	js, err := NewJetStream(nc)
	if err != nil {
		nc.Close()
		return &OperationError{Operation: "JetStream initialization", Cause: err}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		nc.Close()
		return errors.New("NATS provider is closed")
	}
	if p.nc != nil {
		nc.Close()
		return errors.New("NATS provider is already connected")
	}
	p.nc, p.js = nc, js
	return nil
}
func (p *Provider) connection() (JetStream, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed {
		return nil, errors.New("NATS provider is closed")
	}
	if p.js == nil {
		return nil, errors.New("NATS not connected")
	}
	return p.js, nil
}
func (p *Provider) NewPublisher(ctx context.Context, opts pubsub.PublisherOptions) (pubsub.Publisher, error) {
	js, err := p.connection()
	if err != nil {
		return nil, err
	}
	return NewPublisher(ctx, js, p.binding, opts)
}
func (p *Provider) NewConsumer(ctx context.Context, opts pubsub.ConsumerOptions) (pubsub.Consumer, error) {
	js, err := p.connection()
	if err != nil {
		return nil, err
	}
	return NewConsumer(ctx, js, p.binding, opts)
}
func (p *Provider) Check(ctx context.Context) error {
	js, err := p.connection()
	if err != nil {
		return err
	}
	_, err = attach(ctx, js, p.binding, false)
	return err
}
func (p *Provider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	if p.nc != nil {
		p.nc.Close()
		p.nc = nil
	}
	p.js = nil
	return nil
}
