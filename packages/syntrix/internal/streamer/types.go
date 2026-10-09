// Package streamer provides centralized fan-out and subscription matching.
// It replaces the pull-based CSP package with a subscription-based model.
//
// Public types: Filter, OperationType, Event, EventDelivery
// Internal types are in internal/ subdirectories.
package streamer

import (
	"github.com/codetreker/syntrix/pkg/model"
)

// OperationType defines the type of change operation.
type OperationType int

const (
	// OperationUnspecified is the default unspecified operation.
	OperationUnspecified OperationType = iota
	// OperationInsert indicates a document was created.
	OperationInsert
	// OperationUpdate indicates a document was updated.
	OperationUpdate
	// OperationDelete indicates a document was deleted.
	OperationDelete
)

// String returns the string representation of the operation type.
func (o OperationType) String() string {
	switch o {
	case OperationInsert:
		return "insert"
	case OperationUpdate:
		return "update"
	case OperationDelete:
		return "delete"
	default:
		return "unspecified"
	}
}

// Event represents a change event delivered to subscribers.
type Event struct {
	// EventID is the unique identifier for this event.
	EventID string

	// Database is the database identifier.
	Database string

	// Collection is the collection name.
	Collection string

	// DocumentID is the document identifier.
	DocumentID string

	// Operation is the type of change operation.
	Operation OperationType

	// Document is the full document data in model format.
	// Contains "id", "version", "updatedAt", "createdAt", "collection" plus user data.
	// May be nil for delete operations.
	Document model.Document

	// Timestamp is the Unix timestamp in milliseconds.
	Timestamp int64
}

// EventDelivery wraps an event with the matching subscription IDs.
type EventDelivery struct {
	// SubscriptionIDs are the subscriptions that matched this event.
	SubscriptionIDs []string

	// Event is the matched change event.
	Event *Event
}
