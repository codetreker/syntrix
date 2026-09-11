package persist_store

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/indexer/config"
	"github.com/syntrixbase/syntrix/internal/indexer/store"
)

func queryRef(collection string) store.QueryIndexRef {
	return store.QueryIndexRef{Database: "db", Collection: collection, TemplateFingerprint: "fingerprint", Generation: "generation"}
}

func projection(ref store.QueryIndexRef, id string, keys ...string) store.Projection {
	p := store.Projection{Index: ref, DocumentID: id}
	for _, key := range keys {
		p.PostingKeys = append(p.PostingKeys, []byte(key))
	}
	return p
}

func scanView(t *testing.T, view store.ReadView, options store.SearchOptions) []store.DocRef {
	t.Helper()
	iter, err := view.Scan(context.Background(), options)
	require.NoError(t, err)
	defer func() { require.NoError(t, iter.Close()) }()
	var rows []store.DocRef
	for {
		row, ok, err := iter.Next()
		require.NoError(t, err)
		if !ok {
			return rows
		}
		rows = append(rows, row)
	}
}

func newProjectionTestStore(t *testing.T) *PebbleStore {
	t.Helper()
	s, err := NewPebbleStore(config.StoreConfig{Path: filepath.Join(t.TempDir(), "index"), BatchSize: 100000, BatchInterval: time.Hour, BlockCacheSize: 1 << 20}, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s
}

func TestProjectionReplacementSnapshotAndPartitions(t *testing.T) {
	s := newProjectionTestStore(t)
	ref, other := queryRef("one"), queryRef("two")
	require.NoError(t, s.ApplyDocumentProjection([]store.Projection{
		projection(ref, "a", "1/a", "3/a"), projection(ref, "b", "2/b"), projection(other, "a", "1/a"),
	}, "p1"))
	s.doFlush()
	require.NoError(t, s.Flush())
	original, err := s.ReadView(context.Background(), ref, store.ReadBudget{})
	require.NoError(t, err)
	defer original.Close()
	replacement := projection(ref, "a", "8/a", "9/a", "9/a")
	require.NoError(t, s.ApplyDocumentProjection([]store.Projection{replacement}, "p2"))
	replacement.PostingKeys[0][0] = '0'
	pending, err := s.ReadView(context.Background(), ref, store.ReadBudget{})
	require.NoError(t, err)
	defer pending.Close()
	require.Equal(t, []store.DocRef{{ID: "b", OrderKey: []byte("2/b")}}, scanView(t, pending, store.SearchOptions{Upper: []byte("4")}))
	require.Equal(t, []store.DocRef{{ID: "a", OrderKey: []byte("8/a")}, {ID: "a", OrderKey: []byte("9/a")}}, scanView(t, pending, store.SearchOptions{Lower: []byte("8")}))
	s.doFlush()
	require.NoError(t, s.ApplyDocumentProjection([]store.Projection{projection(ref, "a")}, "p3"))
	empty, err := s.ReadView(context.Background(), ref, store.ReadBudget{})
	require.NoError(t, err)
	defer empty.Close()
	require.Equal(t, []store.DocRef{{ID: "b", OrderKey: []byte("2/b")}}, scanView(t, empty, store.SearchOptions{}))
	s.doFlush()
	require.Len(t, scanView(t, original, store.SearchOptions{}), 3)
	require.Len(t, scanView(t, pending, store.SearchOptions{}), 3)
	otherView, err := s.ReadView(context.Background(), other, store.ReadBudget{})
	require.NoError(t, err)
	defer otherView.Close()
	require.Equal(t, []store.DocRef{{ID: "a", OrderKey: []byte("1/a")}}, scanView(t, otherView, store.SearchOptions{}))
	persisted, err := s.ReadView(context.Background(), ref, store.ReadBudget{})
	require.NoError(t, err)
	defer persisted.Close()
	require.Equal(t, scanView(t, empty, store.SearchOptions{}), scanView(t, persisted, store.SearchOptions{}))
	progress, err := s.LoadProgress()
	require.NoError(t, err)
	require.Equal(t, "p3", progress)
}

type blockedCommitDB struct {
	DB
	entered chan struct{}
	release chan struct{}
}

func (d *blockedCommitDB) NewBatch() Batch {
	return &blockedCommitBatch{Batch: d.DB.NewBatch(), entered: d.entered, release: d.release}
}

type blockedCommitBatch struct {
	Batch
	entered chan struct{}
	release chan struct{}
}

func (b *blockedCommitBatch) Commit(options *pebble.WriteOptions) error {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	<-b.release
	return b.Batch.Commit(options)
}

func TestProjectionViewDuringFlushRetainsWholeReplacements(t *testing.T) {
	s := newProjectionTestStore(t)
	ref := queryRef("items")
	require.NoError(t, s.ApplyDocumentProjection([]store.Projection{projection(ref, "a", "1/a", "2/a")}, "p1"))
	s.doFlush()
	blocked := &blockedCommitDB{DB: s.db, entered: make(chan struct{}, 1), release: make(chan struct{})}
	s.db = blocked
	var release sync.Once
	defer release.Do(func() { close(blocked.release) })
	require.NoError(t, s.ApplyDocumentProjection([]store.Projection{projection(ref, "a", "5/a", "6/a")}, "p2"))
	flushed := make(chan struct{})
	go func() { s.doFlush(); close(flushed) }()
	select {
	case <-blocked.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("flush did not start")
	}
	flushing, err := s.ReadView(context.Background(), ref, store.ReadBudget{})
	require.NoError(t, err)
	defer flushing.Close()
	require.NoError(t, s.ApplyDocumentProjection([]store.Projection{projection(ref, "a", "9/a")}, "p3"))
	pending, err := s.ReadView(context.Background(), ref, store.ReadBudget{})
	require.NoError(t, err)
	defer pending.Close()
	release.Do(func() { close(blocked.release) })
	select {
	case <-flushed:
	case <-time.After(5 * time.Second):
		t.Fatal("flush did not finish")
	}
	s.doFlush()
	require.Equal(t, []store.DocRef{{ID: "a", OrderKey: []byte("5/a")}, {ID: "a", OrderKey: []byte("6/a")}}, scanView(t, flushing, store.SearchOptions{}))
	require.Empty(t, scanView(t, pending, store.SearchOptions{Upper: []byte("8")}))
	require.Equal(t, []store.DocRef{{ID: "a", OrderKey: []byte("9/a")}}, scanView(t, pending, store.SearchOptions{}))
}

func TestProjectionAdmissionAndOverlayLimits(t *testing.T) {
	s := newProjectionTestStore(t)
	ref := queryRef("items")
	good := projection(ref, "a", "1/a")
	invalid := projection(ref, "", "2/b")
	require.Error(t, s.ApplyDocumentProjection([]store.Projection{good, invalid}, "bad"))
	progress, err := s.LoadProgress()
	require.NoError(t, err)
	require.Empty(t, progress)
	require.NoError(t, s.ApplyDocumentProjection([]store.Projection{good, projection(ref, "b")}, "good"))
	_, err = s.ReadView(context.Background(), ref, store.ReadBudget{MaxOverlayReplacements: 1})
	require.ErrorIs(t, err, store.ErrWorkLimit)
	_, err = s.ReadView(context.Background(), ref, store.ReadBudget{MaxOverlayBytes: 4})
	require.ErrorIs(t, err, store.ErrWorkLimit)
	view, err := s.ReadView(context.Background(), ref, store.ReadBudget{MaxOverlayReplacements: 2, MaxOverlayBytes: 5})
	require.NoError(t, err)
	defer view.Close()
	require.Len(t, scanView(t, view, store.SearchOptions{}), 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = s.ReadView(ctx, ref, store.ReadBudget{})
	require.ErrorIs(t, err, context.Canceled)
	ctx, cancel = context.WithCancel(context.Background())
	iter, err := view.Scan(ctx, store.SearchOptions{})
	require.NoError(t, err)
	cancel()
	_, _, err = iter.Next()
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, iter.Close())
}

func TestProjectionFlushFailureIsStickyAndAtomic(t *testing.T) {
	for _, stage := range []string{"read", "delete", "set", "commit"} {
		t.Run(stage, func(t *testing.T) {
			db := newMockDB()
			s := newMockPebbleStore(db)
			ref := queryRef("items")
			require.NoError(t, s.ApplyDocumentProjection([]store.Projection{projection(ref, "a", "1/a")}, "p1"))
			s.doFlush()
			failure := errors.New(stage + " failed")
			switch stage {
			case "read":
				db.getErr = failure
				db.getErrKey = projectionPrefix("reverse", ref)
			case "delete":
				db.batchDelErr = failure
			case "set":
				db.batchSetErr = failure
			case "commit":
				db.commitErr = failure
			}
			require.NoError(t, s.ApplyDocumentProjection([]store.Projection{projection(ref, "a", "2/a"), projection(queryRef("other"), "a", "3/a")}, "p2"))
			require.ErrorIs(t, s.PublishGeneration(ref, "p2"), failure)
			require.ErrorIs(t, s.Flush(), failure)
			require.Equal(t, "p1", string(db.data[keyProgress]))
			require.NotContains(t, db.data, string(generationKey(ref.Database, ref.Collection, ref.TemplateFingerprint)))
			require.Contains(t, db.data, string(append(projectionPrefix("posting", ref), []byte("1/a")...)))
			require.Len(t, s.projectionFlushing, 2)
			_, err := s.LoadProgress()
			require.ErrorIs(t, err, failure)
			require.ErrorIs(t, s.ApplyDocumentProjection([]store.Projection{projection(ref, "a")}, "p3"), failure)
			_, err = s.ReadView(context.Background(), ref, store.ReadBudget{})
			require.ErrorIs(t, err, failure)
			require.ErrorIs(t, s.Upsert("db", "p", "t", "a", []byte("a"), "p4"), failure)
			require.ErrorIs(t, s.Close(), failure)
		})
	}
}

func TestGenerationPublicationAndFormatReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index")
	cfg := config.StoreConfig{Path: path, BatchInterval: time.Hour, BatchSize: 100000, BlockCacheSize: 1 << 20}
	s, err := NewPebbleStore(cfg, slog.Default())
	require.NoError(t, err)
	ref := queryRef("items")
	require.NoError(t, s.ApplyDocumentProjection([]store.Projection{projection(ref, "a", "1/a", "2/a")}, "p1"))
	require.NoError(t, s.PublishGeneration(ref, "boundary"))
	require.NoError(t, s.Close())
	reopened, err := NewPebbleStore(cfg, slog.Default())
	require.NoError(t, err)
	defer reopened.Close()
	gen, found, err := reopened.ReadGeneration(ref.Database, ref.Collection, ref.TemplateFingerprint)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, store.Generation{ID: ref.Generation, Ready: true, BootstrapProgress: "boundary"}, gen)
	progress, err := reopened.LoadProgress()
	require.NoError(t, err)
	require.Equal(t, "boundary", progress)
	view, err := reopened.ReadView(context.Background(), ref, store.ReadBudget{})
	require.NoError(t, err)
	defer view.Close()
	require.Len(t, scanView(t, view, store.SearchOptions{}), 2)
	require.NoError(t, reopened.SetFailure(ref, "projection limit"))
	gen, found, err = reopened.ReadGeneration(ref.Database, ref.Collection, ref.TemplateFingerprint)
	require.NoError(t, err)
	require.True(t, found)
	require.False(t, gen.Ready)
	require.Equal(t, "projection limit", gen.Failure)
	require.Equal(t, "boundary", gen.BootstrapProgress)
}

