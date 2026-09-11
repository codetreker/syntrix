package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/syntrixbase/syntrix/internal/indexer/config"
	"github.com/syntrixbase/syntrix/internal/indexer/encoding"
	"github.com/syntrixbase/syntrix/internal/indexer/mem_store"
	"github.com/syntrixbase/syntrix/internal/indexer/persist_store"
	"github.com/syntrixbase/syntrix/internal/indexer/store"
)

func candidateStores(t *testing.T, run func(*testing.T, store.Store)) {
	t.Helper()
	for _, backend := range []string{"memory", "pebble"} {
		t.Run(backend, func(t *testing.T) {
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
			run(t, st)
		})
	}
}

func candidateManager(t *testing.T, st store.Store, fields string) (*Manager, store.QueryIndexRef) {
	t.Helper()
	m := New(st)
	require.NoError(t, m.LoadTemplatesFromBytes([]byte("templates:\n  - name: candidates\n    collectionPattern: items\n    fields:\n"+fields)))
	templates := m.Templates()
	return m, store.QueryIndexRef{Database: "db", Collection: "items", TemplateFingerprint: templates[0].Fingerprint(), Generation: "generation-1"}
}

func candidatePosting(t *testing.T, st store.Store, ref store.QueryIndexRef, id string, fields ...encoding.Field) []byte {
	t.Helper()
	key, err := encoding.Encode(fields, id)
	require.NoError(t, err)
	require.NoError(t, st.ApplyDocumentProjection([]store.Projection{{Index: ref, DocumentID: id, PostingKeys: [][]byte{key}}}, ""))
	return key
}

func collectCandidates(t *testing.T, stream CandidateStream) []CandidateGroup {
	t.Helper()
	var groups []CandidateGroup
	for {
		group, ok, err := stream.Next()
		require.NoError(t, err)
		if !ok {
			return groups
		}
		groups = append(groups, group)
	}
}

func candidateIDs(groups []CandidateGroup) []string {
	ids := make([]string, len(groups))
	for i, group := range groups {
		ids[i] = group.ID
	}
	return ids
}

func TestCandidatesTypedIntervals(t *testing.T) {
	for _, direction := range []encoding.Direction{encoding.Asc, encoding.Desc} {
		order := "asc"
		if direction == encoding.Desc {
			order = "desc"
		}
		t.Run(order, func(t *testing.T) {
			candidateStores(t, func(t *testing.T, st store.Store) {
				m, ref := candidateManager(t, st, "      - {field: value, order: "+order+"}\n")
				for _, row := range []struct {
					id    string
					value any
				}{
					{"null", nil}, {"bool", true}, {"low", json.Number("9007199254740992")},
					{"mid", json.Number("9007199254740993")}, {"high", json.Number("9007199254740994")},
					{"string", "9007199254740993"},
				} {
					candidatePosting(t, st, ref, row.id, encoding.Field{Value: row.value, Direction: direction})
				}
				candidatePosting(t, st, ref, "missing", encoding.Field{Missing: true, Direction: direction})
				require.NoError(t, st.PublishGeneration(ref, "ready"))
				notEqual := []string{"null", "bool", "low", "high", "string"}
				if direction == encoding.Desc {
					notEqual = []string{"string", "high", "low", "bool", "null"}
				}
				for _, tc := range []struct {
					name    string
					filters []Filter
					ids     []string
				}{
					{"eq-exact", []Filter{{Field: "value", Op: FilterEq, Value: json.Number("9007199254740993")}}, []string{"mid"}},
					{"gt", []Filter{{Field: "value", Op: FilterGt, Value: json.Number("9007199254740993")}}, []string{"high"}},
					{"lt", []Filter{{Field: "value", Op: FilterLt, Value: json.Number("9007199254740993")}}, []string{"low"}},
					{"closed", []Filter{{Field: "value", Op: FilterGte, Value: json.Number("9007199254740993")}, {Field: "value", Op: FilterLte, Value: json.Number("9007199254740993")}}, []string{"mid"}},
					{"string-family", []Filter{{Field: "value", Op: FilterGte, Value: "0"}}, []string{"string"}},
					{"ne-excludes-missing", []Filter{{Field: "value", Op: FilterNe, Value: json.Number("9007199254740993")}}, notEqual},
					{"empty-point-intersection", []Filter{{Field: "value", Op: FilterEq, Value: json.Number("9007199254740993")}, {Field: "value", Op: FilterLt, Value: json.Number("9007199254740993")}}, []string{}},
					{"contradiction", []Filter{{Field: "value", Op: FilterGt, Value: json.Number("9007199254740993")}, {Field: "value", Op: FilterLt, Value: json.Number("9007199254740993")}}, []string{}},
				} {
					t.Run(tc.name, func(t *testing.T) {
						stream, err := m.OpenCandidates(context.Background(), "db", Plan{Collection: "items", Filters: tc.filters})
						require.NoError(t, err)
						defer stream.Close()
						require.Equal(t, tc.ids, candidateIDs(collectCandidates(t, stream)))
					})
				}
			})
		})
	}
}

