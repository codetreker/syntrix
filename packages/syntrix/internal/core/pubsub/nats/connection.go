package nats

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type JetStream interface {
	Stream(context.Context, string) (jetstream.Stream, error)
	Consumer(context.Context, string, string) (jetstream.Consumer, error)
	CreateStream(context.Context, jetstream.StreamConfig) (jetstream.Stream, error)
	CreateConsumer(context.Context, string, jetstream.ConsumerConfig) (jetstream.Consumer, error)
	Publish(context.Context, string, []byte, ...jetstream.PublishOpt) (*jetstream.PubAck, error)
}

func NewJetStream(nc *nats.Conn) (JetStream, error) {
	if nc == nil {
		return nil, errors.New("NATS connection cannot be nil")
	}
	return jetstream.New(nc)
}

// Startup cancellation covers dial and handshake; reconnects have independent deadlines.
type startupDialer struct {
	mu    sync.Mutex
	ctx   context.Context
	stops []func() bool
}

func (d *startupDialer) Dial(network, address string) (net.Conn, error) {
	d.mu.Lock()
	ctx := d.ctx
	d.mu.Unlock()
	dialer := net.Dialer{Timeout: nats.DefaultTimeout}
	if ctx == nil {
		ctx = context.Background()
	}
	conn, err := dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	if d.ctx != nil {
		d.stops = append(d.stops, context.AfterFunc(d.ctx, func() { _ = conn.Close() }))
	}
	d.mu.Unlock()
	return conn, nil
}
func ConnectContext(ctx context.Context, url string, connectFn func(string, ...nats.Option) (*nats.Conn, error)) (*nats.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dialer := &startupDialer{ctx: ctx}
	timeout := nats.DefaultTimeout
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, context.DeadlineExceeded
		}
		if remaining < timeout {
			timeout = remaining
		}
	}
	nc, err := connectFn(url, nats.SetCustomDialer(dialer), nats.Timeout(timeout), nats.SkipHostLookup())
	dialer.mu.Lock()
	for _, stop := range dialer.stops {
		stop()
	}
	dialer.ctx = nil
	dialer.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		nc.Close()
		return nil, err
	}
	return nc, nil
}
