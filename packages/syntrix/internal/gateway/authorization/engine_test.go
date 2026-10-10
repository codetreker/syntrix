package authorization

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/storage"
	"github.com/codetreker/syntrix/pkg/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// MockQueryService
type MockQueryService struct {
	mock.Mock
}

func (m *MockQueryService) GetDocument(ctx context.Context, database string, path string) (model.Document, error) {
	args := m.Called(ctx, database, path)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(model.Document), args.Error(1)
}

func (m *MockQueryService) ListDocuments(ctx context.Context, parent string, pageSize int, pageToken string) ([]*storage.StoredDoc, string, error) {
	return nil, "", nil
}

func (m *MockQueryService) QueryDocuments(ctx context.Context, parent string, filter string) ([]*storage.StoredDoc, error) {
	return nil, nil
}

func (m *MockQueryService) CreateDocument(ctx context.Context, database string, doc model.Document) error {
	return nil
}

func (m *MockQueryService) ReplaceDocument(ctx context.Context, database string, data model.Document, pred model.Filters) (model.Document, error) {
	return nil, nil
}

func (m *MockQueryService) PatchDocument(ctx context.Context, database string, data model.Document, pred model.Filters) (model.Document, error) {
	return nil, nil
}

func (m *MockQueryService) DeleteDocument(ctx context.Context, database string, path string, pred model.Filters) error {
	return nil
}

func (m *MockQueryService) ExecuteQuery(ctx context.Context, database string, q model.Query) ([]model.Document, error) {
	return nil, nil
}

func (m *MockQueryService) WatchCollection(ctx context.Context, database string, collection string) (<-chan storage.Event, error) {
	return nil, nil
}

func (m *MockQueryService) Pull(ctx context.Context, database string, req storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
	return nil, nil
}

func (m *MockQueryService) Push(ctx context.Context, database string, req storage.ReplicationPushRequest) (*storage.ReplicationPushResponse, error) {
	return nil, nil
}

// createTestRulesDir creates a temp directory with test rules
func createTestRulesDir(t *testing.T, database string, rulesYAML string) string {
	tmpDir := t.TempDir()
	fullYAML := "database: " + database + "\n" + rulesYAML
	err := os.WriteFile(tmpDir+"/"+database+".yml", []byte(fullYAML), 0644)
	require.NoError(t, err)
	return tmpDir
}

