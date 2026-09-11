package persist_store

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/cockroachdb/pebble"
	"github.com/syntrixbase/syntrix/internal/indexer/store"
)

const catalogInventoryKey = "v2/catalog-inventory"

type catalogInventory struct {
	Generation        string
	BootstrapProgress string
}

func retiredDatabaseKey(database string) []byte {
	return []byte("v2/retired/" + encodePathComponent(database))
}

func (s *PebbleStore) IsDatabaseRetired(database string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.writeErrorLocked(); err != nil {
		return false, err
	}
	return s.isDatabaseRetiredLocked(database)
}

func (s *PebbleStore) isDatabaseRetiredLocked(database string) (bool, error) {
	var retired bool
	exists, err := s.readMetadata(retiredDatabaseKey(database), &retired)
	if err != nil {
		return false, err
	}
	if exists && !retired {
		return false, fmt.Errorf("invalid database retirement marker")
	}
	return retired, nil
}

func catalogKey(database string) []byte { return []byte("v2/catalog/" + encodePathComponent(database)) }

type metadataRecord interface {
	store.QueryIndexRef | store.Generation | store.BootstrapCatalog | catalogInventory
}

// The closed record set contains only strings, booleans, and string slices,
// so every admitted value has a JSON representation.
func encodeMetadata[T metadataRecord](value T) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}

func setMetadata[T metadataRecord](batch Batch, key []byte, value T) error {
	return batch.Set(key, encodeMetadata(value), nil)
}

func setPartition(batch Batch, ref store.QueryIndexRef) error {
	return setMetadata(batch, projectionPrefix("partition", ref), ref)
}

func (s *PebbleStore) readMetadata(key []byte, value any) (bool, error) {
	encoded, closer, err := s.db.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	err = errors.Join(json.Unmarshal(encoded, value), closer.Close())
	return err == nil, err
}

func (s *PebbleStore) ReadGeneration(db, collection, fingerprint string) (store.Generation, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.writeErrorLocked(); err != nil {
		return store.Generation{}, false, err
	}
	retired, err := s.isDatabaseRetiredLocked(db)
	if err != nil {
		return store.Generation{}, false, err
	}
	if retired {
		return store.Generation{}, false, nil
	}
	var catalog store.BootstrapCatalog
	catalogExists, err := s.readMetadata(catalogKey(db), &catalog)
	if err != nil {
		return store.Generation{}, false, err
	}
	if !catalogExists {
		// Complete inventories fence earlier partial publications for omitted databases.
		var inventory catalogInventory
		exists, err := s.readMetadata([]byte(catalogInventoryKey), &inventory)
		if err != nil {
			return store.Generation{}, false, err
		}
		if exists {
			if inventory.Generation == "" {
				return store.Generation{}, false, fmt.Errorf("invalid bootstrap catalog inventory")
			}
			return store.Generation{}, false, nil
		}
	}
	var local store.Generation
	localExists, err := s.readMetadata(generationKey(db, collection, fingerprint), &local)
	if err != nil {
		return store.Generation{}, false, err
	}
	generation, found := store.ResolveGeneration(catalog, catalogExists, fingerprint, local, localExists)
	return generation, found, nil
}

func (s *PebbleStore) PublishGeneration(ref store.QueryIndexRef, progress string) error {
	if ref.Database == "" || ref.Collection == "" || ref.TemplateFingerprint == "" || ref.Generation == "" {
		return fmt.Errorf("incomplete index generation identity")
	}
	value := encodeMetadata(store.Generation{ID: ref.Generation, Ready: true, BootstrapProgress: progress})
	return s.publishMetadata(&metadataWrite{key: generationKey(ref.Database, ref.Collection, ref.TemplateFingerprint), value: value, progress: progress, partition: &ref})
}

func (s *PebbleStore) publishMetadata(write *metadataWrite) error {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	s.mu.RLock()
	err := s.writeErrorLocked()
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	return s.flushLocked(write)
}

func (s *PebbleStore) PublishBootstrapCatalog(catalog store.BootstrapCatalog) error {
	owned, err := store.OwnBootstrapCatalog(catalog)
	if err != nil {
		return err
	}
	value := encodeMetadata(owned)
	return s.publishMetadata(&metadataWrite{key: catalogKey(owned.Database), value: value, progress: owned.BootstrapProgress, singleCatalog: true})
}

