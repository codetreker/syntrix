// Package manager provides subscription management for the Streamer service.
package manager

import (
	"sync"

	pb "github.com/codetreker/syntrix/api/gen/streamer/v1"
	"github.com/google/cel-go/cel"
)

// Subscriber represents a single subscription.
type Subscriber struct {
	ID         string
	GatewayID  string
	Database   string
	Collection string
	Filters    []*pb.Filter // Structured filters from proto
}

// IsExactMatch returns true if this subscription uses exact document ID matching.
// An exact match is a single filter with field="_id" and op="==".
func (s *Subscriber) IsExactMatch() bool {
	if len(s.Filters) != 1 {
		return false
	}
	f := s.Filters[0]
	return f.Field == "_id" && f.Op == "=="
}

// GetDocumentID returns the document ID for exact match subscriptions.
// Returns empty string if not an exact match.
func (s *Subscriber) GetDocumentID() string {
	if !s.IsExactMatch() {
		return ""
	}
	v := s.Filters[0].Value
	if v == nil {
		return ""
	}
	if sv, ok := v.Kind.(*pb.Value_StringValue); ok {
		return sv.StringValue
	}
	return ""
}

// ExpressionSubscriber is a subscriber with a compiled CEL expression.
type ExpressionSubscriber struct {
	*Subscriber
	Program cel.Program
}

// GatewaySubscriptions tracks all subscriptions for a single gateway.
type GatewaySubscriptions struct {
	mu            sync.RWMutex
	subscriptions map[string]*Subscriber // subID -> Subscriber
}

// NewGatewaySubscriptions creates a new GatewaySubscriptions.
func NewGatewaySubscriptions() *GatewaySubscriptions {
	return &GatewaySubscriptions{
		subscriptions: make(map[string]*Subscriber),
	}
}

// Add adds a subscription.
func (gs *GatewaySubscriptions) Add(sub *Subscriber) {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	gs.subscriptions[sub.ID] = sub
}

// Remove removes a subscription by ID.
func (gs *GatewaySubscriptions) Remove(subID string) {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	delete(gs.subscriptions, subID)
}

// Get returns a subscription by ID.
func (gs *GatewaySubscriptions) Get(subID string) (*Subscriber, bool) {
	gs.mu.RLock()
	defer gs.mu.RUnlock()
	sub, ok := gs.subscriptions[subID]
	return sub, ok
}

// List returns all subscriptions.
func (gs *GatewaySubscriptions) List() []*Subscriber {
	gs.mu.RLock()
	defer gs.mu.RUnlock()
	result := make([]*Subscriber, 0, len(gs.subscriptions))
	for _, sub := range gs.subscriptions {
		result = append(result, sub)
	}
	return result
}

// Len returns the number of subscriptions.
func (gs *GatewaySubscriptions) Len() int {
	gs.mu.RLock()
	defer gs.mu.RUnlock()
	return len(gs.subscriptions)
}