func TestEngine_Evaluate(t *testing.T) {
	rules := `
rules_version: '1'
service: syntrix
match:
  /databases/{database}/documents:
    match:
      /users/{userId}:
        allow:
          read: "request.auth.userId == userId"
          write: "request.auth.userId == userId"
      /public/{doc=**}:
        allow:
          read: "true"
      /rooms/{roomId}:
        allow:
          read: "resource.data.public == true || request.auth.userId in resource.data.members"
          write: "request.auth.userId in resource.data.members"
      /admin/{doc=**}:
        allow:
          read, write: "exists('/databases/default/documents/admins/' + request.auth.userId)"
`
	tmpDir := createTestRulesDir(t, "default", rules)

	mockQuery := new(MockQueryService)
	engine, err := NewEngine(Config{RulesPath: tmpDir}, mockQuery)
	assert.NoError(t, err)

	// Test Case 1: Allowed Read (User matches)
	req := Request{
		Auth: Authenticated{UID: "user123"},
		Time: time.Now(),
	}
	allowed, err := engine.Evaluate(context.Background(), "default", "/users/user123", "read", req, nil)
	assert.NoError(t, err)
	assert.True(t, allowed)

	// Test Case 2: Denied Read (User mismatch)
	req = Request{
		Auth: Authenticated{UID: "otherUser"},
		Time: time.Now(),
	}
	allowed, err = engine.Evaluate(context.Background(), "default", "/users/user123", "read", req, nil)
	assert.NoError(t, err)
	assert.False(t, allowed)

	// Test Case 3: Public Read (Wildcard)
	allowed, err = engine.Evaluate(context.Background(), "default", "/public/some/doc", "read", req, nil)
	assert.NoError(t, err)
	assert.True(t, allowed)

	// Test Case 4: Resource Data Check (Public Room)
	publicRoom := &Resource{
		Data: map[string]interface{}{
			"public": true,
		},
	}
	allowed, err = engine.Evaluate(context.Background(), "default", "/rooms/room1", "read", req, publicRoom)
	assert.NoError(t, err)
	assert.True(t, allowed)

	// Test Case 5: Resource Data Check (Private Room, Member)
	privateRoom := &Resource{
		Data: map[string]interface{}{
			"public":  false,
			"members": []interface{}{"otherUser"},
		},
	}
	allowed, err = engine.Evaluate(context.Background(), "default", "/rooms/room2", "read", req, privateRoom)
	assert.NoError(t, err)
	assert.True(t, allowed)

	// Test Case 6: Resource Data Check (Private Room, Non-Member)
	reqNonMember := Request{
		Auth: Authenticated{UID: "stranger"},
		Time: time.Now(),
	}
	allowed, err = engine.Evaluate(context.Background(), "default", "/rooms/room2", "read", reqNonMember, privateRoom)
	assert.NoError(t, err)
	assert.False(t, allowed)

	// Test Case 7: Custom Function 'exists' (Admin Check - Success)
	reqAdmin := Request{
		Auth: Authenticated{UID: "adminUser"},
		Time: time.Now(),
	}
	mockQuery.On("GetDocument", mock.Anything, "default", "admins/adminUser").Return(model.Document{"id": "adminUser"}, nil)

	allowed, err = engine.Evaluate(context.Background(), "default", "/admin/config", "write", reqAdmin, nil)
	assert.NoError(t, err)
	assert.True(t, allowed)

	// Test Case 8: Custom Function 'exists' (Admin Check - Fail)
	mockQuery.On("GetDocument", mock.Anything, "default", "admins/stranger").Return(nil, model.ErrNotFound)

	allowed, err = engine.Evaluate(context.Background(), "default", "/admin/config", "write", reqNonMember, nil)
	assert.NoError(t, err)
	assert.False(t, allowed)
}

func TestEvaluate_ActionAliases(t *testing.T) {
	rules := `
rules_version: '1'
service: syntrix
match:
  /databases/{database}/documents:
    match:
      /test/{id}:
        allow:
          read: "true"
          write: "true"
`
	tmpDir := createTestRulesDir(t, "default", rules)

	mockQuery := new(MockQueryService)
	engine, err := NewEngine(Config{RulesPath: tmpDir}, mockQuery)
	assert.NoError(t, err)

	req := Request{Auth: Authenticated{UID: "user1"}}

	// Test read aliases
	for _, action := range []string{"get", "list"} {
		allowed, err := engine.Evaluate(context.Background(), "default", "/test/1", action, req, nil)
		assert.NoError(t, err)
		assert.True(t, allowed, "Action %s should be allowed by read", action)
	}

	// Test write aliases
	for _, action := range []string{"create", "update", "delete"} {
		allowed, err := engine.Evaluate(context.Background(), "default", "/test/1", action, req, nil)
		assert.NoError(t, err)
		assert.True(t, allowed, "Action %s should be allowed by write", action)
	}

	// Test exact match
	allowed, err := engine.Evaluate(context.Background(), "default", "/test/1", "read", req, nil)
	assert.True(t, allowed)
	allowed, err = engine.Evaluate(context.Background(), "default", "/test/1", "write", req, nil)
	assert.True(t, allowed)

	// Test mismatch
	allowed, err = engine.Evaluate(context.Background(), "default", "/test/1", "other", req, nil)
	assert.False(t, allowed)
}

func TestEngine_UpdateRules(t *testing.T) {
	engine, err := NewEngine(Config{}, new(MockQueryService))
	assert.NoError(t, err)

	rules := `
database: testdb
rules_version: '2'
service: syntrix
match:
  /databases/{database}/documents:
    match:
      /test:
        allow:
          read: "true"
`
	err = engine.UpdateRules("testdb", []byte(rules))
	assert.NoError(t, err)

	loadedRules := engine.GetRulesForDatabase("testdb")
	assert.NotNil(t, loadedRules)
	assert.Equal(t, "2", loadedRules.Version)
}

