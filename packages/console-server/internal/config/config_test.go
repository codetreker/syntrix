package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	authconfig "github.com/codetreker/syntrix/console-server/internal/auth/config"
	dbconfig "github.com/codetreker/syntrix/console-server/internal/database/config"
	provisionconfig "github.com/codetreker/syntrix/console-server/internal/provisioning/config"
	serverconfig "github.com/codetreker/syntrix/console-server/internal/server/config"
	workloadconfig "github.com/codetreker/syntrix/console-server/internal/workload/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validConfiguration = `database:
  dsn: postgres://developer:password@localhost/console?sslmode=disable
auth:
  developers:
    - id: dev-alice
      username: alice
      password: configured-password
server:
  public_origin: https://console.example
provisioning:
  mode: in_cluster
  namespace: console-tests
  installation_id: console-development
workload:
  templates:
    - id: http-test
      image: example/http-test:1
      port: 8080
      cpu_request: 25m
      cpu_limit: 100m
      memory_request: 32Mi
      memory_limit: 64Mi
      probe_path: /
`

func writeConfiguration(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "console.yml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0600))
	return path
}

func TestLoadComposesModuleDefaultsAndEnvironmentDSN(t *testing.T) {
	t.Setenv("CONSOLE_POSTGRES_DSN", "postgres://other:replacement@localhost/console?sslmode=disable")
	cfg, err := Load(writeConfiguration(t, validConfiguration))
	require.NoError(t, err)
	assert.Equal(t, os.Getenv("CONSOLE_POSTGRES_DSN"), cfg.Database.DSN)
	assert.IsType(t, dbconfig.Config{}, cfg.Database)
	assert.IsType(t, authconfig.Config{}, cfg.Auth)
	assert.IsType(t, serverconfig.Config{}, cfg.Server)
	assert.IsType(t, provisionconfig.Config{}, cfg.Provisioning)
	assert.IsType(t, workloadconfig.Config{}, cfg.Workload)
	assert.Equal(t, dbconfig.DefaultConfig().MaxOpenConnections, cfg.Database.MaxOpenConnections)
	assert.Equal(t, authconfig.DefaultConfig().SessionTTL, cfg.Auth.SessionTTL)
	assert.Equal(t, serverconfig.DefaultConfig().ReadTimeout, cfg.Server.ReadTimeout)
	assert.Equal(t, provisionconfig.DefaultConfig().RequestTimeout, cfg.Provisioning.RequestTimeout)
	assert.Equal(t, workloadconfig.DefaultConfig().OperationTimeout, cfg.Workload.OperationTimeout)
}

func TestLoadResolvesKubeconfigRelativeToConfigurationFile(t *testing.T) {
	t.Setenv("CONSOLE_POSTGRES_DSN", "")
	content := strings.Replace(validConfiguration, "  mode: in_cluster", "  mode: kubeconfig\n  kubeconfig: kubernetes/config.yml\n  context: explicit-test-context", 1)
	path := writeConfiguration(t, content)
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(filepath.Dir(path), "kubernetes", "config.yml"), cfg.Provisioning.Kubeconfig)
	assert.Equal(t, "explicit-test-context", cfg.Provisioning.Context)
}

func TestLoadRejectsUnknownOrMultipleYAMLDocuments(t *testing.T) {
	t.Setenv("CONSOLE_POSTGRES_DSN", "")
	for _, test := range []struct{ name, content string }{
		{"unknown root field", validConfiguration + "misspelled_module: true\n"},
		{"unknown module field", strings.Replace(validConfiguration, "server:\n", "server:\n  unknown: true\n", 1)},
		{"second document", validConfiguration + "---\nserver:\n  public_origin: http://untrusted.example\n"},
		{"malformed trailing document", validConfiguration + "---\ninvalid: [\n"},
		{"malformed document", "server: ["},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(writeConfiguration(t, test.content))
			assert.Error(t, err)
		})
	}
}

func TestLoadValidatesEveryModule(t *testing.T) {
	t.Setenv("CONSOLE_POSTGRES_DSN", "")
	for _, test := range []struct{ name, content, message string }{
		{"database", strings.Replace(validConfiguration, "  dsn: postgres://developer:password@localhost/console?sslmode=disable", "  dsn: ''", 1), "database.dsn"},
		{"auth", strings.Replace(validConfiguration, "      password: configured-password", "      password: ''", 1), "password must"},
		{"server", strings.Replace(validConfiguration, "https://console.example", "http://console.example", 1), "HTTPS"},
		{"provisioning", strings.Replace(validConfiguration, "  mode: in_cluster", "  mode: kubeconfig", 1), "explicit file and context"},
		{"workload", strings.Replace(validConfiguration, "      port: 8080", "      port: 0", 1), "valid port"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(writeConfiguration(t, test.content))
			assert.ErrorContains(t, err, test.message)
		})
	}
}

func TestLoadExplicitDurationsAndMissingFile(t *testing.T) {
	t.Setenv("CONSOLE_POSTGRES_DSN", "")
	content := strings.Replace(validConfiguration, "auth:\n", "auth:\n  session_ttl: 4h\n", 1)
	cfg, err := Load(writeConfiguration(t, content))
	require.NoError(t, err)
	assert.Equal(t, 4*time.Hour, cfg.Auth.SessionTTL)
	_, err = Load(filepath.Join(t.TempDir(), "absent.yml"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}
