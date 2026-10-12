package main

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunRejectsInvalidArgumentsWithoutManifest(t *testing.T) {
	for _, args := range [][]string{
		nil, {"unknown"}, {"render-kubernetes"}, {"render-kubernetes", "--unknown"},
		{"render-kubernetes", "positional"}, {"render-kubernetes", "--bootstrap=invalid"},
		{"render-kubernetes", "--template", "test", "--config", "test", "--output", "-", "--bootstrap"},
	} {
		var stdout, stderr bytes.Buffer
		require.Error(t, run(args, &stdout, &stderr))
		assert.Empty(t, stdout.String())
	}
}

func TestRunHelpDoesNotRequireFiles(t *testing.T) {
	var stdout, stderr bytes.Buffer
	require.NoError(t, run([]string{"render-kubernetes", "--help"}, &stdout, &stderr))
	assert.Empty(t, stdout.String())
	assert.Contains(t, stderr.String(), "Usage of render-kubernetes:")
	assert.Contains(t, stderr.String(), "-template")
}

func TestRunRendersCheckedInConfigurations(t *testing.T) {
	for _, mode := range []string{"standalone", "distributed"} {
		for _, bootstrap := range []bool{false, true} {
			base := filepath.Join("..", "..", "deployment", mode)
			args := []string{"render-kubernetes", "--template", filepath.Join(base, "kubernetes.template.yml"), "--config", filepath.Join(base, "values.example.yml"), "--output", "-"}
			if bootstrap {
				args = append(args, "--bootstrap", "--writes-quiesced")
			}
			var stdout, stderr bytes.Buffer
			require.NoError(t, run(args, &stdout, &stderr))
			assert.Contains(t, stdout.String(), "kind: Deployment")
			assert.Contains(t, stdout.String(), "kind: Secret")
			assert.Equal(t, bootstrap, bytes.Contains(stdout.Bytes(), []byte("kind: Pod\n")))
		}
	}
}