func TestCandidatesMergeBranchesAndResume(t *testing.T) {
	candidateStores(t, func(t *testing.T, st store.Store) {
		m, ref := candidateManager(t, st, "      - {field: status, order: asc}\n      - {field: createdAt, order: desc}\n")
		postings := map[string][]byte{}
		for _, row := range []struct {
			id, status string
			createdAt  int
		}{{"a", "open", 10}, {"aa", "closed", 10}, {"z", "closed", 20}, {"b", "open", 5}} {
			postings[row.id] = candidatePosting(t, st, ref, row.id, encoding.Field{Value: row.status}, encoding.Field{Value: row.createdAt, Direction: encoding.Desc})
		}
		require.NoError(t, st.PublishGeneration(ref, "ready"))
		plan := Plan{Collection: "items", Filters: []Filter{{Field: "status", Op: FilterIn, Value: []any{"open", "closed", "open"}}}, OrderBy: []OrderField{{Field: "createdAt", Direction: encoding.Desc}}}
		stream, err := m.OpenCandidates(context.Background(), "db", plan)
		require.NoError(t, err)
		metadata := stream.Metadata()
		require.Len(t, metadata.Branches, 2)
		groups := collectCandidates(t, stream)
		require.NoError(t, stream.Close())
		require.Equal(t, []string{"z", "a", "aa", "b"}, candidateIDs(groups))
		for _, group := range groups {
			require.Len(t, group.Branches, 1)
			key, err := metadata.PostingKey(group, group.Branches[0])
			require.NoError(t, err)
			require.Equal(t, postings[group.ID], key)
		}
		plan.TemplateFingerprint, plan.Generation, plan.BranchHash = metadata.TemplateFingerprint, metadata.Generation, metadata.BranchHash
		for i, group := range groups {
			plan.AfterPosition = group.Position
			resumed, err := m.OpenCandidates(context.Background(), "db", plan)
			require.NoError(t, err)
			require.Equal(t, candidateIDs(groups[i+1:]), candidateIDs(collectCandidates(t, resumed)))
			require.NoError(t, resumed.Close())
		}
	})
}

func TestCandidatesResumeWithFixedFieldsInOrder(t *testing.T) {
	candidateStores(t, func(t *testing.T, st store.Store) {
		m, ref := candidateManager(t, st, "      - {field: status, order: asc}\n      - {field: createdAt, order: desc}\n")
		for _, row := range []struct {
			id, status string
			createdAt  int
		}{{"a", "closed", 10}, {"aa", "closed", 5}, {"b", "open", 20}, {"c", "open", 1}} {
			candidatePosting(t, st, ref, row.id, encoding.Field{Value: row.status}, encoding.Field{Value: row.createdAt, Direction: encoding.Desc})
		}
		require.NoError(t, st.PublishGeneration(ref, "ready"))
		for _, tc := range []struct {
			name  string
			order []OrderField
			ids   []string
		}{
			{"fixed-first", nil, []string{"a", "aa", "b", "c"}},
			{"fixed-after-variable", []OrderField{{Field: "createdAt", Direction: encoding.Desc}, {Field: "status", Direction: encoding.Asc}}, []string{"b", "a", "aa", "c"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				plan := Plan{Collection: "items", Filters: []Filter{{Field: "status", Op: FilterIn, Value: []any{"open", "closed"}}}, OrderBy: tc.order}
				stream, err := m.OpenCandidates(context.Background(), "db", plan)
				require.NoError(t, err)
				groups := collectCandidates(t, stream)
				require.NoError(t, stream.Close())
				require.Equal(t, tc.ids, candidateIDs(groups))
				for i, group := range groups {
					plan.AfterPosition = group.Position
					resumed, err := m.OpenCandidates(context.Background(), "db", plan)
					require.NoError(t, err)
					require.Equal(t, candidateIDs(groups[i+1:]), candidateIDs(collectCandidates(t, resumed)))
					require.NoError(t, resumed.Close())
				}
			})
		}
	})
}

