package trigger

import (
	"context"

	"github.com/nats-io/nats.go"
)

// NATSProvider abstracts NATS connection management.
// This allows swapping between external NATS server and future embedded NATS.
type NATSProvider interface {
	// Connect establishes a connection to NATS and returns the connection.
	Connect(ctx context.Context) (*nats.Conn, error)
	// Close closes the NATS connection and any embedded server.
	Close() error
}
