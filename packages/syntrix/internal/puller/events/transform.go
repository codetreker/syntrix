package events

import (
	"errors"

	"github.com/codetreker/syntrix/internal/core/storage"
)

// EventType represents the type of change operation.
type EventType string

const (
	EventCreate EventType = "create"
	EventUpdate EventType = "update"
	EventDelete EventType = "delete"
)

var (
	ErrDeleteOPIgnored = errors.New("delete operation ignored")
	ErrUnknownOpType   = errors.New("unknown operation type")
)

// SyntrixChangeEvent represents an event that triggers a rule evaluation.
// It is decoupled from the underlying storage event to allow different sources (e.g. Puller).
type SyntrixChangeEvent struct {
	Id         string
	Database   string
	Type       EventType
	Document   *storage.StoredDoc // Using storage.StoredDoc as the common data model
	Before     *storage.StoredDoc
	UpdateDesc *UpdateDescription
	Timestamp  int64
	Progress   string // String progress token from Puller
}

func Transform(pEvent *PullerEvent) (SyntrixChangeEvent, error) {
	if pEvent.Change.OpType == StoreOperationDelete {
		// Syntrix implements delete events differently; ignore here
		return SyntrixChangeEvent{}, ErrDeleteOPIgnored
	}

	evt := SyntrixChangeEvent{
		Id:         pEvent.Change.EventID,
		Database:   pEvent.Change.Database,
		Timestamp:  pEvent.Change.Timestamp,
		UpdateDesc: pEvent.Change.UpdateDesc,
		Progress:   pEvent.Progress,
	}

	// Map OpType
	switch pEvent.Change.OpType {
	case StoreOperationInsert:
		evt.Type = EventCreate
		evt.Document = pEvent.Change.FullDocument
	case StoreOperationUpdate, StoreOperationReplace:
		if pEvent.Change.FullDocument != nil && pEvent.Change.FullDocument.Deleted {
			evt.Type = EventDelete
			// For soft delete, we might have the document
			evt.Document = pEvent.Change.FullDocument
		} else if pEvent.Change.OpType == StoreOperationReplace {
			evt.Type = EventCreate
			evt.Document = pEvent.Change.FullDocument
		} else {
			evt.Type = EventUpdate
			evt.Document = pEvent.Change.FullDocument
		}
	default:
		return SyntrixChangeEvent{}, ErrUnknownOpType
	}

	return evt, nil
}
