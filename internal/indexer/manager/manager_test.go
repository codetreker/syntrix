package manager

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/syntrixbase/syntrix/internal/indexer/encoding"
	"github.com/syntrixbase/syntrix/internal/indexer/mem_store"
	"github.com/syntrixbase/syntrix/internal/indexer/store"
	"github.com/syntrixbase/syntrix/internal/indexer/template"
)

const testTemplatesYAML = `
templates:
  - name: chats_by_timestamp
    collectionPattern: users/{uid}/chats
    fields:
      - { field: timestamp, order: desc }

  - name: chats_by_name_age
    collectionPattern: users/{uid}/chats
    fields:
      - { field: name, order: asc }
      - { field: age, order: desc }

  - name: messages_by_sender
    collectionPattern: rooms/{rid}/messages
    fields:
      - { field: senderId, order: asc }
      - { field: timestamp, order: desc }

  - name: specific_alice_chats
    collectionPattern: users/alice/chats
    fields:
      - { field: timestamp, order: desc }
`

func TestNew(t *testing.T) {
	st := mem_store.New()
	m := New(st)
	assert.NotNil(t, m)
	dbs, _ := m.Store().ListDatabases()
	assert.Empty(t, dbs)
	assert.Empty(t, m.Templates())
}

func TestManager_LoadTemplatesFromBytes(t *testing.T) {
	st := mem_store.New()
	m := New(st)

	err := m.LoadTemplatesFromBytes([]byte(testTemplatesYAML))
	require.NoError(t, err)

	templates := m.Templates()
	assert.Len(t, templates, 4)
}

func TestManager_LoadTemplatesFromBytes_Invalid(t *testing.T) {
	st := mem_store.New()
	m := New(st)

	err := m.LoadTemplatesFromBytes([]byte("invalid: yaml: ["))
	assert.ErrorIs(t, err, ErrTemplateLoadFailed)
}

func TestManager_DatabaseCreation(t *testing.T) {
	st := mem_store.New()
	m := New(st)

	// Initially no databases
	dbs, _ := m.Store().ListDatabases()
	assert.Empty(t, dbs)

	// Upsert creates database implicitly
	err := m.Store().Upsert("myapp", "test/*", "tmpl1", "doc1", []byte{0x01}, "")
	require.NoError(t, err)

	// Database now exists
	dbs, _ = m.Store().ListDatabases()
	assert.Len(t, dbs, 1)
	assert.Contains(t, dbs, "myapp")

	// Second database
	err = m.Store().Upsert("other", "test/*", "tmpl1", "doc1", []byte{0x01}, "")
	require.NoError(t, err)

	dbs, _ = m.Store().ListDatabases()
	assert.Len(t, dbs, 2)
	assert.Contains(t, dbs, "myapp")
	assert.Contains(t, dbs, "other")
}

func TestManager_DeleteIndex(t *testing.T) {
	st := mem_store.New()
	m := New(st)

	// Create indexes in two databases
	err := m.Store().Upsert("myapp", "test/*", "tmpl1", "doc1", []byte{0x01}, "")
	require.NoError(t, err)
	err = m.Store().Upsert("other", "test/*", "tmpl1", "doc1", []byte{0x01}, "")
	require.NoError(t, err)
	dbs, _ := m.Store().ListDatabases()
	assert.Len(t, dbs, 2)

	// Delete index from myapp
	err = m.Store().DeleteIndex("myapp", "test/*", "tmpl1")
	require.NoError(t, err)

	// Index should be empty (database may still exist in list)
	indexes, _ := m.Store().ListIndexes("myapp")
	assert.Empty(t, indexes)

	// Deleting again is a no-op
	err = m.Store().DeleteIndex("myapp", "test/*", "tmpl1")
	require.NoError(t, err)
}

func TestManager_MatchTemplatesForCollection(t *testing.T) {
	st := mem_store.New()
	m := New(st)
	require.NoError(t, m.LoadTemplatesFromBytes([]byte(testTemplatesYAML)))

	t.Run("matches generic pattern", func(t *testing.T) {
		results := m.MatchTemplatesForCollection("users/bob/chats")
		// Should match: chats_by_timestamp, chats_by_name_age
		assert.Len(t, results, 2)
	})

	t.Run("matches specific over generic", func(t *testing.T) {
		results := m.MatchTemplatesForCollection("users/alice/chats")
		// Should match: specific_alice_chats (priority), chats_by_timestamp, chats_by_name_age
		assert.Len(t, results, 3)
		// First should be the specific one
		assert.Equal(t, "specific_alice_chats", results[0].Template.Name)
	})

	t.Run("no match", func(t *testing.T) {
		results := m.MatchTemplatesForCollection("other/path")
		assert.Len(t, results, 0)
	})
}

func TestManager_SelectBestTemplate(t *testing.T) {
	st := mem_store.New()
	m := New(st)
	require.NoError(t, m.LoadTemplatesFromBytes([]byte(testTemplatesYAML)))

	t.Run("select by orderBy", func(t *testing.T) {
		plan := Plan{
			Collection: "users/bob/chats",
			OrderBy:    []OrderField{{Field: "timestamp", Direction: encoding.Desc}},
		}
		tmpl, err := m.SelectBestTemplate(plan)
		require.NoError(t, err)
		assert.Equal(t, "chats_by_timestamp", tmpl.Name)
	})

	t.Run("select by multiple orderBy fields", func(t *testing.T) {
		plan := Plan{
			Collection: "users/bob/chats",
			OrderBy: []OrderField{
				{Field: "name", Direction: encoding.Asc},
				{Field: "age", Direction: encoding.Desc},
			},
		}
		tmpl, err := m.SelectBestTemplate(plan)
		require.NoError(t, err)
		assert.Equal(t, "chats_by_name_age", tmpl.Name)
	})

	t.Run("select specific over generic", func(t *testing.T) {
		plan := Plan{
			Collection: "users/alice/chats",
			OrderBy:    []OrderField{{Field: "timestamp", Direction: encoding.Desc}},
		}
		tmpl, err := m.SelectBestTemplate(plan)
		require.NoError(t, err)
		assert.Equal(t, "specific_alice_chats", tmpl.Name)
	})

	t.Run("no matching collection", func(t *testing.T) {
		plan := Plan{
			Collection: "other/path",
			OrderBy:    []OrderField{{Field: "timestamp", Direction: encoding.Desc}},
		}
		_, err := m.SelectBestTemplate(plan)
		assert.ErrorIs(t, err, ErrNoMatchingIndex)
	})

	t.Run("no compatible template - wrong orderBy", func(t *testing.T) {
		plan := Plan{
			Collection: "users/bob/chats",
			OrderBy:    []OrderField{{Field: "nonexistent", Direction: encoding.Asc}},
		}
		_, err := m.SelectBestTemplate(plan)
		assert.ErrorIs(t, err, ErrNoMatchingIndex)
	})

	t.Run("no compatible template - wrong direction", func(t *testing.T) {
		plan := Plan{
			Collection: "users/bob/chats",
			OrderBy:    []OrderField{{Field: "timestamp", Direction: encoding.Asc}},
		}
		_, err := m.SelectBestTemplate(plan)
		assert.ErrorIs(t, err, ErrNoMatchingIndex)
	})
}

