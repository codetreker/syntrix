package mem_store

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/indexer/store"
)

func collectProjection(t *testing.T, view store.ReadView, opts store.SearchOptions) []store.DocRef {
	t.Helper()
	it, err := view.Scan(context.Background(), opts)
	require.NoError(t, err)
	defer it.Close()
	var refs []store.DocRef
	for {
		ref, ok, err := it.Next()
		require.NoError(t, err)
		if !ok {
			return refs
		}
		refs = append(refs, ref)
	}
}

func TestProjectionViewConcurrentReplacement(t *testing.T) {
	s := New()
	ref := store.QueryIndexRef{Database: "db", Collection: "items", TemplateFingerprint: "sort", Generation: "build"}
	initial := make([]store.Projection, 200)
	for i := range initial {
		id := fmt.Sprintf("doc-%03d", i)
		initial[i] = store.Projection{Index: ref, DocumentID: id, PostingKeys: [][]byte{[]byte(id)}}
	}
	require.NoError(t, s.ApplyDocumentProjection(initial, "initial"))
	view, err := s.ReadView(context.Background(), ref, store.ReadBudget{})
	require.NoError(t, err)
	defer view.Close()
	done := make(chan error, 1)
	go func() {
		for _, projection := range initial {
			projection.PostingKeys = [][]byte{[]byte("updated/" + projection.DocumentID)}
			if err := s.ApplyDocumentProjection([]store.Projection{projection}, "updated"); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	rows := collectProjection(t, view, store.SearchOptions{})
	require.Len(t, rows, len(initial))
	for i, row := range rows {
		require.Equal(t, initial[i].DocumentID, row.ID)
		require.Equal(t, initial[i].PostingKeys[0], row.OrderKey)
	}
	require.NoError(t, <-done)
}

func TestProjectionReplacementAndSnapshot(t *testing.T) {
	s := New()
	ref := store.QueryIndexRef{Database: "db", Collection: "users/a/posts", TemplateFingerprint: "tags-v2", Generation: "build-1"}
	key := []byte("a/doc1")
	require.NoError(t, s.ApplyDocumentProjection([]store.Projection{{Index: ref, DocumentID: "doc1", PostingKeys: [][]byte{key, []byte("b/doc1"), key}}}, "one"))
	key[0] = 'z'
	view, err := s.ReadView(context.Background(), ref, store.ReadBudget{})
	require.NoError(t, err)
	defer view.Close()
	require.NoError(t, s.ApplyDocumentProjection([]store.Projection{{Index: ref, DocumentID: "doc1", PostingKeys: [][]byte{[]byte("c/doc1")}}}, "two"))
	old := collectProjection(t, view, store.SearchOptions{})
	require.Equal(t, []store.DocRef{{ID: "doc1", OrderKey: []byte("a/doc1")}, {ID: "doc1", OrderKey: []byte("b/doc1")}}, old)
	old[0].OrderKey[0] = 'x'
	require.Equal(t, "a/doc1", string(collectProjection(t, view, store.SearchOptions{})[0].OrderKey))
	latest, err := s.ReadView(context.Background(), ref, store.ReadBudget{})
	require.NoError(t, err)
	require.Equal(t, []store.DocRef{{ID: "doc1", OrderKey: []byte("c/doc1")}}, collectProjection(t, latest, store.SearchOptions{}))
	require.NoError(t, latest.Close())
	require.NoError(t, s.ApplyDocumentProjection([]store.Projection{{Index: ref, DocumentID: "doc1"}}, "three"))
	empty, err := s.ReadView(context.Background(), ref, store.ReadBudget{})
	require.NoError(t, err)
	defer empty.Close()
	require.Empty(t, collectProjection(t, empty, store.SearchOptions{}))
	progress, err := s.LoadProgress()
	require.NoError(t, err)
	require.Equal(t, "three", progress)
}

func TestProjectionIdentityAndAtomicAdmission(t *testing.T) {
	s := New()
	base := store.QueryIndexRef{Database: "db", Collection: "users/a/posts", TemplateFingerprint: "tags-v2", Generation: "build-1"}
	refs := []store.QueryIndexRef{base, base, base, base}
	refs[1].Collection = "users/b/posts"
	refs[2].TemplateFingerprint = "tags-v3"
	refs[3].Generation = "build-2"
	for i, ref := range refs {
		require.NoError(t, s.ApplyDocumentProjection([]store.Projection{{Index: ref, DocumentID: "doc", PostingKeys: [][]byte{{byte(i + 1)}}}}, "before"))
	}
	for i, ref := range refs {
		view, err := s.ReadView(context.Background(), ref, store.ReadBudget{})
		require.NoError(t, err)
		require.Equal(t, []store.DocRef{{ID: "doc", OrderKey: []byte{byte(i + 1)}}}, collectProjection(t, view, store.SearchOptions{}))
		require.NoError(t, view.Close())
	}
	err := s.ApplyDocumentProjection([]store.Projection{{Index: base, DocumentID: "doc", PostingKeys: [][]byte{[]byte("changed")}}, {Index: refs[1], DocumentID: "", PostingKeys: [][]byte{[]byte("invalid")}}}, "after")
	require.Error(t, err)
	progress, err := s.LoadProgress()
	require.NoError(t, err)
	require.Equal(t, "before", progress)
	view, err := s.ReadView(context.Background(), base, store.ReadBudget{})
	require.NoError(t, err)
	defer view.Close()
	require.Equal(t, []byte{1}, collectProjection(t, view, store.SearchOptions{})[0].OrderKey)
}

func TestProjectionScanBoundsCancellationAndGeneration(t *testing.T) {
	s := New()
	ref := store.QueryIndexRef{Database: "db", Collection: "items", TemplateFingerprint: "sort", Generation: "build"}
	require.NoError(t, s.ApplyDocumentProjection([]store.Projection{{Index: ref, DocumentID: "a", PostingKeys: [][]byte{[]byte("a"), []byte("b"), []byte("c"), []byte("d")}}}, "events"))
	view, err := s.ReadView(context.Background(), ref, store.ReadBudget{})
	require.NoError(t, err)
	defer view.Close()
	result := collectProjection(t, view, store.SearchOptions{Lower: []byte("b"), Upper: []byte("d"), StartAfter: []byte("a")})
	require.Equal(t, []store.DocRef{{ID: "a", OrderKey: []byte("b")}, {ID: "a", OrderKey: []byte("c")}}, result)
	result = collectProjection(t, view, store.SearchOptions{Lower: []byte("a"), Upper: []byte("d"), StartAfter: []byte("b"), Limit: 1})
	require.Equal(t, []store.DocRef{{ID: "a", OrderKey: []byte("c")}}, result)
	ctx, cancel := context.WithCancel(context.Background())
	it, err := view.Scan(ctx, store.SearchOptions{})
	require.NoError(t, err)
	cancel()
	_, _, err = it.Next()
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, it.Close())
	require.NoError(t, s.PublishGeneration(ref, "bootstrap"))
	g, ok, err := s.ReadGeneration("db", "items", "sort")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, store.Generation{ID: "build", Ready: true, BootstrapProgress: "bootstrap"}, g)
	require.NoError(t, s.SetFailure(ref, "projection limit"))
	g, _, err = s.ReadGeneration("db", "items", "sort")
	require.NoError(t, err)
	require.False(t, g.Ready)
	require.Equal(t, "projection limit", g.Failure)
	progress, err := s.LoadProgress()
	require.NoError(t, err)
	require.Equal(t, "bootstrap", progress)
}

