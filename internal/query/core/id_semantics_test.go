package core

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/pkg/model"
)

func TestQueryIDPredicateRouteConformance(t *testing.T) {
	tests := []struct {
		name    string
		filters model.Filters
		want    []string
	}{
		{"numeric equality", model.Filters{{Field: "id", Op: model.OpEq, Value: 123}}, []string{}},
		{"string equality", model.Filters{{Field: "id", Op: model.OpEq, Value: "123"}}, []string{"123"}},
		{"mixed membership", model.Filters{{Field: "id", Op: model.OpIn, Value: []any{"a", 123}}}, []string{"a"}},
		{"empty equality", model.Filters{{Field: "id", Op: model.OpEq, Value: ""}}, []string{}},
		{"empty membership", model.Filters{{Field: "id", Op: model.OpIn, Value: []any{}}}, []string{}},
		{"scalar union", model.Filters{{Field: "id", Op: model.OpIn, Value: []any{nil, true, int64(123), 123.5, "", "a", "a", "b"}}}, []string{"a", "b"}},
		{"invalid path alternatives", model.Filters{{Field: "id", Op: model.OpIn, Value: []any{"a/b", "a\x00b", "b"}}}, []string{"b"}},
		{"conjunction", model.Filters{{Field: "id", Op: model.OpIn, Value: []any{"a", "123", 123}}, {Field: "id", Op: model.OpIn, Value: []any{"a", 123}}}, []string{"a"}},
		{"reversed conjunction", model.Filters{{Field: "id", Op: model.OpIn, Value: []any{"a", 123}}, {Field: "id", Op: model.OpIn, Value: []any{"a", "123", 123}}}, []string{"a"}},
		{"contradiction", model.Filters{{Field: "id", Op: model.OpEq, Value: 123}, {Field: "id", Op: model.OpEq, Value: "123"}}, []string{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evaluated := []string{}
			for _, id := range []string{"123", "a", "b"} {
				matched, err := model.EvaluateFilters(test.filters, func(field string) (any, bool) {
					require.Equal(t, "id", field)
					return id, true
				})
				require.NoError(t, err)
				if matched {
					evaluated = append(evaluated, id)
				}
			}
			require.Equal(t, test.want, evaluated)
			for _, route := range []struct {
				name   string
				fields string
				index  bool
			}{
				{"source IDs", "      - {field: score, order: asc}\n", false},
				{"indexed residual IDs", "      - {field: score, order: asc}\n", true},
				{"indexed access IDs", "      - {field: id, order: asc}\n", true},
			} {
				t.Run(route.name, func(t *testing.T) {
					engine, source, st, idx := pageFixture(t, "memory", route.fields)
					for _, id := range []string{"123", "a", "b"} {
						doc := queryDocument(id, map[string]any{"score": int64(1), "id": "shadow"})
						source.docs[doc.Fullpath] = doc
						projectPageDocument(t, st, idx, doc)
					}
					filters := append(model.Filters(nil), test.filters...)
					if route.index {
						filters = append(filters, model.Filter{Field: "score", Op: model.OpEq, Value: int64(1)})
					}
					q := model.Query{Collection: "items", Filters: filters, Limit: 1}
					got := []string{}
					for pageCount := 0; ; pageCount++ {
						require.Less(t, pageCount, 5, "pagination must terminate")
						page, err := engine.ExecuteQueryPage(context.Background(), "db", q)
						require.NoError(t, err)
						got = append(got, pageIDs(page)...)
						if page.NextCursor == nil {
							break
						}
						q.StartAfter = *page.NextCursor
					}
					require.Equal(t, evaluated, got)
				})
			}
		})
	}
}

func TestQuerySourceIDPositionBudget(t *testing.T) {
	for _, route := range []string{"list", "IDs"} {
		for _, size := range []int{maxPositionBytes, maxPositionBytes + 1} {
			t.Run(fmt.Sprintf("%s/%d bytes", route, size), func(t *testing.T) {
				engine, source, _, _ := pageFixture(t, "memory", "      - {field: score, order: asc}\n")
				id := strings.Repeat("a", size)
				for _, candidate := range []string{id, "z"} {
					doc := queryDocument(candidate, map[string]any{})
					source.docs[doc.Fullpath] = doc
				}
				q := model.Query{Collection: "items", Limit: 1}
				if route == "IDs" {
					q.Filters = model.Filters{{Field: "id", Op: model.OpIn, Value: []any{id, "z"}}}
				}
				page, err := engine.ExecuteQueryPage(context.Background(), "db", q)
				if size > maxPositionBytes {
					require.ErrorIs(t, err, model.ErrQueryWorkLimit)
					require.Empty(t, page.Documents)
					return
				}
				require.NoError(t, err)
				require.Equal(t, []string{id}, pageIDs(page))
				require.NotNil(t, page.NextCursor)
				q.StartAfter = *page.NextCursor
				page, err = engine.ExecuteQueryPage(context.Background(), "db", q)
				require.NoError(t, err)
				require.Equal(t, []string{"z"}, pageIDs(page))
			})
		}
	}
}
