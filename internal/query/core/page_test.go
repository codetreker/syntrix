package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/indexer"
	"github.com/syntrixbase/syntrix/internal/indexer/config"
	"github.com/syntrixbase/syntrix/internal/indexer/encoding"
	"github.com/syntrixbase/syntrix/internal/indexer/manager"
	"github.com/syntrixbase/syntrix/internal/indexer/mem_store"
	"github.com/syntrixbase/syntrix/internal/indexer/persist_store"
	"github.com/syntrixbase/syntrix/internal/indexer/store"
	"github.com/syntrixbase/syntrix/pkg/model"
)

type pageSource struct {
	afterRead func()
	storage.DocumentStore
	docs        map[string]*types.StoredDoc
	reads       []types.ReadOptions
	readLengths []int
	scans       []types.SourceScanRequest
	fail        error
	scan        func(context.Context, string, types.SourceScanRequest) (types.SourceScanPage, error)
}

func (s *pageSource) GetMany(ctx context.Context, db string, paths []string, opts ...types.ReadOptions) ([]*types.StoredDoc, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.fail != nil {
		return nil, s.fail
	}
	s.reads = append(s.reads, opts...)
	s.readLengths = append(s.readLengths, len(paths))
	readOptions, err := types.ResolveReadOptions(opts)
	if err != nil {
		return nil, err
	}
	var readBytes int64
	docs := make([]*types.StoredDoc, len(paths))
	for i, path := range paths {
		docs[i] = s.docs[path]
		size, err := types.StoredDocumentBytes(docs[i])
		if err != nil {
			return nil, err
		}
		readBytes += size
		if readOptions.MaxBytes > 0 && readBytes > readOptions.MaxBytes {
			return nil, types.ErrReadBudget
		}
	}
	if s.afterRead != nil {
		s.afterRead()
	}
	return docs, nil
}
func (s *pageSource) ScanDocuments(ctx context.Context, db string, request types.SourceScanRequest) (types.SourceScanPage, error) {
	if err := ctx.Err(); err != nil {
		return types.SourceScanPage{}, err
	}
	if s.fail != nil {
		return types.SourceScanPage{}, s.fail
	}
	s.scans = append(s.scans, request)
	if s.scan != nil {
		return s.scan(ctx, db, request)
	}
	ids := []string{}
	for _, doc := range s.docs {
		if doc.Database == db && doc.Collection == request.Collection {
			id, err := types.LogicalDocumentID(doc)
			if err != nil {
				return types.SourceScanPage{}, err
			}
			if id > request.AfterID {
				ids = append(ids, id)
			}
		}
	}
	sort.Strings(ids)
	result := types.SourceScanPage{Exhausted: len(ids) < request.Limit}
	if len(ids) > request.Limit {
		ids = ids[:request.Limit]
	}
	for _, id := range ids {
		doc := s.docs[request.Collection+"/"+id]
		size, err := types.StoredDocumentBytes(doc)
		if err != nil {
			return types.SourceScanPage{}, err
		}
		result.Bytes += size
		if request.MaxBytes > 0 && result.Bytes > request.MaxBytes {
			return types.SourceScanPage{}, types.ErrSourceScanBudget
		}
		result.Documents = append(result.Documents, doc)
		result.NextAfter = id
	}
	return result, nil
}

type pageIndex struct {
	MockIndexerService
	manager *manager.Manager
	wrap    func(manager.CandidateStream) manager.CandidateStream
}

func (p *pageIndex) OpenCandidates(ctx context.Context, db string, plan manager.Plan) (manager.CandidateStream, error) {
	stream, err := p.manager.OpenCandidates(ctx, db, plan)
	if err == nil && p.wrap != nil {
		stream = p.wrap(stream)
	}
	return stream, err
}

func queryDocument(id string, data map[string]any) *types.StoredDoc {
	return &types.StoredDoc{Database: "db", Collection: "items", Fullpath: "items/" + id, Version: 9007199254740993, CreatedAt: 12, UpdatedAt: 34, Data: data}
}

