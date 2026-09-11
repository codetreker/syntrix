package mem_store

import (
	"errors"
	"sync"

	"github.com/syntrixbase/syntrix/internal/indexer/store"
)

// Store implements store.Store interface using in-memory data structures.
type Store struct {
	mu                        sync.RWMutex
	databases                 map[string]*Database // key: db name
	progress                  string
	partitions                map[store.QueryIndexRef]*projectionIndex
	generations               map[generationKey]store.Generation
	catalogs                  map[string]store.BootstrapCatalog
	catalogInventoryPublished bool
	retiredDatabases          map[string]struct{}
}

// New creates a new in-memory Store.
func New() *Store {
	return &Store{
		databases:        make(map[string]*Database),
		partitions:       make(map[store.QueryIndexRef]*projectionIndex),
		generations:      make(map[generationKey]store.Generation),
		catalogs:         make(map[string]store.BootstrapCatalog),
		retiredDatabases: make(map[string]struct{}),
	}
}

// getOrCreateDB returns an existing database or creates a new one.
func (s *Store) getOrCreateDB(db string) *Database {
	if d, ok := s.databases[db]; ok {
		return d
	}
	d := NewDatabase(db)
	s.databases[db] = d
	return d
}

// Upsert inserts or updates a document in the index.
func (s *Store) Upsert(db, pattern, tmplID, docID string, orderKey []byte, progress string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	d := s.getOrCreateDB(db)
	idx := d.GetOrCreateIndex(pattern, tmplID, pattern)
	idx.Upsert(docID, orderKey)

	if progress != "" {
		s.progress = progress
	}
	return nil
}

// Delete removes a document from the index.
func (s *Store) Delete(db, pattern, tmplID, docID string, progress string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	d, ok := s.databases[db]
	if !ok {
		if progress != "" {
			s.progress = progress
		}
		return nil
	}

	idx := d.GetIndex(pattern, tmplID)
	if idx != nil {
		idx.Delete(docID)
	}

	if progress != "" {
		s.progress = progress
	}
	return nil
}

// Get returns the OrderKey for a document by ID.
func (s *Store) Get(db, pattern, tmplID, docID string) ([]byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	d, ok := s.databases[db]
	if !ok {
		return nil, false
	}

	idx := d.GetIndex(pattern, tmplID)
	if idx == nil {
		return nil, false
	}

	orderKey := idx.Get(docID)
	if orderKey == nil {
		return nil, false
	}

	result := make([]byte, len(orderKey))
	copy(result, orderKey)
	return result, true
}

// Search returns documents within the specified bounds.
func (s *Store) Search(db, pattern, tmplID string, opts store.SearchOptions) ([]store.DocRef, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	d, ok := s.databases[db]
	if !ok {
		return []store.DocRef{}, nil
	}

	idx := d.GetIndex(pattern, tmplID)
	if idx == nil {
		return []store.DocRef{}, nil
	}

	localOpts := SearchOptions{
		Lower:      opts.Lower,
		Upper:      opts.Upper,
		StartAfter: opts.StartAfter,
		Limit:      opts.Limit,
	}

	results := idx.Search(localOpts)

	storeResults := make([]store.DocRef, len(results))
	for i, ref := range results {
		storeResults[i] = store.DocRef{
			ID:       ref.ID,
			OrderKey: ref.OrderKey,
		}
	}

	return storeResults, nil
}

// DeleteIndex removes all data for an index.
func (s *Store) DeleteIndex(db, pattern, tmplID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	d, ok := s.databases[db]
	if !ok {
		return nil
	}

	d.DeleteIndex(pattern, tmplID)
	return nil
}

// DeleteDatabase removes all indexes for a database.
func (s *Store) DeleteDatabase(db string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retiredDatabases[db] = struct{}{}

	delete(s.databases, db)
	delete(s.catalogs, db)
	for ref := range s.partitions {
		if ref.Database == db {
			delete(s.partitions, ref)
		}
	}
	for key := range s.generations {
		if key.database == db {
			delete(s.generations, key)
		}
	}
	return nil
}

func (s *Store) IsDatabaseRetired(db string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, retired := s.retiredDatabases[db]
	return retired, nil
}

// SetState sets the state of an index.
func (s *Store) SetState(db, pattern, tmplID string, state store.IndexState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	d := s.getOrCreateDB(db)
	idx := d.GetOrCreateIndex(pattern, tmplID, pattern)

	var idxState State
	switch state {
	case store.IndexStateHealthy:
		idxState = StateHealthy
	case store.IndexStateRebuilding:
		idxState = StateRebuilding
	case store.IndexStateFailed:
		idxState = StateFailed
	default:
		idxState = StateHealthy
	}

	idx.SetState(idxState)
	return nil
}

// GetState returns the state of an index.
func (s *Store) GetState(db, pattern, tmplID string) (store.IndexState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	d, ok := s.databases[db]
	if !ok {
		return store.IndexStateHealthy, nil
	}

	idx := d.GetIndex(pattern, tmplID)
	if idx == nil {
		return store.IndexStateHealthy, nil
	}

	switch idx.State() {
	case StateRebuilding:
		return store.IndexStateRebuilding, nil
	case StateFailed:
		return store.IndexStateFailed, nil
	default:
		return store.IndexStateHealthy, nil
	}
}

// LoadProgress loads the event processing progress.
func (s *Store) LoadProgress() (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.progress, nil
}

// Flush is a no-op for memory store.
func (s *Store) Flush() error {
	return nil
}

// Close is a no-op for memory store.
func (s *Store) Close() error {
	return nil
}

// ListDatabases returns all database names.
func (s *Store) ListDatabases() ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]string, 0, len(s.databases))
	for name := range s.databases {
		result = append(result, name)
	}
	return result, nil
}

// ListIndexes returns metadata for all indexes in a database.
func (s *Store) ListIndexes(db string) ([]store.IndexInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	d, ok := s.databases[db]
	if !ok {
		return nil, errors.New("database not found")
	}

	return d.ListIndexes(), nil
}

// Compile-time check that Store implements store.Store.
var _ store.Store = (*Store)(nil)
