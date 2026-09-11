package persist_store

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/cockroachdb/pebble"
	"github.com/syntrixbase/syntrix/internal/indexer/store"
)

const manifestKey = "meta/format"
const manifestV2 = "syntrix-index-v2"

func ensureManifest(db *pebble.DB) (err error) {
	value, closer, err := db.Get([]byte(manifestKey))
	if err == nil {
		compatible := string(value) == manifestV2
		if err := closer.Close(); err != nil {
			return err
		}
		if !compatible {
			return fmt.Errorf("incompatible index format: rebuild the index store")
		}
		return nil
	}
	if !errors.Is(err, pebble.ErrNotFound) {
		return err
	}
	iter, err := db.NewIter(nil)
	if err != nil {
		return err
	}
	nonempty := iter.First()
	err = errors.Join(iter.Error(), iter.Close())
	if err != nil {
		return err
	}
	if nonempty {
		return fmt.Errorf("unversioned index store: rebuild the index store")
	}
	return db.Set([]byte(manifestKey), []byte(manifestV2), pebble.Sync)
}

type projectionOp struct {
	projection store.Projection
	bytes      int
}

type metadataWrite struct {
	key             []byte
	value           []byte
	progress        string
	partition       *store.QueryIndexRef
	catalogs        []catalogRecord
	replaceCatalogs bool
	singleCatalog   bool
	inventory       []byte
}

type catalogRecord struct {
	database string
	key      []byte
	value    []byte
}

func projectionPrefix(kind string, ref store.QueryIndexRef) []byte {
	return []byte("v2/" + kind + "/" + encodePathComponent(ref.Database) + "/" + encodePathComponent(ref.Collection) + "/" + encodePathComponent(ref.TemplateFingerprint) + "/" + encodePathComponent(ref.Generation) + "/")
}

func generationKey(db, collection, fingerprint string) []byte {
	return []byte("v2/generation/" + encodePathComponent(db) + "/" + encodePathComponent(collection) + "/" + encodePathComponent(fingerprint))
}

func prefixEnd(prefix []byte) []byte {
	end := bytes.Clone(prefix)
	for i := len(end) - 1; i >= 0; i-- {
		if end[i] != 255 {
			end[i]++
			return end[:i+1]
		}
	}
	return nil
}

func (s *PebbleStore) writeErrorLocked() error {
	if s.flushErr != nil {
		return s.flushErr
	}
	if s.closed {
		return fmt.Errorf("index store is closed")
	}
	return nil
}

func (s *PebbleStore) ApplyDocumentProjection(replacements []store.Projection, progress string) error {
	owned, err := store.OwnProjections(replacements)
	if err != nil {
		return err
	}
	ops := make([]*projectionOp, len(owned))
	for i, projection := range owned {
		size := len(projection.DocumentID)
		for _, key := range projection.PostingKeys {
			size += len(key)
		}
		ops[i] = &projectionOp{projection: projection, bytes: size}
	}
	s.mu.Lock()
	if err := s.writeErrorLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	if s.projectionPending == nil {
		s.projectionPending = make(map[store.QueryIndexRef]map[string]*projectionOp)
	}
	for _, op := range ops {
		ref := op.projection.Index
		if s.projectionPending[ref] == nil {
			s.projectionPending[ref] = make(map[string]*projectionOp)
		}
		s.projectionPending[ref][op.projection.DocumentID] = op
	}
	if progress != "" {
		s.pendingProgress = progress
	}
	count := 0
	for _, partition := range s.projectionPending {
		count += len(partition)
	}
	s.mu.Unlock()
	if count >= s.batchSize || len(ops) == 0 && progress != "" {
		select {
		case s.notifyCh <- struct{}{}:
		default:
		}
	}
	return nil
}

func encodePostingSet(keys [][]byte) []byte {
	encoded := binary.AppendUvarint(nil, uint64(len(keys)))
	for _, key := range keys {
		encoded = binary.AppendUvarint(encoded, uint64(len(key)))
		encoded = append(encoded, key...)
	}
	return encoded
}

