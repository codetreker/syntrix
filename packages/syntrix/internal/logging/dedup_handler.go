// internal/logging/dedup_handler.go
package logging

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/cespare/xxhash/v2"
)

// sharedState holds the shared mutable state for deduplication
// All DedupHandler instances created via WithAttrs/WithGroup share the same state
type sharedState struct {
	mu         sync.Mutex
	dedupMap   map[uint64]*dedupEntry
	dedupOrder []uint64
	closed     bool
}

// DedupHandler wraps a slog.Handler and deduplicates identical log entries
// before they get timestamps added. This prevents logs with identical content
// but different timestamps from being treated as different logs.
type DedupHandler struct {
	handler     slog.Handler
	state       *sharedState // Pointer to shared mutable state
	flushTicker *time.Ticker
	stopChan    chan struct{}
	wg          *sync.WaitGroup

	// Config (immutable, can be copied)
	batchSize    int
	flushTimeout time.Duration

	// Attributes from WithAttrs (for hash computation)
	presetAttrs []slog.Attr
	groups      []string
}

// dedupEntry tracks duplicate log occurrences
// Stores the handler that should be used to output this record
type dedupEntry struct {
	record  slog.Record
	handler slog.Handler // The handler to use when flushing this record
	count   int
}

// DedupHandlerConfig holds configuration for DedupHandler
type DedupHandlerConfig struct {
	// BatchSize is the number of unique entries to accumulate before flushing (default: 100)
	BatchSize int
	// FlushTimeout is the max time to wait before flushing (default: 1s)
	FlushTimeout time.Duration
}

// DefaultDedupHandlerConfig returns default configuration
func DefaultDedupHandlerConfig() DedupHandlerConfig {
	return DedupHandlerConfig{
		BatchSize:    100,
		FlushTimeout: 1 * time.Second,
	}
}

// NewDedupHandler creates a new deduplicating handler with default config
func NewDedupHandler(handler slog.Handler) *DedupHandler {
	return NewDedupHandlerWithConfig(handler, DefaultDedupHandlerConfig())
}

// NewDedupHandlerWithConfig creates a new deduplicating handler with custom config
func NewDedupHandlerWithConfig(handler slog.Handler, cfg DedupHandlerConfig) *DedupHandler {
	dh := &DedupHandler{
		handler: handler,
		state: &sharedState{
			dedupMap:   make(map[uint64]*dedupEntry),
			dedupOrder: make([]uint64, 0, cfg.BatchSize),
		},
		flushTicker:  time.NewTicker(cfg.FlushTimeout),
		stopChan:     make(chan struct{}),
		wg:           &sync.WaitGroup{},
		batchSize:    cfg.BatchSize,
		flushTimeout: cfg.FlushTimeout,
		presetAttrs:  nil,
		groups:       nil,
	}

	dh.wg.Add(1)
	go dh.flushLoop()

	return dh
}

// Enabled reports whether the handler handles records at the given level.
func (h *DedupHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.handler.Enabled(ctx, level)
}

// Handle deduplicates and buffers log records
func (h *DedupHandler) Handle(ctx context.Context, r slog.Record) error {
	// Compute hash of record content (excluding time)
	// Include preset attrs and groups in the hash
	key := h.hashRecord(r)

	h.state.mu.Lock()
	defer h.state.mu.Unlock()

	// Check if already closed
	if h.state.closed {
		// Fallback: directly send to handler without dedup
		return h.handler.Handle(ctx, r)
	}

	if entry, exists := h.state.dedupMap[key]; exists {
		// Duplicate found, increment count
		entry.count++
	} else {
		// New entry - store record AND the handler to use for this record
		h.state.dedupMap[key] = &dedupEntry{
			record:  r.Clone(),
			handler: h.handler, // Store the handler that should output this record
			count:   1,
		}
		h.state.dedupOrder = append(h.state.dedupOrder, key)

		// Flush if batch is full
		if len(h.state.dedupOrder) >= h.batchSize {
			h.flushBatchLocked()
		}
	}

	return nil
}

// hashRecord computes a hash of the record content (level, message, attributes)
// excluding the timestamp. Includes preset attributes and groups.
func (h *DedupHandler) hashRecord(r slog.Record) uint64 {
	hash := xxhash.New()

	// Hash level
	hash.WriteString(r.Level.String())
	hash.WriteString("|")

	// Hash message
	hash.WriteString(r.Message)
	hash.WriteString("|")

	// Hash groups (important for distinguishing loggers with different groups)
	for _, g := range h.groups {
		hash.WriteString("g:")
		hash.WriteString(g)
		hash.WriteString("|")
	}

	// Hash preset attributes (from WithAttrs)
	for _, a := range h.presetAttrs {
		hash.WriteString(a.Key)
		hash.WriteString("=")
		hash.WriteString(a.Value.String())
		hash.WriteString("|")
	}

	// Hash record attributes
	r.Attrs(func(a slog.Attr) bool {
		hash.WriteString(a.Key)
		hash.WriteString("=")
		hash.WriteString(a.Value.String())
		hash.WriteString("|")
		return true
	})

	return hash.Sum64()
}