func TestManager_SelectBestTemplate_WithFilters(t *testing.T) {
	st := mem_store.New()
	m := New(st)
	require.NoError(t, m.LoadTemplatesFromBytes([]byte(testTemplatesYAML)))

	t.Run("equality filter allows skipping prefix", func(t *testing.T) {
		plan := Plan{
			Collection: "users/bob/chats",
			Filters:    []Filter{{Field: "name", Op: FilterEq, Value: "test"}},
			OrderBy:    []OrderField{{Field: "age", Direction: encoding.Desc}},
		}
		tmpl, err := m.SelectBestTemplate(plan)
		require.NoError(t, err)
		assert.Equal(t, "chats_by_name_age", tmpl.Name)
	})

	t.Run("range filter on next field", func(t *testing.T) {
		plan := Plan{
			Collection: "rooms/room1/messages",
			Filters: []Filter{
				{Field: "senderId", Op: FilterEq, Value: "user1"},
				{Field: "timestamp", Op: FilterGt, Value: 1000},
			},
			OrderBy: []OrderField{},
		}
		tmpl, err := m.SelectBestTemplate(plan)
		require.NoError(t, err)
		assert.Equal(t, "messages_by_sender", tmpl.Name)
	})

	t.Run("multiple range filters not supported", func(t *testing.T) {
		plan := Plan{
			Collection: "rooms/room1/messages",
			Filters: []Filter{
				{Field: "timestamp", Op: FilterGt, Value: 1000},
				{Field: "timestamp", Op: FilterLt, Value: 2000},
			},
			OrderBy: []OrderField{},
		}
		_, err := m.SelectBestTemplate(plan)
		assert.ErrorIs(t, err, ErrNoMatchingIndex)
	})
}

func TestManager_SelectBestTemplate_TieBreaker(t *testing.T) {
	// Create templates with same score
	yaml := `
templates:
  - name: z_template
    collectionPattern: test/{id}/items
    fields:
      - { field: name, order: asc }
  - name: a_template
    collectionPattern: test/{id}/items
    fields:
      - { field: name, order: asc }
    includeDeleted: true
`
	st := mem_store.New()
	m := New(st)
	require.NoError(t, m.LoadTemplatesFromBytes([]byte(yaml)))

	plan := Plan{
		Collection: "test/123/items",
		OrderBy:    []OrderField{{Field: "name", Direction: encoding.Asc}},
	}
	tmpl, err := m.SelectBestTemplate(plan)
	require.NoError(t, err)
	// Should pick lexicographically smallest name
	assert.Equal(t, "a_template", tmpl.Name)
}

func TestManager_Search(t *testing.T) {
	st := mem_store.New()
	m := New(st)
	require.NoError(t, m.LoadTemplatesFromBytes([]byte(testTemplatesYAML)))

	// Create index and add data via store
	err := st.Upsert("mydb", "users/*/chats", "chats_by_timestamp", "doc1", []byte{0x01, 0x00, 0x10}, "")
	require.NoError(t, err)
	err = st.Upsert("mydb", "users/*/chats", "chats_by_timestamp", "doc2", []byte{0x01, 0x00, 0x20}, "")
	require.NoError(t, err)
	err = st.Upsert("mydb", "users/*/chats", "chats_by_timestamp", "doc3", []byte{0x01, 0x00, 0x30}, "")
	require.NoError(t, err)

	t.Run("basic search", func(t *testing.T) {
		plan := Plan{
			Collection: "users/bob/chats",
			OrderBy:    []OrderField{{Field: "timestamp", Direction: encoding.Desc}},
			Limit:      10,
		}
		results, err := m.Search(context.Background(), "mydb", plan)
		require.NoError(t, err)
		assert.Len(t, results, 3)
		assert.Equal(t, "doc1", results[0].ID)
		assert.Equal(t, "doc2", results[1].ID)
		assert.Equal(t, "doc3", results[2].ID)
	})

	t.Run("search with limit", func(t *testing.T) {
		plan := Plan{
			Collection: "users/bob/chats",
			OrderBy:    []OrderField{{Field: "timestamp", Direction: encoding.Desc}},
			Limit:      2,
		}
		results, err := m.Search(context.Background(), "mydb", plan)
		require.NoError(t, err)
		assert.Len(t, results, 2)
	})

	t.Run("search with cursor", func(t *testing.T) {
		cursor := encoding.EncodeBase64([]byte{0x01, 0x00, 0x10})
		plan := Plan{
			Collection: "users/bob/chats",
			OrderBy:    []OrderField{{Field: "timestamp", Direction: encoding.Desc}},
			StartAfter: cursor,
			Limit:      10,
		}
		results, err := m.Search(context.Background(), "mydb", plan)
		require.NoError(t, err)
		assert.Len(t, results, 2)
		assert.Equal(t, "doc2", results[0].ID)
		assert.Equal(t, "doc3", results[1].ID)
	})

	t.Run("search empty collection returns empty results", func(t *testing.T) {
		plan := Plan{
			Collection: "users/bob/chats",
			OrderBy:    []OrderField{{Field: "timestamp", Direction: encoding.Desc}},
			Limit:      10,
		}
		// Search in a database where index doesn't exist returns empty results
		results, err := m.Search(context.Background(), "otherdb", plan)
		require.NoError(t, err)
		assert.Empty(t, results)
	})

	t.Run("search no matching template", func(t *testing.T) {
		plan := Plan{
			Collection: "nonexistent/path",
			OrderBy:    []OrderField{{Field: "timestamp", Direction: encoding.Desc}},
			Limit:      10,
		}
		_, err := m.Search(context.Background(), "mydb", plan)
		assert.ErrorIs(t, err, ErrNoMatchingIndex)
	})

	t.Run("search with invalid cursor", func(t *testing.T) {
		plan := Plan{
			Collection: "users/bob/chats",
			OrderBy:    []OrderField{{Field: "timestamp", Direction: encoding.Desc}},
			StartAfter: "invalid-base64!!!",
			Limit:      10,
		}
		_, err := m.Search(context.Background(), "mydb", plan)
		assert.Error(t, err)
	})

	t.Run("search with empty collection", func(t *testing.T) {
		plan := Plan{
			Collection: "",
			OrderBy:    []OrderField{{Field: "timestamp", Direction: encoding.Desc}},
		}
		_, err := m.Search(context.Background(), "mydb", plan)
		assert.ErrorIs(t, err, ErrInvalidPlan)
	})
}

