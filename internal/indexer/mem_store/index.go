// Package index provides the in-memory index implementation.
package mem_store

import (
	"bytes"
	"sync"

	"github.com/google/btree"
)

// DocRef represents a document reference with its OrderKey.
type DocRef struct {
	ID       string // Document ID within the collection
	OrderKey []byte // Encoded sort key
}

// btreeItem wraps DocRef for btree storage.
type btreeItem struct {
	orderKey []byte
	id       string
}

// Index represents an index for a specific (pattern, templateID) pair.
// It maintains a btree for ordered iteration and a ByID map for O(1) lookups.
type Index struct {
	Pattern    string // Normalized pattern (e.g., "users/*/chats")
	TemplateID string // Template identity (name or fields signature)
	RawPattern string // Original pattern (e.g., "users/{uid}/chats")

	mu    sync.RWMutex
	state State // Current index state
	tree  *btree.BTreeG[btreeItem]
	byID  map[string][]byte // docID -> OrderKey
}

// lessFunc is the comparison function for btree items.
// Items are sorted by orderKey first, then by id for deterministic ordering
// when multiple documents have the same orderKey.
func lessFunc(a, b btreeItem) bool {
	cmp := bytes.Compare(a.orderKey, b.orderKey)
	if cmp != 0 {
		return cmp < 0
	}
	return a.id < b.id
}

// NewIndex creates a new index.
func NewIndex(pattern, templateID, rawPattern string) *Index {
	return &Index{
		Pattern:    pattern,
		TemplateID: templateID,
		RawPattern: rawPattern,
		tree:       btree.NewG[btreeItem](32, lessFunc), // degree 32 for good performance
		byID:       make(map[string][]byte),
	}
}

// Upsert inserts or updates a document in the index.
// If the document already exists, it removes the old entry first.
func (idx *Index) Upsert(id string, orderKey []byte) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	// Remove existing entry if present
	if oldKey, ok := idx.byID[id]; ok {
		idx.tree.Delete(btreeItem{orderKey: oldKey, id: id})
	}

	orderKey = bytes.Clone(orderKey)
	// Insert new entry
	idx.tree.ReplaceOrInsert(btreeItem{orderKey: orderKey, id: id})
	idx.byID[id] = orderKey
}

// Delete removes a document from the index.
func (idx *Index) Delete(id string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if oldKey, ok := idx.byID[id]; ok {
		idx.tree.Delete(btreeItem{orderKey: oldKey, id: id})
		delete(idx.byID, id)
	}
}

// SearchOptions configures a search operation.
type SearchOptions struct {
	Lower      []byte // Lower bound (inclusive)
	Upper      []byte // Upper bound (exclusive, nil means no upper bound)
	StartAfter []byte // Cursor for pagination (exclusive)
	Limit      int    // Max results to return
}

// Search returns documents within the specified bounds.
// Results are returned in OrderKey order.
func (idx *Index) Search(opts SearchOptions) []DocRef {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	var results []DocRef
	limit := opts.Limit
	if limit <= 0 {
		limit = 1000 // default limit
	}

	// Determine starting point
	startKey := opts.Lower
	isStartAfter := false
	if opts.StartAfter != nil && bytes.Compare(opts.StartAfter, startKey) >= 0 {
		startKey = opts.StartAfter
		isStartAfter = true
	}

	// Create iterator starting point
	startItem := btreeItem{orderKey: startKey}

	idx.tree.AscendGreaterOrEqual(startItem, func(item btreeItem) bool {
		// Skip the startAfter cursor itself (exclusive)
		if isStartAfter && bytes.Equal(item.orderKey, opts.StartAfter) {
			return true // continue
		}

		// Check upper bound
		if opts.Upper != nil && bytes.Compare(item.orderKey, opts.Upper) >= 0 {
			return false // stop
		}

		results = append(results, DocRef{
			ID:       item.id,
			OrderKey: bytes.Clone(item.orderKey),
		})

		return len(results) < limit
	})

	return results
}

// Get returns the OrderKey for a document by ID.
// Returns nil if the document is not indexed.
func (idx *Index) Get(id string) []byte {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return bytes.Clone(idx.byID[id])
}

// Len returns the number of documents in the index.
func (idx *Index) Len() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.byID)
}

// Clear removes all documents from the index.
func (idx *Index) Clear() {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.tree.Clear(false)
	idx.byID = make(map[string][]byte)
}

// State represents the current state of an index.
type State int

const (
	StateHealthy    State = iota // Index is up-to-date and serving queries
	StateRebuilding              // Index is being rebuilt, queries fall back
	StateFailed                  // Index failed, needs manual intervention
)

// String returns the state name.
func (s State) String() string {
	switch s {
	case StateHealthy:
		return "healthy"
	case StateRebuilding:
		return "rebuilding"
	case StateFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// State returns the current state of the index.
func (idx *Index) State() State {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.state
}

// SetState sets the state of the index.
func (idx *Index) SetState(state State) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.state = state
}

// IsHealthy returns true if the index is in healthy state.
func (idx *Index) IsHealthy() bool {
	return idx.State() == StateHealthy
}
