package core

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/indexer"
	"github.com/syntrixbase/syntrix/internal/indexer/config"
	"github.com/syntrixbase/syntrix/internal/indexer/manager"
	"github.com/syntrixbase/syntrix/internal/indexer/mem_store"
	"github.com/syntrixbase/syntrix/internal/indexer/persist_store"
	"github.com/syntrixbase/syntrix/internal/indexer/store"
	"github.com/syntrixbase/syntrix/pkg/model"
)

type pageBenchmarkMetrics struct {
	reads, sourceDocuments, candidateGroups, examined int64
}

type pageBenchmarkSource struct {
	storage.DocumentStore
	docs    map[string]*types.StoredDoc
	metrics *pageBenchmarkMetrics
}

func (s *pageBenchmarkSource) GetMany(ctx context.Context, database string, paths []string, opts ...types.ReadOptions) ([]*types.StoredDoc, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if database != "db" || len(opts) != 1 || opts[0].Consistency != types.ReadAuthoritative || !opts[0].ShowDeleted {
		return nil, fmt.Errorf("unexpected benchmark source read contract")
	}
	s.metrics.reads++
	s.metrics.sourceDocuments += int64(len(paths))
	docs := make([]*types.StoredDoc, len(paths))
	var size int64
	for i, path := range paths {
		docs[i] = s.docs[path]
		if opts[0].MaxBytes > 0 {
			bytes, err := types.StoredDocumentBytes(docs[i])
			if err != nil {
				return nil, err
			}
			size += bytes
			if size > opts[0].MaxBytes {
				return nil, types.ErrReadBudget
			}
		}
	}
	return docs, nil
}

type pageBenchmarkStream struct {
	manager.CandidateStream
	metrics *pageBenchmarkMetrics
}

func (s *pageBenchmarkStream) Next() (manager.CandidateGroup, bool, error) {
	group, ok, err := s.CandidateStream.Next()
	if ok {
		s.metrics.candidateGroups++
	}
	return group, ok, err
}

func (s *pageBenchmarkStream) Close() error {
	s.metrics.examined += s.Examined()
	return s.CandidateStream.Close()
}

