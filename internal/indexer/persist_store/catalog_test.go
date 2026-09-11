package persist_store

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/indexer/config"
	"github.com/syntrixbase/syntrix/internal/indexer/store"
)

func bootstrapCatalog(ref store.QueryIndexRef, progress string) store.BootstrapCatalog {
	return store.BootstrapCatalog{Database: ref.Database, Generation: ref.Generation, TemplateFingerprints: []string{ref.TemplateFingerprint}, BootstrapProgress: progress}
}

func TestBootstrapCatalogReopenOwnsDefinitionsAndCoversEmptyCollections(t *testing.T) {
	cfg := config.StoreConfig{Path: filepath.Join(t.TempDir(), "index"), BatchSize: 100000, BatchInterval: time.Hour, BlockCacheSize: 1 << 20}
	s, err := NewPebbleStore(cfg, slog.Default())
	require.NoError(t, err)
	ref, empty := queryRef("items"), queryRef("empty")
	require.NoError(t, s.ApplyDocumentProjection([]store.Projection{projection(ref, "a", "1/a"), projection(empty, "missing")}, "scan"))
	catalog := bootstrapCatalog(ref, "boundary")
	catalog.TemplateFingerprints = append(catalog.TemplateFingerprints, ref.TemplateFingerprint)
	require.NoError(t, s.PublishBootstrapCatalog(catalog))
	catalog.TemplateFingerprints[0] = "changed"
	require.NoError(t, s.Close())
	s, err = NewPebbleStore(cfg, slog.Default())
	require.NoError(t, err)
	defer s.Close()
	read, found, err := s.ReadBootstrapCatalog(ref.Database)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, bootstrapCatalog(ref, "boundary"), read)
	read.TemplateFingerprints[0] = "changed"
	catalogs, err := s.ListBootstrapCatalogs()
	require.NoError(t, err)
	require.Equal(t, []store.BootstrapCatalog{bootstrapCatalog(ref, "boundary")}, catalogs)
	for _, collection := range []string{ref.Collection, empty.Collection, "created-after-bootstrap"} {
		generation, found, err := s.ReadGeneration(ref.Database, collection, ref.TemplateFingerprint)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, store.Generation{ID: ref.Generation, Ready: true, BootstrapProgress: "boundary"}, generation)
	}
	refs, err := s.ListQueryIndexes()
	require.NoError(t, err)
	require.ElementsMatch(t, []store.QueryIndexRef{ref, empty}, refs)
	progress, err := s.LoadProgress()
	require.NoError(t, err)
	require.Equal(t, "boundary", progress)
	view, err := s.ReadView(context.Background(), ref, store.ReadBudget{})
	require.NoError(t, err)
	defer view.Close()
	require.Len(t, scanView(t, view, store.SearchOptions{}), 1)
}

func TestBootstrapCatalogGenerationAndFailureAuthority(t *testing.T) {
	s := newProjectionTestStore(t)
	ref := queryRef("items")
	excluded := ref
	excluded.TemplateFingerprint = "excluded"
	require.NoError(t, s.PublishGeneration(excluded, "partial"))
	require.NoError(t, s.PublishBootstrapCatalog(bootstrapCatalog(ref, "boundary1")))
	_, found, err := s.ReadGeneration(excluded.Database, excluded.Collection, excluded.TemplateFingerprint)
	require.NoError(t, err)
	require.False(t, found)
	require.NoError(t, s.SetFailure(ref, "projection failed"))
	generation, found, err := s.ReadGeneration(ref.Database, ref.Collection, ref.TemplateFingerprint)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, store.Generation{ID: ref.Generation, BootstrapProgress: "boundary1", Failure: "projection failed"}, generation)
	fresh := ref
	fresh.Generation = "fresh"
	require.NoError(t, s.PublishBootstrapCatalog(bootstrapCatalog(fresh, "boundary2")))
	require.NoError(t, s.SetFailure(ref, "late old generation failure"))
	generation, found, err = s.ReadGeneration(fresh.Database, fresh.Collection, fresh.TemplateFingerprint)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, store.Generation{ID: fresh.Generation, Ready: true, BootstrapProgress: "boundary2"}, generation)
	require.NoError(t, s.SetFailure(fresh, "new generation failure"))
	generation, found, err = s.ReadGeneration(fresh.Database, fresh.Collection, fresh.TemplateFingerprint)
	require.NoError(t, err)
	require.True(t, found)
	require.False(t, generation.Ready)
	require.Equal(t, "new generation failure", generation.Failure)
	require.Equal(t, "boundary2", generation.BootstrapProgress)
}

