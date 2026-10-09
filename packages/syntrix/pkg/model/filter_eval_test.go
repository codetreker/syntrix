package model

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFilterTruthTable(t *testing.T) {
	tests := []struct {
		name    string
		value   any
		present bool
		op      FilterOp
		operand any
		want    bool
	}{
		{"null equality", nil, true, OpEq, nil, true},
		{"null inequality", nil, true, OpNe, "news", true},
		{"typed equality", int64(1), true, OpEq, "1", false},
		{"numeric equality", int64(1), true, OpEq, float64(1), true},
		{"exact integer equality", int64(9007199254740993), true, OpEq, float64(9007199254740992), false},
		{"greater", int64(9007199254740993), true, OpGt, float64(9007199254740992), true},
		{"greater equal", int64(1), true, OpGte, float64(1), true},
		{"less", "a", true, OpLt, "b", true},
		{"less equal", "a", true, OpLte, "a", true},
		{"wrong range family", "2", true, OpGt, int64(1), false},
		{"null range", nil, true, OpLt, "b", false},
		{"null in", nil, true, OpIn, []any{"x", nil}, true},
		{"empty in", int64(1), true, OpIn, []any{}, false},
		{"number in", int64(1), true, OpIn, []any{float64(1)}, true},
		{"array not scalar", []any{"news"}, true, OpEq, "news", false},
		{"array unequal", []any{"news"}, true, OpNe, "news", true},
		{"array not in", []any{"news"}, true, OpIn, []any{"news"}, false},
		{"object unequal", map[string]any{"tag": "news"}, true, OpNe, "news", true},
		{"membership", []any{"news", "news"}, true, OpContains, "news", true},
		{"null membership", []any{nil}, true, OpContains, nil, true},
		{"numeric membership", []any{int64(1)}, true, OpContains, float64(1), true},
		{"no flattening", []any{[]any{"news"}, map[string]any{"tag": "news"}}, true, OpContains, "news", false},
		{"scalar not array", "news", true, OpContains, "news", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := EvaluateFilter(Filter{Field: "v", Op: test.op, Value: test.operand}, test.value, test.present)
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
	for _, op := range ValidOps() {
		var operand any = "x"
		if op == OpIn {
			operand = []any{"x", nil}
		}
		got, err := EvaluateFilter(Filter{Field: "v", Op: op, Value: operand}, nil, false)
		require.NoError(t, err)
		require.False(t, got, string(op))
	}
}

func TestNormalizeFiltersValidatesWholeConjunction(t *testing.T) {
	invalid := []Filter{
		{Field: "nested.field", Op: OpEq, Value: 1}, {Field: "", Op: OpEq, Value: 1},
		{Field: "v", Op: "unknown", Value: 1}, {Field: "v", Op: OpEq, Value: []any{}},
		{Field: "v", Op: OpNe, Value: map[string]any{}}, {Field: "v", Op: OpGt, Value: nil},
		{Field: "v", Op: OpGte, Value: true}, {Field: "v", Op: OpIn, Value: nil},
		{Field: "v", Op: OpIn, Value: []any{[]any{1}}}, {Field: "v", Op: OpContains, Value: map[string]any{}},
		{Field: "v", Op: OpEq, Value: math.Inf(1)},
	}
	for _, filter := range invalid {
		_, err := EvaluateFilters(Filters{{Field: "v", Op: OpIn, Value: []any{}}, filter}, func(string) (any, bool) { t.Fatal("must validate before access"); return nil, false })
		require.Error(t, err, "%#v", filter)
	}
	filters, err := NormalizeFilters(Filters{{Field: "v", Op: OpIn, Value: []any{int64(1), float64(1), nil, nil, math.Copysign(0, -1), int64(0)}}})
	require.NoError(t, err)
	require.Equal(t, []any{int64(1), nil, float64(0)}, filters[0].Value)
}

func TestFiltersPreserveRepeatedPredicatesAndSourceErrors(t *testing.T) {
	filters := Filters{{Field: "tags", Op: OpContains, Value: "a"}, {Field: "tags", Op: OpContains, Value: "b"}}
	for _, test := range []struct {
		tags []any
		want bool
	}{{[]any{"a"}, false}, {[]any{"a", "b"}, true}} {
		got, err := EvaluateFilters(filters, func(string) (any, bool) { return test.tags, true })
		require.NoError(t, err)
		require.Equal(t, test.want, got)
	}
	_, err := EvaluateFilters(Filters{{Field: "x", Op: OpEq, Value: 1}, {Field: "bad", Op: OpEq, Value: 2}}, func(field string) (any, bool) {
		if field == "x" {
			return int64(0), true
		}
		return math.NaN(), true
	})
	require.Error(t, err)
}

func TestEvaluateFilterRejectsInvalidPredicateBeforeMissingValue(t *testing.T) {
	for _, filter := range []Filter{
		{Field: "value", Op: FilterOp("unknown"), Value: nil},
		{Field: "nested.value", Op: OpEq, Value: nil},
		{Field: "value", Op: OpEq, Value: []any{int64(1)}},
		{Field: "value", Op: OpIn, Value: []any{map[string]any{"nested": true}}},
		{Field: "value", Op: OpGt, Value: math.NaN()},
	} {
		matched, err := EvaluateFilter(filter, nil, false)
		require.Error(t, err, "%#v", filter)
		require.False(t, matched)
	}
	matched, err := EvaluateFilter(Filter{Field: "value", Op: OpEq, Value: int64(1)}, float32(math.Inf(1)), true)
	require.ErrorContains(t, err, "non-finite")
	require.False(t, matched)
}
