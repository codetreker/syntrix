package core

import (
	"github.com/codetreker/syntrix/internal/puller/buffer"
	"github.com/codetreker/syntrix/internal/puller/events"
)

// CoalescingIterator wraps an iterator and coalesces events in batches.
type CoalescingIterator struct {
	source    events.Iterator
	coalescer *buffer.Coalescer
	buffer    []*events.StoreChangeEvent
	index     int
	err       error
	batchSize int
}

// NewCoalescingIterator creates a new CoalescingIterator.
func NewCoalescingIterator(source events.Iterator, batchSize int) *CoalescingIterator {
	return &CoalescingIterator{
		source:    source,
		coalescer: buffer.NewCoalescer(),
		batchSize: batchSize,
		index:     -1, // Start before first element
	}
}

// Next advances to the next event.
func (i *CoalescingIterator) Next() bool {
	if i.err != nil {
		return false
	}

	// If we have buffered events, advance index
	if i.buffer != nil {
		i.index++
		if i.index < len(i.buffer) {
			return true
		}
		// Buffer exhausted
		i.buffer = nil
		i.index = -1
	}

	// Read batchSize events from source
	count := 0
	for count < i.batchSize && i.source.Next() {
		evt := i.source.Event()
		i.coalescer.Add(evt)
		count++
	}

	if err := i.source.Err(); err != nil {
		i.err = err
		return false
	}

	if count == 0 {
		return false
	}

	// Flush coalescer
	i.buffer = i.coalescer.Flush()
	if len(i.buffer) == 0 {
		// All events cancelled out, try next batch
		return i.Next()
	}

	i.index = 0
	return true
}

// Event returns the current event.
func (i *CoalescingIterator) Event() *events.StoreChangeEvent {
	if i.buffer != nil && i.index >= 0 && i.index < len(i.buffer) {
		return i.buffer[i.index]
	}
	return nil
}

// Err returns any error encountered.
func (i *CoalescingIterator) Err() error {
	return i.err
}

// Close closes the underlying iterator.
func (i *CoalescingIterator) Close() error {
	return i.source.Close()
}
