package authorization

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/codetreker/syntrix/internal/query"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockQueryService struct {
	query.Service
}

func TestNewEngine_ErrorPaths(t *testing.T) {
	t.Run("LoadRulesFromDir Error - Path Not Found", func(t *testing.T) {
		cfg := Config{
			RulesPath: "/non_existent_directory",
		}
		_, err := NewEngine(cfg, &mockQueryService{})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to load rules")
	})

	t.Run("LoadRulesFromDir Error - Invalid YAML", func(t *testing.T) {
		tmpDir := t.TempDir()
		err := os.WriteFile(filepath.Join(tmpDir, "bad.yml"), []byte("database: test\ninvalid: yaml: content: :"), 0644)
		require.NoError(t, err)

		cfg := Config{
			RulesPath: tmpDir,
		}
		_, err = NewEngine(cfg, &mockQueryService{})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to load rules")
	})

	t.Run("LoadRulesFromDir Error - Invalid CEL Expression", func(t *testing.T) {
		// CEL validation now happens at load time
		rulesContent := `database: default
match:
  /databases/default/documents/test:
    allow:
      read: "1 + 'a'"
`
		tmpDir := t.TempDir()
		err := os.WriteFile(filepath.Join(tmpDir, "default.yml"), []byte(rulesContent), 0644)
		require.NoError(t, err)

		cfg := Config{
			RulesPath: tmpDir,
		}
		_, err = NewEngine(cfg, &mockQueryService{})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid CEL expression")
	})
}

func TestEvaluate_NoRulesLoaded(t *testing.T) {
	// Empty directory means no rules
	tmpDir := t.TempDir()

	cfg := Config{
		RulesPath: tmpDir,
	}
	engine, err := NewEngine(cfg, &mockQueryService{})
	require.NoError(t, err)

	t.Run("No Rules For Database", func(t *testing.T) {
		req := Request{Auth: Authenticated{}}
		allowed, err := engine.Evaluate(context.Background(), "default", "/test", "read", req, nil)
		assert.NoError(t, err)
		assert.False(t, allowed)
	})
}
