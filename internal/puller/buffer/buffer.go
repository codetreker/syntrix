// Package buffer provides event buffering with PebbleDB persistence.
package buffer

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/cockroachdb/pebble"
	"go.mongodb.org/mongo-driver/bson"
)

// Buffer stores events in PebbleDB for durability and replay.
type Buffer struct {
	db       *pebble.DB
	path     string
	logger   *slog.Logger
	newBatch func() pebbleBatch

	retentionMu sync.Mutex
	lineage     string

	// pending is the queue of writes waiting to be batched
	pending []*writeRequest
	// flushing is the queue of writes currently being batched
	flushing []*writeRequest
	// notifyCh is used to wake up the batcher
	notifyCh chan struct{}
	// capacityCh is replaced after a batch releases queue capacity.
	capacityCh chan struct{}

	// mu protects pending, flushing, capacityCh, closed, and failure
	mu sync.RWMutex

	// closed indicates if the buffer is closed
	closed  bool
	failure error

	// shutdownOnce ensures resources are fewer closed exactly once
	shutdownOnce sync.Once

	// batcher manages batched writes

	batchSize     int
	batchInterval time.Duration
	queueSize     int
	closeCh       chan struct{}
	batcherWG     sync.WaitGroup
}

const checkpointKey = "!checkpoint/resume_token"
const formatKey = "!format"
const lineageKey = "!lineage"
const pruningFloorKey = "!pruning_floor"
const formatVersion = "typed-events-v2"

var checkpointKeyBytes = []byte(checkpointKey)
var formatKeyBytes = []byte(formatKey)

type pebbleBatch interface {
	Set(key, value []byte, opts *pebble.WriteOptions) error
	Delete(key []byte, opts *pebble.WriteOptions) error
	Commit(opts *pebble.WriteOptions) error
	Close() error
}

// Options configures the event buffer.
type Options struct {
	// Path is the directory to store the buffer.
	Path string

	// MaxSize is the maximum size in bytes (0 = unlimited).
	MaxSize int64

	// BatchSize is the max number of events per batch.
	BatchSize int

	// BatchInterval is the max time to wait before flushing a batch.
	BatchInterval time.Duration

	// QueueSize limits the total queued and flushing events.
	QueueSize int

	// Logger for buffer operations.
	Logger *slog.Logger
}

// New creates a new event buffer.
func New(opts Options) (*Buffer, error) {
	if opts.Path == "" {
		return nil, fmt.Errorf("buffer path is required")
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("component", "event-buffer")

	// Ensure directory exists
	if err := os.MkdirAll(opts.Path, 0755); err != nil {
		return nil, fmt.Errorf("failed to create buffer directory: %w", err)
	}

	// Open PebbleDB
	dbOpts := &pebble.Options{
		// Use default comparer for string ordering
	}

	db, err := pebble.Open(opts.Path, dbOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to open pebble database: %w", err)
	}

	if err := validateFormat(db); err != nil {
		return nil, errors.Join(err, db.Close())
	}

	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = 100
	}
	batchInterval := opts.BatchInterval
	if batchInterval <= 0 {
		batchInterval = 10000 * time.Millisecond
	}
	queueSize := opts.QueueSize
	if queueSize <= 0 {
		queueSize = 10000
	}

	buf := &Buffer{
		db:     db,
		path:   opts.Path,
		logger: logger,
		newBatch: func() pebbleBatch {
			return db.NewBatch()
		},
		batchSize:     batchSize,
		batchInterval: batchInterval,
		queueSize:     queueSize,
		closeCh:       make(chan struct{}),
		notifyCh:      make(chan struct{}, 1),
		capacityCh:    make(chan struct{}),
	}
	lineage, err := loadLineage(db)
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}
	buf.lineage = lineage
	buf.startBatcher()

	return buf, nil
}

// NewForBackend creates a buffer for a specific backend.
func NewForBackend(basePath, backendName string, logger *slog.Logger) (*Buffer, error) {
	path := filepath.Join(basePath, backendName)
	return New(Options{
		Path:   path,
		Logger: logger,
	})
}

// Close closes the buffer.
func (b *Buffer) Close() error {
	b.mu.Lock()
	wasClosed := b.closed
	b.closed = true
	b.mu.Unlock()

	if !wasClosed {
		// Signal batcher to stop
		close(b.closeCh)
	}

	// Wait for batcher to finish flushing all pending writes.
	b.batcherWG.Wait()

	b.shutdownOnce.Do(func() {
		var closeErr error
		func() {
			defer func() {
				if r := recover(); r != nil {
					closeErr = fmt.Errorf("%v", r)
				}
			}()
			b.retentionMu.Lock()
			defer b.retentionMu.Unlock()
			closeErr = b.db.Close()
		}()

		if closeErr != nil {
			b.mu.Lock()
			b.failure = errors.Join(b.failure, fmt.Errorf("failed to close pebble database: %w", closeErr))
			b.mu.Unlock()
		}
	})

	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.failure
}