func TestManager_IndexOperations(t *testing.T) {
	st := mem_store.New()
	m := New(st)

	// Index doesn't exist - ListIndexes returns empty
	indexes, _ := m.Store().ListIndexes("mydb")
	assert.Empty(t, indexes)

	// Create index via Upsert
	err := st.Upsert("mydb", "users/*/chats", "ts:desc", "doc1", []byte{0x01}, "")
	require.NoError(t, err)

	// Now it exists
	indexes, _ = m.Store().ListIndexes("mydb")
	assert.Len(t, indexes, 1)
	assert.Equal(t, "users/*/chats", indexes[0].Pattern)
	assert.Equal(t, "ts:desc", indexes[0].TemplateID)
}

func TestManager_Stats(t *testing.T) {
	st := mem_store.New()
	m := New(st)

	stats := m.Stats()
	assert.Equal(t, 0, stats.TemplateCount)

	require.NoError(t, m.LoadTemplatesFromBytes([]byte(testTemplatesYAML)))

	stats = m.Stats()
	assert.Equal(t, 4, stats.TemplateCount)
}

func TestManager_ComputeQueryScore(t *testing.T) {
	st := mem_store.New()
	m := New(st)
	require.NoError(t, m.LoadTemplatesFromBytes([]byte(testTemplatesYAML)))

	t.Run("orderBy prefix match scores positive", func(t *testing.T) {
		plan := Plan{
			Collection: "users/bob/chats",
			OrderBy:    []OrderField{{Field: "name", Direction: encoding.Asc}},
		}
		templates := m.Templates()
		// Find chats_by_name_age
		var tmpl *template.Template
		for i := range templates {
			if templates[i].Name == "chats_by_name_age" {
				tmpl = &templates[i]
				break
			}
		}
		require.NotNil(t, tmpl)
		score := m.computeQueryScore(plan, tmpl)
		assert.Greater(t, score, 0)
	})

	t.Run("orderBy exceeds index scores 0", func(t *testing.T) {
		plan := Plan{
			Collection: "users/bob/chats",
			OrderBy: []OrderField{
				{Field: "name", Direction: encoding.Asc},
				{Field: "age", Direction: encoding.Desc},
				{Field: "extra", Direction: encoding.Asc},
			},
		}
		templates := m.Templates()
		var tmpl *template.Template
		for i := range templates {
			if templates[i].Name == "chats_by_name_age" {
				tmpl = &templates[i]
				break
			}
		}
		require.NotNil(t, tmpl)
		score := m.computeQueryScore(plan, tmpl)
		assert.Equal(t, 0, score)
	})
}

func TestManager_LoadTemplatesFromDir(t *testing.T) {
	t.Run("valid template directory", func(t *testing.T) {
		// Create temp directory
		tmpDir, err := os.MkdirTemp("", "templates-*")
		require.NoError(t, err)
		defer os.RemoveAll(tmpDir)

		templateYAML := `
database: default
templates:
  - name: test_template
    collectionPattern: users/{uid}/docs
    fields:
      - { field: timestamp, order: desc }
`
		err = os.WriteFile(tmpDir+"/default.yml", []byte(templateYAML), 0644)
		require.NoError(t, err)

		st := mem_store.New()
		m := New(st)
		err = m.LoadTemplatesFromDir(tmpDir)
		require.NoError(t, err)

		assert.Len(t, m.Templates(), 1)
		assert.Equal(t, "test_template", m.Templates()[0].Name)
	})

	t.Run("nonexistent directory", func(t *testing.T) {
		st := mem_store.New()
		m := New(st)
		err := m.LoadTemplatesFromDir("/nonexistent/path/templates")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrTemplateLoadFailed)
	})
}

func TestManager_DatabaseTemplates(t *testing.T) {
	t.Run("returns nil when no templates loaded", func(t *testing.T) {
		st := mem_store.New()
		m := New(st)
		assert.Nil(t, m.DatabaseTemplates())
	})

	t.Run("returns templates grouped by database", func(t *testing.T) {
		// Create temp directory with multiple databases
		tmpDir, err := os.MkdirTemp("", "templates-*")
		require.NoError(t, err)
		defer os.RemoveAll(tmpDir)

		db1YAML := `
database: db1
templates:
  - name: template1
    collectionPattern: users/{uid}/docs
    fields:
      - { field: createdAt, order: desc }
`
		db2YAML := `
database: db2
templates:
  - name: template2
    collectionPattern: orders/{oid}/items
    fields:
      - { field: price, order: asc }
`
		err = os.WriteFile(tmpDir+"/db1.yml", []byte(db1YAML), 0644)
		require.NoError(t, err)
		err = os.WriteFile(tmpDir+"/db2.yml", []byte(db2YAML), 0644)
		require.NoError(t, err)

		st := mem_store.New()
		m := New(st)
		err = m.LoadTemplatesFromDir(tmpDir)
		require.NoError(t, err)

		dbTemplates := m.DatabaseTemplates()
		assert.Len(t, dbTemplates, 2)
		assert.Contains(t, dbTemplates, "db1")
		assert.Contains(t, dbTemplates, "db2")
		assert.Len(t, dbTemplates["db1"], 1)
		assert.Len(t, dbTemplates["db2"], 1)
	})
}

func TestManager_TemplatesForDatabase(t *testing.T) {
	t.Run("returns nil for unknown database", func(t *testing.T) {
		st := mem_store.New()
		m := New(st)
		assert.Nil(t, m.TemplatesForDatabase("unknown"))
	})

	t.Run("returns templates for specific database", func(t *testing.T) {
		// Create temp directory
		tmpDir, err := os.MkdirTemp("", "templates-*")
		require.NoError(t, err)
		defer os.RemoveAll(tmpDir)

		yaml := `
database: mydb
templates:
  - name: tmpl1
    collectionPattern: users/{uid}/docs
    fields:
      - { field: createdAt, order: desc }
  - name: tmpl2
    collectionPattern: users/{uid}/messages
    fields:
      - { field: sentAt, order: asc }
`
		err = os.WriteFile(tmpDir+"/mydb.yml", []byte(yaml), 0644)
		require.NoError(t, err)

		st := mem_store.New()
		m := New(st)
		err = m.LoadTemplatesFromDir(tmpDir)
		require.NoError(t, err)

		templates := m.TemplatesForDatabase("mydb")
		assert.Len(t, templates, 2)

		// Unknown database returns nil
		assert.Nil(t, m.TemplatesForDatabase("other"))
	})
}

func TestManager_ListDatabases(t *testing.T) {
	st := mem_store.New()
	m := New(st)

	// Initially empty
	names, _ := m.Store().ListDatabases()
	assert.Empty(t, names)

	// Add databases via Upsert
	err := st.Upsert("db1", "test/*", "tmpl", "doc1", []byte{0x01}, "")
	require.NoError(t, err)
	err = st.Upsert("db2", "test/*", "tmpl", "doc1", []byte{0x01}, "")
	require.NoError(t, err)
	err = st.Upsert("db3", "test/*", "tmpl", "doc1", []byte{0x01}, "")
	require.NoError(t, err)

	names, _ = m.Store().ListDatabases()
	assert.Len(t, names, 3)
	assert.Contains(t, names, "db1")
	assert.Contains(t, names, "db2")
	assert.Contains(t, names, "db3")
}

