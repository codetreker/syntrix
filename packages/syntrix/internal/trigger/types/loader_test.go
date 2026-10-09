package types

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for LoadTriggersFromDir

func TestLoadTriggersFromDir_SingleFile(t *testing.T) {
	tmpDir := t.TempDir()
	content := `database: db1
triggers:
  trigger1:
    collection: users
    events:
      - create
    url: https://example.com/webhook
`
	err := os.WriteFile(filepath.Join(tmpDir, "db1.yml"), []byte(content), 0644)
	require.NoError(t, err)

	result, err := LoadTriggersFromDir(tmpDir)
	assert.NoError(t, err)
	assert.Len(t, result, 1)
	assert.Len(t, result["db1"], 1)
	assert.Equal(t, "trigger1", result["db1"][0].ID)
	assert.Equal(t, "db1", result["db1"][0].Database)
	assert.Equal(t, "users", result["db1"][0].Collection)
}

func TestLoadTriggersFromDir_MultipleFiles(t *testing.T) {
	tmpDir := t.TempDir()

	content1 := `database: db1
triggers:
  trigger1:
    collection: users
    events:
      - create
    url: https://example.com/webhook1
`
	content2 := `database: db2
triggers:
  trigger2:
    collection: orders
    events:
      - update
    url: https://example.com/webhook2
`
	err := os.WriteFile(filepath.Join(tmpDir, "db1.yml"), []byte(content1), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tmpDir, "db2.yaml"), []byte(content2), 0644)
	require.NoError(t, err)

	result, err := LoadTriggersFromDir(tmpDir)
	assert.NoError(t, err)
	assert.Len(t, result, 2)
	assert.Len(t, result["db1"], 1)
	assert.Len(t, result["db2"], 1)
	assert.Equal(t, "trigger1", result["db1"][0].ID)
	assert.Equal(t, "trigger2", result["db2"][0].ID)
}

func TestLoadTriggersFromDir_MultipleTriggersSameFile(t *testing.T) {
	tmpDir := t.TempDir()
	content := `database: db1
triggers:
  trigger1:
    collection: users
    events:
      - create
    url: https://example.com/webhook1
  trigger2:
    collection: orders
    events:
      - update
    url: https://example.com/webhook2
`
	err := os.WriteFile(filepath.Join(tmpDir, "db1.yml"), []byte(content), 0644)
	require.NoError(t, err)

	result, err := LoadTriggersFromDir(tmpDir)
	assert.NoError(t, err)
	assert.Len(t, result, 1)
	assert.Len(t, result["db1"], 2)

	ids := make(map[string]bool)
	for _, tr := range result["db1"] {
		ids[tr.ID] = true
		assert.Equal(t, "db1", tr.Database) // All triggers should have file-level database
	}
	assert.True(t, ids["trigger1"])
	assert.True(t, ids["trigger2"])
}

func TestLoadTriggersFromDir_NotDirectory(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "file.yml")
	err := os.WriteFile(tmpFile, []byte("database: db1\ntriggers: {}"), 0644)
	require.NoError(t, err)

	_, err = LoadTriggersFromDir(tmpFile)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrNotDirectory)
}

func TestLoadTriggersFromDir_NotFound(t *testing.T) {
	_, err := LoadTriggersFromDir("/nonexistent/directory")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to stat directory")
}

func TestLoadTriggersFromDir_EmptyDatabase(t *testing.T) {
	tmpDir := t.TempDir()
	content := `triggers:
  trigger1:
    collection: users
    events:
      - create
    url: https://example.com/webhook
`
	err := os.WriteFile(filepath.Join(tmpDir, "missing_db.yml"), []byte(content), 0644)
	require.NoError(t, err)

	_, err = LoadTriggersFromDir(tmpDir)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrEmptyDatabase)
}

func TestLoadTriggersFromDir_DuplicateDatabase(t *testing.T) {
	tmpDir := t.TempDir()
	content1 := `database: db1
triggers:
  trigger1:
    collection: users
    events: [create]
    url: https://example.com/1
`
	content2 := `database: db1
triggers:
  trigger2:
    collection: orders
    events: [update]
    url: https://example.com/2
`
	err := os.WriteFile(filepath.Join(tmpDir, "file1.yml"), []byte(content1), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tmpDir, "file2.yml"), []byte(content2), 0644)
	require.NoError(t, err)

	_, err = LoadTriggersFromDir(tmpDir)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrDuplicateDatabase)
}

func TestLoadTriggersFromDir_InvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	content := `database: db1
triggers:
  trigger1:
    invalid: [unclosed
`
	err := os.WriteFile(filepath.Join(tmpDir, "invalid.yml"), []byte(content), 0644)
	require.NoError(t, err)

	_, err = LoadTriggersFromDir(tmpDir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse YAML trigger file")
}

func TestLoadTriggersFromDir_SkipsNonYAMLFiles(t *testing.T) {
	tmpDir := t.TempDir()
	yamlContent := `database: db1
triggers:
  trigger1:
    collection: users
    events: [create]
    url: https://example.com/webhook
`
	err := os.WriteFile(filepath.Join(tmpDir, "db1.yml"), []byte(yamlContent), 0644)
	require.NoError(t, err)
	// Create non-YAML files that should be ignored
	err = os.WriteFile(filepath.Join(tmpDir, "readme.txt"), []byte("ignore me"), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tmpDir, ".gitkeep"), []byte(""), 0644)
	require.NoError(t, err)

	result, err := LoadTriggersFromDir(tmpDir)
	assert.NoError(t, err)
	assert.Len(t, result, 1) // Only db1 should be loaded
}

