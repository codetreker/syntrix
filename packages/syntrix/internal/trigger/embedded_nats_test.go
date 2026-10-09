package trigger

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewEmbeddedNATSProvider(t *testing.T) {
	p := NewEmbeddedNATSProvider("/data/nats")
	assert.Equal(t, "/data/nats", p.dataDir)
}

func TestEmbeddedNATSProvider_Connect(t *testing.T) {
	p := NewEmbeddedNATSProvider("/data/nats")
	conn, err := p.Connect(context.Background())

	assert.Nil(t, conn)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrEmbeddedNATSNotImplemented)
}

func TestEmbeddedNATSProvider_Close(t *testing.T) {
	p := NewEmbeddedNATSProvider("/data/nats")
	err := p.Close()
	assert.NoError(t, err)
}

// Verify interface compliance
var _ NATSProvider = (*EmbeddedNATSProvider)(nil)
