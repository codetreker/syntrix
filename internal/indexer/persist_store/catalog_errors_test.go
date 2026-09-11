package persist_store

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/indexer/store"
)

func TestMetadataPublicationsRejectInvalidIdentityWithoutAdvancingCheckpoint(t *testing.T) {
	db := newMockDB()
	s := newMockPebbleStore(db)
	ref := queryRef("items")
	original := bootstrapCatalog(ref, "boundary")
	require.NoError(t, s.PublishBootstrapCatalog(original))
	invalidGeneration := ref
	invalidGeneration.Collection = ""
	invalidCatalog := original
	invalidCatalog.Database = ""
	mismatchedCatalog := original
	mismatchedCatalog.Generation = "different"
	calls := []func() error{
		func() error { return s.PublishGeneration(invalidGeneration, "rejected") },
		func() error { return s.PublishBootstrapCatalog(invalidCatalog) },
		func() error { return s.PublishBootstrapCatalogs(nil, "rejected") },
		func() error {
			return s.PublishBootstrapCatalogs([]store.BootstrapCatalog{original}, "mismatched-boundary")
		},
		func() error {
			return s.PublishBootstrapCatalogs([]store.BootstrapCatalog{original, mismatchedCatalog}, "boundary")
		},
	}
	for _, call := range calls {
		require.Error(t, call())
		progress, err := s.LoadProgress()
		require.NoError(t, err)
		require.Equal(t, "boundary", progress)
		catalog, found, err := s.ReadBootstrapCatalog(ref.Database)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, original, catalog)
	}
	require.NoError(t, s.Close())
}

func TestCatalogOperationsRejectClosedStore(t *testing.T) {
	s := newMockPebbleStore(newMockDB())
	require.NoError(t, s.Close())
	ref := queryRef("items")
	require.ErrorContains(t, s.PublishGeneration(ref, "boundary"), "closed")
	require.ErrorContains(t, s.PublishBootstrapCatalog(bootstrapCatalog(ref, "boundary")), "closed")
	require.ErrorContains(t, s.PublishBootstrapCatalogs([]store.BootstrapCatalog{bootstrapCatalog(ref, "boundary")}, "boundary"), "closed")
	require.ErrorContains(t, s.DeleteDatabase(ref.Database), "closed")
	refs, err := s.ListQueryIndexes()
	require.ErrorContains(t, err, "closed")
	require.Nil(t, refs)
	catalogs, err := s.ListBootstrapCatalogs()
	require.ErrorContains(t, err, "closed")
	require.Nil(t, catalogs)
}

func TestCatalogEnumerationReportsIteratorAndCorruptionFailures(t *testing.T) {
	for _, stage := range []string{"open", "advance", "corrupt"} {
		t.Run(stage, func(t *testing.T) {
			db := newMockDB()
			s := newMockPebbleStore(db)
			ref := queryRef("items")
			require.NoError(t, s.PublishGeneration(ref, "boundary"))
			require.NoError(t, s.PublishBootstrapCatalog(bootstrapCatalog(ref, "boundary")))
			failure := errors.New(stage + " iterator failure")
			switch stage {
			case "open":
				db.newIterErr = failure
			case "advance":
				db.iterError = failure
			case "corrupt":
				db.data[string(projectionPrefix("partition", ref))] = []byte("corrupt record")
				db.data[string(catalogKey(ref.Database))] = []byte("corrupt record")
			}
			refs, err := s.ListQueryIndexes()
			require.Nil(t, refs)
			if stage == "corrupt" {
				var syntax *json.SyntaxError
				require.ErrorAs(t, err, &syntax)
			} else {
				require.ErrorIs(t, err, failure)
			}
			catalogs, err := s.ListBootstrapCatalogs()
			require.Nil(t, catalogs)
			if stage == "corrupt" {
				var syntax *json.SyntaxError
				require.ErrorAs(t, err, &syntax)
			} else {
				require.ErrorIs(t, err, failure)
			}
			require.NoError(t, s.Close())
		})
	}
}

