package cel

import (
	"testing"

	pb "github.com/codetreker/syntrix/api/gen/streamer/v1"
	"github.com/codetreker/syntrix/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompiler_Compile(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	tests := []struct {
		name    string
		expr    string
		doc     map[string]interface{}
		want    bool
		wantErr bool
	}{
		{
			name: "simple equality",
			expr: "doc['status'] == 'active'",
			doc:  map[string]interface{}{"status": "active"},
			want: true,
		},
		{
			name: "nested field",
			expr: "doc['user']['name'] == 'alice'",
			doc:  map[string]interface{}{"user": map[string]interface{}{"name": "alice"}},
			want: true,
		},
		{
			name: "greater than",
			expr: "doc['count'] > 5",
			doc:  map[string]interface{}{"count": int64(10)},
			want: true,
		},
		{
			name: "less than - false",
			expr: "doc['count'] < 5",
			doc:  map[string]interface{}{"count": int64(10)},
			want: false,
		},
		{
			name: "boolean and",
			expr: "doc['a'] == 1 && doc['b'] == 2",
			doc:  map[string]interface{}{"a": int64(1), "b": int64(2)},
			want: true,
		},
		{
			name: "boolean or",
			expr: "doc['a'] == 1 || doc['a'] == 2",
			doc:  map[string]interface{}{"a": int64(2)},
			want: true,
		},
		{
			name:    "invalid expression",
			expr:    "invalid syntax !!!",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exprSub, err := c.Compile(tt.expr)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, exprSub)
			require.NotNil(t, exprSub.Program)

			result, err := Evaluate(exprSub.Program, tt.doc)
			require.NoError(t, err)
			assert.Equal(t, tt.want, result)
		})
	}
}

func TestCompiler_CompileFilters(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	tests := []struct {
		name    string
		filters []model.Filter
		doc     map[string]interface{}
		want    bool
	}{
		{
			name:    "empty filters match all",
			filters: nil,
			doc:     map[string]interface{}{"anything": "value"},
			want:    true,
		},
		{
			name: "equality filter",
			filters: []model.Filter{
				{Field: "status", Op: model.OpEq, Value: "active"},
			},
			doc:  map[string]interface{}{"status": "active"},
			want: true,
		},
		{
			name: "nested field filter",
			filters: []model.Filter{
				{Field: "user.role", Op: model.OpEq, Value: "admin"},
			},
			doc:  map[string]interface{}{"user": map[string]interface{}{"role": "admin"}},
			want: true,
		},
		{
			name: "multiple filters AND",
			filters: []model.Filter{
				{Field: "status", Op: model.OpEq, Value: "active"},
				{Field: "priority", Op: model.OpGt, Value: int64(5)},
			},
			doc:  map[string]interface{}{"status": "active", "priority": int64(10)},
			want: true,
		},
		{
			name: "multiple filters - one fails",
			filters: []model.Filter{
				{Field: "status", Op: model.OpEq, Value: "active"},
				{Field: "priority", Op: model.OpGt, Value: int64(5)},
			},
			doc:  map[string]interface{}{"status": "active", "priority": int64(3)},
			want: false,
		},
		{
			name: "in operator",
			filters: []model.Filter{
				{Field: "type", Op: model.OpIn, Value: []interface{}{"a", "b", "c"}},
			},
			doc:  map[string]interface{}{"type": "b"},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prg, err := c.CompileFilters(tt.filters)
			require.NoError(t, err)

			result, err := Evaluate(prg, tt.doc)
			require.NoError(t, err)
			assert.Equal(t, tt.want, result)
		})
	}
}

func TestEvaluate_NilProgram(t *testing.T) {
	result, err := Evaluate(nil, map[string]interface{}{"any": "data"})
	require.NoError(t, err)
	assert.True(t, result, "nil program should match all")
}

