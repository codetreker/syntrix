package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	authconfig "github.com/codetreker/syntrix/console-server/internal/auth/config"
	"github.com/codetreker/syntrix/console-server/internal/config"
	dbconfig "github.com/codetreker/syntrix/console-server/internal/database/config"
	provisionconfig "github.com/codetreker/syntrix/console-server/internal/provisioning/config"
	serverconfig "github.com/codetreker/syntrix/console-server/internal/server/config"
	workloadconfig "github.com/codetreker/syntrix/console-server/internal/workload/config"
	"github.com/codetreker/syntrix/console-server/internal/workload/types"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func runtimeLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func runtimeDatabase(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dsn := os.Getenv("CONSOLE_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("CONSOLE_TEST_POSTGRES_DSN is required for PostgreSQL composition tests")
	}
	admin, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, admin.Close()) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, admin.PingContext(ctx))
	var unique [8]byte
	_, err = rand.Read(unique[:])
	require.NoError(t, err)
	schema := "console_runtime_" + hex.EncodeToString(unique[:])
	_, err = admin.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(schema))
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := admin.ExecContext(cleanup, "DROP SCHEMA "+pq.QuoteIdentifier(schema)+" CASCADE")
		assert.NoError(t, err)
	})
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		parsed, err := url.Parse(dsn)
		require.NoError(t, err)
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		dsn = parsed.String()
	} else {
		dsn += " search_path=" + schema
	}
	return admin, dsn
}

func freeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	return address
}

func runtimeConfiguration(t *testing.T, dsn, address string) config.Config {
	t.Helper()
	kubernetes := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("empty runtime unexpectedly accessed Kubernetes: %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	t.Cleanup(kubernetes.Close)
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig.yml")
	require.NoError(t, os.WriteFile(kubeconfig, []byte(`apiVersion: v1
kind: Config
clusters:
  - name: test-cluster
    cluster:
      server: `+kubernetes.URL+`
contexts:
  - name: test-context
    context:
      cluster: test-cluster
      user: test-user
users:
  - name: test-user
    user: {}
current-context: test-context
`), 0600))
	cfg := config.Config{Database: dbconfig.DefaultConfig(), Auth: authconfig.DefaultConfig(), Server: serverconfig.DefaultConfig(), Provisioning: provisionconfig.DefaultConfig(), Workload: workloadconfig.DefaultConfig()}
	cfg.Database.DSN = dsn
	cfg.Auth.Developers = []authconfig.Developer{{ID: "dev-alice", Username: "alice", Password: "configured-password"}}
	cfg.Server.Address = address
	cfg.Server.PublicOrigin = "http://" + address
	cfg.Server.ShutdownTimeout = 3 * time.Second
	cfg.Provisioning.Mode = "kubeconfig"
	cfg.Provisioning.Kubeconfig = kubeconfig
	cfg.Provisioning.Context = "test-context"
	cfg.Provisioning.Namespace = "runtime-tests"
	cfg.Provisioning.InstallationID = "console-runtime"
	cfg.Workload.Templates = []types.Template{{ID: "http-test", Image: "example/http-test:1", Port: 8080, CPURequest: "25m", CPULimit: "100m", MemoryRequest: "32Mi", MemoryLimit: "64Mi", ProbePath: "/"}}
	cfg.Workload.ReconcileInterval = 10 * time.Millisecond
	return cfg
}

func runtimeConfigPath(t *testing.T, cfg config.Config) string {
	t.Helper()
	encoded, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "console.yml")
	require.NoError(t, os.WriteFile(path, encoded, 0600))
	return path
}

func assertStage(t *testing.T, err error, phase, reason string) {
	t.Helper()
	var failure *stageError
	require.ErrorAs(t, err, &failure)
	assert.Equal(t, phase, failure.phase)
	assert.Equal(t, reason, failure.reason)
	assert.NotNil(t, failure.Unwrap())
	assert.NotContains(t, failure.Error(), "password")
}

func waitForRuntime(t *testing.T, admin *sql.DB, address string) {
	t.Helper()
	client := &http.Client{Timeout: 100 * time.Millisecond}
	const lockID int64 = 0x434f4e534f4c4501
	require.Eventually(t, func() bool {
		response, err := client.Get("http://" + address + "/health/live")
		if err != nil {
			return false
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		var locked bool
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		err = admin.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND classid=$1 AND objid=$2 AND granted)", uint32(uint64(lockID)>>32), uint32(lockID&0xffffffff)).Scan(&locked)
		return response.StatusCode == http.StatusOK && err == nil && locked
	}, 5*time.Second, 10*time.Millisecond)
}

func startRuntime(t *testing.T, cfg config.Config) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	path := runtimeConfigPath(t, cfg)
	go func() {
		done <- run(ctx, path, runtimeLogger())
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Console runtime cleanup did not join workers")
		}
	})
	return cancel, done
}

func awaitRuntime(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Console runtime did not join HTTP and reconciliation workers")
		return nil
	}
}

func TestRunRejectsMalformedConfigurationAndPreservesCause(t *testing.T) {
	t.Setenv("CONSOLE_POSTGRES_DSN", "")
	path := filepath.Join(t.TempDir(), "absent.yml")
	err := run(context.Background(), path, runtimeLogger())
	assertStage(t, err, "configuration", "configuration_invalid")
	assert.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, os.WriteFile(path, []byte("database: ["), 0600))
	assertStage(t, run(context.Background(), path, runtimeLogger()), "configuration", "configuration_invalid")
}