func (s *PebbleStore) ReadBootstrapCatalog(database string) (store.BootstrapCatalog, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.writeErrorLocked(); err != nil {
		return store.BootstrapCatalog{}, false, err
	}
	var catalog store.BootstrapCatalog
	exists, err := s.readMetadata(catalogKey(database), &catalog)
	return catalog, exists, err
}

func (s *PebbleStore) ListBootstrapCatalogs() (catalogs []store.BootstrapCatalog, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.writeErrorLocked(); err != nil {
		return nil, err
	}
	prefix := []byte("v2/catalog/")
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixEnd(prefix)})
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, iter.Close()) }()
	for iter.First(); iter.Valid(); iter.Next() {
		var catalog store.BootstrapCatalog
		if err := json.Unmarshal(iter.Value(), &catalog); err != nil {
			return nil, err
		}
		catalogs = append(catalogs, catalog)
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	sort.Slice(catalogs, func(i, j int) bool { return catalogs[i].Database < catalogs[j].Database })
	return catalogs, nil
}

func (s *PebbleStore) SetFailure(ref store.QueryIndexRef, failure string) error {
	if ref.Database == "" || ref.Collection == "" || ref.TemplateFingerprint == "" || ref.Generation == "" || failure == "" {
		return fmt.Errorf("incomplete index generation failure")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writeErrorLocked(); err != nil {
		return err
	}
	var catalog store.BootstrapCatalog
	catalogExists, err := s.readMetadata(catalogKey(ref.Database), &catalog)
	if err != nil {
		return err
	}
	covered := catalogExists && slices.Contains(catalog.TemplateFingerprints, ref.TemplateFingerprint)
	if covered && catalog.Generation != ref.Generation {
		return nil
	}
	key := generationKey(ref.Database, ref.Collection, ref.TemplateFingerprint)
	var local store.Generation
	exists, err := s.readMetadata(key, &local)
	if err != nil {
		return err
	}
	generation := store.Generation{ID: ref.Generation}
	if exists && local.ID == ref.Generation {
		generation = local
	}
	if covered {
		generation.BootstrapProgress = catalog.BootstrapProgress
	}
	generation.Ready = false
	generation.Failure = failure
	batch := s.db.NewBatch()
	err = setPartition(batch, ref)
	if err == nil {
		err = setMetadata(batch, key, generation)
	}
	return s.commitMetadataBatch(batch, err)
}

// commitMetadataBatch is called with mu held so a failed metadata change also
// prevents queries and later checkpoint publication.
func (s *PebbleStore) commitMetadataBatch(batch Batch, err error) error {
	if err == nil {
		err = batch.Commit(pebble.Sync)
	}
	err = errors.Join(err, batch.Close())
	if err != nil {
		s.flushErr = err
	}
	return err
}

func (s *PebbleStore) ListQueryIndexes() (refs []store.QueryIndexRef, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.writeErrorLocked(); err != nil {
		return nil, err
	}
	seen := make(map[store.QueryIndexRef]struct{})
	for ref := range s.projectionFlushing {
		seen[ref] = struct{}{}
	}
	for ref := range s.projectionPending {
		seen[ref] = struct{}{}
	}
	prefix := []byte("v2/partition/")
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixEnd(prefix)})
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, iter.Close()) }()
	for iter.First(); iter.Valid(); iter.Next() {
		var ref store.QueryIndexRef
		if err := json.Unmarshal(iter.Value(), &ref); err != nil {
			return nil, err
		}
		seen[ref] = struct{}{}
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	for ref := range seen {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool {
		a, b := refs[i], refs[j]
		if a.Database != b.Database {
			return a.Database < b.Database
		}
		if a.Collection != b.Collection {
			return a.Collection < b.Collection
		}
		if a.TemplateFingerprint != b.TemplateFingerprint {
			return a.TemplateFingerprint < b.TemplateFingerprint
		}
		return a.Generation < b.Generation
	})
	return refs, nil
}