type metadataFailDB struct {
	DB
	key     string
	failure error
}

func (d *metadataFailDB) NewBatch() Batch {
	return &metadataFailBatch{Batch: d.DB.NewBatch(), key: d.key, failure: d.failure}
}

type metadataFailBatch struct {
	Batch
	key     string
	failure error
}

func (b *metadataFailBatch) Set(key, value []byte, options *pebble.WriteOptions) error {
	if string(key) == b.key {
		return b.failure
	}
	return b.Batch.Set(key, value, options)
}

func TestBootstrapCatalogPublicationFailurePreservesPriorCatalogAndProgress(t *testing.T) {
	for _, stage := range []string{"catalog", "progress", "commit"} {
		t.Run(stage, func(t *testing.T) {
			db := newMockDB()
			s := newMockPebbleStore(db)
			old := queryRef("items")
			require.NoError(t, s.PublishBootstrapCatalog(bootstrapCatalog(old, "boundary1")))
			fresh := old
			fresh.Generation = "fresh"
			failure := errors.New(stage + " publication failed")
			if stage == "commit" {
				db.commitErr = failure
			} else {
				key := string(catalogKey(old.Database))
				if stage == "progress" {
					key = keyProgress
				}
				s.db = &metadataFailDB{DB: db, key: key, failure: failure}
			}
			require.NoError(t, s.ApplyDocumentProjection([]store.Projection{projection(fresh, "a", "1/a"), projection(queryRef("other"), "b", "2/b")}, "scan2"))
			require.ErrorIs(t, s.PublishBootstrapCatalog(bootstrapCatalog(fresh, "boundary2")), failure)
			require.ErrorIs(t, s.Flush(), failure)
			require.Equal(t, "boundary1", string(db.data[keyProgress]))
			var persisted store.BootstrapCatalog
			require.NoError(t, json.Unmarshal(db.data[string(catalogKey(old.Database))], &persisted))
			require.Equal(t, bootstrapCatalog(old, "boundary1"), persisted)
			require.NotContains(t, db.data, string(append(projectionPrefix("posting", fresh), []byte("1/a")...)))
			require.Len(t, s.projectionFlushing, 2)
			require.ErrorIs(t, s.Close(), failure)
		})
	}
}

func TestDeleteQueryIndexIsolatesGenerationsAndFencesActiveDeletion(t *testing.T) {
	s := newProjectionTestStore(t)
	old := queryRef("items")
	fresh := old
	fresh.Generation = "fresh"
	abandoned := queryRef("abandoned")
	require.NoError(t, s.PublishGeneration(old, "partial"))
	require.NoError(t, s.ApplyDocumentProjection([]store.Projection{projection(old, "a", "1/a"), projection(fresh, "a", "2/a")}, "scan"))
	require.NoError(t, s.PublishBootstrapCatalog(bootstrapCatalog(fresh, "boundary")))
	require.NoError(t, s.ApplyDocumentProjection([]store.Projection{projection(abandoned, "a")}, ""))
	refs, err := s.ListQueryIndexes()
	require.NoError(t, err)
	require.ElementsMatch(t, []store.QueryIndexRef{old, fresh, abandoned}, refs)
	require.NoError(t, s.DeleteQueryIndex(abandoned))
	require.NoError(t, s.DeleteQueryIndex(old))
	refs, err = s.ListQueryIndexes()
	require.NoError(t, err)
	require.Equal(t, []store.QueryIndexRef{fresh}, refs)
	generation, found, err := s.ReadGeneration(fresh.Database, fresh.Collection, fresh.TemplateFingerprint)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, generation.Ready)
	view, err := s.ReadView(context.Background(), fresh, store.ReadBudget{})
	require.NoError(t, err)
	defer view.Close()
	require.NoError(t, s.DeleteQueryIndex(fresh))
	generation, found, err = s.ReadGeneration(fresh.Database, fresh.Collection, fresh.TemplateFingerprint)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, store.Generation{ID: fresh.Generation, BootstrapProgress: "boundary", Failure: "index partition deleted"}, generation)
	for _, staleGeneration := range []string{old.Generation, "future"} {
		stale := fresh
		stale.Generation = staleGeneration
		require.NoError(t, s.SetFailure(stale, "late failure"))
		require.Error(t, s.PublishGeneration(stale, "rejected-progress"))
		current, found, err := s.ReadGeneration(fresh.Database, fresh.Collection, fresh.TemplateFingerprint)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, generation, current)
		progress, err := s.LoadProgress()
		require.NoError(t, err)
		require.Equal(t, "boundary", progress)
	}
	refs, err = s.ListQueryIndexes()
	require.NoError(t, err)
	require.Equal(t, []store.QueryIndexRef{fresh}, refs)
	require.Len(t, scanView(t, view, store.SearchOptions{}), 1)
	deleted, err := s.ReadView(context.Background(), fresh, store.ReadBudget{})
	require.NoError(t, err)
	defer deleted.Close()
	require.Empty(t, scanView(t, deleted, store.SearchOptions{}))
}

