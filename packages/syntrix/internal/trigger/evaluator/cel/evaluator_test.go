package cel

import (
	"context"
	"fmt"
	"testing"

	"github.com/codetreker/syntrix/internal/core/storage"
	"github.com/codetreker/syntrix/internal/puller/events"
	"github.com/codetreker/syntrix/internal/trigger/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCELEvaluator(t *testing.T) {
	evaluator, err := NewEvaluator()
	require.NoError(t, err)

	tests := []struct {
		name      string
		trigger   *types.Trigger
		event     events.SyntrixChangeEvent
		wantMatch bool
		wantErr   bool
	}{
		{
			name: "Simple match",
			trigger: &types.Trigger{
				Events:     []string{"create"},
				Collection: "users",
				Condition:  "event.document.age > 18",
			},
			event: events.SyntrixChangeEvent{
				Type: events.EventCreate,
				Document: &storage.StoredDoc{
					Collection: "users",
					Data:       map[string]interface{}{"age": 20},
				},
			},
			wantMatch: true,
			wantErr:   false,
		},
		{
			name: "Simple no match",
			trigger: &types.Trigger{
				Events:     []string{"create"},
				Collection: "users",
				Condition:  "event.document.age > 18",
			},
			event: events.SyntrixChangeEvent{
				Type: events.EventCreate,
				Document: &storage.StoredDoc{
					Collection: "users",
					Data:       map[string]interface{}{"age": 16},
				},
			},
			wantMatch: false,
			wantErr:   false,
		},
		{
			name: "Event type mismatch",
			trigger: &types.Trigger{
				Events:     []string{"delete"},
				Collection: "users",
				Condition:  "true",
			},
			event: events.SyntrixChangeEvent{
				Type: events.EventCreate,
				Document: &storage.StoredDoc{
					Collection: "users",
				},
			},
			wantMatch: false,
			wantErr:   false,
		},
		{
			name: "Collection mismatch",
			trigger: &types.Trigger{
				Events:     []string{"create"},
				Collection: "orders",
				Condition:  "true",
			},
			event: events.SyntrixChangeEvent{
				Type: events.EventCreate,
				Document: &storage.StoredDoc{
					Collection: "users",
				},
			},
			wantMatch: false,
			wantErr:   false,
		},
		{
			name: "Collection wildcard match",
			trigger: &types.Trigger{
				Events:     []string{"create"},
				Collection: "chats/*/members",
				Condition:  "true",
			},
			event: events.SyntrixChangeEvent{
				Type: events.EventCreate,
				Document: &storage.StoredDoc{
					Collection: "chats/123/members",
				},
			},
			wantMatch: true,
			wantErr:   false,
		},
		{
			name: "Collection wildcard no match",
			trigger: &types.Trigger{
				Events:     []string{"create"},
				Collection: "chats/*/members",
				Condition:  "true",
			},
			event: events.SyntrixChangeEvent{
				Type: events.EventCreate,
				Document: &storage.StoredDoc{
					Collection: "chats/123/messages",
				},
			},
			wantMatch: false,
			wantErr:   false,
		},
		{
			name: "Complex condition",
			trigger: &types.Trigger{
				Events:     []string{"update"},
				Collection: "users",
				Condition:  "event.document.role == 'admin' && event.document.active == true",
			},
			event: events.SyntrixChangeEvent{
				Type: events.EventUpdate,
				Document: &storage.StoredDoc{
					Collection: "users",
					Data: map[string]interface{}{
						"role":   "admin",
						"active": true,
					},
				},
			},
			wantMatch: true,
			wantErr:   false,
		},
		{
			name: "Invalid CEL",
			trigger: &types.Trigger{
				Events:     []string{"create"},
				Collection: "users",
				Condition:  "invalid syntax ???",
			},
			event: events.SyntrixChangeEvent{
				Type: events.EventCreate,
				Document: &storage.StoredDoc{
					Collection: "users",
				},
			},
			wantMatch: false,
			wantErr:   true,
		},
		{
			name: "Check Before State",
			trigger: &types.Trigger{
				Events:     []string{"update"},
				Collection: "users",
				Condition:  "event.before.status == 'pending' && event.document.status == 'active'",
			},
			event: events.SyntrixChangeEvent{
				Type: events.EventUpdate,
				Document: &storage.StoredDoc{
					Collection: "users",
					Data:       map[string]interface{}{"status": "active"},
				},
				Before: &storage.StoredDoc{
					Collection: "users",
					Data:       map[string]interface{}{"status": "pending"},
				},
			},
			wantMatch: true,
			wantErr:   false,
		},
		{
			name: "Check Timestamp",
			trigger: &types.Trigger{
				Events:     []string{"create"},
				Collection: "logs",
				Condition:  "event.timestamp > 0",
			},
			event: events.SyntrixChangeEvent{
				Type:      events.EventCreate,
				Timestamp: 1234567890,
				Document: &storage.StoredDoc{
					Collection: "logs",
				},
			},
			wantMatch: true,
			wantErr:   false,
		},
		{
			name: "Empty condition",
			trigger: &types.Trigger{
				Events:     []string{"create"},
				Collection: "users",
				Condition:  "",
			},
			event: events.SyntrixChangeEvent{
				Type: events.EventCreate,
				Document: &storage.StoredDoc{
					Collection: "users",
				},
			},
			wantMatch: true,
			wantErr:   false,
		},
		// "Invalid condition syntax" test case REMOVED - duplicate of "Invalid CEL" above
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			match, err := evaluator.Evaluate(context.Background(), tt.trigger, tt.event)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantMatch, match)
			}
		})
	}
}