func TestManager_Upsert_Concurrent(t *testing.T) {
	st := mem_store.New()
	m := New(st)

	// Run many goroutines concurrently trying to upsert to the same database
	const numGoroutines = 100
	done := make(chan struct{}, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			m.Store().Upsert("concurrent-db", "test/*", "tmpl", "doc"+string(rune('a'+idx)), []byte{byte(idx)}, "")
			done <- struct{}{}
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < numGoroutines; i++ {
		<-done
	}

	// Should only have one database
	dbs, _ := m.Store().ListDatabases()
	assert.Len(t, dbs, 1)
}

func TestManager_SelectBestTemplate_PatternPriority(t *testing.T) {
	st := mem_store.New()
	m := New(st)

	// More specific pattern should win over generic
	templateYAML := `
templates:
  - name: generic
    collectionPattern: "{category}/{id}/items"
    fields:
      - { field: timestamp, order: desc }
  - name: specific
    collectionPattern: "products/{id}/items"
    fields:
      - { field: timestamp, order: desc }
`
	require.NoError(t, m.LoadTemplatesFromBytes([]byte(templateYAML)))

	plan := Plan{
		Collection: "products/123/items",
		OrderBy:    []OrderField{{Field: "timestamp", Direction: encoding.Desc}},
	}

	tmpl, err := m.SelectBestTemplate(plan)
	require.NoError(t, err)
	assert.Equal(t, "specific", tmpl.Name)
}

// ============================================================================
// buildSearchOptions Unit Tests
// ============================================================================

func TestManager_BuildSearchOptions_NoFilters(t *testing.T) {
	st := mem_store.New()
	m := New(st)
	templateYAML := `
templates:
  - name: products_by_price
    collectionPattern: products
    fields:
      - { field: price, order: asc }
`
	require.NoError(t, m.LoadTemplatesFromBytes([]byte(templateYAML)))

	plan := Plan{
		Collection: "products",
		OrderBy:    []OrderField{{Field: "price", Direction: encoding.Asc}},
		Limit:      50,
	}

	tmpl, err := m.SelectBestTemplate(plan)
	require.NoError(t, err)

	opts, err := m.buildSearchOptions(plan, tmpl)
	require.NoError(t, err)

	assert.Equal(t, 50, opts.Limit)
	assert.Nil(t, opts.Lower)
	assert.Nil(t, opts.Upper)
	assert.Nil(t, opts.StartAfter)
}

func TestManager_BuildSearchOptions_DefaultLimit(t *testing.T) {
	st := mem_store.New()
	m := New(st)
	templateYAML := `
templates:
  - name: products_by_price
    collectionPattern: products
    fields:
      - { field: price, order: asc }
`
	require.NoError(t, m.LoadTemplatesFromBytes([]byte(templateYAML)))

	plan := Plan{
		Collection: "products",
		OrderBy:    []OrderField{{Field: "price", Direction: encoding.Asc}},
		Limit:      0, // No limit specified
	}

	tmpl, err := m.SelectBestTemplate(plan)
	require.NoError(t, err)

	opts, err := m.buildSearchOptions(plan, tmpl)
	require.NoError(t, err)

	assert.Equal(t, 100, opts.Limit) // Default limit
}

func TestManager_BuildSearchOptions_EqualityFilter(t *testing.T) {
	st := mem_store.New()
	m := New(st)
	templateYAML := `
templates:
  - name: products_by_category_price
    collectionPattern: products
    fields:
      - { field: category, order: asc }
      - { field: price, order: asc }
`
	require.NoError(t, m.LoadTemplatesFromBytes([]byte(templateYAML)))

	plan := Plan{
		Collection: "products",
		Filters: []Filter{
			{Field: "category", Op: FilterEq, Value: "electronics"},
		},
		OrderBy: []OrderField{{Field: "price", Direction: encoding.Asc}},
		Limit:   10,
	}

	tmpl, err := m.SelectBestTemplate(plan)
	require.NoError(t, err)

	opts, err := m.buildSearchOptions(plan, tmpl)
	require.NoError(t, err)

	assert.Equal(t, 10, opts.Limit)
	assert.NotNil(t, opts.Lower)
	assert.NotNil(t, opts.Upper)

	// Lower and upper should have same prefix for equality filter
	// Lower should be prefix, Upper should be prefix + 0xFF...
	assert.True(t, len(opts.Upper) > len(opts.Lower))
}

func TestManager_BuildSearchOptions_MultipleEqualityFilters(t *testing.T) {
	st := mem_store.New()
	m := New(st)
	templateYAML := `
templates:
  - name: products_by_category_brand_price
    collectionPattern: products
    fields:
      - { field: category, order: asc }
      - { field: brand, order: asc }
      - { field: price, order: asc }
`
	require.NoError(t, m.LoadTemplatesFromBytes([]byte(templateYAML)))

	plan := Plan{
		Collection: "products",
		Filters: []Filter{
			{Field: "category", Op: FilterEq, Value: "electronics"},
			{Field: "brand", Op: FilterEq, Value: "sony"},
		},
		OrderBy: []OrderField{{Field: "price", Direction: encoding.Asc}},
		Limit:   10,
	}

	tmpl, err := m.SelectBestTemplate(plan)
	require.NoError(t, err)

	opts, err := m.buildSearchOptions(plan, tmpl)
	require.NoError(t, err)

	assert.NotNil(t, opts.Lower)
	assert.NotNil(t, opts.Upper)
}

func TestManager_BuildSearchOptions_RangeFilter_Asc(t *testing.T) {
	st := mem_store.New()
	m := New(st)
	templateYAML := `
templates:
  - name: products_by_price
    collectionPattern: products
    fields:
      - { field: price, order: asc }
`
	require.NoError(t, m.LoadTemplatesFromBytes([]byte(templateYAML)))

	tests := []struct {
		name        string
		filters     []Filter
		expectLower bool
		expectUpper bool
	}{
		{
			name:        "greater than",
			filters:     []Filter{{Field: "price", Op: FilterGt, Value: float64(100)}},
			expectLower: true,
			expectUpper: false,
		},
		{
			name:        "greater than or equal",
			filters:     []Filter{{Field: "price", Op: FilterGte, Value: float64(100)}},
			expectLower: true,
			expectUpper: false,
		},
		{
			name:        "less than",
			filters:     []Filter{{Field: "price", Op: FilterLt, Value: float64(500)}},
			expectLower: false,
			expectUpper: true,
		},
		{
			name:        "less than or equal",
			filters:     []Filter{{Field: "price", Op: FilterLte, Value: float64(500)}},
			expectLower: false,
			expectUpper: true,
		},
		{
			name: "both bounds",
			filters: []Filter{
				{Field: "price", Op: FilterGte, Value: float64(100)},
				{Field: "price", Op: FilterLt, Value: float64(500)},
			},
			expectLower: true,
			expectUpper: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := Plan{
				Collection: "products",
				Filters:    tt.filters,
				Limit:      10,
			}

			tmpl, err := m.SelectBestTemplate(plan)
			require.NoError(t, err)

			opts, err := m.buildSearchOptions(plan, tmpl)
			require.NoError(t, err)

			if tt.expectLower {
				assert.NotNil(t, opts.Lower, "expected lower bound")
			}
			if tt.expectUpper {
				assert.NotNil(t, opts.Upper, "expected upper bound")
			}
		})
	}
}