func TestManifestRejectsExistingIncompatibleStoreWithoutDeletingData(t *testing.T) {
	for _, manifest := range []string{"", "syntrix-index-v1"} {
		t.Run(manifest, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "index")
			db, err := pebble.Open(path, nil)
			require.NoError(t, err)
			require.NoError(t, db.Set([]byte("old-data"), []byte("keep"), pebble.Sync))
			if manifest != "" {
				require.NoError(t, db.Set([]byte(manifestKey), []byte(manifest), pebble.Sync))
			}
			require.NoError(t, db.Close())
			_, err = NewPebbleStore(config.StoreConfig{Path: path, BlockCacheSize: 1 << 20}, slog.Default())
			require.Error(t, err)
			db, err = pebble.Open(path, nil)
			require.NoError(t, err)
			value, closer, err := db.Get([]byte("old-data"))
			require.NoError(t, err)
			require.Equal(t, "keep", string(value))
			require.NoError(t, closer.Close())
			require.NoError(t, db.Close())
		})
	}
}

func TestProjectionReadWorkLimitIncludesSuppressionAndBranches(t *testing.T) {
	s := newProjectionTestStore(t)
	ref := queryRef("items")
	require.NoError(t, s.ApplyDocumentProjection([]store.Projection{projection(ref, "a", "1/a", "2/a", "3/a"), projection(ref, "b", "4/b")}, ""))
	s.doFlush()
	require.NoError(t, s.ApplyDocumentProjection([]store.Projection{projection(ref, "a")}, ""))
	view, err := s.ReadView(context.Background(), ref, store.ReadBudget{MaxExamined: 2})
	require.NoError(t, err)
	defer view.Close()
	iter, err := view.Scan(context.Background(), store.SearchOptions{})
	require.NoError(t, err)
	defer iter.Close()
	_, ok, err := iter.Next()
	require.ErrorIs(t, err, store.ErrWorkLimit)
	require.False(t, ok)
	require.EqualValues(t, 2, iter.Examined())
	shared, err := s.ReadView(context.Background(), ref, store.ReadBudget{MaxExamined: 1})
	require.NoError(t, err)
	defer shared.Close()
	first, err := shared.Scan(context.Background(), store.SearchOptions{Lower: []byte("4")})
	require.NoError(t, err)
	defer first.Close()
	_, ok, err = first.Next()
	require.NoError(t, err)
	require.True(t, ok)
	second, err := shared.Scan(context.Background(), store.SearchOptions{Lower: []byte("4")})
	require.NoError(t, err)
	defer second.Close()
	_, ok, err = second.Next()
	require.ErrorIs(t, err, store.ErrWorkLimit)
	require.False(t, ok)
}

