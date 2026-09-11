package mem_store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/google/btree"
	"github.com/syntrixbase/syntrix/internal/indexer/store"
)

type projectionIndex struct {
	tree *btree.BTreeG[btreeItem]
	byID map[string][][]byte
}

type generationKey struct{ database, collection, fingerprint string }

func generationIdentity(ref store.QueryIndexRef) generationKey {
	return generationKey{ref.Database, ref.Collection, ref.TemplateFingerprint}
}

func (s *Store) projectionIndex(ref store.QueryIndexRef) *projectionIndex {
	idx := s.partitions[ref]
	if idx == nil {
		idx = &projectionIndex{tree: btree.NewG[btreeItem](32, lessFunc), byID: make(map[string][][]byte)}
		s.partitions[ref] = idx
	}
	return idx
}

func (s *Store) ApplyDocumentProjection(replacements []store.Projection, progress string) error {
	owned, err := store.OwnProjections(replacements)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, projection := range owned {
		idx := s.projectionIndex(projection.Index)
		for _, key := range idx.byID[projection.DocumentID] {
			idx.tree.Delete(btreeItem{orderKey: key, id: projection.DocumentID})
		}
		delete(idx.byID, projection.DocumentID)
		for _, key := range projection.PostingKeys {
			idx.tree.ReplaceOrInsert(btreeItem{orderKey: key, id: projection.DocumentID})
		}
		if len(projection.PostingKeys) != 0 {
			idx.byID[projection.DocumentID] = projection.PostingKeys
		}
	}
	if progress != "" {
		s.progress = progress
	}
	return nil
}

func (s *Store) ReadGeneration(db, collection, fingerprint string) (store.Generation, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, retired := s.retiredDatabases[db]; retired {
		return store.Generation{}, false, nil
	}
	g, ok := s.generations[generationKey{db, collection, fingerprint}]
	catalog, exists := s.catalogs[db]
	if !exists && s.catalogInventoryPublished {
		return store.Generation{}, false, nil
	}
	g, ok = store.ResolveGeneration(catalog, exists, fingerprint, g, ok)
	return g, ok, nil
}

func (s *Store) PublishBootstrapCatalog(catalog store.BootstrapCatalog) error {
	owned, err := store.OwnBootstrapCatalog(catalog)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.catalogInventoryPublished {
		return fmt.Errorf("bootstrap catalog inventory requires plural publication")
	}
	s.catalogs[catalog.Database] = owned
	if catalog.BootstrapProgress != "" {
		s.progress = catalog.BootstrapProgress
	}
	return nil
}

func (s *Store) PublishBootstrapCatalogs(catalogs []store.BootstrapCatalog, progress string) error {
	owned, err := store.OwnBootstrapCatalogs(catalogs, progress)
	if err != nil {
		return err
	}
	replacement := make(map[string]store.BootstrapCatalog, len(owned))
	for _, catalog := range owned {
		replacement[catalog.Database] = catalog
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.catalogs = replacement
	for database := range replacement {
		delete(s.retiredDatabases, database)
	}
	s.catalogInventoryPublished = true
	s.progress = progress
	return nil
}

func (s *Store) ReadBootstrapCatalog(database string) (store.BootstrapCatalog, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	catalog, exists := s.catalogs[database]
	catalog.TemplateFingerprints = slices.Clone(catalog.TemplateFingerprints)
	return catalog, exists, nil
}

func (s *Store) ListBootstrapCatalogs() ([]store.BootstrapCatalog, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]store.BootstrapCatalog, 0, len(s.catalogs))
	for _, catalog := range s.catalogs {
		catalog.TemplateFingerprints = slices.Clone(catalog.TemplateFingerprints)
		result = append(result, catalog)
	}
	slices.SortFunc(result, func(a, b store.BootstrapCatalog) int { return strings.Compare(a.Database, b.Database) })
	return result, nil
}

func (s *Store) ListQueryIndexes() ([]store.QueryIndexRef, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	refs := make(map[store.QueryIndexRef]struct{}, len(s.partitions))
	for ref := range s.partitions {
		refs[ref] = struct{}{}
	}
	for key, generation := range s.generations {
		refs[store.QueryIndexRef{Database: key.database, Collection: key.collection, TemplateFingerprint: key.fingerprint, Generation: generation.ID}] = struct{}{}
	}
	result := make([]store.QueryIndexRef, 0, len(refs))
	for ref := range refs {
		result = append(result, ref)
	}
	slices.SortFunc(result, func(a, b store.QueryIndexRef) int {
		for _, pair := range [][2]string{{a.Database, b.Database}, {a.Collection, b.Collection}, {a.TemplateFingerprint, b.TemplateFingerprint}, {a.Generation, b.Generation}} {
			if cmp := strings.Compare(pair[0], pair[1]); cmp != 0 {
				return cmp
			}
		}
		return 0
	})
	return result, nil
}

func (s *Store) DeleteQueryIndex(ref store.QueryIndexRef) error {
	if ref.Database == "" || ref.Collection == "" || ref.TemplateFingerprint == "" || ref.Generation == "" {
		return fmt.Errorf("incomplete index generation identity")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.partitions, ref)
	key := generationIdentity(ref)
	if s.generations[key].ID == ref.Generation {
		delete(s.generations, key)
	}
	if catalog, exists := s.catalogs[ref.Database]; exists && catalog.Generation == ref.Generation && slices.Contains(catalog.TemplateFingerprints, ref.TemplateFingerprint) {
		s.generations[key] = store.Generation{ID: ref.Generation, Failure: "index partition deleted", BootstrapProgress: catalog.BootstrapProgress}
	}
	return nil
}