func TestEngine_LoadRulesFromDir(t *testing.T) {
	t.Run("valid directory with single file", func(t *testing.T) {
		rules := `
rules_version: '1'
service: syntrix
match:
  /databases/{database}/documents:
    match:
      /{doc=**}:
        allow:
          read: "true"
`
		tmpDir := createTestRulesDir(t, "mydb", rules)

		engine, err := NewEngine(Config{RulesPath: tmpDir}, new(MockQueryService))
		require.NoError(t, err)

		dbRules := engine.GetRulesForDatabase("mydb")
		assert.NotNil(t, dbRules)
		assert.Equal(t, "mydb", dbRules.Database)
	})

	t.Run("multiple databases", func(t *testing.T) {
		tmpDir := t.TempDir()

		rules1 := `database: db1
rules_version: '1'
service: syntrix
match:
  /databases/{database}/documents:
    match:
      /{doc=**}:
        allow:
          read: "true"
`
		rules2 := `database: db2
rules_version: '1'
service: syntrix
match:
  /databases/{database}/documents:
    match:
      /{doc=**}:
        allow:
          read: "false"
`
		err := os.WriteFile(tmpDir+"/db1.yml", []byte(rules1), 0644)
		require.NoError(t, err)
		err = os.WriteFile(tmpDir+"/db2.yml", []byte(rules2), 0644)
		require.NoError(t, err)

		engine, err := NewEngine(Config{RulesPath: tmpDir}, new(MockQueryService))
		require.NoError(t, err)

		assert.NotNil(t, engine.GetRulesForDatabase("db1"))
		assert.NotNil(t, engine.GetRulesForDatabase("db2"))
	})

	t.Run("duplicate database rejected", func(t *testing.T) {
		tmpDir := t.TempDir()

		rules := `database: mydb
rules_version: '1'
service: syntrix
match:
  /databases/{database}/documents:
    match:
      /{doc=**}:
        allow:
          read: "true"
`
		err := os.WriteFile(tmpDir+"/file1.yml", []byte(rules), 0644)
		require.NoError(t, err)
		err = os.WriteFile(tmpDir+"/file2.yml", []byte(rules), 0644)
		require.NoError(t, err)

		_, err = NewEngine(Config{RulesPath: tmpDir}, new(MockQueryService))
		assert.Error(t, err)
		assert.ErrorIs(t, err, ErrDuplicateDatabase)
	})

	t.Run("empty database field rejected", func(t *testing.T) {
		tmpDir := t.TempDir()

		rules := `rules_version: '1'
service: syntrix
match:
  /databases/default/documents:
    match:
      /{doc=**}:
        allow:
          read: "true"
`
		err := os.WriteFile(tmpDir+"/test.yml", []byte(rules), 0644)
		require.NoError(t, err)

		_, err = NewEngine(Config{RulesPath: tmpDir}, new(MockQueryService))
		assert.Error(t, err)
		assert.ErrorIs(t, err, ErrEmptyDatabase)
	})

	t.Run("database placeholder replaced", func(t *testing.T) {
		rules := `
rules_version: '1'
service: syntrix
match:
  /databases/{database}/documents:
    match:
      /{doc=**}:
        allow:
          read: "true"
`
		tmpDir := createTestRulesDir(t, "analytics", rules)

		engine, err := NewEngine(Config{RulesPath: tmpDir}, new(MockQueryService))
		require.NoError(t, err)

		dbRules := engine.GetRulesForDatabase("analytics")
		assert.NotNil(t, dbRules)

		// Verify placeholder was replaced
		_, hasAnalytics := dbRules.Match["/databases/analytics/documents"]
		assert.True(t, hasAnalytics, "placeholder should be replaced with database name")
	})

	t.Run("database mismatch rejected", func(t *testing.T) {
		tmpDir := t.TempDir()

		rules := `database: mydb
rules_version: '1'
service: syntrix
match:
  /databases/wrongdb/documents:
    match:
      /{doc=**}:
        allow:
          read: "true"
`
		err := os.WriteFile(tmpDir+"/mydb.yml", []byte(rules), 0644)
		require.NoError(t, err)

		_, err = NewEngine(Config{RulesPath: tmpDir}, new(MockQueryService))
		assert.Error(t, err)
		assert.ErrorIs(t, err, ErrDatabaseMismatch)
	})

	t.Run("not a directory", func(t *testing.T) {
		tmpFile := t.TempDir() + "/file.yml"
		err := os.WriteFile(tmpFile, []byte("database: test\n"), 0644)
		require.NoError(t, err)

		_, err = NewEngine(Config{RulesPath: tmpFile}, new(MockQueryService))
		assert.Error(t, err)
		assert.ErrorIs(t, err, ErrNotDirectory)
	})

	t.Run("empty directory", func(t *testing.T) {
		tmpDir := t.TempDir()

		engine, err := NewEngine(Config{RulesPath: tmpDir}, new(MockQueryService))
		require.NoError(t, err)

		// No rules loaded, evaluate should deny
		req := Request{Auth: Authenticated{UID: "user1"}}
		allowed, err := engine.Evaluate(context.Background(), "anydb", "/test", "read", req, nil)
		assert.NoError(t, err)
		assert.False(t, allowed)
	})
}

