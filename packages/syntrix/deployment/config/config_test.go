package config

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/origin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func validConfig(mode string) Config {
	replicas := 1
	resources := Resources{Requests: Quantities{CPU: "250m", Memory: "256Mi"}, Limits: Quantities{CPU: "1", Memory: "1Gi"}}
	c := Config{
		Mode: mode, ResourceMode: "operator", Namespace: "example", NamePrefix: "example", Image: "syntrix:development", StorageGiB: 10,
		Connections: Connections{MongoDBURI: "mongodb://db:27017/?replicaSet=rs0", MongoDBDatabase: "example", PostgresDSN: "postgres://developer:password@postgres:5432/example?sslmode=require"},
		Identity:    Identity{AdminUsername: "operator", AdminPassword: "Example_Admin123!"}, AllowedOrigins: []string{"https://example.test"},
		BootstrapResources: resources, Workloads: make(map[string]Workload),
	}
	if mode == "distributed" {
		c.Broker = &BrokerBinding{ResourceMode: "operator", SpecRevision: 1, Ownership: Ownership{InstallationID: "operator", InstanceID: "11111111111111111111111111111111", AllocationID: "22222222222222222222222222222222"}, StreamName: "EXAMPLE_TRIGGERS", SubjectPrefix: "EXAMPLE_TRIGGERS", ConsumerName: "EXAMPLE_DELIVERY", Policy: BrokerPolicy{Storage: "file", Replicas: 1, MaxBytes: 1073741824, MaxMessages: 100000, MaxAge: 24 * time.Hour}}
		c.Connections.NATSURL = "nats://nats:4222"
		c.SigningKeySecret = "shared-signing-key"
	}
	for _, role := range c.Roles() {
		c.Workloads[role] = Workload{Name: "example-" + strings.ReplaceAll(role, "_", "-"), Replicas: &replicas, Resources: resources}
	}
	return c
}

func TestLoadStrictSingleDocument(t *testing.T) {
	data, err := yaml.Marshal(validConfig("distributed"))
	require.NoError(t, err)
	loaded, err := Load(strings.NewReader(string(data)))
	require.NoError(t, err)
	assert.Equal(t, validConfig("distributed"), loaded)
	for _, malformed := range []string{
		"", "mode: [secret-password]", "password: secret-password", "mode: standalone\nmode: distributed",
		string(data) + "---\nmode: standalone\n", string(data) + "---\n", string(data) + "\nunknown: secret-password\n", string(data) + "\n[", strings.Replace(string(data), "mode: distributed", "mode: unknown", 1),
	} {
		t.Run(malformed[:min(len(malformed), 35)], func(t *testing.T) {
			_, err := Load(strings.NewReader(malformed))
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "secret-password")
		})
	}
}

type failingReader struct{ err error }

func (reader failingReader) Read([]byte) (int, error) { return 0, reader.err }

func TestLoadPreservesReadErrorWithoutCredentialDisclosure(t *testing.T) {
	cause := errors.New("reader failure containing credential-secret")
	_, err := Load(failingReader{err: cause})
	require.ErrorIs(t, err, cause)
	assert.NotContains(t, err.Error(), "credential-secret")
	data, marshalErr := yaml.Marshal(validConfig("standalone"))
	require.NoError(t, marshalErr)
	_, err = Load(strings.NewReader(string(data) + "---\nunknown: [secret-password"))
	require.Error(t, err)
	assert.NotNil(t, errors.Unwrap(err))
	assert.NotContains(t, err.Error(), "secret-password")
}

