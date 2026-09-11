// Package buffer provides event buffering with PebbleDB persistence.
package buffer

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/cockroachdb/pebble"
	"github.com/syntrixbase/syntrix/internal/puller/events"
)

// Iterator provides ordered iteration over events.
type Iterator interface {
	// Next advances to the next event. Returns false when done.
	Next() bool

	// Event returns the current event.
	Event() *events.StoreChangeEvent

	// Key returns the current buffer key.
	Key() string

	// Err returns any error encountered during iteration.
	Err() error

	// Close releases the iterator resources.
	Close() error
}

type bufferIterator struct {
	iter  *pebble.Iterator
	evt   *events.StoreChangeEvent
	key   string
	err   error
	first bool
}

func (i *bufferIterator) Next() bool {
	if i.err != nil {
		return false
	}

	for {
		var valid bool
		if i.first {
			valid = i.iter.First()
			i.first = false
		} else {
			valid = i.iter.Next()
		}

		if !valid {
			i.err = i.iter.Error()
			return false
		}

		if isMetadataKey(i.iter.Key()) {
			continue
		}

		i.key = string(i.iter.Key())
		value := i.iter.Value()

		evt, err := events.UnmarshalEvent(value)
		if err != nil {
			i.err = fmt.Errorf("failed to unmarshal event: %w", err)
			return false
		}

		i.evt = evt
		return true
	}
}

func (i *bufferIterator) Event() *events.StoreChangeEvent {
	return i.evt
}

func (i *bufferIterator) Key() string {
	return i.key
}

func (i *bufferIterator) Err() error {
	return i.err
}

func (i *bufferIterator) Close() error {
	if i.iter != nil {
		err := i.iter.Close()
		i.iter = nil
		return err
	}
	return nil
}

// newSnapshotIterator returns an iterator over pending/flushing events after the given key.
func (b *Buffer) newSnapshotIterator(afterKey string) Iterator {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.newSnapshotIteratorLocked(afterKey)
}

// Holding mu across the disk snapshot and queue copy prevents a committed batch
// from disappearing from the queue before it becomes visible to that snapshot.
func (b *Buffer) newSnapshotIteratorLocked(afterKey string) Iterator {
	var requests []*writeRequest
	for _, queue := range [][]*writeRequest{b.flushing, b.pending} {
		for _, req := range queue {
			if req.event != nil && string(req.key) > afterKey {
				requests = append(requests, req)
			}
		}
	}
	// Arrival order within one cluster time need not match the event-ID suffix.
	// Sort the copied queue without changing capture or checkpoint order.
	slices.SortStableFunc(requests, func(a, b *writeRequest) int {
		return strings.Compare(string(a.key), string(b.key))
	})
	iter := &sliceIterator{index: -1}
	for _, req := range requests {
		iter.events = append(iter.events, req.event)
		iter.keys = append(iter.keys, string(req.key))
	}
	return iter
}

type sliceIterator struct {
	events []*events.StoreChangeEvent
	keys   []string
	index  int
}

func (i *sliceIterator) Next() bool {
	if i.index < len(i.events)-1 {
		i.index++
		return true
	}
	return false
}

func (i *sliceIterator) Event() *events.StoreChangeEvent {
	if i.index >= 0 && i.index < len(i.events) {
		return i.events[i.index]
	}
	return nil
}

func (i *sliceIterator) Key() string {
	if i.index >= 0 && i.index < len(i.keys) {
		return i.keys[i.index]
	}
	return ""
}

func (i *sliceIterator) Err() error {
	return nil
}

func (i *sliceIterator) Close() error {
	return nil
}

// deduplicatingIterator merges sorted sources and emits each full event key once.
// A disk snapshot and its queue copy can overlap while a batch commits.
type deduplicatingIterator struct {
	iterators   []Iterator
	ready       []bool
	initialized bool
	current     Iterator
	lastYield   string
	err         error
}

func newDeduplicatingIterator(iters ...Iterator) *deduplicatingIterator {
	return &deduplicatingIterator{iterators: iters, ready: make([]bool, len(iters))}
}

func (i *deduplicatingIterator) Next() bool {
	i.current = nil
	if i.err != nil {
		return false
	}
	for n, iter := range i.iterators {
		if !i.initialized {
			i.ready[n] = iter.Next()
		} else {
			for i.ready[n] && iter.Key() == i.lastYield {
				i.ready[n] = iter.Next()
			}
		}
		if err := iter.Err(); err != nil {
			i.err = err
			i.current = nil
			return false
		}
		if i.ready[n] && (i.current == nil || iter.Key() < i.current.Key()) {
			i.current = iter
		}
	}
	i.initialized = true
	if i.current == nil {
		return false
	}
	i.lastYield = i.current.Key()
	return true
}

func (i *deduplicatingIterator) Event() *events.StoreChangeEvent {
	if i.current != nil {
		return i.current.Event()
	}
	return nil
}

func (i *deduplicatingIterator) Key() string {
	if i.current != nil {
		return i.current.Key()
	}
	return ""
}

func (i *deduplicatingIterator) Err() error {
	return i.err
}

func (i *deduplicatingIterator) Close() error {
	var err error
	for _, it := range i.iterators {
		err = errors.Join(err, it.Close())
	}
	i.iterators = nil
	i.current = nil
	return err
}
