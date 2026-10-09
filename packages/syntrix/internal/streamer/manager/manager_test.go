package manager

import (
	"errors"
	"testing"

	pb "github.com/codetreker/syntrix/api/gen/streamer/v1"
	"github.com/google/cel-go/cel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// docIDFilter creates a filter for exact document ID matching.
func docIDFilter(docID string) []*pb.Filter {
	return []*pb.Filter{{
		Field: "_id",
		Op:    "==",
		Value: &pb.Value{Kind: &pb.Value_StringValue{StringValue: docID}},
	}}
}

func TestManager_Subscribe_ExactMatch(t *testing.T) {
	m := New()

	resp, err := m.Subscribe("gw1", &pb.SubscribeRequest{
		SubscriptionId: "sub1",
		Database:       "database1",
		Collection:     "users",
		Filters:        docIDFilter("doc123"),
	})

	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.Equal(t, "sub1", resp.SubscriptionId)

	// Verify subscription is stored
	sub, ok := m.GetSubscription("sub1")
	require.True(t, ok)
	assert.Equal(t, "gw1", sub.GatewayID)
	assert.Equal(t, "database1", sub.Database)
	assert.Equal(t, "users", sub.Collection)
	assert.Equal(t, "doc123", sub.GetDocumentID())
	assert.True(t, sub.IsExactMatch())
}

func TestManager_Subscribe_CollectionLevel(t *testing.T) {
	m := New()

	resp, err := m.Subscribe("gw1", &pb.SubscribeRequest{
		SubscriptionId: "sub1",
		Database:       "database1",
		Collection:     "users",
		// No Filters = collection-level subscription
	})

	require.NoError(t, err)
	assert.True(t, resp.Success)

	sub, ok := m.GetSubscription("sub1")
	require.True(t, ok)
	assert.Empty(t, sub.GetDocumentID())
	assert.Empty(t, sub.Filters)
	assert.False(t, sub.IsExactMatch())
}

func TestManager_Subscribe_DuplicateID(t *testing.T) {
	m := New()

	// First subscription
	resp1, _ := m.Subscribe("gw1", &pb.SubscribeRequest{
		SubscriptionId: "sub1",
		Database:       "database1",
		Collection:     "users",
	})
	require.True(t, resp1.Success)

	// Duplicate ID
	resp2, err := m.Subscribe("gw1", &pb.SubscribeRequest{
		SubscriptionId: "sub1",
		Database:       "database1",
		Collection:     "orders",
	})

	require.NoError(t, err)
	assert.False(t, resp2.Success)
	assert.Contains(t, resp2.Error, "already exists")
}

func TestManager_Unsubscribe(t *testing.T) {
	m := New()

	// Subscribe
	m.Subscribe("gw1", &pb.SubscribeRequest{
		SubscriptionId: "sub1",
		Database:       "database1",
		Collection:     "users",
		Filters:        docIDFilter("doc123"),
	})

	// Unsubscribe
	err := m.Unsubscribe("sub1")
	require.NoError(t, err)

	// Verify removed
	_, ok := m.GetSubscription("sub1")
	assert.False(t, ok)
}

func TestManager_Unsubscribe_NotFound(t *testing.T) {
	m := New()

	err := m.Unsubscribe("nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestManager_UnregisterGateway(t *testing.T) {
	m := New()

	// Add multiple subscriptions for the same gateway
	m.Subscribe("gw1", &pb.SubscribeRequest{
		SubscriptionId: "sub1",
		Database:       "database1",
		Collection:     "users",
	})
	m.Subscribe("gw1", &pb.SubscribeRequest{
		SubscriptionId: "sub2",
		Database:       "database1",
		Collection:     "orders",
	})
	// Another gateway
	m.Subscribe("gw2", &pb.SubscribeRequest{
		SubscriptionId: "sub3",
		Database:       "database1",
		Collection:     "users",
	})

	// Unregister gw1
	m.UnregisterGateway("gw1")

	// gw1 subscriptions removed
	_, ok1 := m.GetSubscription("sub1")
	_, ok2 := m.GetSubscription("sub2")
	assert.False(t, ok1)
	assert.False(t, ok2)

	// gw2 subscription still exists
	_, ok3 := m.GetSubscription("sub3")
	assert.True(t, ok3)
}

func TestManager_Match_ExactDocumentID(t *testing.T) {
	m := New()

	// Subscribe to specific document
	m.Subscribe("gw1", &pb.SubscribeRequest{
		SubscriptionId: "sub1",
		Database:       "database1",
		Collection:     "users",
		Filters:        docIDFilter("doc123"),
	})
	m.Subscribe("gw2", &pb.SubscribeRequest{
		SubscriptionId: "sub2",
		Database:       "database1",
		Collection:     "users",
		Filters:        docIDFilter("doc456"),
	})

	// Match doc123
	result := m.Match("database1", "users", "doc123", nil)
	require.Len(t, result, 1)
	assert.Contains(t, result["gw1"], "sub1")
	assert.NotContains(t, result, "gw2")

	// Match doc456
	result = m.Match("database1", "users", "doc456", nil)
	require.Len(t, result, 1)
	assert.Contains(t, result["gw2"], "sub2")
}

func TestManager_Match_CollectionLevel(t *testing.T) {
	m := New()

	// Collection-level subscription
	m.Subscribe("gw1", &pb.SubscribeRequest{
		SubscriptionId: "sub1",
		Database:       "database1",
		Collection:     "users",
	})
	// Document-specific subscription
	m.Subscribe("gw2", &pb.SubscribeRequest{
		SubscriptionId: "sub2",
		Database:       "database1",
		Collection:     "users",
		Filters:        docIDFilter("doc123"),
	})

	// Match any doc in users collection
	result := m.Match("database1", "users", "anyDoc", nil)

	// Collection-level subscription matches
	assert.Contains(t, result["gw1"], "sub1")

	// Document-specific doesn't match
	assert.Empty(t, result["gw2"])

	// Match doc123 - both should match
	result = m.Match("database1", "users", "doc123", nil)
	assert.Contains(t, result["gw1"], "sub1")
	assert.Contains(t, result["gw2"], "sub2")
}

func TestManager_Match_DatabaseIsolation(t *testing.T) {
	m := New()

	m.Subscribe("gw1", &pb.SubscribeRequest{
		SubscriptionId: "sub1",
		Database:       "database1",
		Collection:     "users",
	})
	m.Subscribe("gw2", &pb.SubscribeRequest{
		SubscriptionId: "sub2",
		Database:       "database2",
		Collection:     "users",
	})

	// Match database1
	result := m.Match("database1", "users", "doc1", nil)
	require.Len(t, result, 1)
	assert.Contains(t, result["gw1"], "sub1")

	// Match database2
	result = m.Match("database2", "users", "doc1", nil)
	require.Len(t, result, 1)
	assert.Contains(t, result["gw2"], "sub2")
}

func TestManager_Stats(t *testing.T) {
	m := New()

	m.Subscribe("gw1", &pb.SubscribeRequest{
		SubscriptionId: "sub1",
		Database:       "database1",
		Collection:     "users",
		Filters:        docIDFilter("doc1"),
	})
	m.Subscribe("gw1", &pb.SubscribeRequest{
		SubscriptionId: "sub2",
		Database:       "database1",
		Collection:     "users",
	})
	m.Subscribe("gw2", &pb.SubscribeRequest{
		SubscriptionId: "sub3",
		Database:       "database1",
		Collection:     "orders",
	})

	total, exact, expr, gws := m.Stats()
	assert.Equal(t, 3, total)
	assert.Equal(t, 3, exact) // 2 doc-level + 1 collection-level (stored as exact with empty key)
	assert.Equal(t, 0, expr)
	assert.Equal(t, 2, gws)
}

func TestGatewaySubscriptions(t *testing.T) {
	gs := NewGatewaySubscriptions()

	sub1 := &Subscriber{ID: "sub1", GatewayID: "gw1"}
	sub2 := &Subscriber{ID: "sub2", GatewayID: "gw1"}

	gs.Add(sub1)
	gs.Add(sub2)
	assert.Equal(t, 2, gs.Len())

	got, ok := gs.Get("sub1")
	require.True(t, ok)
	assert.Equal(t, sub1, got)

	gs.Remove("sub1")
	assert.Equal(t, 1, gs.Len())

	_, ok = gs.Get("sub1")
	assert.False(t, ok)

	list := gs.List()
	assert.Len(t, list, 1)
	assert.Equal(t, "sub2", list[0].ID)
}

func TestManager_WithCELCompiler(t *testing.T) {
	mockCEL := &mockCELCompiler{}
	m := New(WithCELCompiler(mockCEL))
	require.NotNil(t, m)
	assert.Equal(t, mockCEL, m.celCompiler)
}

type mockCELCompiler struct {
	err error
}

func (m *mockCELCompiler) Compile(expr string) (*ExpressionSubscriber, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &ExpressionSubscriber{Program: nil}, nil
}

func (m *mockCELCompiler) CompileProtoFilters(filters []*pb.Filter) (*ExpressionSubscriber, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &ExpressionSubscriber{Program: nil}, nil
}

// programCELCompiler returns a CEL compiler that produces a specific program.
type programCELCompiler struct {
	program cel.Program
}

func (p *programCELCompiler) Compile(expr string) (*ExpressionSubscriber, error) {
	return &ExpressionSubscriber{Program: p.program}, nil
}

func (p *programCELCompiler) CompileProtoFilters(filters []*pb.Filter) (*ExpressionSubscriber, error) {
	return &ExpressionSubscriber{Program: p.program}, nil
}

// createTestProgram creates a CEL program that returns the specified value.
func createTestProgram(returnValue interface{}) cel.Program {
	env, _ := cel.NewEnv()

	var expr string
	switch v := returnValue.(type) {
	case bool:
		if v {
			expr = "true"
		} else {
			expr = "false"
		}
	case string:
		expr = `"` + v + `"` // Returns a string, not bool
	case error:
		expr = "1/0" // Will cause eval error
	default:
		expr = "true"
	}

	ast, _ := env.Compile(expr)
	prg, _ := env.Program(ast)
	return prg
}

func TestManager_Subscribe_ExpressionMatch_NoCEL(t *testing.T) {
	m := New() // No CEL compiler

	// Subscribe with filter that is NOT an exact match
	resp, err := m.Subscribe("gw1", &pb.SubscribeRequest{
		SubscriptionId: "sub1",
		Database:       "database1",
		Collection:     "users",
		Filters: []*pb.Filter{{
			Field: "status",
			Op:    "==",
			Value: &pb.Value{Kind: &pb.Value_StringValue{StringValue: "active"}},
		}},
	})

	require.NoError(t, err)
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Error, "CEL compiler not configured")
}

func TestManager_Subscribe_ExpressionMatch_CompileError(t *testing.T) {
	mockCEL := &mockCELCompiler{err: assert.AnError}
	m := New(WithCELCompiler(mockCEL))

	resp, err := m.Subscribe("gw1", &pb.SubscribeRequest{
		SubscriptionId: "sub1",
		Database:       "database1",
		Collection:     "users",
		Filters: []*pb.Filter{{
			Field: "status",
			Op:    "==",
			Value: &pb.Value{Kind: &pb.Value_StringValue{StringValue: "active"}},
		}},
	})

	require.NoError(t, err)
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Error, "failed to compile filters")
}