func TestCandidatesMembershipAndResidualAssignments(t *testing.T) {
	candidateStores(t, func(t *testing.T, st store.Store) {
		m, ref := candidateManager(t, st, "      - {field: tags, mode: membership}\n      - {field: createdAt, order: desc}\n")
		wanted := candidatePosting(t, st, ref, "candidate", encoding.Field{Value: "red"}, encoding.Field{Value: 7, Direction: encoding.Desc})
		candidatePosting(t, st, ref, "other", encoding.Field{Value: "blue"}, encoding.Field{Value: 9, Direction: encoding.Desc})
		require.NoError(t, st.PublishGeneration(ref, "ready"))
		plan := Plan{Collection: "items", Filters: []Filter{{Field: "tags", Op: FilterContains, Value: "red"}, {Field: "createdAt", Op: FilterGte, Value: 5}, {Field: "owner", Op: FilterEq, Value: "alice"}}, OrderBy: []OrderField{{Field: "createdAt", Direction: encoding.Desc}}}
		stream, err := m.OpenCandidates(context.Background(), "db", plan)
		require.NoError(t, err)
		defer stream.Close()
		metadata := stream.Metadata()
		require.Equal(t, []PredicateAssignment{{Predicate: 0, Access: true, Residual: true}, {Predicate: 1, Access: true, Residual: true}, {Predicate: 2, Residual: true}}, metadata.Assignments)
		groups := collectCandidates(t, stream)
		require.Equal(t, []string{"candidate"}, candidateIDs(groups))
		key, err := metadata.PostingKey(groups[0], 0)
		require.NoError(t, err)
		require.Equal(t, wanted, key)
		require.Equal(t, 1, metadata.Branches[0].FixedFields)
		require.Len(t, metadata.EffectiveOrder, 2)
	})
}

func TestCandidatesReadinessAndCursorBinding(t *testing.T) {
	candidateStores(t, func(t *testing.T, st store.Store) {
		m, ref := candidateManager(t, st, "      - {field: value, order: asc}\n")
		plan := Plan{Collection: "items", OrderBy: []OrderField{{Field: "value", Direction: encoding.Asc}}}
		_, err := m.OpenCandidates(context.Background(), "db", plan)
		require.ErrorIs(t, err, ErrIndexNotReady)
		candidatePosting(t, st, ref, "a", encoding.Field{Value: 1})
		_, err = m.OpenCandidates(context.Background(), "db", plan)
		require.ErrorIs(t, err, ErrIndexNotReady)
		require.NoError(t, st.PublishGeneration(ref, "ready"))
		stream, err := m.OpenCandidates(context.Background(), "db", plan)
		require.NoError(t, err)
		require.Equal(t, []string{"a"}, candidateIDs(collectCandidates(t, stream)))
		require.NoError(t, stream.Close())
		for _, stale := range []Plan{
			{Collection: "items", OrderBy: plan.OrderBy, Generation: "old"},
			{Collection: "items", OrderBy: plan.OrderBy, BranchHash: "old"},
			{Collection: "items", OrderBy: plan.OrderBy, TemplateFingerprint: "old"},
		} {
			_, err := m.OpenCandidates(context.Background(), "db", stale)
			require.ErrorIs(t, err, ErrStaleCursor)
		}
		require.NoError(t, st.SetFailure(ref, "projection failed"))
		_, err = m.OpenCandidates(context.Background(), "db", plan)
		require.ErrorIs(t, err, ErrIndexNotReady)
	})
}