func TestRunStartupShutdownJoinsWorkersAndReleasesResources(t *testing.T) {
	t.Setenv("CONSOLE_POSTGRES_DSN", "")
	admin, dsn := runtimeDatabase(t)
	address := freeAddress(t)
	cfg := runtimeConfiguration(t, dsn, address)
	cancel, done := startRuntime(t, cfg)
	waitForRuntime(t, admin, address)
	cancel()
	require.NoError(t, awaitRuntime(t, done))
	listener, err := net.Listen("tcp", address)
	require.NoError(t, err, "shutdown must release the HTTP listener before returning")
	require.NoError(t, listener.Close())
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	var locked bool
	const lockID int64 = 0x434f4e534f4c4501
	require.NoError(t, admin.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND classid=$1 AND objid=$2 AND granted)", uint32(uint64(lockID)>>32), uint32(lockID&0xffffffff)).Scan(&locked))
	assert.False(t, locked, "shutdown must release reconciliation authority before returning")
}

func TestRunListenerFailureStopsReconciler(t *testing.T) {
	t.Setenv("CONSOLE_POSTGRES_DSN", "")
	_, dsn := runtimeDatabase(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, listener.Close()) })
	cfg := runtimeConfiguration(t, dsn, listener.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = run(ctx, runtimeConfigPath(t, cfg), runtimeLogger())
	assertStage(t, err, "http_listen", "http_listener_failed")
}

func TestRunInvalidKubeconfigReportsSafeStage(t *testing.T) {
	t.Setenv("CONSOLE_POSTGRES_DSN", "")
	_, dsn := runtimeDatabase(t)
	cfg := runtimeConfiguration(t, dsn, freeAddress(t))
	require.NoError(t, os.WriteFile(cfg.Provisioning.Kubeconfig, []byte("private-password: ["), 0600))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := run(ctx, runtimeConfigPath(t, cfg), runtimeLogger())
	assertStage(t, err, "kubernetes_client", "kubernetes_configuration_failed")
	assert.NotContains(t, err.Error(), "private-password")
}

func TestRunSecondReconcilerCannotServeAlongsideOwner(t *testing.T) {
	t.Setenv("CONSOLE_POSTGRES_DSN", "")
	admin, dsn := runtimeDatabase(t)
	firstCfg := runtimeConfiguration(t, dsn, freeAddress(t))
	cancel, done := startRuntime(t, firstCfg)
	waitForRuntime(t, admin, firstCfg.Server.Address)
	secondCfg := runtimeConfiguration(t, dsn, freeAddress(t))
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	err := run(ctx, runtimeConfigPath(t, secondCfg), runtimeLogger())
	assertStage(t, err, "reconciler", "reconciler_failed")
	assert.ErrorContains(t, errors.Unwrap(err), "another Console reconciler")
	listener, err := net.Listen("tcp", secondCfg.Server.Address)
	require.NoError(t, err)
	require.NoError(t, listener.Close())
	cancel()
	require.NoError(t, awaitRuntime(t, done))
}

func TestRunCanceledStartupPreservesCancellation(t *testing.T) {
	t.Setenv("CONSOLE_POSTGRES_DSN", "")
	_, dsn := runtimeDatabase(t)
	cfg := runtimeConfiguration(t, dsn, freeAddress(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := run(ctx, runtimeConfigPath(t, cfg), runtimeLogger())
	assertStage(t, err, "postgres_connect", "postgres_connection_failed")
	assert.ErrorIs(t, err, context.Canceled)
}

func TestCommandValidatesArgumentsAndLogsSafeStartupFailure(t *testing.T) {
	t.Setenv("CONSOLE_POSTGRES_DSN", "")
	assert.NoError(t, command(context.Background(), []string{"-help"}, runtimeLogger()))
	assert.Error(t, command(context.Background(), []string{"-unknown"}, runtimeLogger()))
	assert.ErrorContains(t, command(context.Background(), []string{"unexpected"}, runtimeLogger()), "accepts only flags")
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	path := filepath.Join(t.TempDir(), "private-password-config.yml")
	err := command(context.Background(), []string{"-config", path}, logger)
	assertStage(t, err, "configuration", "configuration_invalid")
	assert.Contains(t, logs.String(), `"phase":"configuration"`)
	assert.Contains(t, logs.String(), `"reason":"configuration_invalid"`)
	assert.NotContains(t, logs.String(), "private-password")
	assert.NotContains(t, logs.String(), path)
}

func TestCommandRunsConfiguredRuntimeUntilCancellation(t *testing.T) {
	t.Setenv("CONSOLE_POSTGRES_DSN", "")
	admin, dsn := runtimeDatabase(t)
	cfg := runtimeConfiguration(t, dsn, freeAddress(t))
	path := runtimeConfigPath(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- command(ctx, []string{"-config", path}, runtimeLogger())
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Console command cleanup did not join workers")
		}
	})
	waitForRuntime(t, admin, cfg.Server.Address)
	cancel()
	assert.NoError(t, awaitRuntime(t, done))
}

func TestMainHelpReturnsWithoutStartingRuntime(t *testing.T) {
	previous := os.Args
	os.Args = []string{"console-server", "-help"}
	t.Cleanup(func() { os.Args = previous })
	main()
}