func TestCELEvaluator_CacheEviction(t *testing.T) {
	// Test cache eviction by filling the cache to its limit
	// Note: MaxCacheSize is 1000, so we'll test the eviction logic differently
	evaluator, err := NewEvaluator()
	require.NoError(t, err)

	celEval := evaluator.(*celeEvaluator)

	// Directly populate the cache to near capacity
	// We'll manually add entries to test eviction behavior
	celEval.cacheMutex.Lock()
	for i := 0; i < MaxCacheSize-1; i++ {
		cond := fmt.Sprintf("event.document.field%d == %d", i, i)
		celEval.prgCache[cond] = nil // placeholder, not a real program
		celEval.cacheOrder = append(celEval.cacheOrder, cond)
	}
	celEval.cacheMutex.Unlock()

	// Now add one more via Evaluate to trigger eviction check
	event := events.SyntrixChangeEvent{
		Type: events.EventCreate,
		Document: &storage.StoredDoc{
			Collection: "test",
			Data:       map[string]interface{}{"newfield": 1},
		},
	}

	trig := &types.Trigger{
		Events:     []string{"create"},
		Collection: "test",
		Condition:  "event.document.newfield == 1",
	}

	_, err = evaluator.Evaluate(context.Background(), trig, event)
	assert.NoError(t, err)

	// Cache should be at max capacity
	assert.Equal(t, MaxCacheSize, len(celEval.prgCache))

	// Add another one to trigger eviction
	trig2 := &types.Trigger{
		Events:     []string{"create"},
		Collection: "test",
		Condition:  "event.document.newfield == 2",
	}
	_, err = evaluator.Evaluate(context.Background(), trig2, event)
	assert.NoError(t, err)

	// Cache should still be at max capacity after eviction
	assert.Equal(t, MaxCacheSize, len(celEval.prgCache))

	// The oldest entry should be evicted
	_, exists := celEval.prgCache["event.document.field0 == 0"]
	assert.False(t, exists, "Oldest condition should be evicted")
}

func TestCELEvaluator_DeleteEventWithBeforeOnly(t *testing.T) {
	evaluator, err := NewEvaluator()
	require.NoError(t, err)

	trig := &types.Trigger{
		Events:     []string{"delete"},
		Collection: "users",
		Condition:  "true",
	}

	event := events.SyntrixChangeEvent{
		Type:     events.EventDelete,
		Document: nil,
		Before: &storage.StoredDoc{
			Collection: "users",
			Data:       map[string]interface{}{"name": "deleted-user"},
		},
	}

	match, err := evaluator.Evaluate(context.Background(), trig, event)
	assert.NoError(t, err)
	assert.True(t, match)
}

func TestCELEvaluator_CollectionMismatchWithBeforeOnly(t *testing.T) {
	evaluator, err := NewEvaluator()
	require.NoError(t, err)

	trig := &types.Trigger{
		Events:     []string{"delete"},
		Collection: "orders",
		Condition:  "true",
	}

	event := events.SyntrixChangeEvent{
		Type:     events.EventDelete,
		Document: nil,
		Before: &storage.StoredDoc{
			Collection: "users",
			Data:       map[string]interface{}{"name": "deleted-user"},
		},
	}

	match, err := evaluator.Evaluate(context.Background(), trig, event)
	assert.NoError(t, err)
	assert.False(t, match)
}

