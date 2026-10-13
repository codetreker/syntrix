package trigger

import (
	"context"

	natspubsub "github.com/codetreker/syntrix/internal/core/pubsub/nats"
	"github.com/nats-io/nats.go"
)

// natsConnectFunc allows test injection
var natsConnectFunc = nats.Connect

// SetNatsConnectFunc sets the natsConnectFunc for testing.
// This is exported to allow services package tests to inject mock connections.
func SetNatsConnectFunc(f func(string, ...nats.Option) (*nats.Conn, error)) {
	natsConnectFunc = f
}

// GetNatsConnectFunc returns the current natsConnectFunc for testing.
func GetNatsConnectFunc() func(string, ...nats.Option) (*nats.Conn, error) {
	return natsConnectFunc
}

// natsConnection defines the interface for NATS connection operations.
// This interface allows mocking the connection in tests.
type natsConnection interface {
	Close()
}

// RemoteNATSProvider connects to an external NATS server.
type RemoteNATSProvider struct {
	url  string
	conn natsConnection
}

// NewRemoteNATSProvider creates a provider that connects to an external NATS server.
func NewRemoteNATSProvider(url string) *RemoteNATSProvider {
	if url == "" {
		url = nats.DefaultURL
	}
	return &RemoteNATSProvider{url: url}
}

// Connect establishes a connection to the external NATS server.
func (p *RemoteNATSProvider) Connect(ctx context.Context) (*nats.Conn, error) {
	nc, err := natspubsub.ConnectContext(ctx, p.url, natsConnectFunc)
	if err != nil {
		return nil, &natspubsub.OperationError{Operation: "connection", Cause: err}
	}
	p.conn = nc
	return nc, nil
}

// Close closes the NATS connection.
func (p *RemoteNATSProvider) Close() error {
	if p.conn != nil {
		p.conn.Close()
	}
	return nil
}
