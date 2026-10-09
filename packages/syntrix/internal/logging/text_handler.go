// internal/logging/text_handler.go
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"
)

// TextHandler implements a custom text format handler for slog.
// Format: <TIME>: [<LEVEL>] <MSG> <attributes>
// Example: 2024-01-19T10:30:00Z: [INFO] server started port=8080
type TextHandler struct {
	w      io.Writer
	level  slog.Leveler
	attrs  []slog.Attr
	groups []string
	mu     sync.Mutex
}

// NewTextHandler creates a new custom text handler.
func NewTextHandler(w io.Writer, opts *slog.HandlerOptions) *TextHandler {
	h := &TextHandler{
		w:     w,
		level: slog.LevelInfo,
	}
	if opts != nil {
		if opts.Level != nil {
			h.level = opts.Level
		}
	}
	return h
}

// Enabled reports whether the handler handles records at the given level.
func (h *TextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	minLevel := slog.LevelInfo
	if h.level != nil {
		minLevel = h.level.Level()
	}
	return level >= minLevel
}

// Handle formats and writes the log record.
// Format: <TIME>: [<LEVEL>] <MSG> <attributes>
func (h *TextHandler) Handle(ctx context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	buf := make([]byte, 0, 1024)

	// Add timestamp in RFC3339 format
	buf = append(buf, r.Time.Format(time.RFC3339)...)

	// Add separator and level
	buf = append(buf, ": ["...)
	buf = append(buf, r.Level.String()...)
	buf = append(buf, "] "...)

	// Add message
	buf = append(buf, r.Message...)

	// Add handler-level attributes
	for _, attr := range h.attrs {
		buf = h.appendAttr(buf, attr, h.groups)
	}

	// Add record attributes
	r.Attrs(func(attr slog.Attr) bool {
		buf = h.appendAttr(buf, attr, h.groups)
		return true
	})

	// Add newline
	buf = append(buf, '\n')

	// Write to output
	_, err := h.w.Write(buf)
	return err
}

// WithAttrs returns a new handler with additional attributes.
func (h *TextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	// Create a new handler with copied state
	newAttrs := make([]slog.Attr, len(h.attrs)+len(attrs))
	copy(newAttrs, h.attrs)
	copy(newAttrs[len(h.attrs):], attrs)

	newGroups := make([]string, len(h.groups))
	copy(newGroups, h.groups)

	return &TextHandler{
		w:      h.w,
		level:  h.level,
		attrs:  newAttrs,
		groups: newGroups,
	}
}

// WithGroup returns a new handler with a group name appended.
func (h *TextHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}

	newAttrs := make([]slog.Attr, len(h.attrs))
	copy(newAttrs, h.attrs)

	newGroups := make([]string, len(h.groups)+1)
	copy(newGroups, h.groups)
	newGroups[len(h.groups)] = name

	return &TextHandler{
		w:      h.w,
		level:  h.level,
		attrs:  newAttrs,
		groups: newGroups,
	}
}

// appendAttr appends a single attribute to the buffer.
func (h *TextHandler) appendAttr(buf []byte, attr slog.Attr, groups []string) []byte {
	// Resolve the value to handle LogValuer interface
	attr.Value = attr.Value.Resolve()

	// Skip empty attributes
	if attr.Equal(slog.Attr{}) {
		return buf
	}

	buf = append(buf, ' ')

	// Add group prefix if any
	for _, group := range groups {
		buf = append(buf, group...)
		buf = append(buf, '.')
	}

	// Add key
	buf = append(buf, attr.Key...)
	buf = append(buf, '=')

	// Add value
	return h.appendValue(buf, attr.Value)
}

// appendValue appends a value to the buffer.
func (h *TextHandler) appendValue(buf []byte, v slog.Value) []byte {
	switch v.Kind() {
	case slog.KindString:
		return h.appendString(buf, v.String())
	case slog.KindInt64:
		return append(buf, fmt.Sprintf("%d", v.Int64())...)
	case slog.KindUint64:
		return append(buf, fmt.Sprintf("%d", v.Uint64())...)
	case slog.KindFloat64:
		return append(buf, fmt.Sprintf("%g", v.Float64())...)
	case slog.KindBool:
		return append(buf, fmt.Sprintf("%t", v.Bool())...)
	case slog.KindDuration:
		return append(buf, v.Duration().String()...)
	case slog.KindTime:
		return append(buf, v.Time().Format(time.RFC3339)...)
	case slog.KindGroup:
		attrs := v.Group()
		if len(attrs) == 0 {
			return buf
		}
		buf = append(buf, '{')
		for i, attr := range attrs {
			if i > 0 {
				buf = append(buf, ' ')
			}
			buf = append(buf, attr.Key...)
			buf = append(buf, '=')
			buf = h.appendValue(buf, attr.Value)
		}
		buf = append(buf, '}')
		return buf
	case slog.KindAny:
		return append(buf, fmt.Sprintf("%+v", v.Any())...)
	default:
		return append(buf, fmt.Sprintf("%+v", v.Any())...)
	}
}

// appendString appends a string value, quoting it if necessary.
func (h *TextHandler) appendString(buf []byte, s string) []byte {
	// Check if string needs quoting (contains spaces or special chars)
	needsQuote := false
	for _, r := range s {
		if r == ' ' || r == '"' || r == '\n' || r == '\t' || r == '\\' {
			needsQuote = true
			break
		}
	}

	if !needsQuote {
		return append(buf, s...)
	}

	// Quote the string
	buf = append(buf, '"')
	for _, r := range s {
		switch r {
		case '"':
			buf = append(buf, '\\', '"')
		case '\\':
			buf = append(buf, '\\', '\\')
		case '\n':
			buf = append(buf, '\\', 'n')
		case '\t':
			buf = append(buf, '\\', 't')
		default:
			buf = append(buf, string(r)...)
		}
	}
	buf = append(buf, '"')
	return buf
}