func decodePostingSet(encoded []byte) ([][]byte, error) {
	count, n := binary.Uvarint(encoded)
	if n <= 0 || count > uint64(len(encoded)) {
		return nil, fmt.Errorf("invalid reverse posting set")
	}
	encoded = encoded[n:]
	keys := make([][]byte, 0, int(count))
	for range count {
		size, n := binary.Uvarint(encoded)
		if n <= 0 {
			return nil, fmt.Errorf("invalid reverse posting length")
		}
		encoded = encoded[n:]
		if size == 0 || size > uint64(len(encoded)) {
			return nil, fmt.Errorf("invalid reverse posting length")
		}
		keys = append(keys, encoded[:int(size)])
		encoded = encoded[int(size):]
	}
	if len(encoded) != 0 {
		return nil, fmt.Errorf("trailing reverse posting bytes")
	}
	return keys, nil
}

func (s *PebbleStore) applyProjection(batch Batch, op *projectionOp) error {
	projection := op.projection
	prefix := projectionPrefix("posting", projection.Index)
	reverse := append(projectionPrefix("reverse", projection.Index), encodePathComponent(projection.DocumentID)...)
	old, closer, err := s.db.Get(reverse)
	if err == nil {
		keys, decodeErr := decodePostingSet(old)
		if decodeErr == nil {
			for _, key := range keys {
				if decodeErr = batch.Delete(append(bytes.Clone(prefix), key...), nil); decodeErr != nil {
					break
				}
			}
		}
		if err = errors.Join(decodeErr, closer.Close()); err != nil {
			return err
		}
	} else if !errors.Is(err, pebble.ErrNotFound) {
		return err
	}
	for _, key := range projection.PostingKeys {
		if err := batch.Set(append(bytes.Clone(prefix), key...), []byte(projection.DocumentID), nil); err != nil {
			return err
		}
	}
	if err := setPartition(batch, projection.Index); err != nil {
		return err
	}
	if len(projection.PostingKeys) == 0 {
		return batch.Delete(reverse, nil)
	}
	return batch.Set(reverse, encodePostingSet(projection.PostingKeys), nil)
}

type projectionView struct {
	mu           sync.Mutex
	snapshot     Snapshot
	prefix       []byte
	replacements map[string]*projectionOp
	overlay      []store.DocRef
	closed       bool
	examined     atomic.Int64
	maxExamined  int64
}

func (s *PebbleStore) ReadView(ctx context.Context, ref store.QueryIndexRef, budget store.ReadBudget) (store.ReadView, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	budget, err := store.NormalizeReadBudget(budget)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	if err := s.writeErrorLocked(); err != nil {
		s.mu.RUnlock()
		return nil, err
	}
	replacements := make(map[string]*projectionOp)
	size := 0
	for _, partition := range []map[string]*projectionOp{s.projectionPending[ref], s.projectionFlushing[ref]} {
		for id, op := range partition {
			if err := ctx.Err(); err != nil {
				s.mu.RUnlock()
				return nil, err
			}
			if _, exists := replacements[id]; exists {
				continue
			}
			if len(replacements) >= budget.MaxOverlayReplacements || op.bytes > budget.MaxOverlayBytes-size {
				s.mu.RUnlock()
				return nil, store.ErrWorkLimit
			}
			replacements[id] = op
			size += op.bytes
		}
	}
	snapshot := s.db.NewSnapshot()
	s.mu.RUnlock()
	view := &projectionView{snapshot: snapshot, prefix: projectionPrefix("posting", ref), replacements: replacements, maxExamined: budget.MaxExamined}
	for id, op := range replacements {
		for _, key := range op.projection.PostingKeys {
			if err := ctx.Err(); err != nil {
				return nil, errors.Join(err, snapshot.Close())
			}
			view.overlay = append(view.overlay, store.DocRef{ID: id, OrderKey: key})
		}
	}
	sort.Slice(view.overlay, func(i, j int) bool { return bytes.Compare(view.overlay[i].OrderKey, view.overlay[j].OrderKey) < 0 })
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, snapshot.Close())
	}
	return view, nil
}