func TestProjectionReadBudgetSharedByBranches(t *testing.T) {
	s := New()
	ref := store.QueryIndexRef{Database: "db", Collection: "items", TemplateFingerprint: "sort", Generation: "build"}
	require.NoError(t, s.ApplyDocumentProjection([]store.Projection{{Index: ref, DocumentID: "doc", PostingKeys: [][]byte{[]byte("a"), []byte("b")}}}, ""))
	view, err := s.ReadView(context.Background(), ref, store.ReadBudget{MaxExamined: 1})
	require.NoError(t, err)
	defer view.Close()
	first, err := view.Scan(context.Background(), store.SearchOptions{Upper: []byte("b")})
	require.NoError(t, err)
	defer first.Close()
	_, ok, err := first.Next()
	require.NoError(t, err)
	require.True(t, ok)
	require.EqualValues(t, 1, first.Examined())
	_, ok, err = first.Next()
	require.NoError(t, err)
	require.False(t, ok)
	second, err := view.Scan(context.Background(), store.SearchOptions{Lower: []byte("b")})
	require.NoError(t, err)
	defer second.Close()
	_, _, err = second.Next()
	require.ErrorIs(t, err, store.ErrWorkLimit)
}

func TestBootstrapCatalogReadinessOwnershipAndReplacement(t *testing.T) {
	s := New()
	ref := store.QueryIndexRef{Database: "db", Collection: "items", TemplateFingerprint: "sort", Generation: "build-1"}
	require.NoError(t, s.ApplyDocumentProjection([]store.Projection{{Index: ref, DocumentID: "doc", PostingKeys: [][]byte{[]byte("a/doc")}}}, "event"))
	catalog := store.BootstrapCatalog{Database: "db", Generation: "build-1", TemplateFingerprints: []string{"sort", "other", "sort"}, BootstrapProgress: "complete"}
	require.NoError(t, s.PublishBootstrapCatalog(catalog))
	catalog.TemplateFingerprints[0] = "changed"
	stored, exists, err := s.ReadBootstrapCatalog("db")
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, []string{"other", "sort"}, stored.TemplateFingerprints)
	stored.TemplateFingerprints[0] = "changed-again"
	catalogs, err := s.ListBootstrapCatalogs()
	require.NoError(t, err)
	require.Equal(t, []string{"other", "sort"}, catalogs[0].TemplateFingerprints)
	generation, exists, err := s.ReadGeneration("db", "brand-new-empty-collection", "sort")
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, store.Generation{ID: "build-1", Ready: true, BootstrapProgress: "complete"}, generation)
	emptyRef := ref
	emptyRef.Collection = "brand-new-empty-collection"
	empty, err := s.ReadView(context.Background(), emptyRef, store.ReadBudget{})
	require.NoError(t, err)
	require.Empty(t, collectProjection(t, empty, store.SearchOptions{}))
	require.NoError(t, empty.Close())
	require.NoError(t, s.SetFailure(ref, "source shape"))
	generation, _, err = s.ReadGeneration("db", "items", "sort")
	require.NoError(t, err)
	require.False(t, generation.Ready)
	require.Equal(t, "source shape", generation.Failure)
	require.NoError(t, s.PublishBootstrapCatalog(store.BootstrapCatalog{Database: "db", Generation: "build-2", TemplateFingerprints: []string{"sort"}, BootstrapProgress: "rebuilt"}))
	generation, _, err = s.ReadGeneration("db", "items", "sort")
	require.NoError(t, err)
	require.Equal(t, store.Generation{ID: "build-2", Ready: true, BootstrapProgress: "rebuilt"}, generation)
	_, exists, err = s.ReadGeneration("db", "items", "other")
	require.NoError(t, err)
	require.False(t, exists)
	progress, err := s.LoadProgress()
	require.NoError(t, err)
	require.Equal(t, "rebuilt", progress)
}