func TestEngine_EvaluateNoRulesForDatabase(t *testing.T) {
	rules := `
rules_version: '1'
service: syntrix
match:
  /databases/{database}/documents:
    match:
      /{doc=**}:
        allow:
          read: "true"
`
	tmpDir := createTestRulesDir(t, "db1", rules)

	engine, err := NewEngine(Config{RulesPath: tmpDir}, new(MockQueryService))
	require.NoError(t, err)

	req := Request{Auth: Authenticated{UID: "user1"}}

	// Should allow for db1
	allowed, err := engine.Evaluate(context.Background(), "db1", "/test", "read", req, nil)
	assert.NoError(t, err)
	assert.True(t, allowed)

	// Should deny for unknown database
	allowed, err = engine.Evaluate(context.Background(), "unknown", "/test", "read", req, nil)
	assert.NoError(t, err)
	assert.False(t, allowed)
}

func TestEngine_GetRules(t *testing.T) {
	rules := `
rules_version: '1'
service: syntrix
match:
  /databases/{database}/documents:
    match:
      /{doc=**}:
        allow:
          read: "true"
`
	tmpDir := createTestRulesDir(t, "testdb", rules)

	engine, err := NewEngine(Config{RulesPath: tmpDir}, new(MockQueryService))
	require.NoError(t, err)

	// GetRules should return the first ruleset for backward compatibility
	ruleSet := engine.GetRules()
	assert.NotNil(t, ruleSet)
	assert.Equal(t, "testdb", ruleSet.Database)
}

func TestEngine_GetRules_Empty(t *testing.T) {
	tmpDir := t.TempDir()

	engine, err := NewEngine(Config{RulesPath: tmpDir}, new(MockQueryService))
	require.NoError(t, err)

	// GetRules should return nil when no rules loaded
	ruleSet := engine.GetRules()
	assert.Nil(t, ruleSet)
}

func TestEngine_UpdateRules_DatabaseMismatch(t *testing.T) {
	tmpDir := t.TempDir()

	engine, err := NewEngine(Config{RulesPath: tmpDir}, new(MockQueryService))
	require.NoError(t, err)

	// Try to update with content that has different database than target
	content := []byte(`database: otherdb
rules_version: '1'
service: syntrix
match:
  /databases/{database}/documents:
    match:
      /{doc=**}:
        allow:
          read: "true"
`)
	err = engine.UpdateRules("targetdb", content)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrDatabaseMismatch)
}