func TestDeleteDatabaseClearsGenerationsAndQueuedProjections(t *testing.T) {
	s := newProjectionTestStore(t)
	ref := queryRef("items")
	other := ref
	other.Database = "other"
	require.NoError(t, s.ApplyDocumentProjection([]store.Projection{projection(ref, "a", "1/a"), projection(other, "a", "1/a")}, "p1"))
	require.NoError(t, s.PublishGeneration(ref, "p1"))
	require.NoError(t, s.PublishGeneration(other, "p1"))
	old, err := s.ReadView(context.Background(), ref, store.ReadBudget{})
	require.NoError(t, err)
	defer old.Close()
	require.NoError(t, s.ApplyDocumentProjection([]store.Projection{projection(ref, "b", "2/b")}, "p2"))
	require.NoError(t, s.DeleteDatabase(ref.Database))
	s.doFlush()
	_, found, err := s.ReadGeneration(ref.Database, ref.Collection, ref.TemplateFingerprint)
	require.NoError(t, err)
	require.False(t, found)
	empty, err := s.ReadView(context.Background(), ref, store.ReadBudget{})
	require.NoError(t, err)
	defer empty.Close()
	require.Empty(t, scanView(t, empty, store.SearchOptions{}))
	require.Len(t, scanView(t, old, store.SearchOptions{}), 1)
	retained, err := s.ReadView(context.Background(), other, store.ReadBudget{})
	require.NoError(t, err)
	defer retained.Close()
	require.Len(t, scanView(t, retained, store.SearchOptions{}), 1)
	_, found, err = s.ReadGeneration(other.Database, other.Collection, other.TemplateFingerprint)
	require.NoError(t, err)
	require.True(t, found)
}