func TestLoadRequiresIntegerSyntaxForReplicasAndStorage(t *testing.T) {
	data, err := yaml.Marshal(validConfig("standalone"))
	require.NoError(t, err)
	base := string(data)
	for _, field := range []string{"replicas", "storage_gib"} {
		old := "replicas: 1"
		if field == "storage_gib" {
			old = "storage_gib: 10"
		}
		for _, value := range []string{"1.5", "-0.5", "1.0", "1e0", `"1"`, "true", "null", "[1]"} {
			t.Run(field+"/"+value, func(t *testing.T) {
				_, err := Load(strings.NewReader(strings.Replace(base, old, field+": "+value, 1)))
				require.ErrorContains(t, err, field+" must be a YAML integer")
			})
		}
	}
	for _, integer := range []string{"1", "!!int 1", "0x1", "0o1", "+1"} {
		_, err := Load(strings.NewReader(strings.Replace(base, "replicas: 1", "replicas: "+integer, 1)))
		require.NoError(t, err, integer)
	}
	for _, malformed := range []string{
		strings.Replace(base, "replicas: 1", "<<: {replicas: 1.5}", 1),
		strings.Replace(strings.Replace(base, "image: syntrix:development", "image: &fraction 1.5", 1), "replicas: 1", "replicas: *fraction", 1),
		strings.Replace(strings.Replace(base, "image: syntrix:development", "image: &fraction -0.5", 1), "replicas: 1", "replicas: *fraction", 1),
		strings.Replace(strings.Replace(base, "image: syntrix:development", "image: &replica-field replicas", 1), "replicas: 1", "*replica-field : 1.5", 1),
	} {
		_, err := Load(strings.NewReader(malformed))
		require.ErrorContains(t, err, "replicas must be a YAML integer")
	}
	_, err = Load(strings.NewReader(strings.Replace(strings.Replace(base, "image: syntrix:development", "image: &fraction 1.5", 1), "storage_gib: 10", "storage_gib: *fraction", 1)))
	require.ErrorContains(t, err, "storage_gib must be a YAML integer")
	for _, valid := range []string{
		strings.Replace(base, "replicas: 1", "<<: {replicas: 1}", 1),
		strings.Replace(strings.Replace(base, "storage_gib: 10", "storage_gib: &integer 1", 1), "replicas: 1", "replicas: *integer", 1),
	} {
		_, err := Load(strings.NewReader(valid))
		require.NoError(t, err)
	}
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*Config)
		field  string
	}{
		{"mode", func(c *Config) { c.Mode = "unsupported" }, "mode"},
		{"resource-mode", func(c *Config) { c.ResourceMode = "" }, "resource_mode"},
		{"broker-mode-mismatch", func(c *Config) { c.ResourceMode = "managed" }, "broker.resource_mode"},
		{"namespace", func(c *Config) { c.Namespace = "Invalid" }, "namespace"},
		{"namespace-leading-digit", func(c *Config) { c.Namespace = "1example" }, "namespace"},
		{"prefix", func(c *Config) { c.NamePrefix = "" }, "name_prefix"},
		{"prefix-derived-length", func(c *Config) { c.NamePrefix = strings.Repeat("p", 51) }, "derived resource"},
		{"image", func(c *Config) { c.Image = "bad image" }, "image"},
		{"broker-required", func(c *Config) { c.Broker = nil }, "broker"},
		{"broker-invalid", func(c *Config) { c.Broker.SpecRevision = 0 }, "broker"},
		{"storage-size", func(c *Config) { c.StorageGiB = 0 }, "storage_gib"},
		{"storage-class", func(c *Config) { c.StorageClass = "Invalid" }, "storage_class"},
		{"bootstrap-resources", func(c *Config) { c.BootstrapResources.Requests.CPU = "0" }, "bootstrap_resources"},
		{"mongodb-uri", func(c *Config) { c.Connections.MongoDBURI = "mongodb://secret-password@" }, "connections.mongodb_uri"},
		{"mongodb-scheme", func(c *Config) { c.Connections.MongoDBURI = "https://mongo" }, "connections.mongodb_uri"},
		{"mongodb-database", func(c *Config) { c.Connections.MongoDBDatabase = "invalid.database" }, "connections.mongodb_database"},
		{"postgres", func(c *Config) { c.Connections.PostgresDSN = "postgres://secret-password@%" }, "connections.postgres_dsn"},
		{"postgres-fragment", func(c *Config) { c.Connections.PostgresDSN = "postgres://db/example#secret-password" }, "connections.postgres_dsn"},
		{"nats", func(c *Config) { c.Connections.NATSURL = "https://secret-password@nats" }, "connections.nats_url"},
		{"key-required", func(c *Config) { c.SigningKeySecret = "" }, "signing_key_secret"},
		{"key-invalid", func(c *Config) { c.SigningKeySecret = "Invalid" }, "signing_key_secret"},
		{"key-collision", func(c *Config) { c.SigningKeySecret = c.NamePrefix + "-credentials" }, "signing_key_secret"},
		{"admin-user", func(c *Config) { c.Identity.AdminUsername = "" }, "identity.admin_username"},
		{"admin-password", func(c *Config) { c.Identity.AdminPassword = "" }, "identity.admin_password"},
		{"origins-required", func(c *Config) { c.AllowedOrigins = nil }, "allowed_origins"},
		{"origin-auth", func(c *Config) { c.AllowedOrigins = []string{"https://secret-password@example.test"} }, "allowed_origins"},
		{"origin-path", func(c *Config) { c.AllowedOrigins = []string{"https://example.test/"} }, "allowed_origins"},
		{"origin-query", func(c *Config) { c.AllowedOrigins = []string{"https://example.test?"} }, "allowed_origins"},
		{"origin-fragment", func(c *Config) { c.AllowedOrigins = []string{"https://example.test#fragment"} }, "allowed_origins"},
		{"origin-scheme", func(c *Config) { c.AllowedOrigins = []string{"ftp://example.test"} }, "allowed_origins"},
		{"origin-malformed", func(c *Config) { c.AllowedOrigins = []string{"https://%"} }, "allowed_origins"},
		{"roles-extra", func(c *Config) { c.Workloads["unknown"] = c.Workloads["api"] }, "workloads"},
		{"roles-wrong", func(c *Config) { c.Workloads["unknown"] = c.Workloads["api"]; delete(c.Workloads, "api") }, "workloads.api"},
		{"name-required", func(c *Config) { w := c.Workloads["api"]; w.Name = ""; c.Workloads["api"] = w }, "workloads.api.name"},
		{"service-name-leading-digit", func(c *Config) { w := c.Workloads["api"]; w.Name = "1service"; c.Workloads["api"] = w }, "workloads.api.name"},
		{"name-too-long", func(c *Config) { w := c.Workloads["api"]; w.Name = strings.Repeat("x", 53); c.Workloads["api"] = w }, "workloads.api.name"},
		{"name-collision", func(c *Config) { w := c.Workloads["query"]; w.Name = c.Workloads["api"].Name; c.Workloads["query"] = w }, "collides"},
		{"config-collision", func(c *Config) {
			w := c.Workloads["query"]
			w.Name = c.Workloads["api"].Name + "-config"
			c.Workloads["query"] = w
		}, "collides"},
		{"config-collision-earlier-workload", func(c *Config) {
			w := c.Workloads["api"]
			w.Name = c.Workloads["query"].Name + "-config"
			c.Workloads["api"] = w
		}, "colliding ConfigMap"},
		{"replicas-required", func(c *Config) { w := c.Workloads["api"]; w.Replicas = nil; c.Workloads["api"] = w }, "replicas"},
		{"replicas-negative", func(c *Config) { w := c.Workloads["api"]; n := -1; w.Replicas = &n; c.Workloads["api"] = w }, "replicas"},
		{"replicas-overflow", func(c *Config) {
			w := c.Workloads["api"]
			n := int(int64(2147483648))
			w.Replicas = &n
			c.Workloads["api"] = w
		}, "replicas"},
		{"replicas-singleton", func(c *Config) { w := c.Workloads["puller"]; n := 2; w.Replicas = &n; c.Workloads["puller"] = w }, "singleton"},
		{"resources", func(c *Config) { w := c.Workloads["api"]; w.Resources.Limits.Memory = "bad"; c.Workloads["api"] = w }, "workloads.api.resources"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := validConfig("distributed")
			test.modify(&c)
			err := c.Validate()
			require.ErrorContains(t, err, test.field)
			assert.NotContains(t, err.Error(), "secret-password")
		})
	}
	for _, mode := range []string{"standalone", "distributed"} {
		c := validConfig(mode)
		c.StorageClass = "fast-storage"
		require.NoError(t, c.Validate())
		for role, w := range c.Workloads {
			n := 0
			w.Replicas = &n
			c.Workloads[role] = w
		}
		require.NoError(t, c.Validate())
	}
	c := validConfig("standalone")
	c.Broker = validConfig("distributed").Broker
	require.ErrorContains(t, c.Validate(), "broker is only supported")
	c.Broker = nil
	c.SigningKeySecret = "shared-key"
	c.Connections.NATSURL = "tls://nats:4222"
	c.Connections.MongoDBURI = "mongodb+srv://mongo.example.test/example"
	c.Connections.PostgresDSN = "postgresql://db/example"
	require.NoError(t, c.Validate())
}

