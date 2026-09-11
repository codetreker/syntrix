package persist_store

import (
	"io"

	"github.com/cockroachdb/pebble"
)

type Snapshot interface {
	NewIter(o *pebble.IterOptions) (Iterator, error)
	Close() error
}

type DB interface {
	NewSnapshot() Snapshot

	// Get gets the value for the given key. It returns ErrNotFound if the DB
	// does not contain the key.
	//
	// The caller should not modify the contents of the returned slice, but it is
	// safe to modify the contents of the argument after Get returns. The
	// returned slice will remain valid until the returned Closer is closed. On
	// success, the caller MUST call closer.Close() or a memory leak will occur.
	Get(key []byte) (value []byte, closer io.Closer, err error)

	// NewIter returns an iterator that is unpositioned (Iterator.Valid() will
	// return false). The iterator can be positioned via a call to SeekGE,
	// SeekLT, First or Last.
	NewIter(o *pebble.IterOptions) (Iterator, error)

	// Set sets the value for the given key. It overwrites any previous value
	// for that key; a DB is not a multi-map.
	//
	// It is safe to modify the contents of the arguments after Set returns.
	Set(key, value []byte, o *pebble.WriteOptions) error

	// NewBatch returns a new empty write-only batch. Any reads on the batch will
	// return an error. If the batch is committed it will be applied to the DB.
	NewBatch() Batch

	// Close closes the database.
	Close() error
}

type Iterator interface {
	// First moves the iterator to the first key in the source. It returns
	// whether the iterator is valid after the operation.
	First() bool

	// Valid returns whether the iterator is valid. An invalid iterator
	// indicates the end of the source has been reached or an error has occurred.
	Valid() bool

	// Key returns the key at the current position. The caller must not modify
	// the contents of the returned slice.
	Key() []byte

	// Value returns the value at the current position. The caller must not
	// modify the contents of the returned slice.
	Value() []byte

	// Next moves the iterator to the next key in the source. It returns whether
	// the iterator is valid after the operation.
	Next() bool

	Error() error

	// Close closes the iterator.
	Close() error
}

type Batch interface {
	// Set adds a Set operation to the batch. It overwrites any previous value
	// for that key; a DB is not a multi-map.
	//
	// It is safe to modify the contents of the arguments after Set returns.
	Set(key, value []byte, opt *pebble.WriteOptions) error

	// Delete adds a Delete operation to the batch. Deletes are blind all will
	// succeed even if the given key does not exist.
	//
	// It is safe to modify the contents of the arguments after Delete returns.
	Delete(key []byte, opt *pebble.WriteOptions) error

	// Commit applies the operations in the batch to the database.
	Commit(o *pebble.WriteOptions) error

	// Close closes the batch.
	Close() error
}

// PebbleDB wraps a pebble.DB to implement the DB interface.
type PebbleDB struct {
	db *pebble.DB
}

func (p *PebbleDB) Get(key []byte) (value []byte, closer io.Closer, err error) {
	return p.db.Get(key)
}

func (p *PebbleDB) NewIter(o *pebble.IterOptions) (iter Iterator, err error) {
	return p.db.NewIter(o)
}

func (p *PebbleDB) Set(key, value []byte, o *pebble.WriteOptions) error {
	return p.db.Set(key, value, o)
}

func (p *PebbleDB) NewBatch() Batch {
	return p.db.NewBatch()
}

func (p *PebbleDB) Close() error {
	return p.db.Close()
}

func (p *PebbleDB) NewSnapshot() Snapshot {
	return &pebbleSnapshot{snapshot: p.db.NewSnapshot()}
}

type pebbleSnapshot struct {
	snapshot *pebble.Snapshot
}

func (s *pebbleSnapshot) NewIter(o *pebble.IterOptions) (Iterator, error) {
	return s.snapshot.NewIter(o)
}

func (s *pebbleSnapshot) Close() error { return s.snapshot.Close() }