func pageFixture(t *testing.T, backend, fields string) (*Engine, *pageSource, store.Store, *pageIndex) {
	t.Helper()
	var st store.Store
	if backend == "memory" {
		st = mem_store.New()
	} else {
		cfg := config.DefaultStoreConfig()
		cfg.Path = t.TempDir()
		cfg.BlockCacheSize = 1 << 20
		var err error
		st, err = persist_store.NewPebbleStore(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
		require.NoError(t, err)
	}
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	idx := &pageIndex{manager: manager.New(st)}
	require.NoError(t, idx.manager.LoadTemplatesFromBytes([]byte("templates:\n  - name: query\n    collectionPattern: items\n    includeDeleted: true\n    fields:\n"+fields)))
	source := &pageSource{docs: map[string]*types.StoredDoc{}}
	return New(source, idx), source, st, idx
}
func projectPageDocument(t *testing.T, st store.Store, idx *pageIndex, doc *types.StoredDoc) {
	t.Helper()
	tmpl := idx.manager.Templates()[0]
	projection, err := indexer.BuildDocumentProjection(doc, &tmpl, "gen-1", indexer.DefaultProjectionLimits())
	require.NoError(t, err)
	require.NoError(t, st.ApplyDocumentProjection([]store.Projection{projection}, ""))
	require.NoError(t, st.PublishGeneration(projection.Index, "ready"))
}
func pageIDs(page model.QueryPage) []string {
	ids := make([]string, len(page.Documents))
	for i, doc := range page.Documents {
		ids[i] = doc["id"].(string)
	}
	return ids
}

func TestQueryPagesRefillAndPositionValidation(t *testing.T) {
	for _, backend := range []string{"memory", "pebble"} {
		t.Run(backend, func(t *testing.T) {
			engine, source, st, idx := pageFixture(t, backend, "      - {field: score, order: asc}\n")
			for i := 0; i < 9; i++ {
				id := fmt.Sprint(i)
				doc := queryDocument(id, map[string]any{"score": int64(i), "active": true, "id": "business-shadow"})
				source.docs[doc.Fullpath] = doc
				projectPageDocument(t, st, idx, doc)
			}
			delete(source.docs, "items/0")
			source.docs["items/1"].Data["score"] = int64(100)
			source.docs["items/2"].Data["active"] = false
			source.docs["items/3"].Deleted = true
			source.docs["items/4"].Collection = "other"
			q := model.Query{Collection: "items", Filters: model.Filters{{Field: "score", Op: model.OpGte, Value: int64(0)}, {Field: "active", Op: model.OpEq, Value: true}}, Limit: 2}
			first, err := engine.ExecuteQueryPage(context.Background(), "db", q)
			require.NoError(t, err)
			require.Equal(t, []string{"5", "6"}, pageIDs(first))
			require.NotNil(t, first.NextCursor)
			require.Equal(t, int64(9007199254740993), first.Documents[0]["version"])
			for _, read := range source.reads {
				require.Equal(t, types.ReadAuthoritative, read.Consistency)
				require.True(t, read.ShowDeleted)
			}
			delete(source.docs, "items/6")
			q.StartAfter = *first.NextCursor
			q.Limit = 3
			second, err := engine.ExecuteQueryPage(context.Background(), "db", q)
			require.NoError(t, err)
			require.Equal(t, []string{"7", "8"}, pageIDs(second))
			require.Nil(t, second.NextCursor)
			q.StartAfter = ""
			docs, err := engine.ExecuteQuery(context.Background(), "db", q)
			require.NoError(t, err)
			require.Equal(t, []string{"5", "7", "8"}, pageIDs(model.QueryPage{Documents: docs}))
		})
	}
}

func TestQueryPagesMembershipProofAndDeduplication(t *testing.T) {
	engine, source, st, idx := pageFixture(t, "memory", "      - {field: tags, mode: membership}\n      - {field: score, order: asc}\n")
	for i := 0; i < 5; i++ {
		doc := queryDocument(fmt.Sprint(i), map[string]any{"tags": []any{"x", "x", "y"}, "score": int64(i)})
		source.docs[doc.Fullpath] = doc
		projectPageDocument(t, st, idx, doc)
	}
	source.docs["items/0"].Data["tags"] = []any{"z"}
	source.docs["items/1"].Data["tags"] = []any{"x"}
	q := model.Query{Collection: "items", Filters: model.Filters{{Field: "tags", Op: model.OpContains, Value: "x"}, {Field: "tags", Op: model.OpContains, Value: "y"}}, Limit: 2}
	first, err := engine.ExecuteQueryPage(context.Background(), "db", q)
	require.NoError(t, err)
	require.Equal(t, []string{"2", "3"}, pageIDs(first))
	require.NotNil(t, first.NextCursor)
	q.StartAfter = *first.NextCursor
	second, err := engine.ExecuteQueryPage(context.Background(), "db", q)
	require.NoError(t, err)
	require.Equal(t, []string{"4"}, pageIDs(second))
	require.Nil(t, second.NextCursor)
}

func TestQueryPageCursors(t *testing.T) {
	engine, source, st, idx := pageFixture(t, "memory", "      - {field: score, order: asc}\n")
	for i := 0; i < 3; i++ {
		doc := queryDocument(fmt.Sprint(i), map[string]any{"score": int64(i)})
		source.docs[doc.Fullpath] = doc
		projectPageDocument(t, st, idx, doc)
	}
	q := model.Query{Collection: "items", OrderBy: []model.Order{{Field: "score", Direction: "asc"}}, Limit: 1}
	page, err := engine.ExecuteQueryPage(context.Background(), "db", q)
	require.NoError(t, err)
	require.NotNil(t, page.NextCursor)
	for _, token := range []string{"not-a-token", strings.Repeat("x", maxCursorBytes+1), base64.RawURLEncoding.EncodeToString([]byte(`{"v":99}`))} {
		q.StartAfter = token
		_, err := engine.ExecuteQueryPage(context.Background(), "db", q)
		require.ErrorIs(t, err, model.ErrInvalidQuery)
	}
	cursor, err := decodeCursor(*page.NextCursor)
	require.NoError(t, err)
	raw, err := json.Marshal(cursor)
	require.NoError(t, err)
	raw = append(raw[:len(raw)-1], []byte(`,"unknown":true}`)...)
	q.StartAfter = base64.RawURLEncoding.EncodeToString(raw)
	_, err = engine.ExecuteQueryPage(context.Background(), "db", q)
	require.ErrorIs(t, err, model.ErrInvalidQuery)
	q.StartAfter = *page.NextCursor
	q.ShowDeleted = true
	_, err = engine.ExecuteQueryPage(context.Background(), "db", q)
	require.ErrorIs(t, err, model.ErrInvalidQuery)
	q.ShowDeleted = false
	cursor.Generation = "other"
	token, err := encodeCursor(cursor)
	require.NoError(t, err)
	q.StartAfter = token
	_, err = engine.ExecuteQueryPage(context.Background(), "db", q)
	require.ErrorIs(t, err, model.ErrStaleCursor)
	cursor.Generation = "gen-1"
	cursor.Order = []model.Order{{Field: "id", Direction: "asc"}}
	token, err = encodeCursor(cursor)
	require.NoError(t, err)
	q.StartAfter = token
	_, err = engine.ExecuteQueryPage(context.Background(), "db", q)
	require.ErrorIs(t, err, model.ErrStaleCursor)
}

func TestSourcePagesTombstonesIDsAndExactValues(t *testing.T) {
	source := &pageSource{docs: map[string]*types.StoredDoc{}}
	for _, id := range []string{"a", "b", "c", "d"} {
		doc := queryDocument(id, map[string]any{"id": "shadow", "n": int64(9007199254740993)})
		source.docs[doc.Fullpath] = doc
	}
	source.docs["items/a"].Deleted = true
	source.docs["items/c"].Deleted = true
	engine := New(source, nil)
	q := model.Query{Collection: "items", Limit: 1}
	page, err := engine.ExecuteQueryPage(context.Background(), "db", q)
	require.NoError(t, err)
	require.Equal(t, []string{"b"}, pageIDs(page))
	require.NotNil(t, page.NextCursor)
	require.Len(t, source.scans, 2)
	for _, scan := range source.scans {
		require.Equal(t, types.ReadAuthoritative, scan.Consistency)
		require.Equal(t, 1, scan.Limit)
		require.Equal(t, int64(16<<20), scan.MaxBytes)
	}
	q.StartAfter = *page.NextCursor
	q.Limit = 2
	page, err = engine.ExecuteQueryPage(context.Background(), "db", q)
	require.NoError(t, err)
	require.Equal(t, []string{"d"}, pageIDs(page))
	require.Nil(t, page.NextCursor)
	q = model.Query{Collection: "items", Filters: model.Filters{{Field: "id", Op: model.OpIn, Value: []any{"d", "a", "b", "b"}}, {Field: "id", Op: model.OpIn, Value: []any{"a", "b"}}}, ShowDeleted: true}
	page, err = engine.ExecuteQueryPage(context.Background(), "db", q)
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, pageIDs(page))
	require.NotContains(t, page.Documents[0], "n")
	require.Equal(t, int64(9007199254740993), page.Documents[1]["n"])
	for _, read := range source.reads {
		require.Equal(t, types.ReadOptions{Consistency: types.ReadAuthoritative, ShowDeleted: true, MaxBytes: maxMaterializationBytes}, read)
	}
}

