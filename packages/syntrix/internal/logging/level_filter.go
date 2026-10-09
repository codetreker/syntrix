// internal/logging/level_filter.go
package logging

import (
	"context"
	"log/slog"
)

// LevelFilter wraps a slog.Handler and filters log records by minimum level.
// Only records at or above minLevel are passed to the wrapped handler.
type LevelFilter struct {
	handler  slog.Handler
	minLevel slog.Level
}

// NewLevelFilter creates a new level filter handler.
func NewLevelFilter(handler slog.Handler, minLevel slog.Level) *LevelFilter {
	return &LevelFilter{
		handler:  handler,
		minLevel: minLevel,
	}
}

// Enabled reports whether the handler handles records at the given level.
// It returns false if level is below minLevel, otherwise delegates to the wrapped handler.
func (h *LevelFilter) Enabled(ctx context.Context, level slog.Level) bool {
	if level < h.minLevel {
		return false
	}
	return h.handler.Enabled(ctx, level)
}

// Handle handles the Record by passing it to the wrapped handler if it meets the minimum level.
// Returns nil without calling the wrapped handler if level < minLevel.
func (h *LevelFilter) Handle(ctx context.Context, r slog.Record) error {
	if r.Level < h.minLevel {
		return nil
	}
	return h.handler.Handle(ctx, r)
}

// WithAttrs returns a new LevelFilter with the same minLevel but with
// attrs applied to the wrapped handler.
func (h *LevelFilter) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &LevelFilter{
		handler:  h.handler.WithAttrs(attrs),
		minLevel: h.minLevel,
	}
}

// WithGroup returns a new LevelFilter with the same minLevel but with
// the group applied to the wrapped handler.
func (h *LevelFilter) WithGroup(name string) slog.Handler {
	return &LevelFilter{
		handler:  h.handler.WithGroup(name),
		minLevel: h.minLevel,
	}
}