// flushLoop periodically flushes the deduplicated logs
func (h *DedupHandler) flushLoop() {
	defer h.wg.Done()

	for {
		select {
		case <-h.flushTicker.C:
			h.state.mu.Lock()
			if len(h.state.dedupOrder) > 0 && !h.state.closed {
				h.flushBatchLocked()
			}
			h.state.mu.Unlock()

		case <-h.stopChan:
			// Graceful shutdown: flush remaining logs even if closed
			h.state.mu.Lock()
			if len(h.state.dedupOrder) > 0 {
				h.flushBatchLocked()
			}
			h.state.mu.Unlock()
			return
		}
	}
}

// flushBatchLocked writes all deduplicated entries to the underlying handler
// Must be called with h.state.mu locked. Temporarily releases lock during handler calls.
func (h *DedupHandler) flushBatchLocked() {
	if len(h.state.dedupOrder) == 0 {
		return
	}

	// Collect all entries to flush while holding the lock
	// Each entry contains both the record AND the handler to use
	type flushEntry struct {
		record  slog.Record
		handler slog.Handler
	}
	entries := make([]flushEntry, 0, len(h.state.dedupOrder))

	for _, key := range h.state.dedupOrder {
		entry := h.state.dedupMap[key]

		// Safety check: entry might be nil in rare conditions
		if entry == nil {
			continue
		}

		// Clone the record to avoid modifying the original
		r := entry.record.Clone()

		if entry.count > 1 {
			// Add repeat count as an attribute
			r.AddAttrs(slog.Int("repeated_count", entry.count))
		}

		entries = append(entries, flushEntry{
			record:  r,
			handler: entry.handler, // Use the handler stored with this entry
		})
	}

	// Clear the batch before releasing the lock
	h.state.dedupMap = make(map[uint64]*dedupEntry)
	h.state.dedupOrder = h.state.dedupOrder[:0]

	// Release the lock before calling the underlying handlers
	// This prevents deadlock if the handler logs anything
	h.state.mu.Unlock()

	// Send each record to its corresponding handler
	for _, e := range entries {
		_ = e.handler.Handle(context.Background(), e.record)
	}

	// Re-acquire the lock before returning
	h.state.mu.Lock()
}

// WithAttrs returns a new handler with additional attributes.
func (h *DedupHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}

	// Merge preset attrs
	newPresetAttrs := make([]slog.Attr, len(h.presetAttrs)+len(attrs))
	copy(newPresetAttrs, h.presetAttrs)
	copy(newPresetAttrs[len(h.presetAttrs):], attrs)

	// Copy groups
	newGroups := make([]string, len(h.groups))
	copy(newGroups, h.groups)

	return &DedupHandler{
		handler:      h.handler.WithAttrs(attrs),
		state:        h.state, // Share state pointer
		flushTicker:  h.flushTicker,
		stopChan:     h.stopChan,
		wg:           h.wg,
		batchSize:    h.batchSize,
		flushTimeout: h.flushTimeout,
		presetAttrs:  newPresetAttrs,
		groups:       newGroups,
	}
}

// WithGroup returns a new handler with a group name appended.
func (h *DedupHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}

	// Copy preset attrs
	newPresetAttrs := make([]slog.Attr, len(h.presetAttrs))
	copy(newPresetAttrs, h.presetAttrs)

	// Append group
	newGroups := make([]string, len(h.groups)+1)
	copy(newGroups, h.groups)
	newGroups[len(h.groups)] = name

	return &DedupHandler{
		handler:      h.handler.WithGroup(name),
		state:        h.state, // Share state pointer
		flushTicker:  h.flushTicker,
		stopChan:     h.stopChan,
		wg:           h.wg,
		batchSize:    h.batchSize,
		flushTimeout: h.flushTimeout,
		presetAttrs:  newPresetAttrs,
		groups:       newGroups,
	}
}

// Close gracefully shuts down the deduplicating handler
func (h *DedupHandler) Close() error {
	h.state.mu.Lock()
	if h.state.closed {
		h.state.mu.Unlock()
		return nil // Already closed
	}
	h.state.closed = true
	h.state.mu.Unlock()

	// Stop the ticker
	h.flushTicker.Stop()

	// Signal stop and wait
	close(h.stopChan)
	h.wg.Wait()

	return nil
}