func TestDeleteDatabaseRemovesOnlyItsCatalogAndPartitionManifests(t *testing.T) {
	s := newProjectionTestStore(t)
	first, second := queryRef("items"), queryRef("items")
	second.Database = "other"
	for _, ref := range []store.QueryIndexRef{first, second} {
		require.NoError(t, s.ApplyDocumentProjection([]store.Projection{projection(ref, "a")}, ""))
		require.NoError(t, s.PublishBootstrapCatalog(bootstrapCatalog(ref, "boundary")))
	}
	require.NoError(t, s.DeleteDatabase(first.Database))
	catalogs, err := s.ListBootstrapCatalogs()
	require.NoError(t, err)
	require.Equal(t, []store.BootstrapCatalog{bootstrapCatalog(second, "boundary")}, catalogs)
	refs, err := s.ListQueryIndexes()
	require.NoError(t, err)
	require.Equal(t, []store.QueryIndexRef{second}, refs)
	_, found, err := s.ReadBootstrapCatalog(first.Database)
	require.NoError(t, err)
	require.False(t, found)
}

func TestPartialPublicationMetadataReadFailureRetainsPendingProjection(t *testing.T) {
	db := newMockDB()
	s := newMockPebbleStore(db)
	ref := queryRef("items")
	require.NoError(t, s.PublishBootstrapCatalog(bootstrapCatalog(ref, "boundary")))
	failure := errors.New("catalog read failed")
	db.getErr = failure
	require.NoError(t, s.ApplyDocumentProjection([]store.Projection{projection(ref, "a", "1/a")}, "pending"))
	require.ErrorIs(t, s.PublishGeneration(ref, "rejected"), failure)
	require.ErrorIs(t, s.Flush(), failure)
	require.Equal(t, "boundary", string(db.data[keyProgress]))
	require.Len(t, s.projectionPending[ref], 1)
	require.Empty(t, s.projectionFlushing)
	require.ErrorIs(t, s.Close(), failure)
}

func TestPublishBootstrapCatalogsReplacesWholeSetAndPersistsProjections(t *testing.T) {
	cfg := config.StoreConfig{Path: filepath.Join(t.TempDir(), "index"), BatchSize: 100000, BatchInterval: time.Hour, BlockCacheSize: 1 << 20}
	s, err := NewPebbleStore(cfg, slog.Default())
	require.NoError(t, err)
	old := queryRef("items")
	obsolete := old
	obsolete.Database = "obsolete"
	require.NoError(t, s.PublishBootstrapCatalogs([]store.BootstrapCatalog{bootstrapCatalog(old, "old-boundary"), bootstrapCatalog(obsolete, "old-boundary")}, "old-boundary"))
	fresh := old
	fresh.Generation = "fresh"
	added := fresh
	added.Database = "added"
	require.NoError(t, s.ApplyDocumentProjection([]store.Projection{projection(fresh, "a", "1/a"), projection(added, "b", "2/b")}, "pending"))
	expected := []store.BootstrapCatalog{bootstrapCatalog(added, ""), bootstrapCatalog(fresh, "")}
	require.NoError(t, s.PublishBootstrapCatalogs(expected, ""))
	require.NoError(t, s.Close())
	s, err = NewPebbleStore(cfg, slog.Default())
	require.NoError(t, err)
	defer s.Close()
	catalogs, err := s.ListBootstrapCatalogs()
	require.NoError(t, err)
	require.Equal(t, expected, catalogs)
	progress, err := s.LoadProgress()
	require.NoError(t, err)
	require.Empty(t, progress)
	for _, ref := range []store.QueryIndexRef{fresh, added} {
		view, err := s.ReadView(context.Background(), ref, store.ReadBudget{})
		require.NoError(t, err)
		require.Len(t, scanView(t, view, store.SearchOptions{}), 1)
		require.NoError(t, view.Close())
	}
}

