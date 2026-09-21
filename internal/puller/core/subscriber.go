// Package core implements the local puller service.
package core

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/syntrixbase/syntrix/internal/puller/cursor"
	"github.com/syntrixbase/syntrix/internal/puller/events"
	"github.com/syntrixbase/syntrix/internal/puller/normalizer"
)

// Subscriber represents an active subscription to the event stream.
type Subscriber struct {
	// ID is the consumer ID for logging/monitoring.
	ID string

	// After is the initial position to start from.
	After *cursor.ProgressMarker

	// CoalesceOnCatchUp enables catch-up coalescing.
	CoalesceOnCatchUp bool

	// currentPos tracks the current position for each backend.
	currentPos *cursor.ProgressMarker

	// lastClusterTime tracks the timestamp of the last sent event per backend.
	lastClusterTime map[string]events.ClusterTime
	deliveredIDs    map[string]map[string]struct{}

	// mu protects currentPos.
	mu sync.RWMutex

	// done is closed when the subscriber is terminated.
	done chan struct{}

	// ch receives events for this subscriber.
	ch chan *events.StoreChangeEvent

	// recoveryMu serializes live admission with recovery state transitions.
	recoveryMu      sync.Mutex
	recoveryPending bool
	recovery        chan struct{}
}

// NewSubscriber creates a new subscriber.
func NewSubscriber(id string, after *cursor.ProgressMarker, coalesceOnCatchUp bool, channelSize int) (*Subscriber, error) {
	if after == nil {
		after = cursor.NewProgressMarker()
	}
	if channelSize <= 0 {
		channelSize = 10000
	}
	floors := make(map[string]events.ClusterTime, len(after.Positions))
	for backend, eventID := range after.Positions {
		if eventID == "" {
			continue
		}
		timestamp, err := normalizer.ParseEventID(eventID)
		if err != nil {
			return nil, fmt.Errorf("invalid progress for backend %q: %w", backend, err)
		}
		floors[backend] = timestamp
	}
	return &Subscriber{
		ID:                id,
		After:             after,
		CoalesceOnCatchUp: coalesceOnCatchUp,
		currentPos:        after.Clone(),
		lastClusterTime:   floors,
		deliveredIDs:      make(map[string]map[string]struct{}),
		done:              make(chan struct{}),
		// Increase buffer size to handle transient spikes and avoid flapping between live and catchup modes.
		// 10000 events * ~1KB/event ~= 10MB memory per subscriber.
		ch:       make(chan *events.StoreChangeEvent, channelSize),
		recovery: make(chan struct{}, 1),
	}, nil
}

// enqueue admits an event to live delivery. Once the queue overflows, all later
// events remain in retained history until the consumer begins recovery.
func (s *Subscriber) enqueue(event *events.StoreChangeEvent) (admitted, recoveryStarted bool) {
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()

	if s.recoveryPending {
		return false, false
	}
	select {
	case s.ch <- event:
		return true, false
	default:
		s.recoveryPending = true
		select {
		case s.recovery <- struct{}{}:
		default:
		}
		return false, true
	}
}

// Recovery returns a level-triggered notification that wakes an idle consumer.
func (s *Subscriber) Recovery() <-chan struct{} {
	return s.recovery
}

// BeginRecovery clears the recovery state before retained replay starts. Any
// later overflow publishes a new notification and fences subsequent live events.
func (s *Subscriber) BeginRecovery() {
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()

	s.recoveryPending = false
	select {
	case <-s.recovery:
	default:
	}
}

// RecoveryPending reports whether retained replay is required.
func (s *Subscriber) RecoveryPending() bool {
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()
	return s.recoveryPending
}

// UpdatePosition updates the current position for a backend.
func (s *Subscriber) UpdatePosition(backend, eventID string, clusterTime events.ClusterTime) {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, exists := s.lastClusterTime[backend]
	if !exists || clusterTime.Compare(previous) != 0 || s.deliveredIDs[backend] == nil {
		s.deliveredIDs[backend] = make(map[string]struct{})
	}
	s.currentPos.SetPosition(backend, eventID)
	s.lastClusterTime[backend] = clusterTime
	s.deliveredIDs[backend][eventID] = struct{}{}
}

// ShouldSend distinguishes event identities within the current timestamp group.
// A resumed boundary group starts with no acknowledged identities and is replayed.
func (s *Subscriber) ShouldSend(backend, eventID string, clusterTime events.ClusterTime) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	last, exists := s.lastClusterTime[backend]
	if !exists {
		return true
	}
	switch clusterTime.Compare(last) {
	case -1:
		return false
	case 1:
		return true
	default:
		_, delivered := s.deliveredIDs[backend][eventID]
		return !delivered
	}
}

// CurrentProgress returns the current progress marker.
func (s *Subscriber) CurrentProgress() *cursor.ProgressMarker {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentPos.Clone()
}

// Events returns the channel of events for this subscriber.
func (s *Subscriber) Events() <-chan *events.StoreChangeEvent {
	return s.ch
}

// Done returns a channel that is closed when the subscriber is terminated.
func (s *Subscriber) Done() <-chan struct{} {
	return s.done
}

// Close terminates the subscriber.
func (s *Subscriber) Close() {
	select {
	case <-s.done:
		// Already closed
	default:
		close(s.done)
	}
}

// SubscriberManager manages active subscribers.
type SubscriberManager struct {
	subscribers map[*Subscriber]struct{}
	logger      *slog.Logger
	mu          sync.RWMutex
}

// NewSubscriberManager creates a new subscriber manager.
func NewSubscriberManager(logger *slog.Logger) *SubscriberManager {
	if logger == nil {
		logger = slog.Default()
	}
	return &SubscriberManager{
		subscribers: make(map[*Subscriber]struct{}),
		logger:      logger.With("component", "subscriber-manager"),
	}
}

// Add adds a subscriber.
func (m *SubscriberManager) Add(sub *Subscriber) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subscribers[sub] = struct{}{}
}

// Remove removes a subscriber.
func (m *SubscriberManager) Remove(sub *Subscriber) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.subscribers[sub]; ok {
		sub.Close()
		delete(m.subscribers, sub)
	}
}

// Count returns the number of active subscribers.
func (m *SubscriberManager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.subscribers)
}

// All returns a snapshot of all subscribers.
func (m *SubscriberManager) All() []*Subscriber {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*Subscriber, 0, len(m.subscribers))
	for sub := range m.subscribers {
		result = append(result, sub)
	}
	return result
}

// CloseAll closes all subscribers.
func (m *SubscriberManager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for sub := range m.subscribers {
		sub.Close()
	}
	m.subscribers = make(map[*Subscriber]struct{})
}

// Broadcast sends an event to all subscribers.
func (m *SubscriberManager) Broadcast(be *events.StoreChangeEvent) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for sub := range m.subscribers {
		if _, recoveryStarted := sub.enqueue(be); recoveryStarted {
			m.logger.Warn("slow consumer requires retained replay", "consumerId", sub.ID)
		}
	}
}