func TestManager_BuildSearchOptions_RangeFilter_Desc(t *testing.T) {
	st := mem_store.New()
	m := New(st)
	templateYAML := `
templates:
  - name: products_by_price_desc
    collectionPattern: products
    fields:
      - { field: price, order: desc }
`
	require.NoError(t, m.LoadTemplatesFromBytes([]byte(templateYAML)))

	tests := []struct {
		name        string
		filters     []Filter
		expectLower bool
		expectUpper bool
	}{
		{
			// For descending: > becomes upper bound (larger values encode to smaller keys)
			name:        "greater than",
			filters:     []Filter{{Field: "price", Op: FilterGt, Value: float64(100)}},
			expectLower: false,
			expectUpper: true,
		},
		{
			name:        "greater than or equal",
			filters:     []Filter{{Field: "price", Op: FilterGte, Value: float64(100)}},
			expectLower: false,
			expectUpper: true,
		},
		{
			// For descending: < becomes lower bound
			name:        "less than",
			filters:     []Filter{{Field: "price", Op: FilterLt, Value: float64(500)}},
			expectLower: true,
			expectUpper: false,
		},
		{
			name:        "less than or equal",
			filters:     []Filter{{Field: "price", Op: FilterLte, Value: float64(500)}},
			expectLower: true,
			expectUpper: false,
		},
		{
			name: "both bounds",
			filters: []Filter{
				{Field: "price", Op: FilterGte, Value: float64(100)},
				{Field: "price", Op: FilterLt, Value: float64(500)},
			},
			expectLower: true,
			expectUpper: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := Plan{
				Collection: "products",
				Filters:    tt.filters,
				Limit:      10,
			}

			tmpl, err := m.SelectBestTemplate(plan)
			require.NoError(t, err)

			opts, err := m.buildSearchOptions(plan, tmpl)
			require.NoError(t, err)

			if tt.expectLower {
				assert.NotNil(t, opts.Lower, "expected lower bound")
			} else {
				assert.Nil(t, opts.Lower, "expected no lower bound")
			}
			if tt.expectUpper {
				assert.NotNil(t, opts.Upper, "expected upper bound")
			} else {
				assert.Nil(t, opts.Upper, "expected no upper bound")
			}
		})
	}
}

func TestManager_BuildSearchOptions_EqualityPlusRange(t *testing.T) {
	st := mem_store.New()
	m := New(st)
	templateYAML := `
templates:
  - name: products_by_category_price
    collectionPattern: products
    fields:
      - { field: category, order: asc }
      - { field: price, order: asc }
`
	require.NoError(t, m.LoadTemplatesFromBytes([]byte(templateYAML)))

	plan := Plan{
		Collection: "products",
		Filters: []Filter{
			{Field: "category", Op: FilterEq, Value: "electronics"},
			{Field: "price", Op: FilterGte, Value: float64(100)},
			{Field: "price", Op: FilterLt, Value: float64(500)},
		},
		Limit: 10,
	}

	tmpl, err := m.SelectBestTemplate(plan)
	require.NoError(t, err)

	opts, err := m.buildSearchOptions(plan, tmpl)
	require.NoError(t, err)

	assert.NotNil(t, opts.Lower)
	assert.NotNil(t, opts.Upper)
}

func TestManager_BuildSearchOptions_StartAfter(t *testing.T) {
	st := mem_store.New()
	m := New(st)
	templateYAML := `
templates:
  - name: products_by_price
    collectionPattern: products
    fields:
      - { field: price, order: asc }
`
	require.NoError(t, m.LoadTemplatesFromBytes([]byte(templateYAML)))

	cursor := encoding.EncodeBase64([]byte{0x01, 0x02, 0x03})

	plan := Plan{
		Collection: "products",
		OrderBy:    []OrderField{{Field: "price", Direction: encoding.Asc}},
		StartAfter: cursor,
		Limit:      10,
	}

	tmpl, err := m.SelectBestTemplate(plan)
	require.NoError(t, err)

	opts, err := m.buildSearchOptions(plan, tmpl)
	require.NoError(t, err)

	assert.Equal(t, []byte{0x01, 0x02, 0x03}, opts.StartAfter)
}

func TestManager_BuildSearchOptions_InvalidCursor(t *testing.T) {
	st := mem_store.New()
	m := New(st)
	templateYAML := `
templates:
  - name: products_by_price
    collectionPattern: products
    fields:
      - { field: price, order: asc }
`
	require.NoError(t, m.LoadTemplatesFromBytes([]byte(templateYAML)))

	plan := Plan{
		Collection: "products",
		OrderBy:    []OrderField{{Field: "price", Direction: encoding.Asc}},
		StartAfter: "!!!invalid-base64!!!",
		Limit:      10,
	}

	tmpl, err := m.SelectBestTemplate(plan)
	require.NoError(t, err)

	_, err = m.buildSearchOptions(plan, tmpl)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid cursor")
}

func TestManager_BuildSearchOptions_StringValues(t *testing.T) {
	st := mem_store.New()
	m := New(st)
	templateYAML := `
templates:
  - name: users_by_name
    collectionPattern: users
    fields:
      - { field: name, order: asc }
`
	require.NoError(t, m.LoadTemplatesFromBytes([]byte(templateYAML)))

	plan := Plan{
		Collection: "users",
		Filters: []Filter{
			{Field: "name", Op: FilterGte, Value: "alice"},
			{Field: "name", Op: FilterLt, Value: "bob"},
		},
		Limit: 10,
	}

	tmpl, err := m.SelectBestTemplate(plan)
	require.NoError(t, err)

	opts, err := m.buildSearchOptions(plan, tmpl)
	require.NoError(t, err)

	assert.NotNil(t, opts.Lower)
	assert.NotNil(t, opts.Upper)
}

func TestManager_BuildSearchOptions_IntegerValues(t *testing.T) {
	st := mem_store.New()
	m := New(st)
	templateYAML := `
templates:
  - name: orders_by_count
    collectionPattern: orders
    fields:
      - { field: count, order: asc }
`
	require.NoError(t, m.LoadTemplatesFromBytes([]byte(templateYAML)))

	plan := Plan{
		Collection: "orders",
		Filters: []Filter{
			{Field: "count", Op: FilterGte, Value: int64(10)},
			{Field: "count", Op: FilterLte, Value: int64(100)},
		},
		Limit: 10,
	}

	tmpl, err := m.SelectBestTemplate(plan)
	require.NoError(t, err)

	opts, err := m.buildSearchOptions(plan, tmpl)
	require.NoError(t, err)

	assert.NotNil(t, opts.Lower)
	assert.NotNil(t, opts.Upper)
}