type commitFailureDB struct {
	DB
	failure error
}

func (d *commitFailureDB) NewBatch() Batch {
	return &commitFailureBatch{Batch: d.DB.NewBatch(), failure: d.failure}
}

type commitFailureBatch struct {
	Batch
	failure error
}

func (b *commitFailureBatch) Commit(*pebble.WriteOptions) error { return b.failure }

func TestPublishBootstrapCatalogsFailureReopensExactPreviousSet(t *testing.T) {
	for _, stage := range []string{"catalog", "inventory", "progress", "commit"} {
		t.Run(stage, func(t *testing.T) {
			cfg := config.StoreConfig{Path: filepath.Join(t.TempDir(), "index"), BatchSize: 100000, BatchInterval: time.Hour, BlockCacheSize: 1 << 20}
			s, err := NewPebbleStore(cfg, slog.Default())
			require.NoError(t, err)
			old := queryRef("items")
			obsolete := old
			obsolete.Database = "obsolete"
			previous := []store.BootstrapCatalog{bootstrapCatalog(old, "old-boundary"), bootstrapCatalog(obsolete, "old-boundary")}
			require.NoError(t, s.PublishBootstrapCatalogs(previous, "old-boundary"))
			fresh := old
			fresh.Generation = "fresh"
			added := fresh
			added.Database = "added"
			require.NoError(t, s.ApplyDocumentProjection([]store.Projection{projection(fresh, "a", "1/a"), projection(added, "b", "2/b")}, "pending"))
			failure := errors.New(stage + " publication failed")
			if stage == "commit" {
				s.db = &commitFailureDB{DB: s.db, failure: failure}
			} else {
				key := string(catalogKey(fresh.Database))
				if stage == "inventory" {
					key = catalogInventoryKey
				}
				if stage == "progress" {
					key = keyProgress
				}
				s.db = &metadataFailDB{DB: s.db, key: key, failure: failure}
			}
			require.ErrorIs(t, s.PublishBootstrapCatalogs([]store.BootstrapCatalog{bootstrapCatalog(added, "new-boundary"), bootstrapCatalog(fresh, "new-boundary")}, "new-boundary"), failure)
			require.ErrorIs(t, s.Close(), failure)
			reopened, err := NewPebbleStore(cfg, slog.Default())
			require.NoError(t, err)
			defer reopened.Close()
			catalogs, err := reopened.ListBootstrapCatalogs()
			require.NoError(t, err)
			require.Equal(t, previous, catalogs)
			var inventory catalogInventory
			found, err := reopened.readMetadata([]byte(catalogInventoryKey), &inventory)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, catalogInventory{Generation: old.Generation, BootstrapProgress: "old-boundary"}, inventory)

			progress, err := reopened.LoadProgress()
			require.NoError(t, err)
			require.Equal(t, "old-boundary", progress)
			for _, ref := range []store.QueryIndexRef{fresh, added} {
				view, err := reopened.ReadView(context.Background(), ref, store.ReadBudget{})
				require.NoError(t, err)
				require.Empty(t, scanView(t, view, store.SearchOptions{}))
				require.NoError(t, view.Close())
			}
		})
	}
}