func TestQueryIndexDeletionFencesActiveCatalogAndPreservesViews(t *testing.T) {
	s := New()
	ref := store.QueryIndexRef{Database: "db", Collection: "items", TemplateFingerprint: "sort", Generation: "build"}
	old := ref
	old.Generation = "old"
	require.NoError(t, s.ApplyDocumentProjection([]store.Projection{
		{Index: ref, DocumentID: "doc", PostingKeys: [][]byte{[]byte("a/doc")}},
		{Index: old, DocumentID: "doc"},
	}, ""))
	require.NoError(t, s.PublishBootstrapCatalog(store.BootstrapCatalog{Database: "db", Generation: "build", TemplateFingerprints: []string{"sort"}, BootstrapProgress: "ready"}))
	view, err := s.ReadView(context.Background(), ref, store.ReadBudget{})
	require.NoError(t, err)
	defer view.Close()
	refs, err := s.ListQueryIndexes()
	require.NoError(t, err)
	require.Len(t, refs, 2)
	require.NoError(t, s.DeleteQueryIndex(old))
	generation, _, err := s.ReadGeneration("db", "items", "sort")
	require.NoError(t, err)
	require.True(t, generation.Ready)
	require.NoError(t, s.DeleteQueryIndex(ref))
	generation, _, err = s.ReadGeneration("db", "items", "sort")
	require.NoError(t, err)
	require.False(t, generation.Ready)
	require.Equal(t, "index partition deleted", generation.Failure)
	require.NoError(t, s.SetFailure(old, "late old-generation failure"))
	require.Error(t, s.PublishGeneration(old, "must-not-advance"))
	generation, _, err = s.ReadGeneration("db", "items", "sort")
	require.NoError(t, err)
	require.False(t, generation.Ready)
	require.Equal(t, "index partition deleted", generation.Failure)
	progress, err := s.LoadProgress()
	require.NoError(t, err)
	require.Equal(t, "ready", progress)
	require.Len(t, collectProjection(t, view, store.SearchOptions{}), 1)
	refs, err = s.ListQueryIndexes()
	require.NoError(t, err)
	require.Equal(t, []store.QueryIndexRef{ref}, refs)
	require.NoError(t, s.DeleteDatabase("db"))
	catalogs, err := s.ListBootstrapCatalogs()
	require.NoError(t, err)
	require.Empty(t, catalogs)
	refs, err = s.ListQueryIndexes()
	require.NoError(t, err)
	require.Empty(t, refs)
}

