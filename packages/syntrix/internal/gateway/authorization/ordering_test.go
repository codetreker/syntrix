package authorization

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatchOrdering(t *testing.T) {
	// Rule set with overlapping paths, nested under the default database prefix
	rulesYAML := `
database: default
rules_version: "1.0"
service: "test"
match:
  /databases/{database}/documents:
    match:
      /users/{uid}:
        allow:
          read: "false" # Wildcard should be overridden by concrete
      /users/admin:
        allow:
          read: "true" # Concrete path
      /posts/{pid}:
        allow:
          read: "false"
      /posts/special/featured:
        allow:
          read: "true" # Longer path
`

	tmpDir := t.TempDir()
	err := os.WriteFile(filepath.Join(tmpDir, "default.yml"), []byte(rulesYAML), 0644)
	require.NoError(t, err)

	engine, err := NewEngine(Config{RulesPath: tmpDir}, nil)
	require.NoError(t, err)

	ctx := context.Background()
	req := Request{}

	tests := []struct {
		name     string
		path     string
		expected bool
	}{
		{
			name:     "Concrete path /users/admin should match before /users/{uid}",
			path:     "/users/admin",
			expected: true,
		},
		{
			name:     "Wildcard path /users/other should match /users/{uid}",
			path:     "/users/other",
			expected: false,
		},
		{
			name:     "Longer path /posts/special/featured should match before /posts/{pid}",
			path:     "/posts/special/featured",
			expected: true,
		},
		{
			name:     "Shorter path /posts/other should match /posts/{pid}",
			path:     "/posts/other",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allowed, err := engine.Evaluate(ctx, "default", tt.path, "read", req, nil)
			assert.NoError(t, err)
			assert.Equal(t, tt.expected, allowed)
		})
	}
}