func (v *projectionView) Scan(ctx context.Context, opts store.SearchOptions) (store.Iterator, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return nil, fmt.Errorf("index read view is closed")
	}
	lower := bytes.Clone(opts.Lower)
	if bytes.Compare(opts.StartAfter, lower) > 0 {
		lower = bytes.Clone(opts.StartAfter)
	}
	upper := prefixEnd(v.prefix)
	if opts.Upper != nil {
		upper = append(bytes.Clone(v.prefix), opts.Upper...)
	}
	iter, err := v.snapshot.NewIter(&pebble.IterOptions{LowerBound: append(bytes.Clone(v.prefix), lower...), UpperBound: upper})
	if err != nil {
		return nil, err
	}
	pos := sort.Search(len(v.overlay), func(i int) bool { return bytes.Compare(v.overlay[i].OrderKey, lower) >= 0 })
	opts.Lower = bytes.Clone(opts.Lower)
	opts.Upper = bytes.Clone(opts.Upper)
	opts.StartAfter = bytes.Clone(opts.StartAfter)
	return &projectionIterator{ctx: ctx, iter: iter, view: v, options: opts, overlayPosition: pos}, nil
}

func (v *projectionView) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return nil
	}
	v.closed = true
	return v.snapshot.Close()
}

type projectionIterator struct {
	ctx             context.Context
	iter            Iterator
	view            *projectionView
	options         store.SearchOptions
	started         bool
	valid           bool
	closed          bool
	overlayPosition int
	dbRow           *store.DocRef
	emitted         int
	examined        int64
}

func (i *projectionIterator) Examined() int64 { return i.examined }

func (i *projectionIterator) Next() (store.DocRef, bool, error) {
	if i.closed {
		return store.DocRef{}, false, fmt.Errorf("index iterator is closed")
	}
	if err := i.ctx.Err(); err != nil {
		return store.DocRef{}, false, err
	}
	if i.options.Limit > 0 && i.emitted >= i.options.Limit {
		return store.DocRef{}, false, nil
	}
	if !i.started {
		i.valid = i.iter.First()
		i.started = true
	}
	for i.valid && i.dbRow == nil {
		if err := i.ctx.Err(); err != nil {
			return store.DocRef{}, false, err
		}
		if err := i.charge(); err != nil {
			return store.DocRef{}, false, err
		}
		id := string(i.iter.Value())
		key := i.iter.Key()[len(i.view.prefix):]
		if _, replaced := i.view.replacements[id]; !replaced && isInBounds(key, i.options) {
			i.dbRow = &store.DocRef{ID: id, OrderKey: bytes.Clone(key)}
			break
		}
		i.valid = i.iter.Next()
	}
	if !i.valid {
		if err := i.iter.Error(); err != nil {
			return store.DocRef{}, false, err
		}
	}
	var overlay *store.DocRef
	for i.overlayPosition < len(i.view.overlay) {
		if err := i.ctx.Err(); err != nil {
			return store.DocRef{}, false, err
		}
		row := &i.view.overlay[i.overlayPosition]
		if i.options.Upper != nil && bytes.Compare(row.OrderKey, i.options.Upper) >= 0 {
			break
		}
		if isInBounds(row.OrderKey, i.options) {
			overlay = row
			break
		}
		if err := i.charge(); err != nil {
			return store.DocRef{}, false, err
		}
		i.overlayPosition++
	}
	if overlay == nil && i.dbRow == nil {
		return store.DocRef{}, false, nil
	}
	var row store.DocRef
	if overlay != nil && (i.dbRow == nil || bytes.Compare(overlay.OrderKey, i.dbRow.OrderKey) < 0) {
		row = store.DocRef{ID: overlay.ID, OrderKey: bytes.Clone(overlay.OrderKey)}
		if err := i.charge(); err != nil {
			return store.DocRef{}, false, err
		}
		i.overlayPosition++
	} else {
		row = *i.dbRow
		i.dbRow = nil
		i.valid = i.iter.Next()
	}
	i.emitted++
	return row, true, nil
}

func (i *projectionIterator) charge() error {
	if i.view.examined.Add(1) > i.view.maxExamined {
		return store.ErrWorkLimit
	}
	i.examined++
	return nil
}

func (i *projectionIterator) Close() error {
	if i.closed {
		return nil
	}
	i.closed = true
	return i.iter.Close()
}
