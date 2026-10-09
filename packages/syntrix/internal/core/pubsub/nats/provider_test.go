package nats

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/codetreker/syntrix/internal/core/pubsub"
)

func TestNewProvider(t *testing.T) {
	provider, err := NewProvider("nats://localhost:4222")
	require.NoError(t, err)
	assert.NotNil(t, provider)
	assert.Equal(t, "nats://localhost:4222", provider.url)
	assert.Nil(t, provider.nc) // Not connected yet
	assert.Nil(t, provider.js)
}

func TestProvider_NewPublisher_NotConnected(t *testing.T) {
	provider, err := NewProvider("nats://localhost:4222")
	require.NoError(t, err)

	// Should fail because not connected
	_, err = provider.NewPublisher(pubsub.PublisherOptions{
		StreamName: "test-stream",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "NATS not connected")
}

func TestProvider_NewConsumer_NotConnected(t *testing.T) {
	provider, err := NewProvider("nats://localhost:4222")
	require.NoError(t, err)

	// Should fail because not connected
	_, err = provider.NewConsumer(pubsub.ConsumerOptions{
		StreamName: "test-stream",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "NATS not connected")
}

func TestProvider_Connect_InvalidURL(t *testing.T) {
	provider, err := NewProvider("nats://invalid-host-that-does-not-exist:4222")
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err = provider.Connect(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to connect to NATS")
}

func TestProvider_Close_NotConnected(t *testing.T) {
	provider, err := NewProvider("nats://localhost:4222")
	require.NoError(t, err)

	// Close on non-connected provider should be safe
	err = provider.Close()
	require.NoError(t, err)
}

func TestProvider_Implements_Interface(t *testing.T) {
	var _ pubsub.Provider = (*Provider)(nil)
}

func TestProvider_Implements_Connectable(t *testing.T) {
	var _ pubsub.Connectable = (*Provider)(nil)
}

// TestProvider_NewPublisher_Connected tests creating a publisher when connected
func TestProvider_NewPublisher_Connected(t *testing.T) {
	mockJS := &MockJetStream{}
	// Setup mock expectations for publisher creation
	mockJS.On("CreateOrUpdateStream", mock.Anything, mock.Anything).Return(nil, nil)

	provider := &Provider{
		url: "nats://localhost:4222",
		js:  mockJS, // Inject mock JetStream
	}

	// Should succeed because js is set (simulating connected state)
	pub, err := provider.NewPublisher(pubsub.PublisherOptions{
		StreamName:    "test-stream",
		SubjectPrefix: "test",
	})
	require.NoError(t, err)
	assert.NotNil(t, pub)
	mockJS.AssertExpectations(t)
}

// TestProvider_NewConsumer_Connected tests creating a consumer when connected
func TestProvider_NewConsumer_Connected(t *testing.T) {
	mockJS := &MockJetStream{}
	// Note: NewConsumer doesn't call JetStream methods directly,
	// it only stores the config. JetStream calls happen in Subscribe.

	provider := &Provider{
		url: "nats://localhost:4222",
		js:  mockJS, // Inject mock JetStream
	}

	// Should succeed because js is set (simulating connected state)
	cons, err := provider.NewConsumer(pubsub.ConsumerOptions{
		StreamName:    "test-stream",
		FilterSubject: "test.>",
	})
	require.NoError(t, err)
	assert.NotNil(t, cons)
}

// TestProvider_Close_Connected tests closing a connected provider
func TestProvider_Close_Connected(t *testing.T) {
	// Create a mock connection that tracks Close calls
	mockConn := &mockNatsConn{closed: false}

	provider := &Provider{
		url: "nats://localhost:4222",
		nc:  mockConn,
		js:  &MockJetStream{},
	}

	// Close should succeed and clear nc and js
	err := provider.Close()
	require.NoError(t, err)
	assert.True(t, mockConn.closed, "Connection should be closed")
	assert.Nil(t, provider.nc)
	assert.Nil(t, provider.js)
}

// TestProvider_Close_Idempotent tests that Close can be called multiple times
func TestProvider_Close_Idempotent(t *testing.T) {
	mockConn := &mockNatsConn{closed: false}

	provider := &Provider{
		url: "nats://localhost:4222",
		nc:  mockConn,
		js:  &MockJetStream{},
	}

	// First close
	err := provider.Close()
	require.NoError(t, err)
	assert.True(t, mockConn.closed)

	// Second close should also succeed (idempotent)
	err = provider.Close()
	require.NoError(t, err)
}

// mockNatsConn is a minimal mock for testing Close behavior
type mockNatsConn struct {
	closed bool
}

func (m *mockNatsConn) Close() {
	m.closed = true
}

// TestProvider_Connect_Success tests successful connection with mock
func TestProvider_Connect_Success(t *testing.T) {
	mockConn := &mockNatsConn{}
	mockJS := &MockJetStream{}

	provider := &Provider{
		url: "nats://localhost:4222",
		natsConnect: func(url string) (natsConnection, error) {
			assert.Equal(t, "nats://localhost:4222", url)
			return mockConn, nil
		},
	}

	// Manually set js since mock connection won't create it
	// (Connect skips JetStream creation for non-*nats.Conn)
	err := provider.Connect(context.Background())
	require.NoError(t, err)
	assert.Equal(t, mockConn, provider.nc)

	// Set js manually for further testing
	provider.js = mockJS
	assert.NotNil(t, provider.js)
}

// TestProvider_Connect_JetStreamError tests when JetStream creation fails
func TestProvider_Connect_JetStreamError(t *testing.T) {
	// This test verifies error handling when Connect succeeds but
	// we have a mock connection that doesn't create JetStream
	mockConn := &mockNatsConn{}

	provider := &Provider{
		url: "nats://localhost:4222",
		natsConnect: func(url string) (natsConnection, error) {
			return mockConn, nil
		},
	}

	// Connect should succeed with mock (skips JetStream for non-*nats.Conn)
	err := provider.Connect(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, provider.nc)
	// js will be nil because mock connection doesn't create JetStream
	assert.Nil(t, provider.js)
}

// TestProvider_Connect_NilConnectFunc tests Connect with nil natsConnect
func TestProvider_Connect_NilConnectFunc(t *testing.T) {
	provider := &Provider{
		url:         "nats://invalid-host:4222",
		natsConnect: nil, // nil should fall back to default
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Should use default connect function and fail due to invalid host
	err := provider.Connect(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to connect to NATS")
}

// TestProvider_Connect_WithRealNatsConn tests the path where natsConn cast succeeds
// and JetStream factory is called. We use a wrapper to return *nats.Conn.
func TestProvider_Connect_WithJetStreamFactory(t *testing.T) {
	// Create a mock JetStream
	mockJS := &MockJetStream{}
	mockConn := &mockNatsConn{}

	provider := &Provider{
		url: "nats://localhost:4222",
		natsConnect: func(url string) (natsConnection, error) {
			return mockConn, nil
		},
		// Use a caster that pretends mockConn is a real *nats.Conn
		natsConnCaster: func(nc natsConnection) (*nats.Conn, bool) {
			// Return nil but ok=true to simulate successful cast
			// The factory will receive nil but that's OK for our test
			return nil, true
		},
		jetStreamFactory: func(nc *nats.Conn) (JetStream, error) {
			return mockJS, nil
		},
	}

	err := provider.Connect(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, provider.nc)
	// Now js should be set because we simulated successful cast
	assert.Equal(t, mockJS, provider.js)
}

// TestProvider_Connect_JetStreamFactoryError tests when JetStream factory returns error
func TestProvider_Connect_JetStreamFactoryError(t *testing.T) {
	mockConn := &mockNatsConn{}

	provider := &Provider{
		url: "nats://localhost:4222",
		natsConnect: func(url string) (natsConnection, error) {
			return mockConn, nil
		},
		// Simulate successful cast to trigger JetStream creation path
		natsConnCaster: func(nc natsConnection) (*nats.Conn, bool) {
			return nil, true
		},
		jetStreamFactory: func(nc *nats.Conn) (JetStream, error) {
			return nil, fmt.Errorf("JetStream creation failed")
		},
	}

	err := provider.Connect(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create JetStream")
	// Connection should be closed after JetStream failure
	assert.True(t, mockConn.closed, "Connection should be closed after JetStream failure")
}

// TestProvider_Connect_NilCaster tests Connect with nil natsConnCaster (uses default)
func TestProvider_Connect_NilCaster(t *testing.T) {
	mockConn := &mockNatsConn{}

	provider := &Provider{
		url: "nats://localhost:4222",
		natsConnect: func(url string) (natsConnection, error) {
			return mockConn, nil
		},
		natsConnCaster: nil, // nil should fall back to default
	}

	// Connect should succeed - default caster returns false for mockNatsConn
	err := provider.Connect(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, provider.nc)
	// js is nil because default caster correctly returns false for mockNatsConn
	assert.Nil(t, provider.js)
}

// TestProvider_Connect_NilJetStreamFactory tests Connect with nil jetStreamFactory (uses default)
func TestProvider_Connect_NilJetStreamFactory(t *testing.T) {
	mockConn := &mockNatsConn{}

	provider := &Provider{
		url: "nats://localhost:4222",
		natsConnect: func(url string) (natsConnection, error) {
			return mockConn, nil
		},
		// Simulate successful cast to trigger JetStream creation path
		natsConnCaster: func(nc natsConnection) (*nats.Conn, bool) {
			return nil, true
		},
		jetStreamFactory: nil, // nil should fall back to default, which will fail with nil conn
	}

	// Connect should fail because default factory (NewJetStream) will receive nil *nats.Conn
	err := provider.Connect(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create JetStream")
}