func TestManager_BuildSearchOptions_BooleanValues(t *testing.T) {
	st := mem_store.New()
	m := New(st)
	templateYAML := `
templates:
  - name: users_by_active
    collectionPattern: users
    fields:
      - { field: active, order: asc }
`
	require.NoError(t, m.LoadTemplatesFromBytes([]byte(templateYAML)))

	plan := Plan{
		Collection: "users",
		Filters: []Filter{
			{Field: "active", Op: FilterEq, Value: true},
		},
		Limit: 10,
	}

	tmpl, err := m.SelectBestTemplate(plan)
	require.NoError(t, err)

	opts, err := m.buildSearchOptions(plan, tmpl)
	require.NoError(t, err)

	assert.NotNil(t, opts.Lower)
	assert.NotNil(t, opts.Upper)
}

func TestManager_BuildSearchOptions_EqualityWithLowerRangeBound(t *testing.T) {
	st := mem_store.New()
	m := New(st)
	templateYAML := `
templates:
  - name: products_by_category_price
    collectionPattern: products
    fields:
      - { field: category, order: asc }
      - { field: price, order: asc }
`
	require.NoError(t, m.LoadTemplatesFromBytes([]byte(templateYAML)))

	// Equality on category, lower bound on price (no upper bound on price)
	// Upper bound is created from equality prefix on category
	plan := Plan{
		Collection: "products",
		Filters: []Filter{
			{Field: "category", Op: FilterEq, Value: "electronics"},
			{Field: "price", Op: FilterGt, Value: float64(100)},
		},
		Limit: 10,
	}

	tmpl, err := m.SelectBestTemplate(plan)
	require.NoError(t, err)

	opts, err := m.buildSearchOptions(plan, tmpl)
	require.NoError(t, err)

	// Should have both bounds - upper created from equality filter on category
	assert.NotNil(t, opts.Lower)
	assert.NotNil(t, opts.Upper)
}

// ============================================================================
// buildSearchOptions Integration Tests
// ============================================================================

func TestManager_BuildSearchOptions_Integration_EqualitySearch(t *testing.T) {
	st := mem_store.New()
	m := New(st)
	templateYAML := `
templates:
  - name: products_by_category_price
    collectionPattern: products
    fields:
      - { field: category, order: asc }
      - { field: price, order: asc }
`
	require.NoError(t, m.LoadTemplatesFromBytes([]byte(templateYAML)))

	// Insert test documents
	tmpl, _ := m.SelectBestTemplate(Plan{
		Collection: "products",
		Filters:    []Filter{{Field: "category", Op: FilterEq, Value: "electronics"}},
	})
	// Using store for index operations (testdb, products, tmpl.Identity())

	docs := []struct {
		id       string
		category string
		price    float64
	}{
		{"p1", "electronics", 100},
		{"p2", "electronics", 200},
		{"p3", "electronics", 300},
		{"p4", "clothing", 50},
		{"p5", "clothing", 150},
		{"p6", "food", 10},
	}

	for _, doc := range docs {
		key, err := encoding.Encode([]encoding.Field{
			{Value: doc.category, Direction: encoding.Asc},
			{Value: doc.price, Direction: encoding.Asc},
		}, doc.id)
		require.NoError(t, err)
		st.Upsert("testdb", "products", tmpl.Identity(), doc.id, key, "")
	}

	// Search for electronics only
	plan := Plan{
		Collection: "products",
		Filters: []Filter{
			{Field: "category", Op: FilterEq, Value: "electronics"},
		},
		Limit: 10,
	}

	results, err := m.Search(context.Background(), "testdb", plan)
	require.NoError(t, err)

	assert.Len(t, results, 3)
	ids := make([]string, len(results))
	for i, r := range results {
		ids[i] = r.ID
	}
	assert.Contains(t, ids, "p1")
	assert.Contains(t, ids, "p2")
	assert.Contains(t, ids, "p3")
}

func TestManager_BuildSearchOptions_Integration_RangeSearch_Asc(t *testing.T) {
	st := mem_store.New()
	m := New(st)
	templateYAML := `
templates:
  - name: products_by_price
    collectionPattern: products
    fields:
      - { field: price, order: asc }
`
	require.NoError(t, m.LoadTemplatesFromBytes([]byte(templateYAML)))

	tmpl, _ := m.SelectBestTemplate(Plan{
		Collection: "products",
		Filters:    []Filter{{Field: "price", Op: FilterGte, Value: float64(100)}},
	})
	// Using store for index operations (testdb, products, tmpl.Identity())

	docs := []struct {
		id    string
		price float64
	}{
		{"p1", 50},
		{"p2", 100},
		{"p3", 150},
		{"p4", 200},
		{"p5", 250},
		{"p6", 300},
	}

	for _, doc := range docs {
		key, err := encoding.Encode([]encoding.Field{
			{Value: doc.price, Direction: encoding.Asc},
		}, doc.id)
		require.NoError(t, err)
		st.Upsert("testdb", "products", tmpl.Identity(), doc.id, key, "")
	}

	tests := []struct {
		name     string
		filters  []Filter
		expected []string
	}{
		{
			name:     "gte 150",
			filters:  []Filter{{Field: "price", Op: FilterGte, Value: float64(150)}},
			expected: []string{"p3", "p4", "p5", "p6"},
		},
		{
			name:     "gt 150",
			filters:  []Filter{{Field: "price", Op: FilterGt, Value: float64(150)}},
			expected: []string{"p4", "p5", "p6"},
		},
		{
			name:     "lte 200",
			filters:  []Filter{{Field: "price", Op: FilterLte, Value: float64(200)}},
			expected: []string{"p1", "p2", "p3", "p4"},
		},
		{
			name:     "lt 200",
			filters:  []Filter{{Field: "price", Op: FilterLt, Value: float64(200)}},
			expected: []string{"p1", "p2", "p3"},
		},
		{
			name: "between 100 and 250 inclusive",
			filters: []Filter{
				{Field: "price", Op: FilterGte, Value: float64(100)},
				{Field: "price", Op: FilterLte, Value: float64(250)},
			},
			expected: []string{"p2", "p3", "p4", "p5"},
		},
		{
			name: "between 100 and 250 exclusive",
			filters: []Filter{
				{Field: "price", Op: FilterGt, Value: float64(100)},
				{Field: "price", Op: FilterLt, Value: float64(250)},
			},
			expected: []string{"p3", "p4"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := Plan{
				Collection: "products",
				Filters:    tt.filters,
				Limit:      100,
			}

			results, err := m.Search(context.Background(), "testdb", plan)
			require.NoError(t, err)

			ids := make([]string, len(results))
			for i, r := range results {
				ids[i] = r.ID
			}
			assert.ElementsMatch(t, tt.expected, ids)
		})
	}
}