func TestCompleteBootstrapPublicationReplacesInventoryAtomically(t *testing.T) {
	s := New()
	retired := store.QueryIndexRef{Database: "retired", Collection: "items", TemplateFingerprint: "fp", Generation: "old"}
	require.NoError(t, s.PublishGeneration(retired, "old-boundary"))
	old := store.BootstrapCatalog{Database: "retired", Generation: "old", TemplateFingerprints: []string{"fp"}, BootstrapProgress: "old-boundary"}
	require.NoError(t, s.PublishBootstrapCatalog(old))
	first := store.BootstrapCatalog{Database: "a", Generation: "new", TemplateFingerprints: []string{"fp"}, BootstrapProgress: "new-boundary"}
	second := first
	second.Database = "b"
	second.Generation = "mismatched"
	require.Error(t, s.PublishBootstrapCatalogs([]store.BootstrapCatalog{first, second}, "new-boundary"))
	catalogs, err := s.ListBootstrapCatalogs()
	require.NoError(t, err)
	require.Equal(t, []store.BootstrapCatalog{old}, catalogs)
	progress, err := s.LoadProgress()
	require.NoError(t, err)
	require.Equal(t, "old-boundary", progress)
	second.Generation = first.Generation
	require.NoError(t, s.PublishBootstrapCatalogs([]store.BootstrapCatalog{first, second}, "new-boundary"))
	catalogs, err = s.ListBootstrapCatalogs()
	require.NoError(t, err)
	require.Equal(t, []store.BootstrapCatalog{first, second}, catalogs)
	_, exists, err := s.ReadBootstrapCatalog("retired")
	require.NoError(t, err)
	require.False(t, exists)
	_, exists, err = s.ReadGeneration(retired.Database, retired.Collection, retired.TemplateFingerprint)
	require.NoError(t, err)
	require.False(t, exists)
	progress, err = s.LoadProgress()
	require.NoError(t, err)
	require.Equal(t, "new-boundary", progress)
	require.Error(t, s.PublishBootstrapCatalog(old))
	catalogs, err = s.ListBootstrapCatalogs()
	require.NoError(t, err)
	require.Equal(t, []store.BootstrapCatalog{first, second}, catalogs)
}

func TestDatabaseRetirementRequiresCompleteBootstrapReactivation(t *testing.T) {
	s := New()
	ref := store.QueryIndexRef{Database: "retired", Collection: "items", TemplateFingerprint: "fp", Generation: "old"}
	require.NoError(t, s.PublishGeneration(ref, "old-boundary"))
	require.NoError(t, s.DeleteDatabase(ref.Database))
	retired, err := s.IsDatabaseRetired(ref.Database)
	require.NoError(t, err)
	require.True(t, retired)
	require.NoError(t, s.PublishGeneration(ref, "partial"))
	_, exists, err := s.ReadGeneration(ref.Database, ref.Collection, ref.TemplateFingerprint)
	require.NoError(t, err)
	require.False(t, exists)
	other := store.BootstrapCatalog{Database: "other", Generation: "new", TemplateFingerprints: []string{"fp"}, BootstrapProgress: "complete"}
	require.NoError(t, s.PublishBootstrapCatalogs([]store.BootstrapCatalog{other}, "complete"))
	retired, err = s.IsDatabaseRetired(ref.Database)
	require.NoError(t, err)
	require.True(t, retired)
	included := other
	included.Database = ref.Database
	invalid := included
	invalid.Generation = "mismatch"
	require.Error(t, s.PublishBootstrapCatalogs([]store.BootstrapCatalog{other, invalid}, "complete"))
	retired, err = s.IsDatabaseRetired(ref.Database)
	require.NoError(t, err)
	require.True(t, retired)
	require.NoError(t, s.PublishBootstrapCatalogs([]store.BootstrapCatalog{other, included}, "complete"))
	retired, err = s.IsDatabaseRetired(ref.Database)
	require.NoError(t, err)
	require.False(t, retired)
	generation, exists, err := s.ReadGeneration(ref.Database, ref.Collection, ref.TemplateFingerprint)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, store.Generation{ID: "new", Ready: true, BootstrapProgress: "complete"}, generation)
}
