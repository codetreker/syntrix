package manager

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"testing"

	"github.com/codetreker/syntrix/internal/indexer/config"
	"github.com/codetreker/syntrix/internal/indexer/encoding"
	"github.com/codetreker/syntrix/internal/indexer/mem_store"
	"github.com/codetreker/syntrix/internal/indexer/persist_store"
	"github.com/codetreker/syntrix/internal/indexer/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type intersectionStore struct {
	store.Store
	searches int
}

func (s *intersectionStore) Search(db, pattern, tmplID string, opts store.SearchOptions) ([]store.DocRef, error) {
	s.searches++
	return s.Store.Search(db, pattern, tmplID, opts)
}

func TestManager_Search_FilterIntersection(t *testing.T) {
	filter := func(op FilterOp, value any) Filter { return Filter{Field: "value", Op: op, Value: value} }
	type queryCase struct {
		name    string
		filters []Filter
		want    []int
	}
	groups := []struct {
		name   string
		values []any
		cases  []queryCase
	}{
		{
			name: "numbers", values: []any{10, 20, 30, 40},
			cases: []queryCase{
				{"equal numeric representations", []Filter{filter(FilterEq, 20), filter(FilterEq, float64(20)), filter(FilterEq, int64(20))}, []int{1}},
				{"different equalities", []Filter{filter(FilterEq, 10), filter(FilterEq, 20)}, nil},
				{"equality inside range", []Filter{filter(FilterEq, 20), filter(FilterGt, 10), filter(FilterLte, 30)}, []int{1}},
				{"equality below range", []Filter{filter(FilterEq, 10), filter(FilterGte, 20)}, nil},
				{"equality above range", []Filter{filter(FilterEq, 30), filter(FilterLte, 20)}, nil},
				{"equality on inclusive lower", []Filter{filter(FilterEq, 20), filter(FilterGte, 20)}, []int{1}},
				{"equality on exclusive lower", []Filter{filter(FilterEq, 20), filter(FilterGt, 20)}, nil},
				{"equality on inclusive upper", []Filter{filter(FilterEq, 20), filter(FilterLte, 20)}, []int{1}},
				{"equality on exclusive upper", []Filter{filter(FilterEq, 20), filter(FilterLt, 20)}, nil},
				{"strongest lower", []Filter{filter(FilterGte, 30), filter(FilterGt, 10)}, []int{2, 3}},
				{"strongest upper", []Filter{filter(FilterLte, 20), filter(FilterLt, 40)}, []int{0, 1}},
				{"strict lower tie", []Filter{filter(FilterGt, 20), filter(FilterGte, 20)}, []int{2, 3}},
				{"strict upper tie", []Filter{filter(FilterLt, 30), filter(FilterLte, 30)}, []int{0, 1}},
				{"intersected interval", []Filter{filter(FilterGte, 20), filter(FilterGt, 10), filter(FilterLte, 30), filter(FilterLt, 40)}, []int{1, 2}},
				{"closed point", []Filter{filter(FilterGte, 20), filter(FilterLte, 20)}, []int{1}},
				{"crossed bounds", []Filter{filter(FilterGte, 30), filter(FilterLte, 20)}, nil},
				{"open lower point", []Filter{filter(FilterGt, 20), filter(FilterLte, 20)}, nil},
				{"open upper point", []Filter{filter(FilterGte, 20), filter(FilterLt, 20)}, nil},
				{"open point", []Filter{filter(FilterGt, 20), filter(FilterLt, 20)}, nil},
			},
		},
		{
			name: "strings", values: []any{"a", "a\x00", "ab", "b"},
			cases: []queryCase{
				{"escaped string lower", []Filter{filter(FilterGte, "a\x00"), filter(FilterGte, "a"), filter(FilterLt, "b")}, []int{1, 2}},
				{"string strict upper", []Filter{filter(FilterLt, "ab"), filter(FilterLte, "ab")}, []int{0, 1}},
				{"string equality conflict", []Filter{filter(FilterEq, "a"), filter(FilterEq, "a\x00")}, nil},
			},
		},
		{
			name: "null and booleans", values: []any{nil, false, true},
			cases: []queryCase{
				{"null equality", []Filter{filter(FilterEq, nil), filter(FilterLte, nil)}, []int{0}},
				{"null exclusive", []Filter{filter(FilterEq, nil), filter(FilterGt, nil)}, nil},
				{"false equality", []Filter{filter(FilterEq, false), filter(FilterGte, false)}, []int{1}},
				{"boolean conflict", []Filter{filter(FilterEq, false), filter(FilterEq, true)}, nil},
				{"type ordering", []Filter{filter(FilterGte, nil), filter(FilterGte, false), filter(FilterLte, true)}, []int{1, 2}},
			},
		},
	}

	for _, backend := range []string{"memory", "pebble"} {
		for _, direction := range []encoding.Direction{encoding.Asc, encoding.Desc} {
			for _, group := range groups {
				t.Run(fmt.Sprintf("%s/direction_%d/%s", backend, direction, group.name), func(t *testing.T) {
					var backing store.Store = mem_store.New()
					if backend == "pebble" {
						cfg := config.DefaultStoreConfig()
						cfg.Path = t.TempDir()
						var err error
						backing, err = persist_store.NewPebbleStore(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
						require.NoError(t, err)
					}
					t.Cleanup(func() { require.NoError(t, backing.Close()) })
					st := &intersectionStore{Store: backing}
					m, keys := intersectionFixture(t, st, direction, group.values)
					require.NoError(t, st.Flush())

					for _, tc := range group.cases {
						for _, reverse := range []bool{false, true} {
							t.Run(fmt.Sprintf("%s/reverse_%t", tc.name, reverse), func(t *testing.T) {
								filters := slices.Clone(tc.filters)
								if reverse {
									slices.Reverse(filters)
								}
								filters = append([]Filter{{Field: "tenant", Op: FilterEq, Value: "selected"}}, filters...)
								plan := Plan{Collection: "items", Filters: filters, Limit: 100}
								before := st.searches
								refs, err := m.Search(context.Background(), "testdb", plan)
								require.NoError(t, err)
								if len(tc.want) == 0 {
									assert.Empty(t, refs)
									assert.Equal(t, before, st.searches, "contradiction must bypass storage")
									plan.StartAfter = encoding.EncodeBase64(keys[0])
									refs, err = m.Search(context.Background(), "testdb", plan)
									require.NoError(t, err)
									assert.Empty(t, refs)
									assert.Equal(t, before, st.searches, "cursor must not revive an empty interval")
									return
								}
								want := make([]string, len(tc.want))
								for i, index := range tc.want {
									want[i] = fmt.Sprintf("v%d", index)
								}
								if direction == encoding.Desc {
									slices.Reverse(want)
								}
								ids := make([]string, len(refs))
								for i, ref := range refs {
									ids[i] = ref.ID
								}
								assert.Equal(t, want, ids)
							})
						}
					}
				})
			}
		}
	}
}

func intersectionFixture(t *testing.T, st store.Store, direction encoding.Direction, values []any) (*Manager, [][]byte) {
	t.Helper()
	m := New(st)
	order := "asc"
	if direction == encoding.Desc {
		order = "desc"
	}
	require.NoError(t, m.LoadTemplatesFromBytes([]byte(fmt.Sprintf(`
templates:
  - name: items_by_tenant_value
    collectionPattern: items
    fields:
      - { field: tenant, order: asc }
      - { field: value, order: %s }
`, order))))
	tmpl := m.Templates()[0]
	keys := make([][]byte, len(values))
	for i, value := range values {
		for _, tenant := range []string{"selected", "other"} {
			id := fmt.Sprintf("v%d", i)
			if tenant == "other" {
				id += "-other"
			}
			key, err := encoding.Encode([]encoding.Field{
				{Value: tenant, Direction: encoding.Asc},
				{Value: value, Direction: direction},
			}, id)
			require.NoError(t, err)
			require.NoError(t, st.Upsert("testdb", tmpl.NormalizedPattern(), tmpl.Identity(), id, key, ""))
			if tenant == "selected" {
				keys[i] = key
			}
		}
	}
	return m, keys
}

func TestManager_Search_ContradictionPreservesErrors(t *testing.T) {
	st := &intersectionStore{Store: mem_store.New()}
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	m, _ := intersectionFixture(t, st, encoding.Asc, []any{10, 20})
	filters := []Filter{
		{Field: "tenant", Op: FilterEq, Value: "selected"},
		{Field: "value", Op: FilterEq, Value: 10},
		{Field: "value", Op: FilterEq, Value: 20},
	}

	_, err := m.Search(context.Background(), "testdb", Plan{Collection: "missing", Filters: filters})
	require.ErrorIs(t, err, ErrNoMatchingIndex)
	_, err = m.Search(context.Background(), "testdb", Plan{Collection: "items", Filters: filters, StartAfter: "!invalid!"})
	require.ErrorContains(t, err, "invalid cursor")
	filters = append(filters, Filter{Field: "value", Op: FilterLte, Value: []int{30}})
	_, err = m.Search(context.Background(), "testdb", Plan{Collection: "items", Filters: filters})
	require.ErrorIs(t, err, encoding.ErrUnsupportedType)
	_, err = m.Search(context.Background(), "testdb", Plan{Collection: "items", Filters: []Filter{
		{Field: "tenant", Op: FilterEq, Value: "selected"},
		{Field: "tenant", Op: FilterEq, Value: "other"},
		{Field: "value", Op: FilterEq, Value: []int{30}},
	}})
	require.ErrorIs(t, err, encoding.ErrUnsupportedType)
	assert.Zero(t, st.searches)
}

func TestManager_Search_IntersectedEqualityKeepsPrefix(t *testing.T) {
	st := &intersectionStore{Store: mem_store.New()}
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	m, _ := intersectionFixture(t, st, encoding.Asc, []any{10, 20, 30})
	refs, err := m.Search(context.Background(), "testdb", Plan{Collection: "items", Filters: []Filter{
		{Field: "tenant", Op: FilterEq, Value: "selected"},
		{Field: "tenant", Op: FilterGte, Value: "selected"},
		{Field: "tenant", Op: FilterLt, Value: "z"},
		{Field: "value", Op: FilterGte, Value: 20},
		{Field: "value", Op: FilterLt, Value: 30},
	}})
	require.NoError(t, err)
	require.Len(t, refs, 1)
	assert.Equal(t, "v1", refs[0].ID)
}