func TestQueryPageErrorsDiscardPartialResults(t *testing.T) {
	source := &pageSource{docs: map[string]*types.StoredDoc{"items/a": queryDocument("a", map[string]any{"x": strings.Repeat("x", 17<<20)})}}
	page, err := New(source, nil).ExecuteQueryPage(context.Background(), "db", model.Query{Collection: "items", Limit: 1})
	require.ErrorIs(t, err, model.ErrQueryWorkLimit)
	require.Nil(t, page.Documents)
	require.Nil(t, page.NextCursor)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = New(source, nil).ExecuteQueryPage(ctx, "db", model.Query{Collection: "items"})
	require.ErrorIs(t, err, context.Canceled)
	source.fail = types.ErrSourceScanBudget
	_, err = New(source, nil).ExecuteQueryPage(context.Background(), "db", model.Query{Collection: "items"})
	require.ErrorIs(t, err, model.ErrQueryWorkLimit)
	for _, q := range []model.Query{{Collection: "items", Limit: 1001}, {Collection: "items", Filters: model.Filters{{Field: "x", Op: model.OpIn, Value: []any{}}, {Field: "bad.path", Op: model.OpEq, Value: 1}}}, {Collection: "items", OrderBy: []model.Order{{Field: "x", Direction: "sideways"}}}} {
		_, err = New(source, nil).ExecuteQueryPage(context.Background(), "db", q)
		require.ErrorIs(t, err, model.ErrInvalidQuery)
	}
}