func TestDeploymentOriginsAgreeWithRuntimePolicy(t *testing.T) {
	for _, resourceMode := range []string{"operator", "managed"} {
		for _, test := range []struct {
			value string
			valid bool
		}{
			{"https://example.test", true},
			{"http://example.test:80", true},
			{"https://example.test:65535", true},
			{"http://[::1]:8080", true},
			{"https://[2001:db8::1]", true},
			{"http://*", false},
			{"https://*.example.test", false},
			{"*", false},
			{"https://example.test:0", false},
			{"https://example.test:65536", false},
			{"https://example.test:", false},
			{"https://example.test:port", false},
			{"http://[not-ipv6]", false},
		} {
			t.Run(resourceMode+"/"+test.value, func(t *testing.T) {
				c := validConfig("standalone")
				c.ResourceMode, c.AllowedOrigins = resourceMode, []string{test.value}
				policyError := (origin.Policy{AllowedOrigins: c.AllowedOrigins}).Validate(resourceMode == "managed")
				err := c.Validate()
				if test.valid {
					require.NoError(t, policyError)
					require.NoError(t, err)
				} else {
					require.Error(t, policyError)
					require.ErrorContains(t, err, "allowed_origins")
					assert.Equal(t, policyError.Error(), errors.Unwrap(err).Error())
				}
			})
		}
	}
}

