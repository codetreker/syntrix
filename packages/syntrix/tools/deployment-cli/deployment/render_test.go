package deployment

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"text/template"
	"time"

	pubsubconfig "github.com/codetreker/syntrix/internal/core/pubsub/config"

	runtimeconfig "github.com/codetreker/syntrix/internal/config"
	"github.com/codetreker/syntrix/internal/core/origin"
	"github.com/codetreker/syntrix/tools/deployment-cli/deployment/config"
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
		Mode: mode, ResourceMode: "operator", Namespace: "example", NamePrefix: "example", Image: "syntrix:development", StorageGiB: 10,
		Connections: config.Connections{MongoDBURI: "mongodb://mongo:27017/?replicaSet=rs0", MongoDBDatabase: "example", PostgresDSN: "postgres://developer:password@postgres:5432/example?sslmode=require"},
		Identity:    config.Identity{AdminUsername: "operator", AdminPassword: "Example_Admin123!"}, AllowedOrigins: []string{"https://example.test"},
		BootstrapResources: resources, Workloads: make(map[string]config.Workload),
	}
	if mode == "distributed" {
		c.Broker = &pubsubconfig.BrokerBinding{ResourceMode: "operator", SpecRevision: 1, Ownership: pubsubconfig.Ownership{InstallationID: "operator", InstanceID: "11111111111111111111111111111111", AllocationID: "22222222222222222222222222222222"}, StreamName: "EXAMPLE_TRIGGERS", SubjectPrefix: "EXAMPLE_TRIGGERS", ConsumerName: "EXAMPLE_DELIVERY", Policy: pubsubconfig.BrokerPolicy{Storage: "file", Replicas: 1, MaxBytes: 1073741824, MaxMessages: 100000, MaxAge: 24 * time.Hour}}
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
	manifest, err := RenderSource([]byte(minimalTemplate), c, Options{})
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
			model := buildModel(c, Options{Bootstrap: bootstrap})
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
	model := buildModel(fixtureConfig("standalone"), Options{})
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
		result, err := RenderSource([]byte(source), c, Options{})
		require.Error(t, err, source)
		assert.Empty(t, result)
	}
	for _, options := range []Options{{Bootstrap: true}, {WritesQuiesced: true}} {
		_, err := RenderSource([]byte(minimalTemplate), c, options)
		require.ErrorContains(t, err, "must be specified together")
	}
	c.Mode = "unsupported"
	_, err := RenderSource([]byte(minimalTemplate), c, Options{})
	require.ErrorContains(t, err, "mode")
	_, err = quote(make(chan int))
	require.Error(t, err)
	result, err := RenderSource([]byte("{{requireMode \"standalone\"}}"+minimalTemplate), fixtureConfig("standalone"), Options{Bootstrap: true, WritesQuiesced: true})
	require.NoError(t, err)
	assert.NotEmpty(t, result)
	_, err = RenderSource([]byte("apiVersion: v1\nkind: Secret\nmetadata: {name: secret}\nstringData: [private-credential"), fixtureConfig("standalone"), Options{})
	require.Error(t, err)
	assert.NotNil(t, errors.Unwrap(err))
	assert.NotContains(t, err.Error(), "private-credential")
}

func TestTemplateErrorsHideCredentialsAndPreserveCause(t *testing.T) {
	marker := "CRED_LEAK_FAKE_Password123!"
	c := fixtureConfig("standalone")
	c.Identity.AdminPassword = marker
	output, err := RenderSource([]byte("{{credentials}}\n{{range credentials}}value{{end}}"), c, Options{})
	require.Error(t, err)
	assert.Empty(t, output)
	assert.NotContains(t, err.Error(), marker)
	assert.NotContains(t, err.Error(), c.Connections.PostgresDSN)
	var execution template.ExecError
	require.ErrorAs(t, err, &execution)
	assert.Contains(t, execution.Error(), marker, "the actual template failure evaluated credentials")
	assert.Equal(t, execution, errors.Unwrap(err))
	parseMarker := "CRED_LEAK_FAKE_TEMPLATE_VALUE"
	_, err = RenderSource([]byte("{{"+parseMarker+"}}"), c, Options{})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), parseMarker)
	require.NotNil(t, errors.Unwrap(err))
	assert.Contains(t, errors.Unwrap(err).Error(), parseMarker)
}