func TestSourceCandidateBudgetCountsTombstones(t *testing.T) {
	source := &pageSource{}
	count := 0
	source.scan = func(ctx context.Context, db string, req types.SourceScanRequest) (types.SourceScanPage, error) {
		count++
		doc := queryDocument(fmt.Sprintf("%08d", count), nil)
		doc.Deleted = true
		return types.SourceScanPage{Documents: []*types.StoredDoc{doc}, NextAfter: fmt.Sprintf("%08d", count)}, nil
	}
	page, err := New(source, nil).ExecuteQueryPage(context.Background(), "db", model.Query{Collection: "items", Limit: 1})
	require.ErrorIs(t, err, model.ErrQueryWorkLimit)
	require.Equal(t, maxQueryCandidates, count)
	require.Nil(t, page.Documents)
}

type closeObservedStream struct {
	manager.CandidateStream
	closed bool
}

func (s *closeObservedStream) Close() error { s.closed = true; return s.CandidateStream.Close() }

func TestQueryCancellationClosesStreamAndDiscardsPage(t *testing.T) {
	engine, source, st, idx := pageFixture(t, "memory", "      - {field: score, order: asc}\n")
	for i := 0; i < 3; i++ {
		doc := queryDocument(fmt.Sprint(i), map[string]any{"score": int64(i)})
		source.docs[doc.Fullpath] = doc
		projectPageDocument(t, st, idx, doc)
	}
	var observed *closeObservedStream
	idx.wrap = func(stream manager.CandidateStream) manager.CandidateStream {
		observed = &closeObservedStream{CandidateStream: stream}
		return observed
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source.afterRead = cancel
	page, err := engine.ExecuteQueryPage(ctx, "db", model.Query{Collection: "items", OrderBy: []model.Order{{Field: "score", Direction: "asc"}}, Limit: 2})
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, page.Documents)
	require.Nil(t, page.NextCursor)
	require.NotNil(t, observed)
	require.True(t, observed.closed)
}