// Path returns the buffer storage path.
func (b *Buffer) Path() string {
	return b.path
}

// LoadCheckpoint returns the last saved checkpoint token.
func (b *Buffer) LoadCheckpoint() (bson.Raw, error) {
	b.mu.RLock()
	if b.closed {
		err := b.failure
		b.mu.RUnlock()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("buffer is closed")
	}
	b.mu.RUnlock()

	value, closer, err := b.db.Get(checkpointKeyBytes)
	if err != nil {
		if err == pebble.ErrNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read checkpoint: %w", err)
	}
	defer closer.Close()

	copied := append([]byte(nil), value...)
	return bson.Raw(copied), nil
}

// SaveCheckpoint writes the checkpoint token without an accompanying event.
func (b *Buffer) SaveCheckpoint(token bson.Raw) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return fmt.Errorf("buffer is closed")
	}
	if token == nil {
		return nil
	}
	if len(token) == 0 {
		return fmt.Errorf("checkpoint token is required")
	}
	if len(b.pending)+len(b.flushing) != 0 {
		return fmt.Errorf("flush buffered events before saving a source boundary")
	}
	if err := b.applyBatch(func(batch pebbleBatch) error { return batch.Set(checkpointKeyBytes, token, pebble.Sync) }); err != nil {
		return fmt.Errorf("failed to save checkpoint: %w", err)
	}
	return nil
}