func TestLoadTriggersFromDir_EmptyDirectory(t *testing.T) {
	tmpDir := t.TempDir()

	result, err := LoadTriggersFromDir(tmpDir)
	assert.NoError(t, err)
	assert.Len(t, result, 0)
}

func TestLoadTriggersFromDir_JSONFormat(t *testing.T) {
	tmpDir := t.TempDir()
	content := `{
	"database": "db1",
	"triggers": {
		"trigger1": {
			"collection": "users",
			"events": ["create"],
			"url": "https://example.com/webhook"
		}
	}
}`
	err := os.WriteFile(filepath.Join(tmpDir, "db1.json"), []byte(content), 0644)
	require.NoError(t, err)

	result, err := LoadTriggersFromDir(tmpDir)
	assert.NoError(t, err)
	assert.Len(t, result, 1)
	assert.Len(t, result["db1"], 1)
	assert.Equal(t, "trigger1", result["db1"][0].ID)
	assert.Equal(t, "db1", result["db1"][0].Database)
}

func TestLoadTriggersFromDir_FileRetryPolicyDefault(t *testing.T) {
	tmpDir := t.TempDir()
	content := `database: db1
retryPolicy:
  maxAttempts: 5
  initialBackoff: 2s
  maxBackoff: 30s
triggers:
  trigger1:
    collection: users
    events: [create]
    url: https://example.com/webhook1
  trigger2:
    collection: orders
    events: [update]
    url: https://example.com/webhook2
`
	err := os.WriteFile(filepath.Join(tmpDir, "db1.yml"), []byte(content), 0644)
	require.NoError(t, err)

	result, err := LoadTriggersFromDir(tmpDir)
	assert.NoError(t, err)
	assert.Len(t, result["db1"], 2)

	// Both triggers should inherit file-level retryPolicy
	for _, tr := range result["db1"] {
		assert.Equal(t, 5, tr.RetryPolicy.MaxAttempts)
		assert.Equal(t, Duration(2*time.Second), tr.RetryPolicy.InitialBackoff)
		assert.Equal(t, Duration(30*time.Second), tr.RetryPolicy.MaxBackoff)
	}
}

func TestLoadTriggersFromDir_TriggerRetryPolicyOverride(t *testing.T) {
	tmpDir := t.TempDir()
	content := `database: db1
retryPolicy:
  maxAttempts: 3
  initialBackoff: 1s
  maxBackoff: 10s
triggers:
  trigger1:
    collection: users
    events: [create]
    url: https://example.com/webhook1
  trigger2:
    collection: orders
    events: [update]
    url: https://example.com/webhook2
    retryPolicy:
      maxAttempts: 10
      initialBackoff: 500ms
      maxBackoff: 60s
`
	err := os.WriteFile(filepath.Join(tmpDir, "db1.yml"), []byte(content), 0644)
	require.NoError(t, err)

	result, err := LoadTriggersFromDir(tmpDir)
	assert.NoError(t, err)
	assert.Len(t, result["db1"], 2)

	// Find triggers by ID
	var trigger1, trigger2 *Trigger
	for _, tr := range result["db1"] {
		if tr.ID == "trigger1" {
			trigger1 = tr
		} else if tr.ID == "trigger2" {
			trigger2 = tr
		}
	}

	// trigger1 should inherit file-level retryPolicy
	require.NotNil(t, trigger1)
	assert.Equal(t, 3, trigger1.RetryPolicy.MaxAttempts)
	assert.Equal(t, Duration(1*time.Second), trigger1.RetryPolicy.InitialBackoff)
	assert.Equal(t, Duration(10*time.Second), trigger1.RetryPolicy.MaxBackoff)

	// trigger2 should use its own retryPolicy
	require.NotNil(t, trigger2)
	assert.Equal(t, 10, trigger2.RetryPolicy.MaxAttempts)
	assert.Equal(t, Duration(500*time.Millisecond), trigger2.RetryPolicy.InitialBackoff)
	assert.Equal(t, Duration(60*time.Second), trigger2.RetryPolicy.MaxBackoff)
}

func TestLoadTriggersFromDir_NoFileRetryPolicy(t *testing.T) {
	tmpDir := t.TempDir()
	content := `database: db1
triggers:
  trigger1:
    collection: users
    events: [create]
    url: https://example.com/webhook
    retryPolicy:
      maxAttempts: 7
      initialBackoff: 3s
      maxBackoff: 45s
`
	err := os.WriteFile(filepath.Join(tmpDir, "db1.yml"), []byte(content), 0644)
	require.NoError(t, err)

	result, err := LoadTriggersFromDir(tmpDir)
	assert.NoError(t, err)
	assert.Len(t, result["db1"], 1)

	// trigger should keep its own retryPolicy
	tr := result["db1"][0]
	assert.Equal(t, 7, tr.RetryPolicy.MaxAttempts)
	assert.Equal(t, Duration(3*time.Second), tr.RetryPolicy.InitialBackoff)
	assert.Equal(t, Duration(45*time.Second), tr.RetryPolicy.MaxBackoff)
}
