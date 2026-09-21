package realtime

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/syntrixbase/syntrix/internal/streamer"
)

var errReplicaStreamChanged = errors.New("replica streamer ownership changed")
var errReplicaCapacity = errors.New("replica streamer work capacity exhausted")

type hubStreamOwner struct {
	stream     streamer.Stream
	generation uint64
	retired    atomic.Bool
}

type hubReplicaOwner struct {
	stream        *hubStreamOwner
	generation    uint64
	id            string
	active        atomic.Bool
	cancel        context.CancelFunc
	onChanged     func()
	onInvalidated func(error)
}

type hubReplicaConnection struct {
	stream     *hubStreamOwner
	generation uint64
	close      func()
}

func (h *Hub) setReplicaCleanupAdmission(admit func() (func(), bool)) {
	h.replicaMu.Lock()
	h.replicaCleanupAdmission = admit
	h.replicaMu.Unlock()
}

// Connection ownership starts before authentication or any source subscription.
func (h *Hub) RegisterReplicaConnection(closeConnection func()) (func(), bool) {
	h.streamMu.Lock()
	owner := h.streamOwner
	if owner == nil || h.closed || owner.retired.Load() {
		h.streamMu.Unlock()
		return nil, false
	}
	status := owner.stream.Status()
	if status.Terminal || status.State != streamer.StateConnected {
		h.streamMu.Unlock()
		return nil, false
	}
	connection := &hubReplicaConnection{stream: owner, generation: status.Generation, close: closeConnection}
	h.replicaMu.Lock()
	h.replicaConnections[connection] = struct{}{}
	h.replicaMu.Unlock()
	h.streamMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			h.replicaMu.Lock()
			delete(h.replicaConnections, connection)
			h.replicaMu.Unlock()
		})
	}, true
}

func (h *Hub) SubscribeReplica(ctx context.Context, namespace, collection string, onChanged func(), onInvalidated func(error)) (func(), error) {
	request, cancel := context.WithTimeout(ctx, 10*time.Second)
	h.streamMu.Lock()
	stream := h.streamOwner
	if stream == nil || h.closed || stream.retired.Load() {
		h.streamMu.Unlock()
		cancel()
		return nil, errReplicaStreamChanged
	}
	status := stream.stream.Status()
	if status.Terminal || status.State != streamer.StateConnected {
		h.streamMu.Unlock()
		cancel()
		return nil, errReplicaStreamChanged
	}
	owner := &hubReplicaOwner{stream: stream, generation: status.Generation, cancel: cancel, onChanged: onChanged, onInvalidated: onInvalidated}
	owner.active.Store(true)
	h.replicaMu.Lock()
	h.replicaOwners[owner] = struct{}{}
	h.replicaMu.Unlock()
	h.streamMu.Unlock()
	registration, err := stream.stream.Subscribe(request, namespace, collection, nil)
	if err != nil {
		h.releaseReplicaOwner(owner, nil)
		return nil, err
	}
	h.streamMu.Lock()
	current := stream.stream.Status()
	h.replicaMu.Lock()
	valid := request.Err() == nil && owner.active.Load() && !stream.retired.Load() && registration.ID != "" && h.streamOwner == stream && !h.closed &&
		!current.Terminal && current.State == streamer.StateConnected && current.Generation == registration.Generation && owner.generation == registration.Generation
	if valid {
		owner.id = registration.ID
		h.replicaSubscriptions[registration.ID] = owner
	}
	h.replicaMu.Unlock()
	h.streamMu.Unlock()
	if !valid {
		h.releaseReplicaOwner(owner, nil)
		h.cleanupReplicaRegistration(stream, registration.ID)
		return nil, errReplicaStreamChanged
	}
	// The registration deadline must not become the lifetime of an acknowledged
	// source. Its caller explicitly releases the returned owner on cancellation.
	cancel()
	return func() { h.releaseReplicaOwner(owner, nil) }, nil
}

func (h *Hub) releaseReplicaOwner(owner *hubReplicaOwner, reason error) {
	if !owner.active.CompareAndSwap(true, false) {
		return
	}
	owner.cancel()
	h.replicaMu.Lock()
	delete(h.replicaOwners, owner)
	id := owner.id
	if id != "" && h.replicaSubscriptions[id] == owner {
		delete(h.replicaSubscriptions, id)
	}
	h.replicaMu.Unlock()
	if reason != nil && owner.onInvalidated != nil {
		owner.onInvalidated(reason)
	}
	if id != "" {
		h.cleanupReplicaRegistration(owner.stream, id)
	}
}

func (h *Hub) deliverReplica(delivery *streamer.EventDelivery) {
	h.streamMu.Lock()
	owner := h.streamOwner
	h.streamMu.Unlock()
	h.deliverReplicaFor(owner, delivery)
}