// DeleteCheckpoint deletes the checkpoint token.
func (b *Buffer) DeleteCheckpoint() error {
	b.mu.RLock()
	if b.closed {
		b.mu.RUnlock()
		return fmt.Errorf("buffer is closed")
	}
	b.mu.RUnlock()

	if err := b.applyBatch(func(batch pebbleBatch) error {
		if err := batch.Delete(checkpointKeyBytes, pebble.Sync); err != nil {
			return fmt.Errorf("failed to batch delete checkpoint: %w", err)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("failed to delete checkpoint: %w", err)
	}

	return nil
}

// Delete removes an event from the buffer.
func (b *Buffer) Delete(key string) error {
	b.retentionMu.Lock()
	defer b.retentionMu.Unlock()
	b.mu.RLock()
	closed := b.closed
	b.mu.RUnlock()
	if closed {
		return fmt.Errorf("buffer is closed")
	}
	if isMetadataKey([]byte(key)) {
		return fmt.Errorf("cannot delete buffer metadata as an event")
	}
	_, closer, err := b.db.Get([]byte(key))
	exists := err == nil
	if exists {
		if err := closer.Close(); err != nil {
			return err
		}
	} else if !errors.Is(err, pebble.ErrNotFound) {
		return err
	}
	return b.applyBatch(func(batch pebbleBatch) error {
		if err := batch.Delete([]byte(key), pebble.Sync); err != nil {
			return err
		}
		if exists {
			return b.advancePruningFloor(batch, key)
		}
		return nil
	})
}

// DeleteBefore atomically records the greatest removed key with the deletions.
func (b *Buffer) DeleteBefore(beforeKey string) (int, error) {
	b.retentionMu.Lock()
	defer b.retentionMu.Unlock()
	return b.deleteBeforeLocked(beforeKey)
}

// PruneBefore applies capacity retention while preserving the entire newest
// persisted timestamp group. Explicit Delete and DeleteBefore remain forceful.
func (b *Buffer) PruneBefore(beforeKey string) (int, error) {
	return b.pruneBefore(beforeKey, false)
}

// PruneExpired retains the newest complete persisted group before the age
// cutoff and every later event, keeping an idle consumer's boundary available
// while the first new group is being delivered.
func (b *Buffer) PruneExpired(beforeKey string) (int, error) {
	return b.pruneBefore(beforeKey, true)
}

func (b *Buffer) pruneBefore(beforeKey string, age bool) (int, error) {
	b.retentionMu.Lock()
	defer b.retentionMu.Unlock()
	b.mu.RLock()
	closed := b.closed
	b.mu.RUnlock()
	if closed {
		return 0, fmt.Errorf("buffer is closed")
	}
	options := &pebble.IterOptions{}
	if age {
		options.UpperBound = []byte(beforeKey)
	}
	iter, err := b.db.NewIter(options)
	if err != nil {
		return 0, err
	}
	protectedGroup := ""
	for iter.Last(); iter.Valid(); iter.Prev() {
		if isMetadataKey(iter.Key()) {
			continue
		}
		protectedGroup, err = timestampGroupStart(string(iter.Key()))
		break
	}
	if err = errors.Join(err, iter.Error(), iter.Close()); err != nil {
		return 0, err
	}
	if protectedGroup == "" {
		return 0, nil
	}
	if beforeKey > protectedGroup {
		beforeKey = protectedGroup
	}
	return b.deleteBeforeLocked(beforeKey)
}

func (b *Buffer) deleteBeforeLocked(beforeKey string) (int, error) {
	b.mu.RLock()
	closed := b.closed
	b.mu.RUnlock()
	if closed {
		return 0, fmt.Errorf("buffer is closed")
	}
	iter, err := b.db.NewIter(&pebble.IterOptions{UpperBound: []byte(beforeKey)})
	if err != nil {
		return 0, err
	}
	defer iter.Close()
	batch := b.newBatch()
	defer batch.Close()
	count, floor := 0, ""
	for iter.First(); iter.Valid(); iter.Next() {
		if isMetadataKey(iter.Key()) {
			continue
		}
		if err := batch.Delete(iter.Key(), pebble.Sync); err != nil {
			return 0, err
		}
		floor = string(iter.Key())
		count++
	}
	if err := iter.Error(); err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, nil
	}
	if err := b.advancePruningFloor(batch, floor); err != nil {
		return 0, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return 0, err
	}
	return count, nil
}

func (b *Buffer) advancePruningFloor(batch pebbleBatch, floor string) error {
	previous, err := b.pruningFloor()
	if err != nil {
		return err
	}
	if floor <= previous {
		return nil
	}
	return batch.Set([]byte(pruningFloorKey), []byte(floor), pebble.Sync)
}

func (b *Buffer) pruningFloor() (string, error) {
	value, closer, err := b.db.Get([]byte(pruningFloorKey))
	if errors.Is(err, pebble.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer closer.Close()
	return string(value), nil
}

// Lineage identifies this buffer incarnation across process restarts.
func (b *Buffer) Lineage() string { return b.lineage }

func loadLineage(db *pebble.DB) (string, error) {
	value, closer, err := db.Get([]byte(lineageKey))
	if err == nil {
		defer closer.Close()
		if len(value) != 32 {
			return "", fmt.Errorf("invalid buffer lineage; offline buffer rebuild required")
		}
		return string(value), nil
	}
	if !errors.Is(err, pebble.ErrNotFound) {
		return "", err
	}
	iter, err := db.NewIter(nil)
	if err != nil {
		return "", err
	}
	for iter.First(); iter.Valid(); iter.Next() {
		if !bytes.Equal(iter.Key(), formatKeyBytes) {
			iter.Close()
			return "", fmt.Errorf("buffer lineage missing; offline buffer rebuild required")
		}
	}
	if err := errors.Join(iter.Error(), iter.Close()); err != nil {
		return "", err
	}
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return "", err
	}
	lineage := hex.EncodeToString(token)
	if err := db.Set([]byte(lineageKey), []byte(lineage), pebble.Sync); err != nil {
		return "", err
	}
	return lineage, nil
}

func isMetadataKey(key []byte) bool {
	return bytes.Equal(key, checkpointKeyBytes) || bytes.Equal(key, formatKeyBytes) || string(key) == lineageKey || string(key) == pruningFloorKey
}

func validateFormat(db *pebble.DB) error {
	value, closer, err := db.Get(formatKeyBytes)
	if err == nil {
		defer closer.Close()
		if string(value) != formatVersion {
			return fmt.Errorf("unsupported buffer format %q; offline buffer rebuild required", value)
		}
		return nil
	}
	if !errors.Is(err, pebble.ErrNotFound) {
		return fmt.Errorf("read buffer format: %w", err)
	}
	iter, err := db.NewIter(nil)
	if err != nil {
		return fmt.Errorf("inspect buffer format: %w", err)
	}
	nonempty := iter.First()
	err = errors.Join(iter.Error(), iter.Close())
	if err != nil {
		return fmt.Errorf("inspect buffer format: %w", err)
	}
	if nonempty {
		return fmt.Errorf("unversioned buffer format; offline buffer rebuild required")
	}
	if err := db.Set(formatKeyBytes, []byte(formatVersion), pebble.Sync); err != nil {
		return fmt.Errorf("initialize buffer format: %w", err)
	}
	return nil
}