func TestEmbeddedTemplatesPreserveExplicitSourceContract(t *testing.T) {
	for _, mode := range []string{"standalone", "distributed"} {
		c := fixtureConfig(mode)
		c.ResourceMode = "managed"
		if c.Broker != nil {
			c.Broker.ResourceMode = c.ResourceMode
			c.Broker.StreamName = "SYNTRIX_" + c.Broker.Ownership.InstanceID + "_TRIGGERS"
			c.Broker.SubjectPrefix = c.Broker.StreamName
			c.Broker.ConsumerName = c.Broker.StreamName + "_DELIVERY"
		}
		c.Image = "ghcr.io/codetreker/syntrix-server:261013.1"
		source := standaloneTemplate
		if mode == "distributed" {
			source = distributedTemplate
		}
		for _, options := range []Options{{}, {Bootstrap: true, WritesQuiesced: true}, {AcceptanceOnly: true}} {
			embedded, err := Render(c, options)
			require.NoError(t, err)
			explicit, err := RenderSource(source, c, options)
			require.NoError(t, err)
			assert.Equal(t, explicit, embedded)
			assert.NotContains(t, string(embedded), "@sha256:")
			assert.NotContains(t, string(embedded), ":latest")
			objects := renderedObjects(t, embedded)
			for _, object := range objects {
				if object["kind"] != "ConfigMap" {
					continue
				}
				data := []byte(object["data"].(map[string]any)["config.yml"].(string))
				var runtime map[string]any
				require.NoError(t, yaml.Unmarshal(data, &runtime))
				loaded := runtimeconfig.DefaultConfig()
				require.True(t, loaded.Server.AllowDevOrigin)
				require.NoError(t, yaml.Unmarshal(data, loaded))
				require.False(t, loaded.Server.AllowDevOrigin)
				require.False(t, loaded.Gateway.Realtime.AllowDevOrigin)
				require.NoError(t, (origin.Policy{AllowedOrigins: loaded.Server.AllowedOrigins, AllowDevelopment: loaded.Server.AllowDevOrigin}).Validate(true))
				require.NoError(t, (origin.Policy{AllowedOrigins: loaded.Gateway.Realtime.AllowedOrigins, AllowDevelopment: loaded.Gateway.Realtime.AllowDevOrigin}).Validate(true))
				assert.Equal(t, loaded.Server.AllowedOrigins, loaded.Gateway.Realtime.AllowedOrigins)
				assert.Equal(t, "managed", runtime["initialization"].(map[string]any)["mode"])
				assert.Equal(t, c.AllowedOrigins[0], runtime["server"].(map[string]any)["allowed_origins"].([]any)[0])
				assert.Equal(t, true, runtime["server"].(map[string]any)["enable_cors"])
				if runtime["deployment"].(map[string]any)["mode"] == "distributed" {
					trigger := runtime["trigger"].(map[string]any)
					broker := trigger["broker"].(map[string]any)
					assert.Equal(t, "managed", broker["resource_mode"])
					assert.Equal(t, c.Broker.StreamName, broker["stream_name"])
					assert.Equal(t, c.Broker.StreamName, trigger["evaluator"].(map[string]any)["stream_name"])
					assert.Equal(t, c.Broker.ConsumerName, trigger["delivery"].(map[string]any)["consumer_name"])
					assert.Equal(t, c.Broker.Policy.Storage, trigger["evaluator"].(map[string]any)["storage_type"])
					assert.Equal(t, c.Broker.Policy.Storage, trigger["delivery"].(map[string]any)["storage_type"])
				}
			}
		}
	}
	_, err := Render(fixtureConfig("standalone"), Options{Bootstrap: true, WritesQuiesced: true, AcceptanceOnly: true})
	require.ErrorContains(t, err, "mutually exclusive")
	c := fixtureConfig("standalone")
	c.Mode = "unknown"
	_, err = Render(c, Options{})
	require.ErrorContains(t, err, "mode")
}
