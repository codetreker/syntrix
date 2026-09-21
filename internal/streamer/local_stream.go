package streamer

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/syntrixbase/syntrix/pkg/model"
)

// localStream implements the Stream interface for local (in-process) communication.
// It provides direct synchronous calls to the subscription handler, avoiding
// unnecessary message serialization and channel passing.
type localStream struct {
	ctx       context.Context
	cancel    context.CancelFunc
	gatewayID string
	handler   subscriptionHandler

	// outgoing delivers events to the consumer
	outgoing chan *EventDelivery

	closed   bool
	closedMu sync.Mutex
}

func newLocalStream(ctx context.Context, gatewayID string, handler subscriptionHandler) *localStream {
	ctx, cancel := context.WithCancel(ctx)
	return &localStream{
		ctx:       ctx,
		cancel:    cancel,
		gatewayID: gatewayID,
		handler:   handler,
		outgoing:  make(chan *EventDelivery, 1000),
	}
}

func (ls *localStream) registrationError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ls.closedMu.Lock()
	defer ls.closedMu.Unlock()
	if ls.closed {
		return io.EOF
	}
	return ls.ctx.Err()
}

func (ls *localStream) Subscribe(ctx context.Context, database, collection string, filters []model.Filter) (Registration, error) {
	if err := ls.registrationError(ctx); err != nil {
		return Registration{}, err
	}
	id, err := ls.handler.subscribe(ls.gatewayID, database, collection, filters)
	if err != nil {
		return Registration{}, err
	}
	// Registration can race service retirement or caller cancellation. Remove
	// the new subscription even if the stream's normal API is already closed.
	if err := ls.registrationError(ctx); err != nil {
		return Registration{}, errors.Join(err, ls.handler.unsubscribe(id))
	}
	return Registration{ID: id, Generation: 1}, nil
}

func (ls *localStream) Status() StreamStatus {
	ls.closedMu.Lock()
	defer ls.closedMu.Unlock()
	// The fixed local generation has only one transition. The context's Done
	// signal and Err are synchronized by context itself, including cancellation
	// inherited from the service or caller; no observer goroutine is required.
	status := StreamStatus{State: StateConnected, Generation: 1, Changed: ls.ctx.Done()}
	if err := ls.ctx.Err(); err != nil {
		status.State = StateDisconnected
		status.Terminal = true
		status.Err = err
	}
	return status
}

// Unsubscribe removes a subscription by ID.
func (ls *localStream) Unsubscribe(subscriptionID string) error {
	ls.closedMu.Lock()
	if ls.closed {
		ls.closedMu.Unlock()
		return io.EOF
	}
	ls.closedMu.Unlock()

	return ls.handler.unsubscribe(subscriptionID)
}

// Recv receives an EventDelivery from the Streamer.
func (ls *localStream) Recv() (*EventDelivery, error) {
	select {
	case delivery, ok := <-ls.outgoing:
		if !ok {
			return nil, io.EOF
		}
		return delivery, nil
	case <-ls.ctx.Done():
		return nil, ls.ctx.Err()
	}
}

// Close closes the stream and releases resources.
func (ls *localStream) Close() error {
	ls.closedMu.Lock()
	defer ls.closedMu.Unlock()

	if !ls.closed {
		ls.closed = true
		ls.cancel()
	}
	return nil
}

func (ls *localStream) close() {
	ls.closedMu.Lock()
	if !ls.closed {
		ls.closed = true
		ls.cancel()
	}
	ls.closedMu.Unlock()
}

// Compile-time check
var _ Stream = (*localStream)(nil)