func TestQueryOperatorsPreserveExactNumericConjunctions(t *testing.T) {
	engine, source, st, idx := pageFixture(t, "memory", "      - {field: score, order: asc}\n")
	base := int64(9007199254740992)
	for i := int64(0); i < 3; i++ {
		doc := queryDocument(fmt.Sprint(i), map[string]any{"score": base + i, "tags": []any{"x"}})
		source.docs[doc.Fullpath] = doc
		projectPageDocument(t, st, idx, doc)
	}
	cases := []struct {
		name    string
		filters model.Filters
		ids     []string
	}{
		{"exact equality", model.Filters{{Field: "score", Op: model.OpEq, Value: base + 1}}, []string{"1"}},
		{"not equal intervals", model.Filters{{Field: "score", Op: model.OpNe, Value: base + 1}}, []string{"0", "2"}},
		{"exclusive range", model.Filters{{Field: "score", Op: model.OpGt, Value: float64(base)}, {Field: "score", Op: model.OpLt, Value: base + 2}}, []string{"1"}},
		{"inclusive repeated bounds", model.Filters{{Field: "score", Op: model.OpGte, Value: base + 1}, {Field: "score", Op: model.OpLte, Value: base + 2}}, []string{"1", "2"}},
		{"union and residual", model.Filters{{Field: "score", Op: model.OpIn, Value: []any{base, base + 1, base + 2}}, {Field: "score", Op: model.OpNe, Value: base + 1}, {Field: "tags", Op: model.OpContains, Value: "x"}}, []string{"0", "2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page, err := engine.ExecuteQueryPage(context.Background(), "db", model.Query{Collection: "items", Filters: tc.filters})
			require.NoError(t, err)
			require.Equal(t, tc.ids, pageIDs(page))
			require.Nil(t, page.NextCursor)
		})
	}
}

func TestQueryMaterializationBatchPreservesPositions(t *testing.T) {
	engine, source, st, idx := pageFixture(t, "memory", "      - {field: score, order: asc}\n")
	for i := 0; i < 140; i++ {
		doc := queryDocument(fmt.Sprintf("%03d", i), map[string]any{"score": int64(i)})
		source.docs[doc.Fullpath] = doc
		projectPageDocument(t, st, idx, doc)
	}
	q := model.Query{Collection: "items", OrderBy: []model.Order{{Field: "score", Direction: "asc"}}, Limit: 130}
	first, err := engine.ExecuteQueryPage(context.Background(), "db", q)
	require.NoError(t, err)
	require.Len(t, first.Documents, 130)
	require.Equal(t, []int{128, 2}, source.readLengths)
	require.Equal(t, "129", first.Documents[129]["id"])
	cursor, err := decodeCursor(*first.NextCursor)
	require.NoError(t, err)
	boundary, err := encoding.ExtractDocID(cursor.Position)
	require.NoError(t, err)
	require.Equal(t, "129", boundary)
	for _, options := range source.reads {
		require.Equal(t, int64(maxMaterializationBytes), options.MaxBytes)
	}
	q.StartAfter = *first.NextCursor
	q.Limit = 50
	second, err := engine.ExecuteQueryPage(context.Background(), "db", q)
	require.NoError(t, err)
	require.Len(t, second.Documents, 10)
	require.Equal(t, "130", second.Documents[0]["id"])
	require.Nil(t, second.NextCursor)
}

func TestQueryMaterializationByteBudgetDiscardsPage(t *testing.T) {
	engine, source, st, idx := pageFixture(t, "memory", "      - {field: score, order: asc}\n")
	for i := 0; i < 20; i++ {
		doc := queryDocument(fmt.Sprintf("%03d", i), map[string]any{"score": int64(i), "payload": strings.Repeat("x", 900<<10)})
		source.docs[doc.Fullpath] = doc
		projectPageDocument(t, st, idx, doc)
	}
	q := model.Query{Collection: "items", OrderBy: []model.Order{{Field: "score", Direction: "asc"}}, Limit: 20}
	page, err := engine.ExecuteQueryPage(context.Background(), "db", q)
	require.ErrorIs(t, err, model.ErrQueryWorkLimit)
	require.Empty(t, page.Documents)
	require.Nil(t, page.NextCursor)
	require.Equal(t, []int{20, 10, 10}, source.readLengths)
	q.Limit = 6
	page, err = engine.ExecuteQueryPage(context.Background(), "db", q)
	require.NoError(t, err)
	require.Len(t, page.Documents, 6)
	require.NotNil(t, page.NextCursor)
}

