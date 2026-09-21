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
	broadcast chan hubDelivery

	// Subscription tracking
	subscriptions   map[string]*SubscriptionInfo
	subscriptionsMu sync.RWMutex

	stream                  streamer.Stream
	streamMu                sync.Mutex
	streamOwner             *hubStreamOwner
	replicaMu               sync.Mutex
	replicaOwners           map[*hubReplicaOwner]struct{}
	replicaSubscriptions    map[string]*hubReplicaOwner
	replicaConnections      map[*hubReplicaConnection]struct{}
	replicaCleanupAdmission func() (func(), bool)
	replicaCleanupSlots     chan struct{}
	closed                  bool

	// Register requests from the clients.
	register chan *Client

	// Unregister requests from clients.
	unregister chan *Client

	mu sync.RWMutex

	runCtx   context.Context
	runCtxMu sync.RWMutex
}

type SubscriptionInfo struct {
	Client       *Client
	ClientSubID  string // Client's subscription ID (for protocol)
	registration hubRegistration
}

type hubDelivery struct {
	delivery *streamer.EventDelivery
	owner    *hubStreamOwner
}

func NewHub() *Hub {
	return &Hub{
		broadcast:            make(chan hubDelivery, 16),
		register:             make(chan *Client),
		unregister:           make(chan *Client),
		clients:              make(map[*Client]bool),
		subscriptions:        make(map[string]*SubscriptionInfo),
		replicaOwners:        make(map[*hubReplicaOwner]struct{}),
		replicaSubscriptions: make(map[string]*hubReplicaOwner),
		replicaConnections:   make(map[*hubReplicaConnection]struct{}),
		replicaCleanupSlots:  make(chan struct{}, 256),
	}
}

func (h *Hub) Run(ctx context.Context) {
	h.setRunCtx(ctx)

	for {
		select {
		case <-ctx.Done():
			h.streamMu.Lock()
			h.closed = true
			h.streamMu.Unlock()
			h.invalidateReplicaConnections(ctx.Err())
			h.shutdownClients()
			return
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
		case client := <-h.unregister:
			h.mu.Lock()
			_, present := h.clients[client]
			if present {
				delete(h.clients, client)
			}
			h.mu.Unlock()
			if present {
				client.closeOutbound()
			}
		case delivery := <-h.broadcast:
			h.deliverLegacy(delivery)
		}
	}
}

type hubRegistration struct {
	ID          string
	owner       *hubStreamOwner
	generation  uint64
	releaseOnce *sync.Once
}

func (h *Hub) RegisterSubscription(registration hubRegistration, client *Client, clientSubID string) bool {
	h.streamMu.Lock()
	valid := !h.closed && h.streamOwner == registration.owner
	if valid && registration.owner != nil {
		status := registration.owner.stream.Status()
		valid = !registration.owner.retired.Load() && !status.Terminal && status.State == streamer.StateConnected && status.Generation == registration.generation
	}
	if valid {
		h.subscriptionsMu.Lock()
		h.subscriptions[registration.ID] = &SubscriptionInfo{Client: client, ClientSubID: clientSubID, registration: registration}
		h.subscriptionsMu.Unlock()
	}
	h.streamMu.Unlock()
	if !valid && registration.owner != nil {
		h.ReleaseSubscription(registration)
	}
	return valid
}

func (h *Hub) ReleaseSubscription(registration hubRegistration) {
	release := func() {
		h.subscriptionsMu.Lock()
		if info := h.subscriptions[registration.ID]; info != nil && info.registration.owner == registration.owner &&
			info.registration.generation == registration.generation && info.registration.releaseOnce == registration.releaseOnce {
			delete(h.subscriptions, registration.ID)
		}
		h.subscriptionsMu.Unlock()
		if registration.owner != nil {
			h.cleanupReplicaRegistration(registration.owner, registration.ID)
		}
	}
	if registration.releaseOnce != nil {
		registration.releaseOnce.Do(release)
	} else {
		release()
	}
}

func (h *Hub) SetStream(s streamer.Stream) {
	h.streamMu.Lock()
	old := h.streamOwner
	if h.stream == s {
		h.streamMu.Unlock()
		return
	}
	h.stream = s
	if s == nil {
		h.streamOwner = nil
	} else {
		h.streamOwner = &hubStreamOwner{stream: s, generation: s.Status().Generation}
	}
	if old != nil {
		h.subscriptionsMu.Lock()
		h.subscriptions = make(map[string]*SubscriptionInfo)
		h.subscriptionsMu.Unlock()
	}
	h.streamMu.Unlock()
	if old != nil {
		h.invalidateReplicaOwner(old, errReplicaStreamChanged, 0)
		h.shutdownClients()
	}
}