func TestCompleteCatalogInventoryPreventsRetiredGenerationFallbackAfterReopen(t *testing.T) {
	cfg := config.StoreConfig{Path: filepath.Join(t.TempDir(), "index"), BatchSize: 100000, BatchInterval: time.Hour, BlockCacheSize: 1 << 20}
	s, err := NewPebbleStore(cfg, slog.Default())
	require.NoError(t, err)
	retired, active := queryRef("items"), queryRef("items")
	retired.Database = "retired"
	require.NoError(t, s.PublishGeneration(retired, "partial"))
	_, found, err := s.ReadGeneration(retired.Database, retired.Collection, retired.TemplateFingerprint)
	require.NoError(t, err)
	require.True(t, found)
	require.NoError(t, s.PublishBootstrapCatalogs([]store.BootstrapCatalog{bootstrapCatalog(active, "boundary")}, "boundary"))
	require.Error(t, s.PublishBootstrapCatalog(bootstrapCatalog(retired, "rejected")))
	catalogs, err := s.ListBootstrapCatalogs()
	require.NoError(t, err)
	require.Equal(t, []store.BootstrapCatalog{bootstrapCatalog(active, "boundary")}, catalogs)
	progress, err := s.LoadProgress()
	require.NoError(t, err)
	require.Equal(t, "boundary", progress)
	_, found, err = s.ReadGeneration(retired.Database, retired.Collection, retired.TemplateFingerprint)
	require.NoError(t, err)
	require.False(t, found)
	require.NoError(t, s.DeleteDatabase(active.Database))
	require.NoError(t, s.Close())
	reopened, err := NewPebbleStore(cfg, slog.Default())
	require.NoError(t, err)
	defer reopened.Close()
	_, found, err = reopened.ReadGeneration(retired.Database, retired.Collection, retired.TemplateFingerprint)
	require.NoError(t, err)
	require.False(t, found)
	var inventory catalogInventory
	found, err = reopened.readMetadata([]byte(catalogInventoryKey), &inventory)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, catalogInventory{Generation: active.Generation, BootstrapProgress: "boundary"}, inventory)
}

func TestMalformedCatalogInventoryFailsGenerationLookup(t *testing.T) {
	for _, value := range []string{"invalid JSON", "{}"} {
		t.Run(value, func(t *testing.T) {
			db := newMockDB()
			s := newMockPebbleStore(db)
			ref := queryRef("items")
			require.NoError(t, s.PublishGeneration(ref, "partial"))
			db.data[catalogInventoryKey] = []byte(value)
			_, found, err := s.ReadGeneration(ref.Database, ref.Collection, ref.TemplateFingerprint)
			require.Error(t, err)
			require.False(t, found)
			require.NoError(t, s.Close())
		})
	}
}

func TestDatabaseRetirementPersistsAndFullBootstrapReactivatesIncludedScope(t *testing.T) {
	cfg := config.StoreConfig{Path: filepath.Join(t.TempDir(), "index"), BatchSize: 100000, BatchInterval: time.Hour, BlockCacheSize: 1 << 20}
	s, err := NewPebbleStore(cfg, slog.Default())
	require.NoError(t, err)
	ref, other := queryRef("items"), queryRef("items")
	other.Database = "other"
	for _, index := range []store.QueryIndexRef{ref, other} {
		require.NoError(t, s.ApplyDocumentProjection([]store.Projection{projection(index, "a", "1/a")}, ""))
	}
	require.NoError(t, s.PublishBootstrapCatalogs([]store.BootstrapCatalog{bootstrapCatalog(ref, "boundary"), bootstrapCatalog(other, "boundary")}, "boundary"))
	require.NoError(t, s.DeleteDatabase(ref.Database))
	retired, err := s.IsDatabaseRetired(ref.Database)
	require.NoError(t, err)
	require.True(t, retired)
	retired, err = s.IsDatabaseRetired(other.Database)
	require.NoError(t, err)
	require.False(t, retired)
	require.NoError(t, s.Close())
	s, err = NewPebbleStore(cfg, slog.Default())
	require.NoError(t, err)
	retired, err = s.IsDatabaseRetired(ref.Database)
	require.NoError(t, err)
	require.True(t, retired)
	require.NoError(t, s.DeleteDatabase(other.Database))
	fresh := ref
	fresh.Generation = "fresh"
	require.NoError(t, s.ApplyDocumentProjection([]store.Projection{projection(fresh, "b", "2/b")}, ""))
	require.NoError(t, s.PublishGeneration(fresh, ""))
	_, found, err := s.ReadGeneration(fresh.Database, fresh.Collection, fresh.TemplateFingerprint)
	require.NoError(t, err)
	require.False(t, found)
	require.NoError(t, s.PublishBootstrapCatalogs([]store.BootstrapCatalog{bootstrapCatalog(fresh, "fresh-boundary")}, "fresh-boundary"))
	retired, err = s.IsDatabaseRetired(fresh.Database)
	require.NoError(t, err)
	require.False(t, retired)
	retired, err = s.IsDatabaseRetired(other.Database)
	require.NoError(t, err)
	require.True(t, retired)
	require.NoError(t, s.Close())
	reopened, err := NewPebbleStore(cfg, slog.Default())
	require.NoError(t, err)
	defer reopened.Close()
	retired, err = reopened.IsDatabaseRetired(fresh.Database)
	require.NoError(t, err)
	require.False(t, retired)
	retired, err = reopened.IsDatabaseRetired(other.Database)
	require.NoError(t, err)
	require.True(t, retired)
	generation, found, err := reopened.ReadGeneration(fresh.Database, fresh.Collection, fresh.TemplateFingerprint)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, store.Generation{ID: fresh.Generation, Ready: true, BootstrapProgress: "fresh-boundary"}, generation)
}

