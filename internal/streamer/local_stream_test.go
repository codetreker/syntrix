package streamer

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/pkg/model"
)

func TestLocalStream_Recv_ContextCanceled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)

	ls := &localStream{
		ctx:      ctx,
		outgoing: make(chan *EventDelivery), // unbuffered
	}

	// Cancel the context immediately
	cancel()

	// Recv should return context error
	_, err := ls.Recv()
	assert.Error(t, err)
}

type localRegistrationHandler struct {
	onSubscribe   func() (string, error)
	onUnsubscribe func(string) error
}

func (h localRegistrationHandler) subscribe(_, _, _ string, _ []model.Filter) (string, error) {
	return h.onSubscribe()
}

func (h localRegistrationHandler) unsubscribe(id string) error { return h.onUnsubscribe(id) }

func TestLocalStream_RegistrationCancellation(t *testing.T) {
	for _, retirement := range []string{"caller", "stream", "close"} {
		t.Run(retirement, func(t *testing.T) {
			owner, cancelOwner := context.WithCancel(context.Background())
			defer cancelOwner()
			caller, cancelCaller := context.WithCancel(context.Background())
			defer cancelCaller()
			var ls *localStream
			var removed string
			rollbackErr := errors.New("rollback failed")
			handler := localRegistrationHandler{
				onSubscribe: func() (string, error) {
					switch retirement {
					case "caller":
						cancelCaller()
					case "stream":
						cancelOwner()
					case "close":
						require.NoError(t, ls.Close())
					}
					return "registered", nil
				},
				onUnsubscribe: func(id string) error { removed = id; return rollbackErr },
			}
			ls = newLocalStream(owner, "gateway", handler)
			defer ls.Close()
			registration, err := ls.Subscribe(caller, "db", "collection", nil)
			require.ErrorIs(t, err, rollbackErr)
			if retirement == "close" {
				require.ErrorIs(t, err, io.EOF)
			} else {
				require.ErrorIs(t, err, context.Canceled)
			}
			require.Equal(t, Registration{}, registration)
			require.Equal(t, "registered", removed)
		})
	}
}

func TestLocalStream_RegistrationLifetime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls int
	ls := newLocalStream(context.Background(), "gateway", localRegistrationHandler{
		onSubscribe:   func() (string, error) { calls++; return "registered", nil },
		onUnsubscribe: func(string) error { t.Fatal("successful registration must survive caller cancellation"); return nil },
	})
	defer ls.Close()
	registration, err := ls.Subscribe(ctx, "db", "collection", nil)
	require.NoError(t, err)
	require.Equal(t, Registration{ID: "registered", Generation: 1}, registration)
	cancel()
	require.False(t, ls.Status().Terminal)
	_, err = ls.Subscribe(ctx, "db", "collection", nil)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, calls)
}

func TestLocalStream_Status(t *testing.T) {
	for _, retirement := range []string{"context", "close", "remove"} {
		t.Run(retirement, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ls := newLocalStream(ctx, "gateway", nil)
			before := ls.Status()
			require.Equal(t, StateConnected, before.State)
			require.Equal(t, uint64(1), before.Generation)
			require.False(t, before.Terminal)
			select {
			case <-before.Changed:
				t.Fatal("premature invalidation")
			default:
			}
			switch retirement {
			case "context":
				cancel()
			case "close":
				require.NoError(t, ls.Close())
			case "remove":
				ls.close()
			}
			select {
			case <-before.Changed:
			case <-time.After(time.Second):
				t.Fatal("missing invalidation")
			}
			after := ls.Status()
			require.True(t, after.Terminal)
			require.Equal(t, StateDisconnected, after.State)
			require.Equal(t, before.Generation, after.Generation)
			require.ErrorIs(t, after.Err, context.Canceled)
			require.NoError(t, ls.Close())
		})
	}
}

func TestLocalStream_Recv_ClosedChannel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	ls := &localStream{
		ctx:      ctx,
		outgoing: make(chan *EventDelivery, 10),
	}

	// Close outgoing channel
	close(ls.outgoing)

	// Recv should return EOF
	_, err := ls.Recv()
	assert.ErrorIs(t, err, io.EOF)
}