func TestCandidatesLimitsAndHiddenOrdering(t *testing.T) {
	candidateStores(t, func(t *testing.T, st store.Store) {
		m, ref := candidateManager(t, st, "      - {field: status, order: asc}\n      - {field: createdAt, order: desc}\n")
		for i := range 3 {
			candidatePosting(t, st, ref, fmt.Sprint(i), encoding.Field{Value: "open"}, encoding.Field{Value: i, Direction: encoding.Desc})
		}
		require.NoError(t, st.PublishGeneration(ref, "ready"))
		_, err := m.OpenCandidates(context.Background(), "db", Plan{Collection: "items", OrderBy: []OrderField{{Field: "createdAt", Direction: encoding.Desc}}})
		require.ErrorIs(t, err, ErrNoMatchingIndex)
		plan := Plan{Collection: "items", Filters: []Filter{{Field: "status", Op: FilterEq, Value: "open"}}, OrderBy: []OrderField{{Field: "createdAt", Direction: encoding.Desc}}, MaxExamined: 1}
		stream, err := m.OpenCandidates(context.Background(), "db", plan)
		require.NoError(t, err)
		_, _, err = stream.Next()
		require.ErrorIs(t, err, store.ErrWorkLimit)
		require.NoError(t, stream.Close())
		values := make([]any, MaxCandidateBranches+1)
		for i := range values {
			values[i] = fmt.Sprint(i)
		}
		plan.Filters = []Filter{{Field: "status", Op: FilterIn, Value: values}}
		_, err = m.OpenCandidates(context.Background(), "db", plan)
		require.ErrorIs(t, err, ErrInvalidPlan)
		plan.Filters[0].Value = values[:MaxCandidateBranches]
		plan.MaxExamined = 0
		stream, err = m.OpenCandidates(context.Background(), "db", plan)
		require.NoError(t, err)
		require.Len(t, stream.Metadata().Branches, MaxCandidateBranches)
		require.Empty(t, collectCandidates(t, stream))
		require.NoError(t, stream.Close())
		plan.AfterPosition = []byte(strings.Repeat("x", MaxCandidatePositionBytes+1))
		_, err = m.OpenCandidates(context.Background(), "db", plan)
		require.ErrorIs(t, err, ErrInvalidPlan)
	})
}

func TestCandidatesPositionBudgetAndCancellation(t *testing.T) {
	candidateStores(t, func(t *testing.T, st store.Store) {
		m, ref := candidateManager(t, st, "      - {field: value, order: asc}\n")
		candidatePosting(t, st, ref, "large", encoding.Field{Value: strings.Repeat("x", MaxCandidatePositionBytes)})
		require.NoError(t, st.PublishGeneration(ref, "ready"))
		plan := Plan{Collection: "items", OrderBy: []OrderField{{Field: "value", Direction: encoding.Asc}}}
		stream, err := m.OpenCandidates(context.Background(), "db", plan)
		require.NoError(t, err)
		_, _, err = stream.Next()
		require.ErrorIs(t, err, store.ErrWorkLimit)
		require.NoError(t, stream.Close())
		ctx, cancel := context.WithCancel(context.Background())
		stream, err = m.OpenCandidates(ctx, "db", plan)
		require.NoError(t, err)
		cancel()
		_, _, err = stream.Next()
		require.ErrorIs(t, err, context.Canceled)
		require.NoError(t, stream.Close())
		_, err = m.OpenCandidates(ctx, "db", plan)
		require.ErrorIs(t, err, context.Canceled)
	})
}