func (h *Hub) SubscribeToStream(database, collection string, filters []model.Filter) (hubRegistration, error) {
	h.streamMu.Lock()
	owner := h.streamOwner
	h.streamMu.Unlock()
	if owner == nil {
		return hubRegistration{}, fmt.Errorf("stream not initialized")
	}
	h.runCtxMu.RLock()
	parent := h.runCtx
	h.runCtxMu.RUnlock()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	registration, err := owner.stream.Subscribe(ctx, database, collection, filters)
	if err != nil {
		return hubRegistration{}, err
	}
	h.streamMu.Lock()
	status := owner.stream.Status()
	valid := h.streamOwner == owner && !owner.retired.Load() && !h.closed && registration.ID != "" && !status.Terminal && status.State == streamer.StateConnected && status.Generation == registration.Generation
	h.streamMu.Unlock()
	if !valid {
		h.cleanupReplicaRegistration(owner, registration.ID)
		return hubRegistration{}, errReplicaStreamChanged
	}
	return hubRegistration{ID: registration.ID, owner: owner, generation: registration.Generation, releaseOnce: &sync.Once{}}, nil
}

func (h *Hub) BroadcastDelivery(delivery *streamer.EventDelivery) {
	h.streamMu.Lock()
	s := h.stream
	h.streamMu.Unlock()
	h.BroadcastStreamDelivery(s, delivery)
}

func (h *Hub) BroadcastStreamDelivery(stream streamer.Stream, delivery *streamer.EventDelivery) {
	if delivery == nil {
		return
	}
	select {
	case <-h.Done():
		return
	default:
	}
	h.streamMu.Lock()
	if stream != h.stream {
		h.streamMu.Unlock()
		return
	}
	owner := h.streamOwner
	h.streamMu.Unlock()
	h.deliverReplicaFor(owner, delivery)
	h.streamMu.Lock()
	if h.streamOwner != owner {
		h.streamMu.Unlock()
		return
	}
	h.subscriptionsMu.RLock()
	affected := make(map[*Client]struct{})
	for _, id := range delivery.SubscriptionIDs {
		if info := h.subscriptions[id]; info != nil {
			affected[info.Client] = struct{}{}
		}
	}
	h.subscriptionsMu.RUnlock()
	h.streamMu.Unlock()
	if len(affected) == 0 {
		return
	}
	// Queue overload must be visible to the affected legacy consumers. Retiring
	// their transports preserves independent replica wakes without a silent gap.
	select {
	case h.broadcast <- hubDelivery{delivery: delivery, owner: owner}:
	case <-h.Done():
	default:
		h.retireLegacyOverflow(owner, affected)
	}
}

func (h *Hub) retireLegacyOverflow(owner *hubStreamOwner, clients map[*Client]struct{}) {
	h.streamMu.Lock()
	if h.streamOwner != owner {
		h.streamMu.Unlock()
		return
	}
	h.mu.Lock()
	for client := range clients {
		delete(h.clients, client)
	}
	h.mu.Unlock()
	h.streamMu.Unlock()
	for client := range clients {
		client.closeOutbound()
	}
}

func (h *Hub) deliverLegacy(queued hubDelivery) {
	h.streamMu.Lock()
	if queued.owner != h.streamOwner {
		h.streamMu.Unlock()
		return
	}
	h.subscriptionsMu.RLock()
	delivery := queued.delivery
	subscriptions := make([]*SubscriptionInfo, 0, len(delivery.SubscriptionIDs))
	for _, id := range delivery.SubscriptionIDs {
		if info := h.subscriptions[id]; info != nil {
			subscriptions = append(subscriptions, info)
		}
	}
	h.subscriptionsMu.RUnlock()
	h.streamMu.Unlock()
	if len(subscriptions) == 0 {
		return
	}
	event := eventDeliveryToStorageEvent(delivery)
	for _, info := range subscriptions {
		client := info.Client
		client.mu.Lock()
		sub, ok := client.subscriptions[info.ClientSubID]
		client.mu.Unlock()
		if !ok {
			continue
		}
		var document map[string]interface{}
		if sub.IncludeData {
			document = flattenDocument(event.Document)
		}
		payload := EventPayload{SubID: info.ClientSubID, Delta: PublicEvent{Type: event.Type, Document: document, ID: event.Id, Timestamp: event.Timestamp}}
		message := BaseMessage{Type: TypeEvent, Payload: mustMarshal(payload)}
		client.enqueue(message, 50*time.Millisecond)
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
	clients := make([]*Client, 0, len(h.clients))
	for client := range h.clients {
		clients = append(clients, client)
		delete(h.clients, client)
	}
	h.mu.Unlock()
	for _, client := range clients {
		client.closeOutbound()
	}
}