func TestIndexDeletionRemainsVisibleWhileBatchCommits(t *testing.T) {
	s := newProjectionTestStore(t)
	require.NoError(t, s.Upsert("db", "items/*", "template", "a", []byte("1/a"), "p1"))
	s.doFlush()
	blocked := &blockedCommitDB{DB: s.db, entered: make(chan struct{}, 1), release: make(chan struct{})}
	s.db = blocked
	var release sync.Once
	defer release.Do(func() { close(blocked.release) })
	require.NoError(t, s.DeleteIndex("db", "items/*", "template"))
	select {
	case <-blocked.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("delete flush did not start")
	}
	_, found := s.Get("db", "items/*", "template", "a")
	require.False(t, found)
	rows, err := s.Search("db", "items/*", "template", store.SearchOptions{})
	require.NoError(t, err)
	require.Empty(t, rows)
	indexes, err := s.ListIndexes("db")
	require.NoError(t, err)
	require.Empty(t, indexes)
	release.Do(func() { close(blocked.release) })
	require.NoError(t, s.Flush())
}

func TestMalformedReversePostingSetStopsAtomicProjection(t *testing.T) {
	for name, corrupt := range map[string][]byte{
		"unterminated count":  {0x80},
		"impossible count":    {2},
		"unterminated length": {1, 0x80},
		"empty key":           {1, 0},
		"truncated key":       {1, 2, 'a'},
		"trailing bytes":      {0, 1},
	} {
		t.Run(name, func(t *testing.T) {
			db := newMockDB()
			s := newMockPebbleStore(db)
			ref := queryRef("items")
			require.NoError(t, s.ApplyDocumentProjection([]store.Projection{projection(ref, "doc", "old/doc")}, "old"))
			s.doFlush()
			reverse := append(projectionPrefix("reverse", ref), encodePathComponent("doc")...)
			db.data[string(reverse)] = corrupt
			require.NoError(t, s.ApplyDocumentProjection([]store.Projection{projection(ref, "doc", "new/doc"), projection(queryRef("other"), "doc", "new/doc")}, "new"))
			s.doFlush()
			err := s.Flush()
			require.ErrorContains(t, err, "reverse posting")
			require.Equal(t, "old", string(db.data[keyProgress]))
			require.Contains(t, db.data, string(append(projectionPrefix("posting", ref), []byte("old/doc")...)))
			require.NotContains(t, db.data, string(append(projectionPrefix("posting", ref), []byte("new/doc")...)))
			require.ErrorIs(t, s.Close(), err)
		})
	}
}