func TestCandidatesBusinessFieldNamedLogicalID(t *testing.T) {
	candidateStores(t, func(t *testing.T, st store.Store) {
		m, ref := candidateManager(t, st, "      - {field: __logicalID, order: asc}\n")
		wanted := candidatePosting(t, st, ref, "document-id", encoding.Field{Value: "business-value"})
		require.NoError(t, st.PublishGeneration(ref, "ready"))
		plan := Plan{Collection: "items", OrderBy: []OrderField{{Field: "__logicalID", Direction: encoding.Asc}}}
		stream, err := m.OpenCandidates(context.Background(), "db", plan)
		require.NoError(t, err)
		metadata := stream.Metadata()
		groups := collectCandidates(t, stream)
		require.NoError(t, stream.Close())
		require.Equal(t, []string{"document-id"}, candidateIDs(groups))
		key, err := metadata.PostingKey(groups[0], 0)
		require.NoError(t, err)
		require.Equal(t, wanted, key)
		plan.AfterPosition = groups[0].Position
		resumed, err := m.OpenCandidates(context.Background(), "db", plan)
		require.NoError(t, err)
		require.Empty(t, collectCandidates(t, resumed))
		require.NoError(t, resumed.Close())
	})
}

func TestCandidatesRejectOppositeFixedIDDirection(t *testing.T) {
	candidateStores(t, func(t *testing.T, st store.Store) {
		m, ref := candidateManager(t, st, "      - {field: id, order: desc}\n      - {field: value, order: asc}\n")
		for _, id := range []string{"a", "b"} {
			candidatePosting(t, st, ref, id, encoding.Field{Value: id, Direction: encoding.Desc}, encoding.Field{Value: 1})
		}
		require.NoError(t, st.PublishGeneration(ref, "ready"))
		_, err := m.OpenCandidates(context.Background(), "db", Plan{
			Collection: "items",
			Filters:    []Filter{{Field: "id", Op: FilterIn, Value: []any{"a", "b"}}},
			OrderBy:    []OrderField{{Field: "id", Direction: encoding.Asc}, {Field: "value", Direction: encoding.Asc}},
		})
		require.ErrorIs(t, err, ErrNoMatchingIndex)
	})
}

func TestCandidatesTrailingPhysicalID(t *testing.T) {
	candidateStores(t, func(t *testing.T, st store.Store) {
		m, ref := candidateManager(t, st, "      - {field: value, order: asc}\n      - {field: id, order: asc}\n")
		postings := make(map[string][]byte)
		for _, row := range []struct {
			id    string
			value int
		}{{"z", 1}, {"a", 2}, {"aa", 2}, {"b", 3}} {
			postings[row.id] = candidatePosting(t, st, ref, row.id, encoding.Field{Value: row.value}, encoding.Field{Value: row.id})
		}
		require.NoError(t, st.PublishGeneration(ref, "ready"))
		for _, tc := range []struct {
			name  string
			order []OrderField
		}{
			{"implicit-id", []OrderField{{Field: "value", Direction: encoding.Asc}}},
			{"explicit-id", []OrderField{{Field: "value", Direction: encoding.Asc}, {Field: "id", Direction: encoding.Asc}}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				plan := Plan{Collection: "items", OrderBy: tc.order}
				stream, err := m.OpenCandidates(context.Background(), "db", plan)
				require.NoError(t, err)
				metadata := stream.Metadata()
				groups := collectCandidates(t, stream)
				require.NoError(t, stream.Close())
				require.Equal(t, []string{"z", "a", "aa", "b"}, candidateIDs(groups))
				for i, group := range groups {
					key, err := metadata.PostingKey(group, 0)
					require.NoError(t, err)
					require.Equal(t, postings[group.ID], key)
					plan.AfterPosition = group.Position
					resumed, err := m.OpenCandidates(context.Background(), "db", plan)
					require.NoError(t, err)
					require.Equal(t, candidateIDs(groups[i+1:]), candidateIDs(collectCandidates(t, resumed)))
					require.NoError(t, resumed.Close())
				}
			})
		}
	})
}

func TestCandidatesEmptyIntersectionValidatesPosition(t *testing.T) {
	candidateStores(t, func(t *testing.T, st store.Store) {
		m, ref := candidateManager(t, st, "      - {field: value, order: asc}\n")
		require.NoError(t, st.PublishGeneration(ref, "ready"))
		_, err := m.OpenCandidates(context.Background(), "db", Plan{
			Collection:    "items",
			Filters:       []Filter{{Field: "value", Op: FilterEq, Value: 1}, {Field: "value", Op: FilterLt, Value: 1}},
			AfterPosition: []byte{encoding.Version},
		})
		require.ErrorIs(t, err, ErrInvalidPlan)
	})
}