func TestManager_Subscribe_ExpressionMatch_Success(t *testing.T) {
	mockCEL := &mockCELCompiler{}
	m := New(WithCELCompiler(mockCEL))

	resp, err := m.Subscribe("gw1", &pb.SubscribeRequest{
		SubscriptionId: "sub1",
		Database:       "database1",
		Collection:     "users",
		Filters: []*pb.Filter{{
			Field: "status",
			Op:    "==",
			Value: &pb.Value{Kind: &pb.Value_StringValue{StringValue: "active"}},
		}},
	})

	require.NoError(t, err)
	assert.True(t, resp.Success)
}

func TestManager_Unsubscribe_ExpressionMatch(t *testing.T) {
	mockCEL := &mockCELCompiler{}
	m := New(WithCELCompiler(mockCEL))

	// Subscribe with expression filter
	m.Subscribe("gw1", &pb.SubscribeRequest{
		SubscriptionId: "sub1",
		Database:       "database1",
		Collection:     "users",
		Filters: []*pb.Filter{{
			Field: "status",
			Op:    "==",
			Value: &pb.Value{Kind: &pb.Value_StringValue{StringValue: "active"}},
		}},
	})

	// Unsubscribe
	err := m.Unsubscribe("sub1")
	require.NoError(t, err)

	// Verify removed
	_, ok := m.GetSubscription("sub1")
	assert.False(t, ok)
}

