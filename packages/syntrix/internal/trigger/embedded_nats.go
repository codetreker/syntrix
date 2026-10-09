package trigger

import (
	"context"
	"errors"

	"github.com/nats-io/nats.go"
)

// ErrEmbeddedNATSNotImplemented is returned when embedded NATS is requested but not implemented.
var ErrEmbeddedNATSNotImplemented = errors.New("embedded NATS not implemented; use external NATS server or set embedded_nats: false in config")

// EmbeddedNATSProvider runs an embedded NATS server.
// Currently this is a stub that returns an error - embedded NATS requires
// adding the nats-server dependency which will be done in a future release.
type EmbeddedNATSProvider struct {
	dataDir string
}

// NewEmbeddedNATSProvider creates a provider for embedded NATS server.
// Note: Currently returns a stub that will fail on Connect().
func NewEmbeddedNATSProvider(dataDir string) *EmbeddedNATSProvider {
	return &EmbeddedNATSProvider{dataDir: dataDir}
}

// Connect attempts to start an embedded NATS server.
// Currently not implemented - returns ErrEmbeddedNATSNotImplemented.
func (p *EmbeddedNATSProvider) Connect(ctx context.Context) (*nats.Conn, error) {
	// TODO: Implement embedded NATS server
	// Requires: go get github.com/nats-io/nats-server/v2@latest
	//
	// Future implementation:
	// opts := &server.Options{
	//     Host:      "127.0.0.1",
	//     Port:      -1,  // Random port
	//     JetStream: true,
	//     StoreDir:  p.dataDir,
	//     NoLog:     true,
	// }
	// ns, err := server.NewServer(opts)
	// go ns.Start()
	// ns.ReadyForConnections(5 * time.Second)
	// return nats.Connect(ns.ClientURL())

	return nil, ErrEmbeddedNATSNotImplemented
}

// Close closes the embedded NATS server and connection.
func (p *EmbeddedNATSProvider) Close() error {
	// TODO: Shutdown embedded server when implemented
	return nil
}
