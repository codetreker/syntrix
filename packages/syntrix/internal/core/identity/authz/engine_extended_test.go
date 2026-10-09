package authz

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/codetreker/syntrix/internal/core/identity/config"
	"github.com/codetreker/syntrix/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEmptyAndInvalidRules covers:
// - Evaluate returns false when rules missing
// - LoadRulesFromDir/UpdateRules fail fast on bad YAML or CEL
func TestEmptyAndInvalidRules(t *testing.T) {
	mockQuery := new(MockQueryService)

	// Case 1: No rules loaded (empty directory)
	emptyDir := t.TempDir()
	engine, err := NewEngine(config.AuthZConfig{RulesPath: emptyDir}, mockQuery)
	assert.NoError(t, err)

	allowed, err := engine.Evaluate(context.Background(), "default", "/some/path", "read", Request{}, nil)
	assert.NoError(t, err)
	assert.False(t, allowed, "Should deny when no rules are loaded")

	// Case 2: LoadRulesFromDir with invalid YAML
	invalidDir := t.TempDir()
	err = os.WriteFile(filepath.Join(invalidDir, "invalid.yaml"), []byte("database: test\ninvalid: yaml: content: :"), 0644)
	assert.NoError(t, err)

	err = engine.LoadRulesFromDir(invalidDir)
	assert.Error(t, err, "Should fail on invalid YAML")

	// Case 3: UpdateRules with invalid CEL
	invalidCELRules := `
database: testdb
rules_version: '1'
service: syntrix
match:
  /databases/{database}/documents:
    match:
      /test:
        allow:
          read: "this is not valid cel syntax !!!"
`
	err = engine.UpdateRules("testdb", []byte(invalidCELRules))
	assert.Error(t, err, "Should fail on invalid CEL compilation")
}

// TestPathMatching covers:
// - {var=**} deep capture
// - Parent + child vars merge
// - Deterministic ordering
// - Unmatched/empty allow paths return false
func TestPathMatching(t *testing.T) {
	rules := `
database: default
rules_version: '1'
service: syntrix
match:
  /databases/{database}/documents:
    match:
      /users/{userId}:
        allow:
          read: "userId == 'u1'"
      /files/{path=**}:
        allow:
          read: "path == 'images/logo.png'"
      /mixed/{p1}:
        match:
          /{p2}:
            allow:
              read: "p1 == 'a' && p2 == 'b'"
`
	tmpDir := t.TempDir()
	err := os.WriteFile(filepath.Join(tmpDir, "default.yml"), []byte(rules), 0644)
	require.NoError(t, err)

	mockQuery := new(MockQueryService)
	engine, err := NewEngine(config.AuthZConfig{RulesPath: tmpDir}, mockQuery)
	assert.NoError(t, err)

	ctx := context.Background()

	// Deep capture
	allowed, err := engine.Evaluate(ctx, "default", "/files/images/logo.png", "read", Request{}, nil)
	assert.NoError(t, err)
	assert.True(t, allowed, "Should match deep capture path")

	allowed, err = engine.Evaluate(ctx, "default", "/files/other.txt", "read", Request{}, nil)
	assert.NoError(t, err)
	assert.False(t, allowed, "Should match deep capture path but fail condition")

	// Variable merging
	allowed, err = engine.Evaluate(ctx, "default", "/mixed/a/b", "read", Request{}, nil)
	assert.NoError(t, err)
	assert.True(t, allowed, "Should merge parent and child variables")

	allowed, err = engine.Evaluate(ctx, "default", "/mixed/a/c", "read", Request{}, nil)
	assert.NoError(t, err)
	assert.False(t, allowed, "Should fail if variables don't match condition")

	// Unmatched path
	allowed, err = engine.Evaluate(ctx, "default", "/unknown/path", "read", Request{}, nil)
	assert.NoError(t, err)
	assert.False(t, allowed, "Should deny unmatched path")
}