func TestManager_UnregisterGateway_Empty(t *testing.T) {
	m := New()

	// Should not panic for non-existent gateway
	m.UnregisterGateway("nonexistent")
}

func TestSubscriber_GetDocumentID_NonString(t *testing.T) {
	sub := &Subscriber{
		Filters: []*pb.Filter{{
			Field: "_id",
			Op:    "==",
			Value: &pb.Value{Kind: &pb.Value_IntValue{IntValue: 123}},
		}},
	}

	// Should return empty string for non-string ID
	assert.Empty(t, sub.GetDocumentID())
}

func TestSubscriber_GetDocumentID_NilValue(t *testing.T) {
	sub := &Subscriber{
		Filters: []*pb.Filter{{
			Field: "_id",
			Op:    "==",
			Value: nil,
		}},
	}

	assert.Empty(t, sub.GetDocumentID())
}

// --- Tests for evaluateCEL and matchExpressions error handling ---

func TestManager_EvaluateCEL_NilProgram(t *testing.T) {
	// Use mock CEL that returns nil program
	mockCEL := &mockCELCompiler{}
	m := New(WithCELCompiler(mockCEL))

	resp, err := m.Subscribe("gw1", &pb.SubscribeRequest{
		SubscriptionId: "sub1",
		Database:       "database1",
		Collection:     "users",
		Filters: []*pb.Filter{{
			Field: "status",
			Op:    "==",
			Value: &pb.Value{Kind: &pb.Value_StringValue{StringValue: "active"}},
		}},
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)

	// Match should not include this subscription (nil program returns false)
	matches := m.Match("database1", "users", "doc1", map[string]interface{}{
		"status": "active",
	})
	assert.Empty(t, matches["gw1"])
}

func TestManager_EvaluateCEL_ReturnsTrue(t *testing.T) {
	// CEL program that always returns true
	celCompiler := &programCELCompiler{program: createTestProgram(true)}
	m := New(WithCELCompiler(celCompiler))

	resp, err := m.Subscribe("gw1", &pb.SubscribeRequest{
		SubscriptionId: "sub1",
		Database:       "database1",
		Collection:     "users",
		Filters: []*pb.Filter{{
			Field: "status",
			Op:    "==",
			Value: &pb.Value{Kind: &pb.Value_StringValue{StringValue: "active"}},
		}},
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)

	// Match should include this subscription
	matches := m.Match("database1", "users", "doc1", map[string]interface{}{})
	assert.Contains(t, matches["gw1"], "sub1")
}

func TestManager_EvaluateCEL_ReturnsFalse(t *testing.T) {
	// CEL program that always returns false
	celCompiler := &programCELCompiler{program: createTestProgram(false)}
	m := New(WithCELCompiler(celCompiler))

	resp, err := m.Subscribe("gw1", &pb.SubscribeRequest{
		SubscriptionId: "sub1",
		Database:       "database1",
		Collection:     "users",
		Filters: []*pb.Filter{{
			Field: "status",
			Op:    "==",
			Value: &pb.Value{Kind: &pb.Value_StringValue{StringValue: "active"}},
		}},
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)

	// Match should NOT include this subscription
	matches := m.Match("database1", "users", "doc1", map[string]interface{}{})
	assert.Empty(t, matches["gw1"])
}

func TestManager_EvaluateCEL_ReturnsNonBool(t *testing.T) {
	// CEL program that returns a string instead of bool
	celCompiler := &programCELCompiler{program: createTestProgram("not-a-bool")}
	m := New(WithCELCompiler(celCompiler))

	resp, err := m.Subscribe("gw1", &pb.SubscribeRequest{
		SubscriptionId: "sub1",
		Database:       "database1",
		Collection:     "users",
		Filters: []*pb.Filter{{
			Field: "status",
			Op:    "==",
			Value: &pb.Value{Kind: &pb.Value_StringValue{StringValue: "active"}},
		}},
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)

	// Match should NOT include this subscription (non-bool result)
	matches := m.Match("database1", "users", "doc1", map[string]interface{}{})
	assert.Empty(t, matches["gw1"])
}

func TestManager_EvaluateCEL_EvalError(t *testing.T) {
	// CEL program that causes an eval error (division by zero)
	celCompiler := &programCELCompiler{program: createTestProgram(errors.New("eval error"))}
	m := New(WithCELCompiler(celCompiler))

	resp, err := m.Subscribe("gw1", &pb.SubscribeRequest{
		SubscriptionId: "sub1",
		Database:       "database1",
		Collection:     "users",
		Filters: []*pb.Filter{{
			Field: "status",
			Op:    "==",
			Value: &pb.Value{Kind: &pb.Value_StringValue{StringValue: "active"}},
		}},
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)

	// Match should NOT include this subscription (eval error)
	matches := m.Match("database1", "users", "doc1", map[string]interface{}{})
	assert.Empty(t, matches["gw1"])
}

func TestManager_MatchExpressions_NoMatchingDatabase(t *testing.T) {
	celCompiler := &programCELCompiler{program: createTestProgram(true)}
	m := New(WithCELCompiler(celCompiler))

	resp, err := m.Subscribe("gw1", &pb.SubscribeRequest{
		SubscriptionId: "sub1",
		Database:       "database1",
		Collection:     "users",
		Filters: []*pb.Filter{{
			Field: "status",
			Op:    "==",
			Value: &pb.Value{Kind: &pb.Value_StringValue{StringValue: "active"}},
		}},
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)

	// Different database - matchExpressions should return early
	matches := m.Match("database2", "users", "doc1", map[string]interface{}{})
	assert.Empty(t, matches)
}

func TestManager_MatchExpressions_NoMatchingCollection(t *testing.T) {
	celCompiler := &programCELCompiler{program: createTestProgram(true)}
	m := New(WithCELCompiler(celCompiler))

	resp, err := m.Subscribe("gw1", &pb.SubscribeRequest{
		SubscriptionId: "sub1",
		Database:       "database1",
		Collection:     "users",
		Filters: []*pb.Filter{{
			Field: "status",
			Op:    "==",
			Value: &pb.Value{Kind: &pb.Value_StringValue{StringValue: "active"}},
		}},
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)

	// Different collection - matchExpressions should return early
	matches := m.Match("database1", "orders", "doc1", map[string]interface{}{})
	assert.Empty(t, matches)
}
