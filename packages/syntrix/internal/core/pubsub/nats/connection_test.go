package nats

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

func TestConnectContextCancellationAndFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ConnectContext(ctx, "unused", func(string, ...nats.Option) (*nats.Conn, error) { t.Fatal("canceled context dialed"); return nil, nil })
	require.ErrorIs(t, err, context.Canceled)
	cause := errors.New("credential-bearing connection error")
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = ConnectContext(ctx, "unused", func(_ string, options ...nats.Option) (*nats.Conn, error) {
		opts := nats.GetDefaultOptions()
		for _, option := range options {
			require.NoError(t, option(&opts))
		}
		require.LessOrEqual(t, opts.Timeout, time.Second)
		require.True(t, opts.SkipHostLookup)
		return nil, cause
	})
	require.ErrorIs(t, err, cause)
	_, err = NewJetStream(nil)
	require.Error(t, err)
}
func TestConnectContextCancellationAfterDial(t *testing.T) {
	ctx, _, address := integrationNATS(t)
	startup, cancel := context.WithCancel(ctx)
	_, err := ConnectContext(startup, address, func(url string, options ...nats.Option) (*nats.Conn, error) {
		nc, err := nats.Connect(url, options...)
		cancel()
		return nc, err
	})
	require.ErrorIs(t, err, context.Canceled)
}
func TestStartupDialerUsesContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dialer := startupDialer{ctx: ctx}
	_, err := dialer.Dial("tcp", "127.0.0.1:4222")
	require.ErrorIs(t, err, context.Canceled)
}
func TestStartupDialerCancellationInterruptsHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, listener.Close()) })
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, err := ConnectContext(ctx, "nats://"+listener.Addr().String(), nats.Connect); result <- err }()
	select {
	case conn := <-accepted:
		t.Cleanup(func() { _ = conn.Close() })
	case <-time.After(time.Second):
		t.Fatal("dial did not connect")
	}
	cancel()
	select {
	case err := <-result:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt handshake")
	}
}
func TestStartupDialerReconnectHasIndependentContext(t *testing.T) {
	ctx, _, address := integrationNATS(t)
	startup, cancel := context.WithCancel(ctx)
	var custom nats.CustomDialer
	nc, err := ConnectContext(startup, address, func(url string, options ...nats.Option) (*nats.Conn, error) {
		opts := nats.GetDefaultOptions()
		for _, option := range options {
			require.NoError(t, option(&opts))
		}
		custom = opts.CustomDialer
		return nats.Connect(url, options...)
	})
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	cancel()
	conn, err := custom.Dial("tcp", nc.ConnectedAddr())
	require.NoError(t, err)
	require.NoError(t, conn.Close())
}
func TestProviderConnectionLifecycle(t *testing.T) {
	ctx, _, address := integrationNATS(t)
	binding := managedTestBinding()
	provider, err := NewProvider(address, binding)
	require.NoError(t, err)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	require.ErrorIs(t, provider.Connect(canceled), context.Canceled)
	require.NoError(t, provider.Connect(ctx))
	require.EqualError(t, provider.Connect(ctx), "NATS provider is already connected")
	require.NoError(t, provider.Close())
	require.EqualError(t, provider.Connect(ctx), "NATS provider is closed")
	failed, err := NewProvider("invalid://admin:password@host", binding)
	require.NoError(t, err)
	err = failed.Connect(ctx)
	require.EqualError(t, err, "NATS connection failed")
}