func TestManager_BuildSearchOptions_Integration_RangeSearch_Desc(t *testing.T) {
	st := mem_store.New()
	m := New(st)
	templateYAML := `
templates:
  - name: products_by_price_desc
    collectionPattern: products
    fields:
      - { field: price, order: desc }
`
	require.NoError(t, m.LoadTemplatesFromBytes([]byte(templateYAML)))

	tmpl, _ := m.SelectBestTemplate(Plan{
		Collection: "products",
		OrderBy:    []OrderField{{Field: "price", Direction: encoding.Desc}},
	})
	// Using store for index operations (testdb, products, tmpl.Identity())

	docs := []struct {
		id    string
		price float64
	}{
		{"p1", 50},
		{"p2", 100},
		{"p3", 150},
		{"p4", 200},
		{"p5", 250},
		{"p6", 300},
	}

	for _, doc := range docs {
		key, err := encoding.Encode([]encoding.Field{
			{Value: doc.price, Direction: encoding.Desc},
		}, doc.id)
		require.NoError(t, err)
		st.Upsert("testdb", "products", tmpl.Identity(), doc.id, key, "")
	}

	tests := []struct {
		name     string
		filters  []Filter
		expected []string
	}{
		{
			name:     "gte 150 (descending order)",
			filters:  []Filter{{Field: "price", Op: FilterGte, Value: float64(150)}},
			expected: []string{"p3", "p4", "p5", "p6"},
		},
		{
			name:     "gt 150 (descending order)",
			filters:  []Filter{{Field: "price", Op: FilterGt, Value: float64(150)}},
			expected: []string{"p4", "p5", "p6"},
		},
		{
			name:     "lte 200 (descending order)",
			filters:  []Filter{{Field: "price", Op: FilterLte, Value: float64(200)}},
			expected: []string{"p1", "p2", "p3", "p4"},
		},
		{
			name:     "lt 200 (descending order)",
			filters:  []Filter{{Field: "price", Op: FilterLt, Value: float64(200)}},
			expected: []string{"p1", "p2", "p3"},
		},
		{
			name: "between 100 and 250 inclusive (descending order)",
			filters: []Filter{
				{Field: "price", Op: FilterGte, Value: float64(100)},
				{Field: "price", Op: FilterLte, Value: float64(250)},
			},
			expected: []string{"p2", "p3", "p4", "p5"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := Plan{
				Collection: "products",
				Filters:    tt.filters,
				OrderBy:    []OrderField{{Field: "price", Direction: encoding.Desc}},
				Limit:      100,
			}

			results, err := m.Search(context.Background(), "testdb", plan)
			require.NoError(t, err)

			ids := make([]string, len(results))
			for i, r := range results {
				ids[i] = r.ID
			}
			assert.ElementsMatch(t, tt.expected, ids)
		})
	}
}

func TestManager_BuildSearchOptions_Integration_EqualityPlusRange(t *testing.T) {
	st := mem_store.New()
	m := New(st)
	templateYAML := `
templates:
  - name: products_by_category_price
    collectionPattern: products
    fields:
      - { field: category, order: asc }
      - { field: price, order: asc }
`
	require.NoError(t, m.LoadTemplatesFromBytes([]byte(templateYAML)))

	tmpl, _ := m.SelectBestTemplate(Plan{
		Collection: "products",
		Filters: []Filter{
			{Field: "category", Op: FilterEq, Value: "electronics"},
			{Field: "price", Op: FilterGte, Value: float64(100)},
		},
	})
	// Using store for index operations (testdb, products, tmpl.Identity())

	docs := []struct {
		id       string
		category string
		price    float64
	}{
		{"p1", "electronics", 50},
		{"p2", "electronics", 100},
		{"p3", "electronics", 200},
		{"p4", "electronics", 300},
		{"p5", "clothing", 100},
		{"p6", "clothing", 200},
	}

	for _, doc := range docs {
		key, err := encoding.Encode([]encoding.Field{
			{Value: doc.category, Direction: encoding.Asc},
			{Value: doc.price, Direction: encoding.Asc},
		}, doc.id)
		require.NoError(t, err)
		st.Upsert("testdb", "products", tmpl.Identity(), doc.id, key, "")
	}

	tests := []struct {
		name     string
		filters  []Filter
		expected []string
	}{
		{
			name: "electronics, price >= 100",
			filters: []Filter{
				{Field: "category", Op: FilterEq, Value: "electronics"},
				{Field: "price", Op: FilterGte, Value: float64(100)},
			},
			expected: []string{"p2", "p3", "p4"},
		},
		{
			name: "electronics, price > 100 and < 300",
			filters: []Filter{
				{Field: "category", Op: FilterEq, Value: "electronics"},
				{Field: "price", Op: FilterGt, Value: float64(100)},
				{Field: "price", Op: FilterLt, Value: float64(300)},
			},
			expected: []string{"p3"},
		},
		{
			name: "clothing, price <= 150",
			filters: []Filter{
				{Field: "category", Op: FilterEq, Value: "clothing"},
				{Field: "price", Op: FilterLte, Value: float64(150)},
			},
			expected: []string{"p5"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := Plan{
				Collection: "products",
				Filters:    tt.filters,
				Limit:      100,
			}

			results, err := m.Search(context.Background(), "testdb", plan)
			require.NoError(t, err)

			ids := make([]string, len(results))
			for i, r := range results {
				ids[i] = r.ID
			}
			assert.ElementsMatch(t, tt.expected, ids)
		})
	}
}

func TestManager_BuildSearchOptions_Integration_MultipleEquality(t *testing.T) {
	st := mem_store.New()
	m := New(st)
	templateYAML := `
templates:
  - name: products_by_category_brand_price
    collectionPattern: products
    fields:
      - { field: category, order: asc }
      - { field: brand, order: asc }
      - { field: price, order: asc }
`
	require.NoError(t, m.LoadTemplatesFromBytes([]byte(templateYAML)))

	tmpl, _ := m.SelectBestTemplate(Plan{
		Collection: "products",
		Filters: []Filter{
			{Field: "category", Op: FilterEq, Value: "electronics"},
			{Field: "brand", Op: FilterEq, Value: "sony"},
		},
	})
	// Using store for index operations (testdb, products, tmpl.Identity())

	docs := []struct {
		id       string
		category string
		brand    string
		price    float64
	}{
		{"p1", "electronics", "sony", 100},
		{"p2", "electronics", "sony", 200},
		{"p3", "electronics", "samsung", 150},
		{"p4", "electronics", "lg", 120},
		{"p5", "clothing", "nike", 80},
	}

	for _, doc := range docs {
		key, err := encoding.Encode([]encoding.Field{
			{Value: doc.category, Direction: encoding.Asc},
			{Value: doc.brand, Direction: encoding.Asc},
			{Value: doc.price, Direction: encoding.Asc},
		}, doc.id)
		require.NoError(t, err)
		st.Upsert("testdb", "products", tmpl.Identity(), doc.id, key, "")
	}

	plan := Plan{
		Collection: "products",
		Filters: []Filter{
			{Field: "category", Op: FilterEq, Value: "electronics"},
			{Field: "brand", Op: FilterEq, Value: "sony"},
		},
		Limit: 100,
	}

	results, err := m.Search(context.Background(), "testdb", plan)
	require.NoError(t, err)

	ids := make([]string, len(results))
	for i, r := range results {
		ids[i] = r.ID
	}
	assert.ElementsMatch(t, []string{"p1", "p2"}, ids)
}

