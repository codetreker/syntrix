package deployment

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runtimeconfig "github.com/codetreker/syntrix/internal/config"
	"github.com/codetreker/syntrix/internal/deployment/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const minimalTemplate = `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ quote .ConfigName }}
  namespace: {{ quote .Namespace }}
data:
  config.yml: |
{{ runtimeConfig (index .Workloads 0).Role | indent 4 }}
---
apiVersion: v1
kind: Secret
metadata:
  name: {{ quote .CredentialsName }}
  namespace: {{ quote .Namespace }}
stringData:
  config.local.yml: |
{{ credentials | indent 4 }}
`

func fixtureConfig(mode string) config.Config {
	replicas := 1
	resources := config.Resources{Requests: config.Quantities{CPU: "250m", Memory: "256Mi"}, Limits: config.Quantities{CPU: "1", Memory: "1Gi"}}
	c := config.Config{
		Mode: mode, Namespace: "example", NamePrefix: "example", Image: "syntrix:development", StorageGiB: 10,
		Connections: config.Connections{MongoDBURI: "mongodb://mongo:27017/?replicaSet=rs0", MongoDBDatabase: "example", PostgresDSN: "postgres://developer:password@postgres:5432/example?sslmode=require"},
		Identity:    config.Identity{AdminUsername: "operator", AdminPassword: "Example_Admin123!"}, AllowedOrigins: []string{"https://example.test"},
		BootstrapResources: resources, Workloads: make(map[string]config.Workload),
	}
	if mode == "distributed" {
		c.Connections.NATSURL, c.SigningKeySecret = "nats://developer:password@nats:4222", "shared-signing-key"
	}
	for _, role := range c.Roles() {
		c.Workloads[role] = config.Workload{Name: "example-" + strings.ReplaceAll(role, "_", "-"), Replicas: &replicas, Resources: resources}
	}
	return c
}

func TestRenderEscapesCredentialsWithoutLeakingIntoPublicConfig(t *testing.T) {
	c := fixtureConfig("distributed")
	c.Identity.AdminPassword = "password\n---\nkind: Pod\nmetadata:\n  name: injected"
	c.Identity.AdminUsername = "#\n: account"
	manifest, err := Render([]byte(minimalTemplate), c, Options{})
	require.NoError(t, err)
	decoder := yaml.NewDecoder(bytes.NewReader(manifest))
	var public, secret map[string]any
	require.NoError(t, decoder.Decode(&public))
	require.NoError(t, decoder.Decode(&secret))
	var trailing any
	require.ErrorIs(t, decoder.Decode(&trailing), io.EOF)
	publicYAML := public["data"].(map[string]any)["config.yml"].(string)
	assert.NotContains(t, publicYAML, c.Identity.AdminPassword)
	assert.NotContains(t, publicYAML, c.Identity.AdminUsername)
	assert.NotContains(t, publicYAML, c.Connections.MongoDBURI)
	assert.NotContains(t, publicYAML, c.Connections.PostgresDSN)
	assert.NotContains(t, publicYAML, c.Connections.NATSURL)
	secretYAML := secret["stringData"].(map[string]any)["config.local.yml"].(string)
	var runtime runtimeconfig.Config
	require.NoError(t, yaml.Unmarshal([]byte(secretYAML), &runtime))
	assert.Equal(t, c.Identity.AdminUsername, runtime.Identity.Admin.Username)
	assert.Equal(t, c.Identity.AdminUsername, runtime.Identity.AuthN.AdminUsername)
	assert.Equal(t, c.Identity.AdminPassword, runtime.Identity.Admin.Password)
	assert.Equal(t, c.Connections.NATSURL, runtime.Trigger.NatsURL)
}

func TestRuntimeWiringAndBootstrapPaths(t *testing.T) {
	for _, mode := range []string{"standalone", "distributed"} {
		for _, bootstrap := range []bool{false, true} {
			c := fixtureConfig(mode)
			model := buildModel(c, bootstrap)
			assert.Equal(t, "example-bootstrap-config", model.BootstrapConfigName)
			assert.Equal(t, "example-data", model.StandalonePVCName)
			assert.Equal(t, "example-puller-data", model.PullerPVCName)
			assert.Equal(t, "example-indexer-data", model.IndexerPVCName)
			for i, workload := range model.Workloads {
				assert.Equal(t, c.Roles()[i], workload.Role)
				assert.Equal(t, mode == "distributed" && (workload.Role == "api" || workload.Role == "trigger_worker"), workload.HasKey)
				if bootstrap {
					assert.Zero(t, workload.Replicas)
				} else {
					assert.Equal(t, 1, workload.Replicas)
				}
				if workload.Role == "standalone" || workload.Role == "puller" || workload.Role == "indexer" {
					assert.True(t, workload.Stateful)
					assert.NotEmpty(t, workload.PVCName)
				} else {
					assert.False(t, workload.Stateful)
					assert.Empty(t, workload.PVCName)
				}
				data, err := model.runtimeConfig(workload.Role)
				require.NoError(t, err)
				var runtime runtimeconfig.Config
				require.NoError(t, yaml.Unmarshal([]byte(data), &runtime))
				assert.Equal(t, mode, string(runtime.Deployment.Mode))
				assert.Equal(t, "0.0.0.0", runtime.Server.Host)
				assert.Equal(t, 9000, runtime.Server.GRPCPort)
				assert.Equal(t, "triggers", runtime.Trigger.Evaluator.RulesPath)
				assert.Equal(t, "indexer/indexes.db", runtime.Indexer.Store.Path)
				assert.Equal(t, "puller/events", runtime.Puller.Buffer.Path)
				if mode == "distributed" {
					assert.Equal(t, "example-query:9000", runtime.Gateway.QueryServiceURL)
					assert.Equal(t, "example-streamer:9000", runtime.Gateway.StreamerServiceURL)
					assert.Equal(t, "example-indexer:9000", runtime.Query.IndexerAddr)
					assert.Equal(t, "example-puller:9000", runtime.Indexer.PullerAddr)
					assert.Equal(t, "example-puller:9000", runtime.Streamer.Server.PullerAddr)
					assert.Equal(t, "example-puller:9000", runtime.Trigger.Evaluator.PullerAddr)
				}
			}
			data, err := model.runtimeConfig("bootstrap")
			require.NoError(t, err)
			var runtime runtimeconfig.Config
			require.NoError(t, yaml.Unmarshal([]byte(data), &runtime))
			assert.Equal(t, "standalone", string(runtime.Deployment.Mode))
			assert.Equal(t, "indexer/indexes.db", runtime.Indexer.Store.Path)
			assert.Equal(t, "puller/events", runtime.Puller.Buffer.Path)
			if mode == "distributed" {
				assert.Equal(t, "/etc/syntrix/keys/auth_private.pem", runtime.Identity.AuthN.PrivateKeyFile)
			} else {
				assert.Equal(t, "/data/keys/auth_private.pem", runtime.Identity.AuthN.PrivateKeyFile)
			}
		}
	}
	model := buildModel(fixtureConfig("standalone"), false)
	_, err := model.runtimeConfig("unknown")
	require.Error(t, err)
}

func TestRenderRejectsIncompleteAndInvalidTemplates(t *testing.T) {
	c := fixtureConfig("standalone")
	for _, source := range []string{
		"", "{{", "{{ .MissingField }}", "{{requireMode \"distributed\"}}" + minimalTemplate,
		"{{ runtimeConfig \"unknown\" }}", "{{indent -1 \"text\"}}", "[",
		"apiVersion: v1\nkind: Pod\n", "apiVersion: v1\nkind: Pod\nmetadata: {name: 1}",
		"apiVersion: v1\nmetadata: {name: test}", "kind: Pod\nmetadata: {name: test}",
		"apiVersion: v1\nkind: Pod\nkind: Secret\nmetadata: {name: test}", minimalTemplate + "---\n",
	} {
		result, err := Render([]byte(source), c, Options{})
		require.Error(t, err, source)
		assert.Empty(t, result)
	}
	for _, options := range []Options{{Bootstrap: true}, {WritesQuiesced: true}} {
		_, err := Render([]byte(minimalTemplate), c, options)
		require.ErrorContains(t, err, "must be specified together")
	}
	c.Mode = "unsupported"
	_, err := Render([]byte(minimalTemplate), c, Options{})
	require.ErrorContains(t, err, "mode")
	_, err = quote(make(chan int))
	require.Error(t, err)
	result, err := Render([]byte("{{requireMode \"standalone\"}}"+minimalTemplate), fixtureConfig("standalone"), Options{Bootstrap: true, WritesQuiesced: true})
	require.NoError(t, err)
	assert.NotEmpty(t, result)
	_, err = Render([]byte("apiVersion: v1\nkind: Secret\nmetadata: {name: secret}\nstringData: [private-credential"), fixtureConfig("standalone"), Options{})
	require.Error(t, err)
	assert.NotNil(t, errors.Unwrap(err))
	assert.NotContains(t, err.Error(), "private-credential")
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

type shortWriter struct{}

func (shortWriter) Write(data []byte) (int, error) { return len(data) - 1, nil }

func createInputs(t *testing.T) (FileOptions, string) {
	t.Helper()
	dir := t.TempDir()
	options := FileOptions{TemplatePath: filepath.Join(dir, "template.yml"), ConfigPath: filepath.Join(dir, "config.yml"), OutputPath: filepath.Join(dir, "kubernetes.yml")}
	require.NoError(t, os.WriteFile(options.TemplatePath, []byte(minimalTemplate), 0600))
	data, err := yaml.Marshal(fixtureConfig("standalone"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(options.ConfigPath, data, 0600))
	return options, dir
}

func TestGenerateAtomicPrivateOutput(t *testing.T) {
	options, dir := createInputs(t)
	require.NoError(t, os.WriteFile(options.OutputPath, []byte("prior content"), 0644))
	var stdout bytes.Buffer
	require.NoError(t, Generate(options, &stdout))
	assert.Empty(t, stdout.String())
	manifest, err := os.ReadFile(options.OutputPath)
	require.NoError(t, err)
	assert.Contains(t, string(manifest), "kind: Secret")
	info, err := os.Stat(options.OutputPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
	require.NoError(t, os.WriteFile(options.TemplatePath, []byte("invalid: yaml"), 0600))
	require.Error(t, Generate(options, &stdout))
	unchanged, err := os.ReadFile(options.OutputPath)
	require.NoError(t, err)
	assert.Equal(t, manifest, unchanged)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 3)
}

func TestGenerateRejectsFractionalIntegersWithoutReplacingOutput(t *testing.T) {
	options, _ := createInputs(t)
	data, err := yaml.Marshal(fixtureConfig("distributed"))
	require.NoError(t, err)
	prior := []byte("existing private manifest")
	require.NoError(t, os.WriteFile(options.OutputPath, prior, 0600))
	for _, change := range []struct{ original, replacement, field string }{
		{"replicas: 1", "replicas: 1.5", "replicas"},
		{"replicas: 1", "replicas: -0.5", "replicas"},
		{"storage_gib: 10", "storage_gib: 10.5", "storage_gib"},
	} {
		malformed := strings.Replace(string(data), change.original, change.replacement, 1)
		require.NoError(t, os.WriteFile(options.ConfigPath, []byte(malformed), 0600))
		var stdout bytes.Buffer
		require.ErrorContains(t, Generate(options, &stdout), change.field+" must be a YAML integer")
		assert.Empty(t, stdout.String())
		unchanged, err := os.ReadFile(options.OutputPath)
		require.NoError(t, err)
		assert.Equal(t, prior, unchanged)
	}
}

func TestGenerateRejectsOutputInputAliases(t *testing.T) {
	for _, source := range []string{"template", "config"} {
		options, dir := createInputs(t)
		input := options.TemplatePath
		if source == "config" {
			input = options.ConfigPath
		}
		for _, alias := range []string{"same", "symlink", "hardlink"} {
			options.OutputPath = input
			if alias == "symlink" {
				options.OutputPath = filepath.Join(dir, source+"-symlink.yml")
				require.NoError(t, os.Symlink(input, options.OutputPath))
			}
			if alias == "hardlink" {
				options.OutputPath = filepath.Join(dir, source+"-hardlink.yml")
				require.NoError(t, os.Link(input, options.OutputPath))
			}
			err := Generate(options, &bytes.Buffer{})
			require.ErrorContains(t, err, "must not overwrite")
		}
	}
}

func TestGenerateStdoutAndFilesystemFailures(t *testing.T) {
	options, dir := createInputs(t)
	options.OutputPath = "-"
	var stdout bytes.Buffer
	require.NoError(t, Generate(options, &stdout))
	assert.Contains(t, stdout.String(), "kind: Secret")
	require.ErrorContains(t, Generate(options, nil), "stdout writer")
	require.ErrorContains(t, Generate(options, failingWriter{}), "broken pipe")
	require.ErrorIs(t, Generate(options, shortWriter{}), io.ErrShortWrite)
	require.NoError(t, os.WriteFile(options.TemplatePath, []byte("{{ .MissingField }}"), 0600))
	stdout.Reset()
	require.Error(t, Generate(options, &stdout))
	assert.Empty(t, stdout.String())
	require.ErrorContains(t, Generate(FileOptions{}, &stdout), "required")
	options.Options.Bootstrap = true
	require.ErrorContains(t, Generate(options, &stdout), "must be specified together")
	options.Options = Options{}
	options.ConfigPath = filepath.Join(dir, "missing.yml")
	require.ErrorContains(t, Generate(options, &stdout), "open deployment configuration")
	options.ConfigPath = filepath.Join(dir, "config.yml")
	options.TemplatePath = filepath.Join(dir, "missing-template.yml")
	require.ErrorContains(t, Generate(options, &stdout), "read Kubernetes template")
	options.TemplatePath = filepath.Join(dir, "template.yml")
	require.NoError(t, os.WriteFile(options.ConfigPath, []byte("unknown: credential-secret"), 0600))
	err := Generate(options, &stdout)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "credential-secret")
	require.ErrorContains(t, writeAtomic(filepath.Join(dir, "missing", "out.yml"), []byte("content")), "create temporary manifest")
	target := filepath.Join(dir, "directory")
	require.NoError(t, os.Mkdir(target, 0700))
	require.ErrorContains(t, writeAtomic(target, []byte("content")), "replace rendered manifest")
	leftovers, err := filepath.Glob(filepath.Join(dir, ".kubernetes-*.tmp"))
	require.NoError(t, err)
	assert.Empty(t, leftovers)
}

type persistenceProbe struct {
	writeError, syncError, closeError error
	shortWrite                        bool
	synced, closed                    bool
}

func (p *persistenceProbe) Write(data []byte) (int, error) {
	if p.writeError != nil {
		return 0, p.writeError
	}
	if p.shortWrite {
		return len(data) - 1, nil
	}
	return len(data), nil
}

func (p *persistenceProbe) Sync() error  { p.synced = true; return p.syncError }
func (p *persistenceProbe) Close() error { p.closed = true; return p.closeError }

func TestPersistenceFailuresPreventPublicationAndClose(t *testing.T) {
	writeErr, syncErr, closeErr := errors.New("write failed"), errors.New("sync failed"), errors.New("close failed")
	for _, test := range []struct {
		probe      persistenceProbe
		failure    error
		shouldSync bool
	}{
		{probe: persistenceProbe{writeError: writeErr}, failure: writeErr},
		{probe: persistenceProbe{shortWrite: true}, failure: io.ErrShortWrite},
		{probe: persistenceProbe{syncError: syncErr}, failure: syncErr, shouldSync: true},
		{probe: persistenceProbe{closeError: closeErr}, failure: closeErr, shouldSync: true},
		{probe: persistenceProbe{writeError: writeErr, closeError: closeErr}, failure: writeErr},
		{probe: persistenceProbe{}, shouldSync: true},
	} {
		result := persistManifest(&test.probe, []byte("sensitive manifest"))
		if test.failure != nil {
			require.ErrorIs(t, result, test.failure)
		} else {
			require.NoError(t, result)
		}
		if test.probe.closeError != nil {
			require.ErrorIs(t, result, closeErr)
		}
		assert.True(t, test.probe.closed)
		assert.Equal(t, test.shouldSync, test.probe.synced)
	}
}