func (h *Hub) deliverReplicaFor(stream *hubStreamOwner, delivery *streamer.EventDelivery) {
	if delivery == nil {
		return
	}
	h.replicaMu.Lock()
	owners := make([]*hubReplicaOwner, 0, len(delivery.SubscriptionIDs))
	for _, id := range delivery.SubscriptionIDs {
		if owner := h.replicaSubscriptions[id]; owner != nil && owner.stream == stream {
			owners = append(owners, owner)
		}
	}
	h.replicaMu.Unlock()
	for _, owner := range owners {
		if owner.active.Load() && owner.onChanged != nil {
			owner.onChanged()
		}
	}
}

// Status snapshots include the next-change signal, so the server supervisor can
// call this for every observed transition without a subscription callback gap.
func (h *Hub) InvalidateReplicaStream(stream streamer.Stream, status streamer.StreamStatus) {
	h.streamMu.Lock()
	owner := h.streamOwner
	if owner == nil || owner.stream != stream {
		h.streamMu.Unlock()
		return
	}
	status = stream.Status()
	changed := status.Terminal || status.State != streamer.StateConnected || owner.generation != status.Generation
	owner.generation = status.Generation
	h.streamMu.Unlock()
	if !changed {
		return
	}
	reason := status.Err
	if reason == nil {
		reason = errReplicaStreamChanged
	}
	keepGeneration := uint64(0)
	if status.State == streamer.StateConnected && !status.Terminal {
		keepGeneration = status.Generation
	}
	h.invalidateReplicaOwner(owner, reason, keepGeneration)
	if status.Terminal {
		h.subscriptionsMu.Lock()
		h.subscriptions = make(map[string]*SubscriptionInfo)
		h.subscriptionsMu.Unlock()
		h.shutdownClients()
	}
}

func (h *Hub) invalidateReplicaOwner(stream *hubStreamOwner, reason error, keepGeneration uint64) {
	h.replicaMu.Lock()
	owners := make([]*hubReplicaOwner, 0)
	connections := make([]func(), 0)
	for owner := range h.replicaOwners {
		if owner.stream == stream && (keepGeneration == 0 || owner.generation != keepGeneration) {
			owners = append(owners, owner)
		}
	}
	for connection := range h.replicaConnections {
		if connection.stream == stream && (keepGeneration == 0 || connection.generation != keepGeneration) {
			delete(h.replicaConnections, connection)
			connections = append(connections, connection.close)
		}
	}
	h.replicaMu.Unlock()
	for _, owner := range owners {
		h.releaseReplicaOwner(owner, reason)
	}
	for _, closeConnection := range connections {
		closeConnection()
	}
}

func (h *Hub) invalidateReplicaConnections(reason error) {
	h.replicaMu.Lock()
	owners := make([]*hubReplicaOwner, 0, len(h.replicaOwners))
	for owner := range h.replicaOwners {
		owners = append(owners, owner)
	}
	connections := make([]func(), 0, len(h.replicaConnections))
	for connection := range h.replicaConnections {
		delete(h.replicaConnections, connection)
		connections = append(connections, connection.close)
	}
	h.replicaMu.Unlock()
	for _, owner := range owners {
		h.releaseReplicaOwner(owner, reason)
	}
	for _, closeConnection := range connections {
		closeConnection()
	}
}

func (h *Hub) cleanupReplicaRegistration(owner *hubStreamOwner, id string) {
	if id == "" || owner.retired.Load() || owner.stream.Status().Terminal {
		return
	}
	h.replicaMu.Lock()
	admit := h.replicaCleanupAdmission
	h.replicaMu.Unlock()
	var release func()
	var ok bool
	if admit != nil {
		release, ok = admit()
	} else {
		select {
		case h.replicaCleanupSlots <- struct{}{}:
			release, ok = func() { <-h.replicaCleanupSlots }, true
		default:
		}
	}
	if !ok {
		h.retireReplicaStream(owner, errReplicaCapacity)
		return
	}
	go func() {
		defer release()
		// Unsubscribe has no context. The timer retires the actual Stream; the
		// permit remains held until its blocked worker really exits.
		timer := time.AfterFunc(10*time.Second, func() { h.retireReplicaStream(owner, context.DeadlineExceeded) })
		defer timer.Stop()
		if err := owner.stream.Unsubscribe(id); err != nil {
			h.retireReplicaStream(owner, err)
		}
	}()
}

func (h *Hub) retireReplicaStream(owner *hubStreamOwner, reason error) {
	if !owner.retired.CompareAndSwap(false, true) {
		return
	}
	h.invalidateReplicaOwner(owner, reason, 0)
	slog.Warn("Replica backend cleanup retired its stream", "component", "realtime", "phase", "backend-cleanup", "error", reason)
	go func() {
		if err := owner.stream.Close(); err != nil {
			slog.Warn("Replica backend close failed", "component", "realtime", "phase", "backend-close", "error", err)
		}
	}()
}