func TestQueryMaterializationSplitsLargeRejectedCandidates(t *testing.T) {
	engine, source, st, idx := pageFixture(t, "memory", "      - {field: score, order: asc}\n")
	for i := 0; i < 30; i++ {
		doc := queryDocument(fmt.Sprintf("%03d", i), map[string]any{"score": int64(i), "keep": i >= 28, "payload": strings.Repeat("x", 900<<10)})
		source.docs[doc.Fullpath] = doc
		projectPageDocument(t, st, idx, doc)
	}
	q := model.Query{Collection: "items", OrderBy: []model.Order{{Field: "score", Direction: "asc"}}, Filters: model.Filters{{Field: "keep", Op: model.OpEq, Value: true}}, Limit: 100}
	page, err := engine.ExecuteQueryPage(context.Background(), "db", q)
	require.NoError(t, err)
	require.Equal(t, []string{"028", "029"}, pageIDs(page))
	require.Nil(t, page.NextCursor)
	require.Equal(t, []int{30, 15, 15}, source.readLengths)
}

func TestSourceScanSplitsLargeRejectedTombstones(t *testing.T) {
	source := &pageSource{docs: map[string]*types.StoredDoc{}}
	for i := 0; i < 30; i++ {
		doc := queryDocument(fmt.Sprintf("%03d", i), map[string]any{"payload": strings.Repeat("x", 900<<10)})
		doc.Deleted = i < 28
		source.docs[doc.Fullpath] = doc
	}
	page, err := New(source, nil).ExecuteQueryPage(context.Background(), "db", model.Query{Collection: "items", Limit: 100})
	require.NoError(t, err)
	require.Equal(t, []string{"028", "029"}, pageIDs(page))
	require.Nil(t, page.NextCursor)
	limits := make([]int, len(source.scans))
	for i, scan := range source.scans {
		limits[i] = scan.Limit
	}
	require.Equal(t, []int{100, 50, 25, 12, 12, 12}, limits)
}

func TestMaterializationDoesNotRetryOtherSourceFailures(t *testing.T) {
	source := &pageSource{fail: fmt.Errorf("source offline")}
	sourceCalls := 0
	// Read counting is performed before storage invocation, including failed reads.
	stats := &queryStats{}
	err := New(source, nil).materialize(context.Background(), "db", []string{"items/a", "items/b"}, stats, func(int, *types.StoredDoc) error { sourceCalls++; return nil })
	require.EqualError(t, err, "source offline")
	require.Equal(t, int64(1), stats.sourceReads)
	require.Zero(t, sourceCalls)
	source.fail = types.ErrReadBudget
	stats = &queryStats{}
	err = New(source, nil).materialize(context.Background(), "db", []string{"items/a", "items/b"}, stats, func(int, *types.StoredDoc) error { return nil })
	require.ErrorIs(t, err, types.ErrReadBudget)
	require.Equal(t, int64(2), stats.sourceReads)
}

func TestEncodeCursorRejectsOversizedOrdering(t *testing.T) {
	cursor := queryCursor{Version: 2, Scope: "scope", Route: "index-v2", Template: "template", Generation: "generation", Branches: "branches", Position: []byte("position"), Order: []model.Order{{Field: strings.Repeat("field", maxCursorBytes), Direction: "asc"}}}
	token, err := encodeCursor(cursor)
	require.ErrorIs(t, err, model.ErrQueryWorkLimit)
	require.Empty(t, token)
	cursor.Order = []model.Order{{Field: "score", Direction: "asc"}}
	token, err = encodeCursor(cursor)
	require.NoError(t, err)
	decoded, err := decodeCursor(token)
	require.NoError(t, err)
	require.Equal(t, cursor, decoded)
}
