package trigger

import (
	"context"
	"errors"
	"testing"
	"time"

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

		cause := errors.New("cannot connect nats://admin:secret@example:4222")
		natsConnectFunc = func(url string, opts ...nats.Option) (*nats.Conn, error) {
			return nil, cause
		}

		p := NewRemoteNATSProvider("nats://unreachable:4222")
		conn, err := p.Connect(context.Background())

		assert.Error(t, err)
		assert.Nil(t, conn)
		assert.ErrorIs(t, err, cause)
		assert.NotContains(t, err.Error(), "secret")
		assert.NotContains(t, err.Error(), "example")
	})
	t.Run("canceled context", func(t *testing.T) {
		origFunc := natsConnectFunc
		defer func() { natsConnectFunc = origFunc }()
		natsConnectFunc = func(string, ...nats.Option) (*nats.Conn, error) {
			t.Fatal("canceled startup must not dial")
			return nil, nil
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		conn, err := NewRemoteNATSProvider("nats://test:4222").Connect(ctx)
		assert.Nil(t, conn)
		assert.ErrorIs(t, err, context.Canceled)
	})
	t.Run("deadline limits startup", func(t *testing.T) {
		origFunc := natsConnectFunc
		defer func() { natsConnectFunc = origFunc }()
		natsConnectFunc = func(_ string, opts ...nats.Option) (*nats.Conn, error) {
			options := nats.GetDefaultOptions()
			for _, option := range opts {
				assert.NoError(t, option(&options))
			}
			assert.LessOrEqual(t, options.Timeout, 100*time.Millisecond)
			assert.Greater(t, options.Timeout, time.Duration(0))
			assert.NotNil(t, options.CustomDialer)
			assert.True(t, options.SkipHostLookup)
			return &nats.Conn{}, nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		conn, err := NewRemoteNATSProvider("nats://test:4222").Connect(ctx)
		assert.NoError(t, err)
		assert.NotNil(t, conn)
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
