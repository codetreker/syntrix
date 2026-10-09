// Package memory provides an in-memory pubsub implementation for standalone mode.
package memory

import "errors"

var (
	// ErrEngineClosed is returned when operating on a closed engine.
	ErrEngineClosed = errors.New("engine is closed")

	// ErrPatternSubscribed is returned when a pattern already has a subscriber.
	ErrPatternSubscribed = errors.New("pattern already has a subscriber")
)
