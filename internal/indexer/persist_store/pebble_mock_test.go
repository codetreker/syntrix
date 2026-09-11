package persist_store

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/indexer/store"
)

// mockDB is a mock implementation of the DB interface for testing error paths.
type mockDB struct {
	mu      sync.Mutex
	data    map[string][]byte
	batches []*mockBatch

	// Error injection
	getErrKey   []byte
	getErr      error
	setErr      error
	newIterErr  error
	closeErr    error
	iterError   error // Error returned by Iterator.Error()
	batchSetErr error // Error for batch.Set
	batchDelErr error // Error for batch.Delete
	commitErr   error // Error for batch.Commit
}

func newMockDB() *mockDB {
	return &mockDB{
		data: make(map[string][]byte),
	}
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

func (m *mockDB) Get(key []byte) (value []byte, closer io.Closer, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.getErr != nil && (len(m.getErrKey) == 0 || bytes.HasPrefix(key, m.getErrKey)) {
		return nil, nil, m.getErr
	}

	v, ok := m.data[string(key)]
	if !ok {
		return nil, nil, pebble.ErrNotFound
	}
	// Return a copy to avoid data races
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nopCloser{}, nil
}

func (m *mockDB) Set(key, value []byte, o *pebble.WriteOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.setErr != nil {
		return m.setErr
	}

	cp := make([]byte, len(value))
	copy(cp, value)
	m.data[string(key)] = cp
	return nil
}

func (m *mockDB) NewIter(o *pebble.IterOptions) (Iterator, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.newIterErr != nil {
		return nil, m.newIterErr
	}

	// Collect keys within bounds
	var keys []string
	for k := range m.data {
		kb := []byte(k)
		if o != nil {
			if o.LowerBound != nil && string(kb) < string(o.LowerBound) {
				continue
			}
			if o.UpperBound != nil && string(kb) >= string(o.UpperBound) {
				continue
			}
		}
		keys = append(keys, k)
	}

	sort.Strings(keys)
	return &mockIterator{
		keys:    keys,
		data:    m.data,
		pos:     -1,
		iterErr: m.iterError,
	}, nil
}

func (m *mockDB) NewBatch() Batch {
	m.mu.Lock()
	defer m.mu.Unlock()

	b := &mockBatch{
		parent:    m,
		ops:       make([]batchOp, 0),
		setErr:    m.batchSetErr,
		deleteErr: m.batchDelErr,
		commitErr: m.commitErr,
	}
	m.batches = append(m.batches, b)
	return b
}

func (m *mockDB) Close() error {
	if m.closeErr != nil {
		return m.closeErr
	}
	return nil
}

// mockIterator implements Iterator for testing.
type mockIterator struct {
	keys    []string
	data    map[string][]byte
	pos     int
	iterErr error
}

func (m *mockIterator) First() bool {
	if len(m.keys) == 0 {
		return false
	}
	m.pos = 0
	return true
}

func (m *mockIterator) Valid() bool {
	return m.pos >= 0 && m.pos < len(m.keys)
}

func (m *mockIterator) Key() []byte {
	if !m.Valid() {
		return nil
	}
	return []byte(m.keys[m.pos])
}

func (m *mockIterator) Value() []byte {
	if !m.Valid() {
		return nil
	}
	return m.data[m.keys[m.pos]]
}

func (m *mockIterator) Next() bool {
	m.pos++
	return m.Valid()
}

func (m *mockIterator) Error() error {
	return m.iterErr
}

func (m *mockIterator) Close() error {
	return nil
}

// batchOp represents a single batch operation.
type batchOp struct {
	isSet bool
	key   []byte
	value []byte
}

// mockBatch implements Batch for testing.
type mockBatch struct {
	parent    *mockDB
	ops       []batchOp
	setErr    error
	deleteErr error
	commitErr error
	closed    bool
}

func (b *mockBatch) Set(key, value []byte, opt *pebble.WriteOptions) error {
	if b.setErr != nil {
		return b.setErr
	}
	b.ops = append(b.ops, batchOp{isSet: true, key: append([]byte(nil), key...), value: append([]byte(nil), value...)})
	return nil
}

func (b *mockBatch) Delete(key []byte, opt *pebble.WriteOptions) error {
	if b.deleteErr != nil {
		return b.deleteErr
	}
	b.ops = append(b.ops, batchOp{isSet: false, key: append([]byte(nil), key...)})
	return nil
}

func (b *mockBatch) Commit(o *pebble.WriteOptions) error {
	if b.commitErr != nil {
		return b.commitErr
	}

	b.parent.mu.Lock()
	defer b.parent.mu.Unlock()

	for _, op := range b.ops {
		if op.isSet {
			b.parent.data[string(op.key)] = op.value
		} else {
			delete(b.parent.data, string(op.key))
		}
	}
	return nil
}

func (b *mockBatch) Close() error {
	b.closed = true
	return nil
}

// newMockPebbleStore creates a PebbleStore with a mock DB for testing.
func newMockPebbleStore(db *mockDB) *PebbleStore {
	return &PebbleStore{
		db:                  db,
		path:                "/mock/path",
		logger:              slog.Default(),
		pending:             make(map[string]map[string]*pendingOp),
		flushing:            make(map[string]map[string]*pendingOp),
		pendingIndexDeletes: make(map[string]indexDeleteOp),
		notifyCh:            make(chan struct{}, 1),
		closeCh:             make(chan struct{}),
		flushDoneCh:         make(chan struct{}, 1),
		batchSize:           100,
		batchInterval:       50 * time.Millisecond,
	}
}

func TestSearchMemoryOverridesPersistedDocument(t *testing.T) {
	ref := func(id string, key byte) store.DocRef {
		return store.DocRef{ID: id, OrderKey: []byte{key}}
	}
	first, middle, last := ref("first", 0x01), ref("middle", 0x05), ref("last", 0x09)
	tests := []struct {
		name         string
		pendingKey   []byte
		hasFlushing  bool
		flushingKey  []byte
		flushingOnly bool
		opts         store.SearchOptions
		want         []store.DocRef
	}{
		{
			name: "same order key", pendingKey: []byte{0x04},
			want: []store.DocRef{first, ref("target", 0x04), middle, last},
		},
		{
			name: "order key moves earlier", pendingKey: []byte{0x02},
			want: []store.DocRef{first, ref("target", 0x02), middle, last},
		},
		{
			name: "order key moves later", pendingKey: []byte{0x06},
			want: []store.DocRef{first, middle, ref("target", 0x06), last},
		},
		{
			name: "pending delete",
			want: []store.DocRef{first, middle, last},
		},
		{
			name: "updated key leaves lower bound", pendingKey: []byte{0x02},
			opts: store.SearchOptions{Lower: []byte{0x03}},
			want: []store.DocRef{middle, last},
		},
		{
			name: "updated key reaches exclusive upper bound", pendingKey: []byte{0x08},
			opts: store.SearchOptions{Upper: []byte{0x08}},
			want: []store.DocRef{first, middle},
		},
		{
			name: "updated key enters inclusive lower bound", pendingKey: []byte{0x06},
			opts: store.SearchOptions{Lower: []byte{0x06}},
			want: []store.DocRef{ref("target", 0x06), last},
		},
		{
			name: "updated key enters below upper bound", pendingKey: []byte{0x02},
			opts: store.SearchOptions{Upper: []byte{0x04}},
			want: []store.DocRef{first, ref("target", 0x02)},
		},
		{
			name: "old key equals cursor", pendingKey: []byte{0x06},
			opts: store.SearchOptions{StartAfter: []byte{0x04}},
			want: []store.DocRef{middle, ref("target", 0x06), last},
		},
		{
			name: "updated key equals cursor", pendingKey: []byte{0x06},
			opts: store.SearchOptions{StartAfter: []byte{0x06}},
			want: []store.DocRef{last},
		},
		{
			name: "updated key moves before cursor", pendingKey: []byte{0x02},
			opts: store.SearchOptions{StartAfter: []byte{0x03}},
			want: []store.DocRef{middle, last},
		},
		{
			name: "limit counts effective results", pendingKey: []byte{0x06},
			opts: store.SearchOptions{Limit: 3},
			want: []store.DocRef{first, middle, ref("target", 0x06)},
		},
		{
			name: "flushing update", hasFlushing: true, flushingKey: []byte{0x06}, flushingOnly: true,
			want: []store.DocRef{first, middle, ref("target", 0x06), last},
		},
		{
			name: "flushing delete", hasFlushing: true, flushingOnly: true,
			want: []store.DocRef{first, middle, last},
		},
		{
			name: "pending update supersedes flushing update", hasFlushing: true, flushingKey: []byte{0x02}, pendingKey: []byte{0x06},
			want: []store.DocRef{first, middle, ref("target", 0x06), last},
		},
		{
			name: "pending delete supersedes flushing update", hasFlushing: true, flushingKey: []byte{0x06},
			want: []store.DocRef{first, middle, last},
		},
		{
			name: "pending update supersedes flushing delete", hasFlushing: true, pendingKey: []byte{0x06},
			want: []store.DocRef{first, middle, ref("target", 0x06), last},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps := newMockPebbleStore(newMockDB())
			// Keep one persisted index row: mockDB iterates a map without sorting.
			// No batcher runs, so the disk/memory overlap lasts through Search.
			require.NoError(t, ps.Upsert("testdb", "users/*", "tmpl1", "target", []byte{0x04}, ""))
			ps.doFlush()
			key, found := ps.Get("testdb", "users/*", "tmpl1", "target")
			require.True(t, found)
			require.Equal(t, []byte{0x04}, key)

			writeTarget := func(key []byte) {
				if key == nil {
					require.NoError(t, ps.Delete("testdb", "users/*", "tmpl1", "target", ""))
				} else {
					require.NoError(t, ps.Upsert("testdb", "users/*", "tmpl1", "target", key, ""))
				}
			}
			if tt.hasFlushing {
				writeTarget(tt.flushingKey)
				// Hold the state after doFlush swaps maps, before its commit.
				ps.flushing = ps.pending
				ps.pending = make(map[string]map[string]*pendingOp)
			}
			if !tt.flushingOnly {
				writeTarget(tt.pendingKey)
			}
			for _, neighbor := range []store.DocRef{first, middle, last} {
				require.NoError(t, ps.Upsert("testdb", "users/*", "tmpl1", neighbor.ID, neighbor.OrderKey, ""))
			}

			got, err := ps.Search("testdb", "users/*", "tmpl1", tt.opts)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

// ============ Error Path Tests ============

// TestApplyOpDeleteReadError tests applyOp when db.Get returns an error during delete.
func TestApplyOpDeleteReadError(t *testing.T) {
	db := newMockDB()
	ps := newMockPebbleStore(db)

	// Create a delete operation (orderKey is nil)
	op := &pendingOp{
		db:      "testdb",
		pattern: "users/*",
		tmplID:  "tmpl1",
		hash:    "hash1",
		docID:   "doc1",
		revKey:  reverseKey("testdb", "users/*", "tmpl1", "doc1"),
		idxKey:  nil, // delete operation
	}

	// Inject error for Get
	db.getErr = errors.New("mock read error")

	batch := db.NewBatch()
	err := ps.applyOp(batch, op)

	if err == nil {
		t.Error("expected error from applyOp, got nil")
	}
	if err != nil && !errors.Is(err, db.getErr) {
		// Check error message contains our error
		if err.Error() != "failed to read reverse index: mock read error" {
			t.Errorf("unexpected error: %v", err)
		}
	}
}

// TestApplyOpDeleteBatchDeleteError tests applyOp when batch.Delete fails during delete.
func TestApplyOpDeleteBatchDeleteError(t *testing.T) {
	db := newMockDB()
	ps := newMockPebbleStore(db)

	// First, set up data so Get succeeds
	revKey := reverseKey("testdb", "users/*", "tmpl1", "doc1")
	db.data[string(revKey)] = []byte{0x01, 0x02}

	// Create a delete operation
	op := &pendingOp{
		db:       "testdb",
		pattern:  "users/*",
		tmplID:   "tmpl1",
		hash:     "hash1",
		docID:    "doc1",
		revKey:   revKey,
		orderKey: nil, // delete operation
	}

	// Inject error for batch.Delete
	db.batchDelErr = errors.New("mock batch delete error")

	batch := db.NewBatch()
	err := ps.applyOp(batch, op)

	if err == nil {
		t.Error("expected error from applyOp, got nil")
	}
}

// TestApplyOpUpsertReadError tests applyOp when db.Get returns an error during upsert.
func TestApplyOpUpsertReadError(t *testing.T) {
	db := newMockDB()
	ps := newMockPebbleStore(db)

	orderKey := []byte{0x01, 0x02}
	op := &pendingOp{
		db:       "testdb",
		pattern:  "users/*",
		tmplID:   "tmpl1",
		hash:     "hash1",
		docID:    "doc1",
		orderKey: orderKey,
		revKey:   reverseKey("testdb", "users/*", "tmpl1", "doc1"),
		idxKey:   indexKey("testdb", "users/*", "tmpl1", orderKey),
		mapKey:   mapKey("testdb", "users/*", "tmpl1"),
	}

	// Inject error for Get (not ErrNotFound)
	db.getErr = errors.New("mock upsert read error")

	batch := db.NewBatch()
	err := ps.applyOp(batch, op)

	if err == nil {
		t.Error("expected error from applyOp, got nil")
	}
}

// TestApplyOpUpsertDeleteOldError tests applyOp when batch.Delete fails for old index entry.
func TestApplyOpUpsertDeleteOldError(t *testing.T) {
	db := newMockDB()
	ps := newMockPebbleStore(db)

	// Set up existing data
	revKey := reverseKey("testdb", "users/*", "tmpl1", "doc1")
	oldOrderKey := []byte{0x00, 0x01}
	db.data[string(revKey)] = oldOrderKey

	orderKey := []byte{0x01, 0x02}
	op := &pendingOp{
		db:       "testdb",
		pattern:  "users/*",
		tmplID:   "tmpl1",
		hash:     "hash1",
		docID:    "doc1",
		orderKey: orderKey,
		revKey:   revKey,
		idxKey:   indexKey("testdb", "users/*", "tmpl1", orderKey),
		mapKey:   mapKey("testdb", "users/*", "tmpl1"),
	}

	// Inject error for batch.Delete
	db.batchDelErr = errors.New("mock batch delete old error")

	batch := db.NewBatch()
	err := ps.applyOp(batch, op)

	if err == nil {
		t.Error("expected error from applyOp, got nil")
	}
}

// TestApplyOpUpsertSetError tests applyOp when batch.Set fails.
func TestApplyOpUpsertSetError(t *testing.T) {
	db := newMockDB()
	ps := newMockPebbleStore(db)

	orderKey := []byte{0x01, 0x02}
	op := &pendingOp{
		db:       "testdb",
		pattern:  "users/*",
		tmplID:   "tmpl1",
		hash:     "hash1",
		docID:    "doc1",
		orderKey: orderKey,
		revKey:   reverseKey("testdb", "users/*", "tmpl1", "doc1"),
		idxKey:   indexKey("testdb", "users/*", "tmpl1", orderKey),
		mapKey:   mapKey("testdb", "users/*", "tmpl1"),
	}

	// Inject error for batch.Set
	db.batchSetErr = errors.New("mock batch set error")

	batch := db.NewBatch()
	err := ps.applyOp(batch, op)

	if err == nil {
		t.Error("expected error from applyOp, got nil")
	}
}

// mockDBWithCallCounter wraps mockDB to fail on specific call counts.
type mockDBWithCallCounter struct {
	*mockDB
	newIterCallCount  int
	failNewIterOnCall int
}

func (m *mockDBWithCallCounter) NewIter(o *pebble.IterOptions) (Iterator, error) {
	m.newIterCallCount++
	if m.failNewIterOnCall > 0 && m.newIterCallCount == m.failNewIterOnCall {
		return nil, errors.New("mock iter error on call")
	}
	return m.mockDB.NewIter(o)
}

func (m *mockDBWithCallCounter) Get(key []byte) (value []byte, closer io.Closer, err error) {
	return m.mockDB.Get(key)
}

func (m *mockDBWithCallCounter) Set(key, value []byte, o *pebble.WriteOptions) error {
	return m.mockDB.Set(key, value, o)
}

func (m *mockDBWithCallCounter) NewBatch() Batch {
	return m.mockDB.NewBatch()
}

func (m *mockDBWithCallCounter) Close() error {
	return m.mockDB.Close()
}

// ============ DeleteDatabase Error Path Tests ============

func TestDeleteDatabaseIteratorFailurePreservesError(t *testing.T) {
	db := newMockDB()
	ps := newMockPebbleStore(db)
	failure := errors.New("database deletion iterator failed")
	db.newIterErr = failure
	require.ErrorIs(t, ps.DeleteDatabase("testdb"), failure)
	require.ErrorIs(t, ps.Flush(), failure)
}

func TestDeleteDatabaseFailurePreservesAllNamespaces(t *testing.T) {
	db := newMockDB()
	ps := newMockPebbleStore(db)
	pattern, tmplID := "users/*", "tmpl1"
	idxK := indexKey("testdb", pattern, tmplID, []byte{0x01, 0x02})
	revK := reverseKey("testdb", pattern, tmplID, "doc1")
	db.data[string(idxK)] = []byte("doc1")
	db.data[string(revK)] = []byte{0x01, 0x02}
	ps.db = &mockDBWithCallCounter{mockDB: db, failNewIterOnCall: 2}
	require.Error(t, ps.DeleteDatabase("testdb"))
	require.Contains(t, db.data, string(idxK))
	require.Contains(t, db.data, string(revK))
}

// TestDeleteDatabaseEmptyDatabase tests DeleteDatabase on a database with no indexes.
func TestDeleteDatabaseEmptyDatabase(t *testing.T) {
	db := newMockDB()
	ps := newMockPebbleStore(db)

	// Delete database with no indexes - should succeed with no error
	err := ps.DeleteDatabase("nonexistent")

	if err != nil {
		t.Errorf("expected no error for empty database, got: %v", err)
	}
}

// TestDeleteDatabaseMultipleIndexes tests DeleteDatabase with multiple indexes.
func TestDeleteDatabaseMultipleIndexes(t *testing.T) {
	db := newMockDB()
	ps := newMockPebbleStore(db)

	// Add multiple index entries
	indexes := []struct {
		pattern string
		tmplID  string
	}{
		{"users/*", "tmpl1"},
		{"posts/*", "tmpl2"},
		{"comments/*", "tmpl3"},
	}

	for _, idx := range indexes {
		mapK := mapKey("testdb", idx.pattern, idx.tmplID)
		db.data[string(mapK)] = []byte(idx.pattern + "|" + idx.tmplID)

		// Add state entry
		stateK := stateKey("testdb", idx.pattern, idx.tmplID)
		db.data[string(stateK)] = []byte("healthy")
	}

	// DeleteDatabase should succeed
	err := ps.DeleteDatabase("testdb")

	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func (m *mockDB) NewSnapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	snapshot := newMockDB()
	for key, value := range m.data {
		snapshot.data[key] = append([]byte(nil), value...)
	}
	snapshot.newIterErr = m.newIterErr
	snapshot.iterError = m.iterError
	return snapshot
}
