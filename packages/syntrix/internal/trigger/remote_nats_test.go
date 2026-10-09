package trigger

import (
	"context"
	"errors"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
)

func TestNewRemoteNATSProvider(t *testing.T) {
	t.Run("with custom URL", func(t *testing.T) {
		p := NewRemoteNATSProvider("nats://custom:4222")
		assert.Equal(t, "nats://custom:4222", p.url)
	})

	t.Run("with empty URL uses default", func(t *testing.T) {
		p := NewRemoteNATSProvider("")
		assert.Equal(t, nats.DefaultURL, p.url)
	})
}

func TestRemoteNATSProvider_Connect(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		// Save original and restore after test
		origFunc := natsConnectFunc
		defer func() { natsConnectFunc = origFunc }()

		mockConn := &nats.Conn{}
		natsConnectFunc = func(url string, opts ...nats.Option) (*nats.Conn, error) {
			assert.Equal(t, "nats://test:4222", url)
			return mockConn, nil
		}

		p := NewRemoteNATSProvider("nats://test:4222")
		conn, err := p.Connect(context.Background())

		assert.NoError(t, err)
		assert.Equal(t, mockConn, conn)
		assert.Equal(t, mockConn, p.conn)
	})

	t.Run("connection error", func(t *testing.T) {
		origFunc := natsConnectFunc
		defer func() { natsConnectFunc = origFunc }()

		natsConnectFunc = func(url string, opts ...nats.Option) (*nats.Conn, error) {
			return nil, errors.New("connection refused")
		}

		p := NewRemoteNATSProvider("nats://unreachable:4222")
		conn, err := p.Connect(context.Background())

		assert.Error(t, err)
		assert.Nil(t, conn)
		assert.Contains(t, err.Error(), "connection refused")
	})
}

// mockNATSConnection is a mock for natsConnection interface
type mockNATSConnection struct {
	closeCalled bool
}

func (m *mockNATSConnection) Close() {
	m.closeCalled = true
}

func TestRemoteNATSProvider_Close(t *testing.T) {
	t.Run("close with nil connection", func(t *testing.T) {
		p := &RemoteNATSProvider{}
		err := p.Close()
		assert.NoError(t, err)
	})

	t.Run("close with non-nil connection", func(t *testing.T) {
		mockConn := &mockNATSConnection{}
		p := &RemoteNATSProvider{conn: mockConn}

		err := p.Close()
		assert.NoError(t, err)
		assert.True(t, mockConn.closeCalled, "Close should be called on connection")
	})
}

func TestSetNatsConnectFunc(t *testing.T) {
	origFunc := natsConnectFunc
	defer func() { natsConnectFunc = origFunc }()

	customFunc := func(url string, opts ...nats.Option) (*nats.Conn, error) {
		return nil, errors.New("custom error")
	}

	SetNatsConnectFunc(customFunc)

	// Verify the function was set
	_, err := natsConnectFunc("", nil)
	assert.ErrorContains(t, err, "custom error")
}

func TestGetNatsConnectFunc(t *testing.T) {
	origFunc := natsConnectFunc
	defer func() { natsConnectFunc = origFunc }()

	customFunc := func(url string, opts ...nats.Option) (*nats.Conn, error) {
		return nil, errors.New("custom error")
	}

	natsConnectFunc = customFunc
	retrieved := GetNatsConnectFunc()

	// Verify the function is the same
	_, err := retrieved("")
	assert.ErrorContains(t, err, "custom error")
}

// Verify interface compliance
var _ NATSProvider = (*RemoteNATSProvider)(nil)