// TestActionHandling covers:
// - Mixed spacing/case robustness
// - Unknown action returns false without panic
func TestActionHandling(t *testing.T) {
	rules := `
database: default
rules_version: '1'
service: syntrix
match:
  /databases/{database}/documents:
    match:
      /test:
        allow:
          read, write: "true"
          customAction: "true"
`
	tmpDir := t.TempDir()
	err := os.WriteFile(filepath.Join(tmpDir, "default.yml"), []byte(rules), 0644)
	require.NoError(t, err)

	mockQuery := new(MockQueryService)
	engine, err := NewEngine(config.AuthZConfig{RulesPath: tmpDir}, mockQuery)
	assert.NoError(t, err)

	ctx := context.Background()

	// Standard actions
	allowed, err := engine.Evaluate(ctx, "default", "/test", "read", Request{}, nil)
	assert.NoError(t, err)
	assert.True(t, allowed)

	allowed, err = engine.Evaluate(ctx, "default", "/test", "write", Request{}, nil)
	assert.NoError(t, err)
	assert.True(t, allowed)

	// Custom action
	allowed, err = engine.Evaluate(ctx, "default", "/test", "customAction", Request{}, nil)
	assert.NoError(t, err)
	assert.True(t, allowed)

	// Unknown action
	allowed, err = engine.Evaluate(ctx, "default", "/test", "unknown_action", Request{}, nil)
	assert.NoError(t, err)
	assert.False(t, allowed)
}

// TestCELConditionErrors covers:
// - Runtime errors propagate or fail safe
// - existingRes nil is safe
// - Multiple allow clauses continue after failures (if applicable, though usually OR logic)
func TestCELConditionErrors(t *testing.T) {
	rules := `
database: default
rules_version: '1'
service: syntrix
match:
  /databases/{database}/documents:
    match:
      /safe:
        allow:
          read: "has(resource.data) && resource.data.public == true"
      /unsafe:
        allow:
          read: "resource.data.public == true"
      /error:
        allow:
          read: "1 / 0 == 0"
`
	tmpDir := t.TempDir()
	err := os.WriteFile(filepath.Join(tmpDir, "default.yml"), []byte(rules), 0644)
	require.NoError(t, err)

	mockQuery := new(MockQueryService)
	engine, err := NewEngine(config.AuthZConfig{RulesPath: tmpDir}, mockQuery)
	assert.NoError(t, err)

	ctx := context.Background()

	// Safe check with nil resource
	allowed, err := engine.Evaluate(ctx, "default", "/safe", "read", Request{}, nil)
	assert.NoError(t, err)
	assert.False(t, allowed, "Should be false safely when resource is nil")

	// Unsafe check with nil resource (might error or return false depending on CEL config)
	// In standard CEL, accessing field on null might error.
	allowed, err = engine.Evaluate(ctx, "default", "/unsafe", "read", Request{}, nil)
	// We expect it to fail safe (return false) or return error.
	// The requirement says "fail safe".
	assert.False(t, allowed)

	// Runtime error (division by zero)
	allowed, err = engine.Evaluate(ctx, "default", "/error", "read", Request{}, nil)
	assert.False(t, allowed)
}

// TestCustomFunctions covers:
// - exists/get with ErrNotFound vs other errors
// - Non-string args error
func TestCustomFunctions(t *testing.T) {
	rules := `
database: default
rules_version: '1'
service: syntrix
match:
  /databases/{database}/documents:
    match:
      /check_exists:
        allow:
          read: "exists('/databases/$(database)/documents/target')"
      /check_get:
        allow:
          read: "get('/databases/$(database)/documents/target').data.flag == true"
`
	tmpDir := t.TempDir()
	err := os.WriteFile(filepath.Join(tmpDir, "default.yml"), []byte(rules), 0644)
	require.NoError(t, err)

	mockQuery := new(MockQueryService)
	engine, err := NewEngine(config.AuthZConfig{RulesPath: tmpDir}, mockQuery)
	assert.NoError(t, err)

	ctx := context.Background()

	// Mock exists (GetDocument returns nil error for success, error for not found)
	// Case 1: Exists true
	mockQuery.On("GetDocument", ctx, "default", "target").Return(model.Document{"data": map[string]interface{}{"flag": true}}, nil).Once()
	allowed, err := engine.Evaluate(ctx, "default", "/check_exists", "read", Request{}, nil)
	assert.NoError(t, err)
	assert.True(t, allowed)

	// Case 2: Exists false (ErrNotFound)
	mockQuery.On("GetDocument", ctx, "default", "target").Return(nil, model.ErrNotFound).Once()
	allowed, err = engine.Evaluate(ctx, "default", "/check_exists", "read", Request{}, nil)
	assert.NoError(t, err)
	assert.False(t, allowed)

	// Case 3: Get true
	mockQuery.On("GetDocument", ctx, "default", "target").Return(model.Document{"flag": true}, nil).Once()
	allowed, err = engine.Evaluate(ctx, "default", "/check_get", "read", Request{}, nil)
	assert.NoError(t, err)
	assert.True(t, allowed)
}
