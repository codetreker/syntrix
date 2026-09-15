package realtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/streamer"
	"github.com/syntrixbase/syntrix/pkg/model"
)

// Hub maintains the set of active clients and broadcasts messages to the
// clients.
type Hub struct {
	// Registered clients.
	clients map[*Client]bool

	// Inbound messages from the clients.
	broadcast chan *streamer.EventDelivery

	// Subscription tracking
	subscriptions   map[string]*SubscriptionInfo
	subscriptionsMu sync.RWMutex

	stream   streamer.Stream
	streamMu sync.Mutex

	// Register requests from the clients.
	register chan *Client

	// Unregister requests from clients.
	unregister chan *Client

	mu sync.RWMutex

	runCtx   context.Context
	runCtxMu sync.RWMutex
}

type SubscriptionInfo struct {
	Client      *Client
	ClientSubID string // Client's subscription ID (for protocol)
}

func NewHub() *Hub {
	return &Hub{
		broadcast:     make(chan *streamer.EventDelivery),
		register:      make(chan *Client),
		unregister:    make(chan *Client),
		clients:       make(map[*Client]bool),
		subscriptions: make(map[string]*SubscriptionInfo),
	}
}

func (h *Hub) Run(ctx context.Context) {
	h.setRunCtx(ctx)

	for {
		select {
		case <-ctx.Done():
			h.shutdownClients()
			return
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				client.closeOutbound()
			}
			h.mu.Unlock()
		case delivery := <-h.broadcast:
			h.subscriptionsMu.RLock()

			// Convert to storage.Event for compatibility
			storageEvt := eventDeliveryToStorageEvent(delivery)

			// Route to matching subscriptions
			for _, streamerSubID := range delivery.SubscriptionIDs {
				if info, ok := h.subscriptions[streamerSubID]; ok {
					// Send to client using clientSubID
					info.Client.mu.Lock()
					if sub, ok := info.Client.subscriptions[info.ClientSubID]; ok {
						// Build event payload
						var doc map[string]interface{}
						if sub.IncludeData {
							doc = flattenDocument(storageEvt.Document)
						}

						payload := EventPayload{
							SubID: info.ClientSubID, // Use client's subscription ID
							Delta: PublicEvent{
								Type:      storageEvt.Type,
								Document:  doc,
								ID:        storageEvt.Id,
								Timestamp: storageEvt.Timestamp,
							},
						}
						msg := BaseMessage{Type: TypeEvent, Payload: mustMarshal(payload)}

						select {
						case info.Client.send <- msg:
						default:
							select {
							case info.Client.send <- msg:
							case <-time.After(50 * time.Millisecond):
							}
						}
					}
					info.Client.mu.Unlock()
				}
			}
			h.subscriptionsMu.RUnlock()
		}
	}
}

func (h *Hub) RegisterSubscription(streamerSubID string, client *Client, clientSubID string) {
	h.subscriptionsMu.Lock()
	h.subscriptions[streamerSubID] = &SubscriptionInfo{
		Client:      client,
		ClientSubID: clientSubID,
	}
	h.subscriptionsMu.Unlock()
}

func (h *Hub) UnregisterSubscription(streamerSubID string) {
	h.subscriptionsMu.Lock()
	delete(h.subscriptions, streamerSubID)
	h.subscriptionsMu.Unlock()
}

func (h *Hub) SetStream(s streamer.Stream) {
	h.streamMu.Lock()
	h.stream = s
	h.streamMu.Unlock()
}

func (h *Hub) SubscribeToStream(database, collection string, filters []model.Filter) (string, error) {
	h.streamMu.Lock()
	defer h.streamMu.Unlock()
	if h.stream == nil {
		return "", fmt.Errorf("stream not initialized")
	}
	return h.stream.Subscribe(database, collection, filters)
}

func (h *Hub) UnsubscribeFromStream(subID string) error {
	h.streamMu.Lock()
	defer h.streamMu.Unlock()
	if h.stream == nil {
		return fmt.Errorf("stream not initialized")
	}
	return h.stream.Unsubscribe(subID)
}

func (h *Hub) BroadcastDelivery(delivery *streamer.EventDelivery) {
	select {
	case <-h.Done():
		return
	default:
	}

	select {
	case h.broadcast <- delivery:
	case <-h.Done():
	}
}

func (h *Hub) Register(client *Client) bool {
	select {
	case h.register <- client:
		return true
	case <-h.Done():
		return false
	}
}

func (h *Hub) Unregister(client *Client) {
	select {
	case h.unregister <- client:
	case <-h.Done():
	}
}

func flattenDocument(doc *storage.StoredDoc) map[string]interface{} {
	if doc == nil {
		return nil
	}
	flat := make(map[string]interface{})
	// Copy data
	for k, v := range doc.Data {
		flat[k] = v
	}

	// Ensure ID is present (extract from Fullpath)
	if _, ok := flat["id"]; !ok {
		if idx := strings.LastIndex(doc.Fullpath, "/"); idx != -1 {
			flat["id"] = doc.Fullpath[idx+1:]
		}
	}

	// Add system fields
	flat["version"] = doc.Version
	flat["updatedAt"] = doc.UpdatedAt
	flat["createdAt"] = doc.CreatedAt
	flat["collection"] = doc.Collection
	return flat
}

func mustMarshal(v interface{}) []byte {
	b, _ := json.Marshal(v) // Should not fail for internal types
	return b
}

func (h *Hub) setRunCtx(ctx context.Context) {
	h.runCtxMu.Lock()
	h.runCtx = ctx
	h.runCtxMu.Unlock()
}

func (h *Hub) Done() <-chan struct{} {
	h.runCtxMu.RLock()
	defer h.runCtxMu.RUnlock()
	if h.runCtx == nil {
		return nil
	}
	return h.runCtx.Done()
}

func eventDeliveryToStorageEvent(delivery *streamer.EventDelivery) storage.Event {
	evt := storage.Event{
		Id:        fmt.Sprintf("%s/%s", delivery.Event.Collection, delivery.Event.DocumentID),
		Database:  delivery.Event.Database,
		Type:      operationToEventType(delivery.Event.Operation),
		Timestamp: delivery.Event.Timestamp,
	}

	if delivery.Event.Document != nil {
		// Convert model.Document to storage.StoredDoc
		evt.Document = documentToStoredDoc(delivery.Event.Document, delivery.Event.Collection)
	}

	return evt
}

func operationToEventType(op streamer.OperationType) storage.EventType {
	switch op {
	case streamer.OperationInsert:
		return storage.EventCreate
	case streamer.OperationUpdate:
		return storage.EventUpdate
	case streamer.OperationDelete:
		return storage.EventDelete
	default:
		return ""
	}
}

func documentToStoredDoc(doc model.Document, collection string) *storage.StoredDoc {
	// Extract system fields
	id := doc.GetID()
	version := doc.GetVersion()
	updatedAt, _ := doc["updatedAt"].(int64)
	createdAt, _ := doc["createdAt"].(int64)

	// Build Fullpath
	fullpath := fmt.Sprintf("%s/%s", collection, id)

	return &storage.StoredDoc{
		Fullpath:   fullpath,
		Collection: collection,
		Data:       doc, // model.Document is map[string]interface{}
		Version:    version,
		UpdatedAt:  updatedAt,
		CreatedAt:  createdAt,
	}
}

func (h *Hub) shutdownClients() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for client := range h.clients {
		client.closeOutbound()
		delete(h.clients, client)
	}
}