func TestManager_BuildSearchOptions_Integration_Pagination(t *testing.T) {
	st := mem_store.New()
	m := New(st)
	templateYAML := `
templates:
  - name: products_by_price
    collectionPattern: products
    fields:
      - { field: price, order: asc }
`
	require.NoError(t, m.LoadTemplatesFromBytes([]byte(templateYAML)))

	tmpl, _ := m.SelectBestTemplate(Plan{
		Collection: "products",
		OrderBy:    []OrderField{{Field: "price", Direction: encoding.Asc}},
	})
	// Using store for index operations (testdb, products, tmpl.Identity())

	// Insert 10 documents
	for i := 1; i <= 10; i++ {
		id := "p" + string(rune('0'+i))
		price := float64(i * 10)
		key, err := encoding.Encode([]encoding.Field{
			{Value: price, Direction: encoding.Asc},
		}, id)
		require.NoError(t, err)
		st.Upsert("testdb", "products", tmpl.Identity(), id, key, "")
	}

	// First page (limit 3)
	plan := Plan{
		Collection: "products",
		OrderBy:    []OrderField{{Field: "price", Direction: encoding.Asc}},
		Limit:      3,
	}

	results, err := m.Search(context.Background(), "testdb", plan)
	require.NoError(t, err)
	require.Len(t, results, 3)

	// Get cursor from last result
	cursor := encoding.EncodeBase64(results[2].OrderKey)

	// Second page using cursor
	plan.StartAfter = cursor
	results2, err := m.Search(context.Background(), "testdb", plan)
	require.NoError(t, err)
	require.Len(t, results2, 3)

	// Ensure no overlap
	ids1 := make([]string, 3)
	ids2 := make([]string, 3)
	for i := 0; i < 3; i++ {
		ids1[i] = results[i].ID
		ids2[i] = results2[i].ID
	}

	for _, id := range ids1 {
		assert.NotContains(t, ids2, id, "page 2 should not contain ids from page 1")
	}
}

func TestManager_AppendMaxSuffix(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
	}{
		{
			name:  "empty",
			input: []byte{},
		},
		{
			name:  "single byte",
			input: []byte{0x42},
		},
		{
			name:  "multiple bytes",
			input: []byte{0x01, 0x02, 0x03},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := appendMaxSuffix(tt.input)

			if len(tt.input) == 0 {
				assert.Nil(t, result)
			} else {
				// Result should be longer
				assert.Greater(t, len(result), len(tt.input))
				// Prefix should match
				assert.Equal(t, tt.input, result[:len(tt.input)])
				// Suffix should be 0xFF
				for i := len(tt.input); i < len(result); i++ {
					assert.Equal(t, byte(0xFF), result[i])
				}
			}
		})
	}
}

// ============================================================================
// Manager Wrapper Methods Tests
// ============================================================================

func TestManager_Upsert(t *testing.T) {
	st := mem_store.New()
	m := New(st)

	err := m.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x01, 0x02}, "event-123")
	require.NoError(t, err)

	// Verify via store
	orderKey, found := st.Get("testdb", "users/*", "tmpl1", "doc1")
	require.True(t, found)
	assert.Equal(t, []byte{0x01, 0x02}, orderKey)

	// Verify progress
	progress, err := st.LoadProgress()
	require.NoError(t, err)
	assert.Equal(t, "event-123", progress)
}

func TestManager_Delete(t *testing.T) {
	st := mem_store.New()
	m := New(st)

	// Insert first
	err := m.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x01}, "")
	require.NoError(t, err)

	// Delete
	err = m.Delete("testdb", "users/*", "tmpl1", "doc1", "event-456")
	require.NoError(t, err)

	// Verify deleted
	_, found := st.Get("testdb", "users/*", "tmpl1", "doc1")
	assert.False(t, found)

	// Verify progress
	progress, err := st.LoadProgress()
	require.NoError(t, err)
	assert.Equal(t, "event-456", progress)
}

func TestManager_DeleteIndex_Wrapper(t *testing.T) {
	st := mem_store.New()
	m := New(st)

	// Insert some data
	for i := 0; i < 5; i++ {
		err := m.Upsert("testdb", "users/*", "tmpl1", string(rune('a'+i)), []byte{byte(i)}, "")
		require.NoError(t, err)
	}

	// Delete the index
	err := m.DeleteIndex("testdb", "users/*", "tmpl1")
	require.NoError(t, err)

	// Verify all documents are gone
	for i := 0; i < 5; i++ {
		_, found := st.Get("testdb", "users/*", "tmpl1", string(rune('a'+i)))
		assert.False(t, found, "doc %d should be deleted", i)
	}
}

func TestManager_SetState(t *testing.T) {
	st := mem_store.New()
	m := New(st)

	// Set state
	err := m.SetState("testdb", "users/*", "tmpl1", store.IndexStateRebuilding)
	require.NoError(t, err)

	// Get state via manager
	state, err := m.GetState("testdb", "users/*", "tmpl1")
	require.NoError(t, err)
	assert.Equal(t, store.IndexStateRebuilding, state)
}

func TestManager_GetState(t *testing.T) {
	st := mem_store.New()
	m := New(st)

	// Default state is healthy
	state, err := m.GetState("testdb", "users/*", "tmpl1")
	require.NoError(t, err)
	assert.Equal(t, store.IndexStateHealthy, state)

	// Set to failed
	err = m.SetState("testdb", "users/*", "tmpl1", store.IndexStateFailed)
	require.NoError(t, err)

	state, err = m.GetState("testdb", "users/*", "tmpl1")
	require.NoError(t, err)
	assert.Equal(t, store.IndexStateFailed, state)
}

func TestManager_LoadProgress(t *testing.T) {
	st := mem_store.New()
	m := New(st)

	// Initially empty
	progress, err := m.LoadProgress()
	require.NoError(t, err)
	assert.Empty(t, progress)

	// Upsert with progress
	err = m.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x01}, "event-789")
	require.NoError(t, err)

	// Load via manager
	progress, err = m.LoadProgress()
	require.NoError(t, err)
	assert.Equal(t, "event-789", progress)
}

func TestManager_Flush(t *testing.T) {
	st := mem_store.New()
	m := New(st)

	// Flush is a no-op for mem_store
	err := m.Flush()
	require.NoError(t, err)
}

func TestManager_Close(t *testing.T) {
	st := mem_store.New()
	m := New(st)

	// Close is a no-op for mem_store
	err := m.Close()
	require.NoError(t, err)
}