func (s *Store) PublishGeneration(ref store.QueryIndexRef, progress string) error {
	if ref.Database == "" || ref.Collection == "" || ref.TemplateFingerprint == "" || ref.Generation == "" {
		return fmt.Errorf("incomplete index generation identity")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if catalog, exists := s.catalogs[ref.Database]; exists && catalog.Generation != ref.Generation && slices.Contains(catalog.TemplateFingerprints, ref.TemplateFingerprint) {
		return fmt.Errorf("index generation does not match active bootstrap catalog")
	}
	s.generations[generationIdentity(ref)] = store.Generation{ID: ref.Generation, Ready: true, BootstrapProgress: progress}
	s.projectionIndex(ref)
	if progress != "" {
		s.progress = progress
	}
	return nil
}

func (s *Store) SetFailure(ref store.QueryIndexRef, failure string) error {
	if ref.Database == "" || ref.Collection == "" || ref.TemplateFingerprint == "" || ref.Generation == "" || failure == "" {
		return fmt.Errorf("incomplete index generation failure")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if catalog, exists := s.catalogs[ref.Database]; exists && catalog.Generation != ref.Generation && slices.Contains(catalog.TemplateFingerprints, ref.TemplateFingerprint) {
		return nil
	}
	key := generationIdentity(ref)
	g := s.generations[key]
	if g.ID != ref.Generation {
		g = store.Generation{ID: ref.Generation}
	}
	g.Ready = false
	g.Failure = failure
	s.generations[key] = g
	s.projectionIndex(ref)
	return nil
}

func (s *Store) ReadView(ctx context.Context, ref store.QueryIndexRef, budget store.ReadBudget) (store.ReadView, error) {
	budget, err := store.NormalizeReadBudget(budget)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Clone switches the mutable tree's copy-on-write ownership, so it requires
	// the same exclusive lock as projection publication.
	s.mu.Lock()
	defer s.mu.Unlock()
	var tree *btree.BTreeG[btreeItem]
	if idx := s.partitions[ref]; idx != nil {
		tree = idx.tree.Clone()
	} else {
		tree = btree.NewG[btreeItem](32, lessFunc)
	}
	return &projectionView{tree: tree, maxExamined: budget.MaxExamined}, nil
}

type projectionView struct {
	mu          sync.RWMutex
	tree        *btree.BTreeG[btreeItem]
	closed      bool
	maxExamined int64
	examined    atomic.Int64
}

func (v *projectionView) Scan(ctx context.Context, opts store.SearchOptions) (store.Iterator, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.closed {
		return nil, errors.New("index read view is closed")
	}
	opts.Lower = bytes.Clone(opts.Lower)
	opts.Upper = bytes.Clone(opts.Upper)
	opts.StartAfter = bytes.Clone(opts.StartAfter)
	return &projectionIterator{view: v, ctx: ctx, opts: opts}, nil
}

func (v *projectionView) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.closed = true
	v.tree = nil
	return nil
}

type projectionIterator struct {
	view            *projectionView
	ctx             context.Context
	opts            store.SearchOptions
	last            btreeItem
	started, closed bool
	count           int
	examined        int64
}

func (it *projectionIterator) Next() (store.DocRef, bool, error) {
	if err := it.ctx.Err(); err != nil {
		return store.DocRef{}, false, err
	}
	if it.closed || (it.opts.Limit > 0 && it.count >= it.opts.Limit) {
		return store.DocRef{}, false, nil
	}
	it.view.mu.RLock()
	defer it.view.mu.RUnlock()
	if it.view.closed {
		return store.DocRef{}, false, errors.New("index read view is closed")
	}
	start := btreeItem{orderKey: it.opts.Lower}
	if it.opts.StartAfter != nil && bytes.Compare(it.opts.StartAfter, start.orderKey) > 0 {
		start.orderKey = it.opts.StartAfter
	}
	if it.started {
		start = it.last
	}
	var result btreeItem
	found := false
	var scanErr error
	it.view.tree.AscendGreaterOrEqual(start, func(item btreeItem) bool {
		if it.ctx.Err() != nil {
			return false
		}
		if it.started && !lessFunc(it.last, item) {
			return true
		}
		if it.opts.Upper != nil && bytes.Compare(item.orderKey, it.opts.Upper) >= 0 {
			return false
		}
		if it.view.examined.Add(1) > it.view.maxExamined {
			scanErr = store.ErrWorkLimit
			return false
		}
		it.examined++
		if it.opts.StartAfter != nil && bytes.Compare(item.orderKey, it.opts.StartAfter) <= 0 {
			return true
		}
		result, found = item, true
		return false
	})
	if scanErr != nil {
		return store.DocRef{}, false, scanErr
	}
	if err := it.ctx.Err(); err != nil {
		return store.DocRef{}, false, err
	}
	if !found {
		it.closed = true
		return store.DocRef{}, false, nil
	}
	it.last, it.started = result, true
	it.count++
	return store.DocRef{ID: result.id, OrderKey: bytes.Clone(result.orderKey)}, true, nil
}

func (it *projectionIterator) Close() error { it.closed = true; return nil }

func (it *projectionIterator) Examined() int64 { return it.examined }