func TestDatabaseRetirementFailurePreservesCatalogAndPostingsAfterReopen(t *testing.T) {
	for _, stage := range []string{"marker", "commit"} {
		t.Run(stage, func(t *testing.T) {
			cfg := config.StoreConfig{Path: filepath.Join(t.TempDir(), "index"), BatchSize: 100000, BatchInterval: time.Hour, BlockCacheSize: 1 << 20}
			s, err := NewPebbleStore(cfg, slog.Default())
			require.NoError(t, err)
			ref := queryRef("items")
			require.NoError(t, s.ApplyDocumentProjection([]store.Projection{projection(ref, "a", "1/a")}, ""))
			require.NoError(t, s.PublishBootstrapCatalogs([]store.BootstrapCatalog{bootstrapCatalog(ref, "boundary")}, "boundary"))
			failure := errors.New(stage + " retirement failed")
			if stage == "commit" {
				s.db = &commitFailureDB{DB: s.db, failure: failure}
			} else {
				s.db = &metadataFailDB{DB: s.db, key: string(retiredDatabaseKey(ref.Database)), failure: failure}
			}
			require.ErrorIs(t, s.DeleteDatabase(ref.Database), failure)
			_, err = s.IsDatabaseRetired(ref.Database)
			require.ErrorIs(t, err, failure)
			require.ErrorIs(t, s.Close(), failure)
			reopened, err := NewPebbleStore(cfg, slog.Default())
			require.NoError(t, err)
			defer reopened.Close()
			retired, err := reopened.IsDatabaseRetired(ref.Database)
			require.NoError(t, err)
			require.False(t, retired)
			catalog, found, err := reopened.ReadBootstrapCatalog(ref.Database)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, bootstrapCatalog(ref, "boundary"), catalog)
			view, err := reopened.ReadView(context.Background(), ref, store.ReadBudget{})
			require.NoError(t, err)
			require.Len(t, scanView(t, view, store.SearchOptions{}), 1)
			require.NoError(t, view.Close())
		})
	}
}

func TestFailedBootstrapReactivationRetainsRetirementAfterReopen(t *testing.T) {
	cfg := config.StoreConfig{Path: filepath.Join(t.TempDir(), "index"), BatchSize: 100000, BatchInterval: time.Hour, BlockCacheSize: 1 << 20}
	s, err := NewPebbleStore(cfg, slog.Default())
	require.NoError(t, err)
	ref := queryRef("items")
	require.NoError(t, s.DeleteDatabase(ref.Database))
	require.NoError(t, s.ApplyDocumentProjection([]store.Projection{projection(ref, "a", "1/a")}, ""))
	failure := errors.New("reactivation commit failed")
	s.db = &commitFailureDB{DB: s.db, failure: failure}
	require.ErrorIs(t, s.PublishBootstrapCatalogs([]store.BootstrapCatalog{bootstrapCatalog(ref, "boundary")}, "boundary"), failure)
	require.ErrorIs(t, s.Close(), failure)
	reopened, err := NewPebbleStore(cfg, slog.Default())
	require.NoError(t, err)
	defer reopened.Close()
	retired, err := reopened.IsDatabaseRetired(ref.Database)
	require.NoError(t, err)
	require.True(t, retired)
	_, found, err := reopened.ReadBootstrapCatalog(ref.Database)
	require.NoError(t, err)
	require.False(t, found)
	view, err := reopened.ReadView(context.Background(), ref, store.ReadBudget{})
	require.NoError(t, err)
	require.Empty(t, scanView(t, view, store.SearchOptions{}))
	require.NoError(t, view.Close())
}
