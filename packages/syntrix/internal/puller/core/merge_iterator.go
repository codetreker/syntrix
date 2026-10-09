package core

import (
	"container/heap"

	"github.com/codetreker/syntrix/internal/puller/events"
)

// MergeIterator merges multiple event iterators into a single ordered stream.
// Events are ordered by ClusterTime, then by Backend name.
type MergeIterator struct {
	iterators []events.Iterator
	pq        priorityQueue
	err       error
	current   *events.StoreChangeEvent
}

// NewMergeIterator creates a new MergeIterator.
func NewMergeIterator(iters []events.Iterator) *MergeIterator {
	mi := &MergeIterator{
		iterators: iters,
		pq:        make(priorityQueue, 0, len(iters)),
	}

	// Initialize priority queue with the first event from each iterator
	for i, iter := range iters {
		if iter.Next() {
			heap.Push(&mi.pq, &item{
				event:  iter.Event(),
				iterID: i,
			})
		} else {
			if err := iter.Err(); err != nil {
				mi.err = err
				// If initialization fails, we should probably stop?
				// Or just ignore this iterator?
				// For now, let's record error.
			}
		}
	}

	return mi
}

// Next advances to the next event.
func (mi *MergeIterator) Next() bool {
	if mi.err != nil {
		return false
	}

	if len(mi.pq) == 0 {
		return false
	}

	// Pop the smallest item
	top := heap.Pop(&mi.pq).(*item)
	mi.current = top.event

	// Advance the iterator that the item came from
	iter := mi.iterators[top.iterID]
	if iter.Next() {
		heap.Push(&mi.pq, &item{
			event:  iter.Event(),
			iterID: top.iterID,
		})
	} else {
		if err := iter.Err(); err != nil {
			mi.err = err
			return false
		}
	}

	return true
}

// Event returns the current event.
func (mi *MergeIterator) Event() *events.StoreChangeEvent {
	return mi.current
}

// Err returns any error encountered.
func (mi *MergeIterator) Err() error {
	return mi.err
}

// Close closes all underlying iterators.
func (mi *MergeIterator) Close() error {
	var firstErr error
	for _, iter := range mi.iterators {
		if err := iter.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// priorityQueue implements heap.Interface
type item struct {
	event  *events.StoreChangeEvent
	iterID int
}

type priorityQueue []*item

func (pq priorityQueue) Len() int { return len(pq) }

func (pq priorityQueue) Less(i, j int) bool {
	// Order by ClusterTime
	cmp := pq[i].event.ClusterTime.Compare(pq[j].event.ClusterTime)
	if cmp < 0 {
		return true
	}
	if cmp > 0 {
		return false
	}
	// Tie-break by Backend name
	return pq[i].event.Backend < pq[j].event.Backend
}

func (pq priorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
}

func (pq *priorityQueue) Push(x interface{}) {
	*pq = append(*pq, x.(*item))
}

func (pq *priorityQueue) Pop() interface{} {
	old := *pq
	n := len(old)
	item := old[n-1]
	*pq = old[0 : n-1]
	return item
}
