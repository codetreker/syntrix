package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunRejectsInvalidArgumentsWithoutManifest(t *testing.T) {
	for _, args := range [][]string{
		nil, {"unknown"}, {"render-kubernetes"}, {"render-kubernetes", "--unknown"},
		{"render-kubernetes", "positional"}, {"render-kubernetes", "--bootstrap=invalid"},
		{"render-kubernetes", "--template", "test", "--config", "test", "--output", "-", "--bootstrap"},
		{"deployment-contract", "extra"}, {"deployment-contract", "--help"},
	} {
		var stdout, stderr bytes.Buffer
		require.Error(t, run(args, &stdout, &stderr))
		assert.Empty(t, stdout.String())
	}
}

func TestRunDeploymentContractIsBoundedAndSideEffectFree(t *testing.T) {
	var stdout, stderr bytes.Buffer
	require.NoError(t, run([]string{"deployment-contract"}, &stdout, &stderr))
	assert.Empty(t, stderr.String())
	assert.Less(t, stdout.Len(), 1024)
	decoder := json.NewDecoder(&stdout)
	var contract struct {
		Contract  int      `json:"managed_runtime_contract"`
		Version   string   `json:"version"`
		GitCommit string   `json:"git_commit"`
		Modes     []string `json:"modes"`
		Phases    []string `json:"render_phases"`
	}
	decoder.DisallowUnknownFields()
	require.NoError(t, decoder.Decode(&contract))
	assert.Equal(t, 1, contract.Contract)
	assert.Equal(t, "development", contract.Version)
	assert.Equal(t, "unknown", contract.GitCommit)
	assert.Equal(t, []string{"standalone", "distributed"}, contract.Modes)
	assert.Equal(t, []string{"bootstrap", "acceptance", "full"}, contract.Phases)
	var trailing any
	require.ErrorIs(t, decoder.Decode(&trailing), io.EOF)
	writeError := errors.New("writer failed")
	require.ErrorIs(t, run([]string{"deployment-contract"}, contractWriter{err: writeError}, &stderr), writeError)
	require.ErrorIs(t, run([]string{"deployment-contract"}, contractWriter{short: true}, &stderr), io.ErrShortWrite)
	oldVersion, oldCommit := version, gitCommit
	t.Cleanup(func() { version, gitCommit = oldVersion, oldCommit })
	for _, field := range []*string{&version, &gitCommit} {
		*field = strings.Repeat("v", 65)
		stdout.Reset()
		require.ErrorContains(t, run([]string{"deployment-contract"}, &stdout, &stderr), "exceeds")
		assert.Empty(t, stdout.String())
		version, gitCommit = oldVersion, oldCommit
	}
}

type contractWriter struct {
	err   error
	short bool
}

func (w contractWriter) Write(data []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if w.short {
		return len(data) - 1, nil
	}
	return len(data), nil
}

func TestRunAcceptanceRendersWriterHold(t *testing.T) {
	for _, mode := range []string{"standalone", "distributed"} {
		base := filepath.Join("..", "..", "deployment", mode)
		args := []string{"render-kubernetes", "--template", filepath.Join(base, "kubernetes.template.yml"), "--config", filepath.Join(base, "values.example.yml"), "--output", "-", "--acceptance-only"}
		var stdout, stderr bytes.Buffer
		require.NoError(t, run(args, &stdout, &stderr))
		assert.Contains(t, stdout.String(), "--initialization-acceptance")
		assert.NotContains(t, stdout.String(), "kind: Pod\n")
		stdout.Reset()
		require.ErrorContains(t, run(append(args, "--bootstrap", "--writes-quiesced"), &stdout, &stderr), "mutually exclusive")
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

func TestCLITemplateFailureDoesNotPrintCredentials(t *testing.T) {
	marker := "CRED_LEAK_FAKE_Password123!"
	values, err := os.ReadFile(filepath.Join("..", "..", "deployment", "standalone", "values.example.yml"))
	require.NoError(t, err)
	values = bytes.ReplaceAll(values, []byte("CHANGE_ME_Admin123!"), []byte(marker))
	require.Contains(t, string(values), marker)
	dir := t.TempDir()
	configPath, templatePath, outputPath := filepath.Join(dir, "values.yml"), filepath.Join(dir, "template.yml"), filepath.Join(dir, "output.yml")
	require.NoError(t, os.WriteFile(configPath, values, 0600))
	require.NoError(t, os.WriteFile(templatePath, []byte("{{credentials}}\n{{range credentials}}value{{end}}"), 0600))
	prior := []byte("prior private manifest")
	require.NoError(t, os.WriteFile(outputPath, prior, 0600))
	for _, output := range []string{outputPath, "-"} {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, "go", "run", ".", "render-kubernetes", "--template", templatePath, "--config", configPath, "--output", output)
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		require.Error(t, err)
		require.NoError(t, ctx.Err(), "CLI validation must complete within its timeout")
		var exit *exec.ExitError
		require.ErrorAs(t, err, &exit)
		assert.Equal(t, 1, exit.ExitCode())
		assert.Empty(t, stdout.String())
		assert.Contains(t, stderr.String(), "render Kubernetes template: execution failed")
		assert.NotContains(t, stderr.String(), marker)
		assert.NotContains(t, strings.ToLower(stderr.String()), "password")
		unchanged, err := os.ReadFile(outputPath)
		require.NoError(t, err)
		assert.Equal(t, prior, unchanged)
	}
}