func TestFilterToExpression(t *testing.T) {
	tests := []struct {
		name    string
		filter  model.Filter
		want    string
		wantErr bool
	}{
		{
			name:   "equality string",
			filter: model.Filter{Field: "status", Op: model.OpEq, Value: "active"},
			want:   "doc['status'] == 'active'",
		},
		{
			name:   "greater than int",
			filter: model.Filter{Field: "count", Op: model.OpGt, Value: 5},
			want:   "doc['count'] > 5",
		},
		{
			name:   "greater than or equal",
			filter: model.Filter{Field: "user.profile.age", Op: model.OpGte, Value: 18},
			want:   "doc['user']['profile']['age'] >= 18",
		},
		{
			name:   "less than",
			filter: model.Filter{Field: "price", Op: model.OpLt, Value: 100},
			want:   "doc['price'] < 100",
		},
		{
			name:   "less than or equal",
			filter: model.Filter{Field: "quantity", Op: model.OpLte, Value: 50},
			want:   "doc['quantity'] <= 50",
		},
		{
			name:   "in operator",
			filter: model.Filter{Field: "role", Op: model.OpIn, Value: []interface{}{"admin", "mod"}},
			want:   "doc['role'] in ['admin', 'mod']",
		},
		{
			name:   "array-contains",
			filter: model.Filter{Field: "tags", Op: model.OpContains, Value: "important"},
			want:   "'important' in doc['tags']",
		},
		{
			name:    "unsupported operator",
			filter:  model.Filter{Field: "x", Op: "like", Value: "abc"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := filterToExpression(tt.filter)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFormatValue(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		want    string
		wantErr bool
	}{
		{name: "string", value: "hello", want: "'hello'"},
		{name: "string with quote", value: "it's", want: "'it\\'s'"},
		{name: "int", value: 42, want: "42"},
		{name: "int32", value: int32(42), want: "42"},
		{name: "int64", value: int64(100), want: "100"},
		{name: "float32", value: float32(2.5), want: "2.5"},
		{name: "float64", value: 3.14, want: "3.14"},
		{name: "bool true", value: true, want: "true"},
		{name: "bool false", value: false, want: "false"},
		{name: "slice", value: []interface{}{"a", "b"}, want: "['a', 'b']"},
		{name: "unsupported", value: struct{}{}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := formatValue(tt.value)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- Proto Filter Tests ---

func TestCompiler_CompileProtoFilters(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	tests := []struct {
		name    string
		filters []*pb.Filter
		doc     map[string]interface{}
		want    bool
		wantErr bool
	}{
		{
			name:    "empty filters match all",
			filters: nil,
			doc:     map[string]interface{}{"anything": "value"},
			want:    true,
		},
		{
			name: "eq operator",
			filters: []*pb.Filter{{
				Field: "status",
				Op:    "==",
				Value: &pb.Value{Kind: &pb.Value_StringValue{StringValue: "active"}},
			}},
			doc:  map[string]interface{}{"status": "active"},
			want: true,
		},
		{
			name: "ne operator",
			filters: []*pb.Filter{{
				Field: "status",
				Op:    "!=",
				Value: &pb.Value{Kind: &pb.Value_StringValue{StringValue: "deleted"}},
			}},
			doc:  map[string]interface{}{"status": "active"},
			want: true,
		},
		{
			name: "gt operator",
			filters: []*pb.Filter{{
				Field: "count",
				Op:    ">",
				Value: &pb.Value{Kind: &pb.Value_IntValue{IntValue: 5}},
			}},
			doc:  map[string]interface{}{"count": int64(10)},
			want: true,
		},
		{
			name: "gte operator",
			filters: []*pb.Filter{{
				Field: "count",
				Op:    ">=",
				Value: &pb.Value{Kind: &pb.Value_IntValue{IntValue: 10}},
			}},
			doc:  map[string]interface{}{"count": int64(10)},
			want: true,
		},
		{
			name: "lt operator",
			filters: []*pb.Filter{{
				Field: "count",
				Op:    "<",
				Value: &pb.Value{Kind: &pb.Value_IntValue{IntValue: 20}},
			}},
			doc:  map[string]interface{}{"count": int64(10)},
			want: true,
		},
		{
			name: "lte operator",
			filters: []*pb.Filter{{
				Field: "count",
				Op:    "<=",
				Value: &pb.Value{Kind: &pb.Value_IntValue{IntValue: 10}},
			}},
			doc:  map[string]interface{}{"count": int64(10)},
			want: true,
		},
		{
			name: "in operator",
			filters: []*pb.Filter{{
				Field: "role",
				Op:    "in",
				Value: &pb.Value{Kind: &pb.Value_ListValue{ListValue: &pb.ListValue{
					Values: []*pb.Value{
						{Kind: &pb.Value_StringValue{StringValue: "admin"}},
						{Kind: &pb.Value_StringValue{StringValue: "mod"}},
					},
				}}},
			}},
			doc:  map[string]interface{}{"role": "admin"},
			want: true,
		},
		{
			name: "contains operator",
			filters: []*pb.Filter{{
				Field: "tags",
				Op:    "contains",
				Value: &pb.Value{Kind: &pb.Value_StringValue{StringValue: "important"}},
			}},
			doc:  map[string]interface{}{"tags": []interface{}{"important", "urgent"}},
			want: true,
		},
		{
			name: "nested field",
			filters: []*pb.Filter{{
				Field: "user.profile.name",
				Op:    "==",
				Value: &pb.Value{Kind: &pb.Value_StringValue{StringValue: "alice"}},
			}},
			doc:  map[string]interface{}{"user": map[string]interface{}{"profile": map[string]interface{}{"name": "alice"}}},
			want: true,
		},
		{
			name: "bool value",
			filters: []*pb.Filter{{
				Field: "active",
				Op:    "==",
				Value: &pb.Value{Kind: &pb.Value_BoolValue{BoolValue: true}},
			}},
			doc:  map[string]interface{}{"active": true},
			want: true,
		},
		{
			name: "double value",
			filters: []*pb.Filter{{
				Field: "score",
				Op:    ">",
				Value: &pb.Value{Kind: &pb.Value_DoubleValue{DoubleValue: 3.5}},
			}},
			doc:  map[string]interface{}{"score": 4.0},
			want: true,
		},
		{
			name: "multiple filters AND",
			filters: []*pb.Filter{
				{
					Field: "status",
					Op:    "==",
					Value: &pb.Value{Kind: &pb.Value_StringValue{StringValue: "active"}},
				},
				{
					Field: "priority",
					Op:    ">",
					Value: &pb.Value{Kind: &pb.Value_IntValue{IntValue: 5}},
				},
			},
			doc:  map[string]interface{}{"status": "active", "priority": int64(10)},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exprSub, err := c.CompileProtoFilters(tt.filters)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, exprSub)

			result, err := Evaluate(exprSub.Program, tt.doc)
			require.NoError(t, err)
			assert.Equal(t, tt.want, result)
		})
	}
}

func TestProtoFilterToExpression_Errors(t *testing.T) {
	t.Run("nil filter", func(t *testing.T) {
		_, err := protoFilterToExpression(nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "filter is nil")
	})

	t.Run("nil value", func(t *testing.T) {
		_, err := protoFilterToExpression(&pb.Filter{
			Field: "test",
			Op:    "==",
			Value: nil,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "value is nil")
	})

	t.Run("unsupported operator", func(t *testing.T) {
		_, err := protoFilterToExpression(&pb.Filter{
			Field: "test",
			Op:    "like",
			Value: &pb.Value{Kind: &pb.Value_StringValue{StringValue: "abc"}},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported operator")
	})
}

func TestFormatProtoValue(t *testing.T) {
	tests := []struct {
		name    string
		value   *pb.Value
		want    string
		wantErr bool
	}{
		{
			name:  "string",
			value: &pb.Value{Kind: &pb.Value_StringValue{StringValue: "hello"}},
			want:  "'hello'",
		},
		{
			name:  "string with quote",
			value: &pb.Value{Kind: &pb.Value_StringValue{StringValue: "it's"}},
			want:  "'it\\'s'",
		},
		{
			name:  "int",
			value: &pb.Value{Kind: &pb.Value_IntValue{IntValue: 42}},
			want:  "42",
		},
		{
			name:  "double",
			value: &pb.Value{Kind: &pb.Value_DoubleValue{DoubleValue: 3.14}},
			want:  "3.14",
		},
		{
			name:  "bool true",
			value: &pb.Value{Kind: &pb.Value_BoolValue{BoolValue: true}},
			want:  "true",
		},
		{
			name:  "bool false",
			value: &pb.Value{Kind: &pb.Value_BoolValue{BoolValue: false}},
			want:  "false",
		},
		{
			name: "list",
			value: &pb.Value{Kind: &pb.Value_ListValue{ListValue: &pb.ListValue{
				Values: []*pb.Value{
					{Kind: &pb.Value_StringValue{StringValue: "a"}},
					{Kind: &pb.Value_IntValue{IntValue: 1}},
				},
			}}},
			want: "['a', 1]",
		},
		{
			name:  "nil list value",
			value: &pb.Value{Kind: &pb.Value_ListValue{ListValue: nil}},
			want:  "[]",
		},
		{
			name:    "nil value",
			value:   nil,
			wantErr: true,
		},
		{
			name:    "empty value",
			value:   &pb.Value{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := formatProtoValue(tt.value)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCompileExpression_Errors(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	t.Run("invalid syntax", func(t *testing.T) {
		_, err := c.CompileExpression("invalid !!! syntax")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "CEL compile error")
	})
}

func TestEvaluate_NonBoolResult(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	// Compile an expression that returns a non-boolean
	prg, err := c.CompileExpression("doc['value']")
	require.NoError(t, err)

	// Evaluate - should error because result is not boolean
	_, err = Evaluate(prg, map[string]interface{}{"value": "string"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not boolean")
}