func TestCatalogEnumerationRetainsFlushingAndPendingEmptyPartitions(t *testing.T) {
	s := newProjectionTestStore(t)
	first := queryRef("items")
	laterFingerprint := first
	laterFingerprint.TemplateFingerprint = "z-fingerprint"
	laterGeneration := first
	laterGeneration.Generation = "z-generation"
	blocked := &blockedCommitDB{DB: s.db, entered: make(chan struct{}, 1), release: make(chan struct{})}
	s.db = blocked
	var release sync.Once
	defer release.Do(func() { close(blocked.release) })
	require.NoError(t, s.ApplyDocumentProjection([]store.Projection{projection(first, "empty")}, ""))
	flushed := make(chan struct{})
	go func() { s.doFlush(); close(flushed) }()
	select {
	case <-blocked.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("flush did not start")
	}
	require.NoError(t, s.ApplyDocumentProjection([]store.Projection{projection(laterFingerprint, "empty"), projection(laterGeneration, "empty")}, ""))
	refs, err := s.ListQueryIndexes()
	require.NoError(t, err)
	require.Equal(t, []store.QueryIndexRef{first, laterGeneration, laterFingerprint}, refs)
	release.Do(func() { close(blocked.release) })
	select {
	case <-flushed:
	case <-time.After(5 * time.Second):
		t.Fatal("flush did not finish")
	}
}

func TestDatabaseRetirementRejectsCorruptMarkers(t *testing.T) {
	for _, value := range []string{"false", "corrupt record"} {
		t.Run(value, func(t *testing.T) {
			db := newMockDB()
			s := newMockPebbleStore(db)
			ref := queryRef("items")
			require.NoError(t, s.PublishGeneration(ref, "boundary"))
			db.data[string(retiredDatabaseKey(ref.Database))] = []byte(value)
			retired, err := s.IsDatabaseRetired(ref.Database)
			require.Error(t, err)
			require.False(t, retired)
			_, found, err := s.ReadGeneration(ref.Database, ref.Collection, ref.TemplateFingerprint)
			require.Error(t, err)
			require.False(t, found)
			require.NoError(t, s.Close())
		})
	}
}

func TestSingleCatalogPublicationFailsClosedOnUnreadableInventory(t *testing.T) {
	for _, stage := range []string{"read", "corrupt"} {
		t.Run(stage, func(t *testing.T) {
			db := newMockDB()
			s := newMockPebbleStore(db)
			ref := queryRef("items")
			original := bootstrapCatalog(ref, "boundary")
			require.NoError(t, s.PublishBootstrapCatalog(original))
			failure := errors.New("inventory read failed")
			if stage == "read" {
				db.getErr = failure
				db.getErrKey = []byte(catalogInventoryKey)
			} else {
				db.data[catalogInventoryKey] = []byte("corrupt inventory")
			}
			require.NoError(t, s.ApplyDocumentProjection([]store.Projection{projection(ref, "a", "1/a")}, "pending"))
			replacement := original
			replacement.Database = "other"
			err := s.PublishBootstrapCatalog(replacement)
			if stage == "read" {
				require.ErrorIs(t, err, failure)
			} else {
				var syntax *json.SyntaxError
				require.ErrorAs(t, err, &syntax)
			}
			require.Error(t, s.Flush())
			require.Equal(t, "boundary", string(db.data[keyProgress]))
			require.NotContains(t, db.data, string(catalogKey(replacement.Database)))
			require.Len(t, s.projectionPending[ref], 1)
			require.Empty(t, s.projectionFlushing)
			require.Error(t, s.Close())
		})
	}
}

func TestDeleteDatabaseDiscardsQueuedScalarWritesAndDeletesOnlyItsScope(t *testing.T) {
	s := newMockPebbleStore(newMockDB())
	require.NoError(t, s.Upsert("retired", "items/*", "one", "a", []byte("1/a"), ""))
	require.NoError(t, s.Upsert("retained", "items/*", "one", "b", []byte("2/b"), ""))
	require.NoError(t, s.DeleteIndex("retired", "other/*", "two"))
	require.NoError(t, s.DeleteDatabase("retired"))
	s.doFlush()
	require.NoError(t, s.Flush())
	_, found := s.Get("retired", "items/*", "one", "a")
	require.False(t, found)
	key, found := s.Get("retained", "items/*", "one", "b")
	require.True(t, found)
	require.Equal(t, []byte("2/b"), key)
	retired, err := s.IsDatabaseRetired("retired")
	require.NoError(t, err)
	require.True(t, retired)
	retired, err = s.IsDatabaseRetired("retained")
	require.NoError(t, err)
	require.False(t, retired)
	require.NoError(t, s.Close())
}