func TestOverlayCursorExclusionConsumesSharedWorkBudget(t *testing.T) {
	s := newProjectionTestStore(t)
	ref := queryRef("items")
	require.NoError(t, s.ApplyDocumentProjection([]store.Projection{projection(ref, "doc", "a/doc", "b/doc")}, ""))
	view, err := s.ReadView(context.Background(), ref, store.ReadBudget{MaxExamined: 1})
	require.NoError(t, err)
	defer view.Close()
	iter, err := view.Scan(context.Background(), store.SearchOptions{StartAfter: []byte("a/doc")})
	require.NoError(t, err)
	_, _, err = iter.Next()
	require.ErrorIs(t, err, store.ErrWorkLimit)
	require.EqualValues(t, 1, iter.Examined())
	require.NoError(t, iter.Close())
	require.NoError(t, iter.Close())
}

func TestProgressOnlyProjectionFlushNotificationCoalesces(t *testing.T) {
	db := newMockDB()
	s := newMockPebbleStore(db)
	require.NoError(t, s.ApplyDocumentProjection(nil, "first"))
	require.NoError(t, s.ApplyDocumentProjection(nil, "second"))
	require.Len(t, s.notifyCh, 1)
	s.doFlush()
	require.NoError(t, s.Flush())
	require.Equal(t, "second", string(db.data[keyProgress]))
	require.NoError(t, s.Close())
}
