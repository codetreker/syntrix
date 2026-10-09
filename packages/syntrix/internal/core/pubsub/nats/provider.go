package nats

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/codetreker/syntrix/internal/core/pubsub"
	"github.com/nats-io/nats.go"
)

// natsConnection abstracts the nats.Conn for testing purposes
type natsConnection interface {
	Close()
}

// natsConnectFunc is a function type for connecting to NATS (injectable for testing)
type natsConnectFunc func(url string) (natsConnection, error)

// jetStreamFactory is a function type for creating JetStream (injectable for testing)
type jetStreamFactory func(nc *nats.Conn) (JetStream, error)

// natsConnCaster is a function type that attempts to cast natsConnection to *nats.Conn
// This is injectable for testing to simulate real *nats.Conn behavior
type natsConnCaster func(nc natsConnection) (*nats.Conn, bool)

// defaultNatsConnect is the default implementation that uses nats.Connect
var defaultNatsConnect natsConnectFunc = func(url string) (natsConnection, error) {
	return nats.Connect(url)
}

// defaultJetStreamFactory is the default implementation that creates JetStream
var defaultJetStreamFactory jetStreamFactory = NewJetStream

// defaultNatsConnCaster is the default implementation that casts natsConnection to *nats.Conn
var defaultNatsConnCaster natsConnCaster = func(nc natsConnection) (*nats.Conn, bool) {
	natsConn, ok := nc.(*nats.Conn)
	return natsConn, ok
}

// Provider implements pubsub.Provider using NATS JetStream.
// It manages the NATS connection lifecycle and provides factory methods
// for creating publishers and consumers.
type Provider struct {
	url              string
	nc               natsConnection
	js               JetStream
	natsConnect      natsConnectFunc  // injectable for testing
	jetStreamFactory jetStreamFactory // injectable for testing
	natsConnCaster   natsConnCaster   // injectable for testing
}

// Compile-time check that Provider implements pubsub.Provider
var _ pubsub.Provider = (*Provider)(nil)

// NewProvider creates a new NATS-based pubsub provider.
// It connects to the NATS server and initializes JetStream.
func NewProvider(url string) (*Provider, error) {
	return &Provider{
		url:              url,
		natsConnect:      defaultNatsConnect,
		jetStreamFactory: defaultJetStreamFactory,
		natsConnCaster:   defaultNatsConnCaster,
	}, nil
}

// Connect establishes the NATS connection and initializes JetStream.
// This must be called before using NewPublisher or NewConsumer.
func (p *Provider) Connect(ctx context.Context) error {
	connectFn := p.natsConnect
	if connectFn == nil {
		connectFn = defaultNatsConnect
	}

	nc, err := connectFn(p.url)
	if err != nil {
		return fmt.Errorf("failed to connect to NATS at %s: %w", p.url, err)
	}
	p.nc = nc

	// Use injectable caster for testing
	caster := p.natsConnCaster
	if caster == nil {
		caster = defaultNatsConnCaster
	}

	// NewJetStream expects *nats.Conn, so we need to cast
	natsConn, ok := caster(nc)
	if !ok {
		// For testing with mock connections, skip JetStream creation
		// The test should set js directly
		slog.Info("Connected to NATS (mock)", "url", p.url)
		return nil
	}

	jsFactory := p.jetStreamFactory
	if jsFactory == nil {
		jsFactory = defaultJetStreamFactory
	}

	js, err := jsFactory(natsConn)
	if err != nil {
		nc.Close()
		return fmt.Errorf("failed to create JetStream: %w", err)
	}
	p.js = js

	slog.Info("Connected to NATS", "url", p.url)
	return nil
}

// NewPublisher creates a new Publisher backed by NATS JetStream.
func (p *Provider) NewPublisher(opts pubsub.PublisherOptions) (pubsub.Publisher, error) {
	if p.js == nil {
		return nil, fmt.Errorf("NATS not connected, call Connect first")
	}
	return NewPublisher(p.js, opts)
}

// NewConsumer creates a new Consumer backed by NATS JetStream.
func (p *Provider) NewConsumer(opts pubsub.ConsumerOptions) (pubsub.Consumer, error) {
	if p.js == nil {
		return nil, fmt.Errorf("NATS not connected, call Connect first")
	}
	return NewConsumer(p.js, opts)
}

// Close closes the NATS connection.
func (p *Provider) Close() error {
	if p.nc != nil {
		slog.Info("Closing NATS connection...")
		p.nc.Close()
		p.nc = nil
		p.js = nil
	}
	return nil
}
