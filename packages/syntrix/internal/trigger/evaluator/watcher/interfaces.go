package watcher

import (
	"context"

	"github.com/codetreker/syntrix/internal/puller/events"
)

// DocumentWatcher watches for document changes in the storage.
type DocumentWatcher interface {
	// Watch starts watching for changes.
	// It returns a channel of events or an error if the watch could not be started.
	Watch(ctx context.Context) (<-chan events.SyntrixChangeEvent, error)

	// SaveCheckpoint saves the resume token for the watcher.
	SaveCheckpoint(ctx context.Context, token interface{}) error

	// Close releases resources held by the watcher.
	Close() error
}
