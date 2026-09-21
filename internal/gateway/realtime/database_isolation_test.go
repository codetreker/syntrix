package realtime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/syntrixbase/syntrix/internal/streamer"
	"github.com/syntrixbase/syntrix/pkg/model"
)

func TestHub_DatabaseIsolation(t *testing.T) {
	hubCtx, hubCancel := context.WithCancel(context.Background())
	defer hubCancel()

	hub := NewTestHub()
	go hub.Run(hubCtx)

	// Client A: Database A
	clientA := &Client{
		hub:            hub,
		send:           make(chan BaseMessage, 10),
		subscriptions:  make(map[string]Subscription),
		streamerSubIDs: make(map[string]hubRegistration),
		database:       "databaseA",
	}
	clientA.subscriptions["subA"] = Subscription{
		Query:       model.Query{Collection: "users"},
		IncludeData: true,
	}
	// Simulate Streamer subscription mapping
	hub.Register(clientA)
	hub.RegisterSubscription(hubRegistration{ID: "stream-sub-A", owner: hub.streamOwner, generation: hub.stream.Status().Generation}, clientA, "subA")

	// Client B: Database B
	clientB := &Client{
		hub:            hub,
		send:           make(chan BaseMessage, 10),
		subscriptions:  make(map[string]Subscription),
		streamerSubIDs: make(map[string]hubRegistration),
		database:       "databaseB",
	}
	clientB.subscriptions["subB"] = Subscription{
		Query:       model.Query{Collection: "users"},
		IncludeData: true,
	}
	hub.Register(clientB)
	hub.RegisterSubscription(hubRegistration{ID: "stream-sub-B", owner: hub.streamOwner, generation: hub.stream.Status().Generation}, clientB, "subB")

	// Client C: Database A (Another client in Database A)
	clientC := &Client{
		hub:            hub,
		send:           make(chan BaseMessage, 10),
		subscriptions:  make(map[string]Subscription),
		streamerSubIDs: make(map[string]hubRegistration),
		database:       "databaseA",
	}
	clientC.subscriptions["subC"] = Subscription{
		Query:       model.Query{Collection: "users"},
		IncludeData: true,
	}
	hub.Register(clientC)
	hub.RegisterSubscription(hubRegistration{ID: "stream-sub-C", owner: hub.streamOwner, generation: hub.stream.Status().Generation}, clientC, "subC")

	// Wait for registration
	time.Sleep(5 * time.Millisecond)

	// 1. Broadcast event for Database A
	// Streamer determines this matches A and C only
	hub.BroadcastDelivery(&streamer.EventDelivery{
		SubscriptionIDs: []string{"stream-sub-A", "stream-sub-C"},
		Event: &streamer.Event{
			Operation:  streamer.OperationInsert,
			EventID:    "databaseA:user1",
			Database:   "databaseA",
			Collection: "users",
			DocumentID: "user1",
			Document:   map[string]interface{}{"name": "User A", "id": "user1"},
			Timestamp:  time.Now().UnixMilli(),
		},
	})

	// Verify Client A received it
	select {
	case msg := <-clientA.send:
		assert.Equal(t, TypeEvent, msg.Type)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Client A should have received the event")
	}

	// Verify Client C received it
	select {
	case msg := <-clientC.send:
		assert.Equal(t, TypeEvent, msg.Type)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Client C should have received the event")
	}

	// Verify Client B did NOT receive it
	select {
	case <-clientB.send:
		t.Fatal("Client B should NOT have received the event for Database A")
	default:
		// OK
	}

	// 2. Broadcast event for Database B
	// Streamer determines this matches B only
	hub.BroadcastDelivery(&streamer.EventDelivery{
		SubscriptionIDs: []string{"stream-sub-B"},
		Event: &streamer.Event{
			Operation:  streamer.OperationInsert,
			EventID:    "databaseB:user2",
			Database:   "databaseB",
			Collection: "users",
			DocumentID: "user2",
			Document:   map[string]interface{}{"name": "User B", "id": "user2"},
			Timestamp:  time.Now().UnixMilli(),
		},
	})

	// Verify Client B received it
	select {
	case msg := <-clientB.send:
		assert.Equal(t, TypeEvent, msg.Type)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Client B should have received the event")
	}

	// Verify Client A did NOT receive it
	select {
	case <-clientA.send:
		t.Fatal("Client A should NOT have received the event for Database B")
	default:
		// OK
	}

	// Verify Client C did NOT receive it
	select {
	case <-clientC.send:
		t.Fatal("Client C should NOT have received the event for Database B")
	default:
		// OK
	}
}

func TestHub_SystemRole_CrossDatabaseAccess(t *testing.T) {
	hubCtx, hubCancel := context.WithCancel(context.Background())
	defer hubCancel()

	hub := NewTestHub()
	go hub.Run(hubCtx)

	// System Client: Has allowAllDatabases = true
	sysClient := &Client{
		hub:               hub,
		send:              make(chan BaseMessage, 10),
		subscriptions:     make(map[string]Subscription),
		streamerSubIDs:    make(map[string]hubRegistration),
		database:          "default", // Primary database
		allowAllDatabases: true,      // Can see all databases
	}
	sysClient.subscriptions["subSys"] = Subscription{
		Query:       model.Query{Collection: "users"},
		IncludeData: true,
	}
	hub.Register(sysClient)
	hub.RegisterSubscription(hubRegistration{ID: "stream-sub-Sys", owner: hub.streamOwner, generation: hub.stream.Status().Generation}, sysClient, "subSys")

	time.Sleep(5 * time.Millisecond)

	// Broadcast event for Database A
	// Streamer says SysClient matches
	hub.BroadcastDelivery(&streamer.EventDelivery{
		SubscriptionIDs: []string{"stream-sub-Sys"},
		Event: &streamer.Event{
			Operation:  streamer.OperationInsert,
			EventID:    "databaseA:user1",
			Database:   "databaseA",
			Collection: "users",
			DocumentID: "user1",
			Document:   map[string]interface{}{"name": "User A", "id": "user1"},
			Timestamp:  time.Now().UnixMilli(),
		},
	})

	// System client SHOULD receive it
	select {
	case msg := <-sysClient.send:
		assert.Equal(t, TypeEvent, msg.Type)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("System client should see event from Database A")
	}

	// Broadcast event for Database B
	// Streamer says SysClient matches
	hub.BroadcastDelivery(&streamer.EventDelivery{
		SubscriptionIDs: []string{"stream-sub-Sys"},
		Event: &streamer.Event{
			Operation:  streamer.OperationInsert,
			EventID:    "databaseB:user2",
			Database:   "databaseB",
			Collection: "users",
			DocumentID: "user2",
			Document:   map[string]interface{}{"name": "User B", "id": "user2"},
			Timestamp:  time.Now().UnixMilli(),
		},
	})

	// System client SHOULD receive it
	select {
	case msg := <-sysClient.send:
		assert.Equal(t, TypeEvent, msg.Type)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("System client should see event from Database B")
	}
}
