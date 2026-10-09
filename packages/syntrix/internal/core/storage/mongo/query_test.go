package mongo

import (
	"context"
	"testing"

	"github.com/codetreker/syntrix/internal/core/storage/types"
	"github.com/codetreker/syntrix/pkg/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMongoBackend_Query(t *testing.T) {
	backend := setupTestBackend(t)
	defer backend.Close(context.Background())

	ctx := context.Background()
	database := "default"

	// Seed data
	users := []map[string]interface{}{
		{"name": "Alice", "age": 30, "active": true},
		{"name": "Bob", "age": 25, "active": true},
		{"name": "Charlie", "age": 35, "active": false},
	}

	for _, u := range users {
		doc := types.NewStoredDoc(database, "users", u["name"].(string), u)
		err := backend.Create(ctx, database, doc)
		require.NoError(t, err)
	}

	// Test 1: Simple Filter
	q1 := model.Query{
		Collection: "users",
		Filters: []model.Filter{
			{Field: "age", Op: model.OpGt, Value: 28},
		},
	}
	docs, err := backend.Query(ctx, database, q1)
	require.NoError(t, err)
	assert.Len(t, docs, 2) // Alice (30), Charlie (35)

	// Test 2: Multiple Filters
	q2 := model.Query{
		Collection: "users",
		Filters: []model.Filter{
			{Field: "age", Op: model.OpGt, Value: 20},
			{Field: "active", Op: model.OpEq, Value: true},
		},
	}
	docs, err = backend.Query(ctx, database, q2)
	require.NoError(t, err)
	assert.Len(t, docs, 2) // Alice, Bob

	// Test 3: Sorting
	q3 := model.Query{
		Collection: "users",
		OrderBy: []model.Order{
			{Field: "age", Direction: "desc"},
		},
	}
	docs, err = backend.Query(ctx, database, q3)
	require.NoError(t, err)
	assert.Len(t, docs, 3)
	assert.Equal(t, "Charlie", docs[0].Data["name"])
	assert.Equal(t, "Alice", docs[1].Data["name"])
	assert.Equal(t, "Bob", docs[2].Data["name"])

	// Test 4: Limit
	q4 := model.Query{
		Collection: "users",
		OrderBy: []model.Order{
			{Field: "age", Direction: "asc"},
		},
		Limit: 2,
	}
	docs, err = backend.Query(ctx, database, q4)
	require.NoError(t, err)
	assert.Len(t, docs, 2)
	assert.Equal(t, "Bob", docs[0].Data["name"])
	assert.Equal(t, "Alice", docs[1].Data["name"])

	// Test 5: Field mapping (version and custom) with multi-filter and IN
	docs, err = backend.Query(ctx, database, model.Query{
		Collection: "users",
		Filters: []model.Filter{
			{Field: "version", Op: model.OpGte, Value: int64(1)},
			{Field: "active", Op: model.OpEq, Value: true},
			{Field: "name", Op: model.OpIn, Value: []string{"Alice", "Charlie"}},
		},
	})
	require.NoError(t, err)
	assert.Len(t, docs, 1)
	assert.Equal(t, "Alice", docs[0].Data["name"])
}