func TestEngine_Evaluate_DBAdminBypass(t *testing.T) {
	// Create rules that deny all access
	rules := `
rules_version: '1'
service: syntrix
match:
  /databases/{database}/documents:
    match:
      /{doc=**}:
        allow:
          read: "false"
          write: "false"
`
	tmpDir := createTestRulesDir(t, "default", rules)

	engine, err := NewEngine(Config{RulesPath: tmpDir}, new(MockQueryService))
	require.NoError(t, err)

	t.Run("db_admin bypasses rules for matching database", func(t *testing.T) {
		req := Request{
			Auth: Authenticated{
				UID:     "user1",
				DBAdmin: []string{"default"},
			},
		}

		// Should allow even though rules say "false"
		allowed, err := engine.Evaluate(context.Background(), "default", "/test/doc", "read", req, nil)
		assert.NoError(t, err)
		assert.True(t, allowed, "db_admin should bypass rules for matching database")

		allowed, err = engine.Evaluate(context.Background(), "default", "/test/doc", "write", req, nil)
		assert.NoError(t, err)
		assert.True(t, allowed, "db_admin should bypass write rules too")
	})

	t.Run("db_admin does not bypass rules for non-matching database", func(t *testing.T) {
		req := Request{
			Auth: Authenticated{
				UID:     "user1",
				DBAdmin: []string{"other_database"},
			},
		}

		// Should deny because user is only db_admin for "other_database", not "default"
		allowed, err := engine.Evaluate(context.Background(), "default", "/test/doc", "read", req, nil)
		assert.NoError(t, err)
		assert.False(t, allowed, "db_admin for other database should not bypass rules")
	})

	t.Run("db_admin with multiple databases", func(t *testing.T) {
		req := Request{
			Auth: Authenticated{
				UID:     "user1",
				DBAdmin: []string{"db1", "default", "db2"},
			},
		}

		// Should allow because "default" is in the list
		allowed, err := engine.Evaluate(context.Background(), "default", "/test/doc", "read", req, nil)
		assert.NoError(t, err)
		assert.True(t, allowed, "db_admin with multiple databases should match")
	})

	t.Run("empty db_admin does not bypass", func(t *testing.T) {
		req := Request{
			Auth: Authenticated{
				UID:     "user1",
				DBAdmin: []string{},
			},
		}

		// Should deny because db_admin is empty
		allowed, err := engine.Evaluate(context.Background(), "default", "/test/doc", "read", req, nil)
		assert.NoError(t, err)
		assert.False(t, allowed, "empty db_admin should not bypass rules")
	})

	t.Run("nil db_admin does not bypass", func(t *testing.T) {
		req := Request{
			Auth: Authenticated{
				UID:     "user1",
				DBAdmin: nil,
			},
		}

		// Should deny because db_admin is nil
		allowed, err := engine.Evaluate(context.Background(), "default", "/test/doc", "read", req, nil)
		assert.NoError(t, err)
		assert.False(t, allowed, "nil db_admin should not bypass rules")
	})
}

func TestEngine_Evaluate_DBAdminBypassNoRules(t *testing.T) {
	// Create engine with no rules
	tmpDir := t.TempDir()
	engine, err := NewEngine(Config{RulesPath: tmpDir}, new(MockQueryService))
	require.NoError(t, err)

	t.Run("db_admin allows access even when no rules exist", func(t *testing.T) {
		req := Request{
			Auth: Authenticated{
				UID:     "user1",
				DBAdmin: []string{"unknown_db"},
			},
		}

		// Should allow because user is db_admin for this database
		// even though there are no rules for it
		allowed, err := engine.Evaluate(context.Background(), "unknown_db", "/test/doc", "read", req, nil)
		assert.NoError(t, err)
		assert.True(t, allowed, "db_admin should allow access even without rules")
	})

	t.Run("non db_admin denied when no rules exist", func(t *testing.T) {
		req := Request{
			Auth: Authenticated{
				UID:     "user1",
				DBAdmin: []string{},
			},
		}

		// Should deny because no rules and not db_admin
		allowed, err := engine.Evaluate(context.Background(), "unknown_db", "/test/doc", "read", req, nil)
		assert.NoError(t, err)
		assert.False(t, allowed, "non db_admin should be denied when no rules exist")
	})
}