// These benchmarks include planning, candidate traversal, source validation,
// current-posting verification, cursor creation, and typed page encoding. Source
// documents stay in memory, so timings exclude database and network latency.
func BenchmarkIndexedQueryPage(b *testing.B) {
	const documentCount, pageSize = 10_000, 50
	for _, backend := range []string{"memory", "pebble"} {
		b.Run(backend, func(b *testing.B) {
			var st store.Store
			if backend == "memory" {
				st = mem_store.New()
			} else {
				tempRoot := filepath.Join("..", "..", "..", ".tmp")
				require.NoError(b, os.MkdirAll(tempRoot, 0o755))
				dir, err := os.MkdirTemp(tempRoot, "query-page-benchmark-")
				require.NoError(b, err)
				b.Cleanup(func() { require.NoError(b, os.RemoveAll(dir)) })
				cfg := config.DefaultStoreConfig()
				cfg.Path = dir
				cfg.BlockCacheSize = 8 << 20
				st, err = persist_store.NewPebbleStore(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
				require.NoError(b, err)
			}
			b.Cleanup(func() { require.NoError(b, st.Close()) })
			metrics := &pageBenchmarkMetrics{}
			idx := &pageIndex{manager: manager.New(st)}
			idx.wrap = func(stream manager.CandidateStream) manager.CandidateStream {
				return &pageBenchmarkStream{CandidateStream: stream, metrics: metrics}
			}
			require.NoError(b, idx.manager.LoadTemplatesFromBytes([]byte("templates:\n  - name: benchmark\n    collectionPattern: items\n    fields:\n      - {field: score, order: asc}\n")))
			source := &pageBenchmarkSource{docs: make(map[string]*types.StoredDoc, documentCount), metrics: metrics}
			tmpl := idx.manager.Templates()[0]
			projections := make([]store.Projection, 0, documentCount)
			var postingBytes int64
			for i := range documentCount {
				doc := queryDocument(fmt.Sprintf("%05d", i), map[string]any{
					"score": int64(i), "oneInTen": i%10 == 0, "oneInHundred": i%100 == 0,
					"payload": "representative business payload", "exact": int64(9007199254740993),
				})
				source.docs[doc.Fullpath] = doc
				projection, err := indexer.BuildDocumentProjection(doc, &tmpl, "benchmark-generation", indexer.DefaultProjectionLimits())
				require.NoError(b, err)
				projections = append(projections, projection)
				for _, key := range projection.PostingKeys {
					postingBytes += int64(len(key))
				}
			}
			require.NoError(b, st.ApplyDocumentProjection(projections, ""))
			flushStart := time.Now()
			require.NoError(b, st.PublishGeneration(projections[0].Index, "benchmark-ready"))
			require.NoError(b, st.Flush())
			b.Logf("%s: %d documents, %d posting bytes, publish and flush %.3f ms", backend, documentCount, postingBytes, float64(time.Since(flushStart))/float64(time.Millisecond))
			engine := New(source, idx)
			base := model.Query{Collection: "items", Filters: model.Filters{{Field: "score", Op: model.OpGte, Value: int64(0)}}, Limit: pageSize}
			first, err := engine.ExecuteQueryPage(context.Background(), "db", base)
			require.NoError(b, err)
			require.NotNil(b, first.NextCursor)
			for _, test := range []struct {
				name, residual, cursor string
				start, stride, results int
				op                     model.FilterOp
			}{
				{name: "common_first", stride: 1, results: pageSize},
				{name: "common_equality", start: 5000, stride: 1, results: 1, op: model.OpEq},
				{name: "common_continuation", cursor: *first.NextCursor, start: pageSize, stride: 1, results: pageSize},
				{name: "multi_branch_in_50", stride: 2, results: pageSize, op: model.OpIn},
				{name: "residual_90_percent", residual: "oneInTen", stride: 10, results: pageSize},
				{name: "residual_99_percent", residual: "oneInHundred", stride: 100, results: pageSize},
			} {
				b.Run(test.name, func(b *testing.B) {
					q := base
					q.Filters = append(model.Filters{}, base.Filters...)
					q.StartAfter = test.cursor
					if test.op == model.OpEq {
						q.Filters[0] = model.Filter{Field: "score", Op: model.OpEq, Value: int64(test.start)}
					} else if test.op == model.OpIn {
						values := make([]any, test.results)
						for i := range values {
							values[i] = int64(test.start + i*test.stride)
						}
						q.Filters[0] = model.Filter{Field: "score", Op: model.OpIn, Value: values}
					}
					if test.residual != "" {
						q.Filters = append(q.Filters, model.Filter{Field: test.residual, Op: model.OpEq, Value: true})
					}
					page, err := engine.ExecuteQueryPage(context.Background(), "db", q)
					require.NoError(b, err)
					require.Len(b, page.Documents, test.results)
					for i, doc := range page.Documents {
						require.Equal(b, fmt.Sprintf("%05d", test.start+i*test.stride), doc["id"])
					}
					*metrics = pageBenchmarkMetrics{}
					b.ReportAllocs()
					b.ResetTimer()
					for range b.N {
						page, err := engine.ExecuteQueryPage(context.Background(), "db", q)
						if err != nil || len(page.Documents) != test.results || (page.NextCursor != nil) != (test.results == pageSize) {
							b.Fatalf("incomplete query page: documents=%d, cursor=%v, error=%v", len(page.Documents), page.NextCursor, err)
						}
					}
					b.StopTimer()
					operations := float64(b.N)
					b.ReportMetric(float64(metrics.reads)/operations, "GetMany/op")
					b.ReportMetric(float64(metrics.candidateGroups)/operations, "candidates/op")
					b.ReportMetric(float64(metrics.examined)/operations, "index_rows/op")
					b.ReportMetric(float64(metrics.sourceDocuments)/operations/float64(test.results), "source_docs/result")
					b.ReportMetric(float64(metrics.sourceDocuments)/float64(metrics.reads), "source_docs/GetMany")
					b.ReportMetric(float64(postingBytes)/documentCount, "posting_bytes/document")
				})
			}
		})
	}
}