func (s *PebbleStore) DeleteQueryIndex(ref store.QueryIndexRef) error {
	if ref.Database == "" || ref.Collection == "" || ref.TemplateFingerprint == "" || ref.Generation == "" {
		return fmt.Errorf("incomplete index generation identity")
	}
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writeErrorLocked(); err != nil {
		return err
	}
	var catalog store.BootstrapCatalog
	catalogExists, err := s.readMetadata(catalogKey(ref.Database), &catalog)
	if err != nil {
		return err
	}
	active := catalogExists && catalog.Generation == ref.Generation && slices.Contains(catalog.TemplateFingerprints, ref.TemplateFingerprint)
	var local store.Generation
	key := generationKey(ref.Database, ref.Collection, ref.TemplateFingerprint)
	localExists, err := s.readMetadata(key, &local)
	if err != nil {
		return err
	}
	batch := s.db.NewBatch()
	for _, kind := range []string{"posting", "reverse"} {
		if err = s.deletePrefixInBatch(batch, projectionPrefix(kind, ref)); err != nil {
			break
		}
	}
	if err == nil {
		if active {
			err = setPartition(batch, ref)
			if err == nil {
				err = setMetadata(batch, key, store.Generation{ID: ref.Generation, BootstrapProgress: catalog.BootstrapProgress, Failure: "index partition deleted"})
			}
		} else {
			err = batch.Delete(projectionPrefix("partition", ref), nil)
			if err == nil && localExists && local.ID == ref.Generation {
				err = batch.Delete(key, nil)
			}
		}
	}
	if err := s.commitMetadataBatch(batch, err); err != nil {
		return err
	}
	delete(s.projectionPending, ref)
	return nil
}

func (s *PebbleStore) validatePublishedGenerationLocked(ref store.QueryIndexRef) error {
	var catalog store.BootstrapCatalog
	exists, err := s.readMetadata(catalogKey(ref.Database), &catalog)
	if err != nil {
		s.flushErr = err
		return err
	}
	if exists && catalog.Generation != ref.Generation && slices.Contains(catalog.TemplateFingerprints, ref.TemplateFingerprint) {
		return fmt.Errorf("index generation does not match active bootstrap catalog")
	}
	return nil
}

func (s *PebbleStore) PublishBootstrapCatalogs(catalogs []store.BootstrapCatalog, progress string) error {
	owned, err := store.OwnBootstrapCatalogs(catalogs, progress)
	if err != nil {
		return err
	}
	inventory := encodeMetadata(catalogInventory{Generation: owned[0].Generation, BootstrapProgress: progress})
	publication := &metadataWrite{replaceCatalogs: true, progress: progress, inventory: inventory, catalogs: make([]catalogRecord, 0, len(owned))}
	for _, catalog := range owned {
		value := encodeMetadata(catalog)
		publication.catalogs = append(publication.catalogs, catalogRecord{database: catalog.Database, key: catalogKey(catalog.Database), value: value})
	}
	return s.publishMetadata(publication)
}

func (s *PebbleStore) applyMetadata(batch Batch, publication *metadataWrite, progress string) error {
	if publication != nil {
		if publication.replaceCatalogs {
			if err := s.deletePrefixInBatch(batch, []byte("v2/catalog/")); err != nil {
				return err
			}
			for _, catalog := range publication.catalogs {
				if err := batch.Set(catalog.key, catalog.value, nil); err != nil {
					return err
				}
				if err := batch.Delete(retiredDatabaseKey(catalog.database), nil); err != nil {
					return err
				}
			}
			if err := batch.Set([]byte(catalogInventoryKey), publication.inventory, nil); err != nil {
				return err
			}
			return batch.Set([]byte(keyProgress), []byte(publication.progress), nil)
		}
		if publication.partition != nil {
			if err := setPartition(batch, *publication.partition); err != nil {
				return err
			}
		}
		if err := batch.Set(publication.key, publication.value, nil); err != nil {
			return err
		}
		if publication.progress != "" {
			progress = publication.progress
		}
	}
	if progress != "" {
		return batch.Set([]byte(keyProgress), []byte(progress), nil)
	}
	return nil
}

func (s *PebbleStore) validateSingleCatalogPublicationLocked() error {
	var inventory catalogInventory
	exists, err := s.readMetadata([]byte(catalogInventoryKey), &inventory)
	if err != nil {
		s.flushErr = err
		return err
	}
	if exists {
		return fmt.Errorf("single catalog publication cannot modify a complete bootstrap inventory")
	}
	return nil
}
