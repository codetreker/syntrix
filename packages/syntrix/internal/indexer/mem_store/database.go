package mem_store

import (
	"sync"

	"github.com/codetreker/syntrix/internal/indexer/store"
)

// Database represents an isolated index namespace for a database.
// It contains multiple indexes keyed by (pattern|templateID).
type Database struct {
	Name string

	mu      sync.RWMutex
	indexes map[string]*Index // key: pattern|templateID
}

// NewDatabase creates a new database index container.
func NewDatabase(name string) *Database {
	return &Database{
		Name:    name,
		indexes: make(map[string]*Index),
	}
}

// IndexKey generates the index map key from pattern and templateID.
func IndexKey(pattern, templateID string) string {
	return pattern + "|" + templateID
}

// GetIndex returns an index by its key, or nil if not found.
func (d *Database) GetIndex(pattern, templateID string) *Index {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.indexes[IndexKey(pattern, templateID)]
}

// GetOrCreateIndex returns an existing index or creates a new one.
func (d *Database) GetOrCreateIndex(pattern, templateID, rawPattern string) *Index {
	key := IndexKey(pattern, templateID)

	// Fast path: check with read lock
	d.mu.RLock()
	if idx, ok := d.indexes[key]; ok {
		d.mu.RUnlock()
		return idx
	}
	d.mu.RUnlock()

	// Slow path: create with write lock
	d.mu.Lock()
	defer d.mu.Unlock()

	// Double-check after acquiring write lock
	if idx, ok := d.indexes[key]; ok {
		return idx
	}

	idx := NewIndex(pattern, templateID, rawPattern)
	d.indexes[key] = idx
	return idx
}

// DeleteIndex removes an index from the database.
func (d *Database) DeleteIndex(pattern, templateID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.indexes, IndexKey(pattern, templateID))
}

// ListIndexes returns metadata for all indexes in the database.
func (d *Database) ListIndexes() []store.IndexInfo {
	d.mu.RLock()
	defer d.mu.RUnlock()

	indexes := make([]store.IndexInfo, 0, len(d.indexes))
	for _, idx := range d.indexes {
		var state store.IndexState
		switch idx.State() {
		case StateRebuilding:
			state = store.IndexStateRebuilding
		case StateFailed:
			state = store.IndexStateFailed
		default:
			state = store.IndexStateHealthy
		}
		indexes = append(indexes, store.IndexInfo{
			Pattern:    idx.Pattern,
			TemplateID: idx.TemplateID,
			RawPattern: idx.RawPattern,
			State:      state,
			DocCount:   idx.Len(),
		})
	}
	return indexes
}

// IndexCount returns the number of indexes in the database.
func (d *Database) IndexCount() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.indexes)
}

// Clear removes all indexes from the database.
func (d *Database) Clear() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.indexes = make(map[string]*Index)
}