func TestCELEvaluator_CELNonBoolReturn(t *testing.T) {
	evaluator, err := NewEvaluator()
	require.NoError(t, err)

	// CEL that returns non-boolean
	trig := &types.Trigger{
		Events:     []string{"create"},
		Collection: "users",
		Condition:  "event.document.name", // Returns string, not bool
	}

	event := events.SyntrixChangeEvent{
		Type: events.EventCreate,
		Document: &storage.StoredDoc{
			Collection: "users",
			Data:       map[string]interface{}{"name": "test"},
		},
	}

	_, err = evaluator.Evaluate(context.Background(), trig, event)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "must return boolean")
}

func TestCELEvaluator_CacheHit(t *testing.T) {
	evaluator, err := NewEvaluator()
	require.NoError(t, err)

	trig := &types.Trigger{
		Events:     []string{"create"},
		Collection: "users",
		Condition:  "event.document.age > 18",
	}

	event := events.SyntrixChangeEvent{
		Type: events.EventCreate,
		Document: &storage.StoredDoc{
			Collection: "users",
			Data:       map[string]interface{}{"age": 25},
		},
	}

	// First call - cache miss
	match1, err := evaluator.Evaluate(context.Background(), trig, event)
	assert.NoError(t, err)
	assert.True(t, match1)

	// Second call - cache hit
	match2, err := evaluator.Evaluate(context.Background(), trig, event)
	assert.NoError(t, err)
	assert.True(t, match2)
}

func TestCELEvaluator_DatabaseFilter(t *testing.T) {
	evaluator, err := NewEvaluator()
	require.NoError(t, err)

	tests := []struct {
		name      string
		trigger   *types.Trigger
		event     events.SyntrixChangeEvent
		wantMatch bool
	}{
		{
			name: "database matches from Document",
			trigger: &types.Trigger{
				Database:   "mydb",
				Events:     []string{"create"},
				Collection: "users",
				Condition:  "",
			},
			event: events.SyntrixChangeEvent{
				Type: events.EventCreate,
				Document: &storage.StoredDoc{
					Database:   "mydb",
					Collection: "users",
				},
			},
			wantMatch: true,
		},
		{
			name: "database mismatch from Document",
			trigger: &types.Trigger{
				Database:   "mydb",
				Events:     []string{"create"},
				Collection: "users",
				Condition:  "",
			},
			event: events.SyntrixChangeEvent{
				Type: events.EventCreate,
				Document: &storage.StoredDoc{
					Database:   "otherdb",
					Collection: "users",
				},
			},
			wantMatch: false,
		},
		{
			name: "database matches from Before when Document is nil",
			trigger: &types.Trigger{
				Database:   "mydb",
				Events:     []string{"delete"},
				Collection: "users",
				Condition:  "",
			},
			event: events.SyntrixChangeEvent{
				Type:     events.EventDelete,
				Document: nil,
				Before: &storage.StoredDoc{
					Database:   "mydb",
					Collection: "users",
				},
			},
			wantMatch: true,
		},
		{
			name: "database mismatch from Before when Document is nil",
			trigger: &types.Trigger{
				Database:   "mydb",
				Events:     []string{"delete"},
				Collection: "users",
				Condition:  "",
			},
			event: events.SyntrixChangeEvent{
				Type:     events.EventDelete,
				Document: nil,
				Before: &storage.StoredDoc{
					Database:   "otherdb",
					Collection: "users",
				},
			},
			wantMatch: false,
		},
		{
			name: "wildcard database matches any",
			trigger: &types.Trigger{
				Database:   "*",
				Events:     []string{"create"},
				Collection: "users",
				Condition:  "",
			},
			event: events.SyntrixChangeEvent{
				Type: events.EventCreate,
				Document: &storage.StoredDoc{
					Database:   "anydb",
					Collection: "users",
				},
			},
			wantMatch: true,
		},
		{
			name: "empty database matches any",
			trigger: &types.Trigger{
				Database:   "",
				Events:     []string{"create"},
				Collection: "users",
				Condition:  "",
			},
			event: events.SyntrixChangeEvent{
				Type: events.EventCreate,
				Document: &storage.StoredDoc{
					Database:   "anydb",
					Collection: "users",
				},
			},
			wantMatch: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			match, err := evaluator.Evaluate(context.Background(), tt.trigger, tt.event)
			assert.NoError(t, err)
			assert.Equal(t, tt.wantMatch, match)
		})
	}
}