func TestResourcesExactQuantities(t *testing.T) {
	for _, cpu := range []string{"1m", "1000m", "0.001", "1", "123.456"} {
		r := Resources{Requests: Quantities{CPU: cpu, Memory: "1"}, Limits: Quantities{CPU: cpu, Memory: "1"}}
		require.NoError(t, r.Validate(), cpu)
	}
	for _, memory := range []string{"1", "1k", "1M", "1G", "1T", "1P", "1E", "1Ki", "1Mi", "1Gi", "1Ti", "1Pi", "1Ei"} {
		r := Resources{Requests: Quantities{CPU: "1m", Memory: memory}, Limits: Quantities{CPU: "1m", Memory: memory}}
		require.NoError(t, r.Validate(), memory)
	}
	for _, cpu := range []string{"", "0", "0m", "0.000", "-1m", "1.0001", "0.1m", "+1", "1e3", "1n", "1u", ".5"} {
		r := Resources{Requests: Quantities{CPU: cpu, Memory: "1"}, Limits: Quantities{CPU: "1", Memory: "1"}}
		require.ErrorContains(t, r.Validate(), "requests.cpu", cpu)
		r.Requests.CPU, r.Limits.CPU = "1m", cpu
		require.ErrorContains(t, r.Validate(), "limits.cpu", cpu)
	}
	for _, memory := range []string{"", "0", "0Mi", "-1Mi", "1.5Gi", "1m", "1e3", "1KB", "1K", "1ki"} {
		r := Resources{Requests: Quantities{CPU: "1m", Memory: memory}, Limits: Quantities{CPU: "1", Memory: "1Gi"}}
		require.ErrorContains(t, r.Validate(), "requests.memory", memory)
		r.Requests.Memory, r.Limits.Memory = "1", memory
		require.ErrorContains(t, r.Validate(), "limits.memory", memory)
	}
	for _, r := range []Resources{
		{Requests: Quantities{CPU: "1001m", Memory: "1"}, Limits: Quantities{CPU: "1", Memory: "1"}},
		{Requests: Quantities{CPU: "1m", Memory: "1Gi"}, Limits: Quantities{CPU: "1m", Memory: "1000M"}},
		{Requests: Quantities{CPU: "1m", Memory: "9007199254740993"}, Limits: Quantities{CPU: "1m", Memory: "9007199254740992"}},
	} {
		require.ErrorContains(t, r.Validate(), "must not exceed")
	}
	r := Resources{Requests: Quantities{CPU: "0.001", Memory: "1024Mi"}, Limits: Quantities{CPU: "1m", Memory: "1Gi"}}
	require.NoError(t, r.Validate())
}
