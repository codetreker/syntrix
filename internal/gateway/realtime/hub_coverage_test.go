package realtime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/streamer"
	"github.com/syntrixbase/syntrix/pkg/model"
)

func TestHub_OperationToEventType_Coverage(t *testing.T) {
	// Test all conversions
	assert.Equal(t, storage.EventCreate, operationToEventType(streamer.OperationInsert))
	assert.Equal(t, storage.EventUpdate, operationToEventType(streamer.OperationUpdate))
	assert.Equal(t, storage.EventDelete, operationToEventType(streamer.OperationDelete))
	assert.Equal(t, storage.EventType(""), operationToEventType(streamer.OperationType(999)))
}

func TestHub_BroadcastDelivery_Coverage(t *testing.T) {
	h := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	h.setRunCtx(ctx)

	// Simulate Hub loop draining the channel
	go func() {
		for {
			select {
			case <-h.broadcast:
			case <-ctx.Done():
				return
			}
		}
	}()

	done := make(chan bool)
	go func() {
		// Case 1: Hub running, broadcast channel accepts
		delivery := &streamer.EventDelivery{
			Event: &streamer.Event{
				Operation: streamer.OperationInsert,
				Database:  "t1",
				Document:  model.Document{"id": "d1"},
			},
		}

		// Ensure run loop or consumer is draining broadcast channel so it doesn't block?
		// BroadcastDelivery sends to h.broadcast
		// h.broadcast is buffered in NewHub?
		// NewHub: broadcast:  make(chan *streamer.EventDelivery, 256),

		h.BroadcastDelivery(delivery) // Should not block if buffer open

		// Case 2: Hub stopped (Context Done)
		cancel()
		// Wait for context to be recognized
		// Ensure we don't wait forever if Done() isn't closed (it relies on runCtx)
		select {
		case <-h.Done():
		case <-time.After(100 * time.Millisecond):
			// fail?
		}

		// BroadcastDelivery should return immediately if done
		h.BroadcastDelivery(delivery)
		done <- true
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		cancel() // Ensure cleanup
		t.Fatal("TestHub_BroadcastDelivery_Coverage timed out")
	}
}

func TestHub_Unregister_Coverage(t *testing.T) {
	done := make(chan bool)
	go func() {
		// Additional check for Unregister flow when done
		h := NewHub()
		ctx, cancel := context.WithCancel(context.Background())
		h.setRunCtx(ctx)
		cancel() // instantly cancel

		client := &Client{send: make(chan BaseMessage)}
		// Should not block
		h.Unregister(client)
		assert.False(t, h.Register(client))
		done <- true
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("TestHub_Unregister_Coverage timed out")
	}
}

func TestHub_Broadcast_ClientBlocked_Coverage(t *testing.T) {
	done := make(chan bool)
	go func() {
		h := NewHub()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		go h.Run(ctx)

		// Create client with full buffer
		client := &Client{
			send:          make(chan BaseMessage, 1),
			subscriptions: make(map[string]Subscription),
		}
		client.subscriptions["csub1"] = Subscription{}

		// Register subscription manually
		h.RegisterSubscription(hubRegistration{ID: "ssub1"}, client, "csub1")

		// Fill buffer
		client.send <- BaseMessage{Type: "dummy"}

		// Broadcast a message
		delivery := &streamer.EventDelivery{
			SubscriptionIDs: []string{"ssub1"},
			Event: &streamer.Event{
				Operation:  streamer.OperationInsert,
				Database:   "t1",
				Document:   model.Document{"id": "d1"},
				Collection: "test",
			},
		}

		// This sends to hub.broadcast. Hub loop picks it up.
		// Hub loop tries to send to client.send. Blocks. Waits 50ms. Drops.
		h.BroadcastDelivery(delivery)

		// Wait enough time for the 50ms timeout + processing
		time.Sleep(150 * time.Millisecond)

		// Verify client.send still only has 1 message (the original one)
		// If it succeeded, it would block forever waiting for send (if no timeout) or add if buffer was larger
		assert.Equal(t, 1, len(client.send))

		done <- true
	}()

	select {
	case <-done:
	case <-time.After(1000 * time.Millisecond): // Needs > 50ms
		t.Fatal("TestHub_Broadcast_ClientBlocked_Coverage timed out")
	}
}
