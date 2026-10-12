package workload

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	workloadconfig "github.com/codetreker/syntrix/console-server/internal/workload/config"
	"github.com/stretchr/testify/require"
)

type testExecutor struct {
	ensure func(context.Context, Workload) (Observation, error)
	delete func(context.Context, Workload) (Observation, error)
}

func (e *testExecutor) Ensure(ctx context.Context, workload Workload) (Observation, error) {
	return e.ensure(ctx, workload)
}

func (e *testExecutor) Delete(ctx context.Context, workload Workload) (Observation, error) {
	return e.delete(ctx, workload)
}

func testConfig() workloadconfig.Config {
	cfg := workloadconfig.DefaultConfig()
	cfg.Templates = []Template{{ID: "http-test", Image: "nginx:1.27", Port: 80, CPURequest: "10m", CPULimit: "100m", MemoryRequest: "16Mi", MemoryLimit: "32Mi", ProbePath: "/", Env: map[string]string{"PASSWORD": "sensitive-value"}}}
	return cfg
}

func TestPublicViewsOmitFrozenSecrets(t *testing.T) {
	workload := Workload{ID: "public-id", Snapshot: testConfig().Templates[0], TemplateDigest: "private-digest", InstallationID: "private-installation"}
	encoded, err := json.Marshal(workload)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "public-id")
	require.NotContains(t, string(encoded), "sensitive-value")
	require.NotContains(t, string(encoded), "PASSWORD")
	require.NotContains(t, string(encoded), "private-")

	cfg := testConfig()
	service, err := New(&sql.DB{}, &testExecutor{}, cfg, Target{Namespace: "test", InstallationID: "console-test"})
	require.NoError(t, err)
	cfg.Templates[0].Env["PASSWORD"] = "changed"
	require.Equal(t, "sensitive-value", service.templates["http-test"].Env["PASSWORD"])
	encoded, err = json.Marshal(service.Templates())
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "PASSWORD")
}

func TestNewRejectsIncompleteComposition(t *testing.T) {
	for _, test := range []struct {
		name     string
		db       *sql.DB
		executor Executor
		cfg      workloadconfig.Config
		target   Target
	}{
		{"missing PostgreSQL", nil, &testExecutor{}, testConfig(), Target{Namespace: "test", InstallationID: "console-test"}},
		{"missing executor", &sql.DB{}, nil, testConfig(), Target{Namespace: "test", InstallationID: "console-test"}},
		{"missing target", &sql.DB{}, &testExecutor{}, testConfig(), Target{}},
		{"invalid template config", &sql.DB{}, &testExecutor{}, workloadconfig.DefaultConfig(), Target{Namespace: "test", InstallationID: "console-test"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(test.db, test.executor, test.cfg, test.target)
			require.Error(t, err)
		})
	}
}

type categorizedError string

func (e categorizedError) Error() string         { return "sensitive-password" }
func (e categorizedError) ErrorCategory() string { return string(e) }

func TestExecutionCategoryRejectsArbitraryDiagnosticText(t *testing.T) {
	require.Equal(t, "kubernetes_operation_failed", executionCategory(errors.New("password=secret")))
	require.Equal(t, "resource_identity_conflict", executionCategory(categorizedError("resource_identity_conflict")))
	require.Equal(t, "kubernetes_operation_failed", executionCategory(categorizedError("password=secret")))
}

func TestCreateRejectsInvalidRequestsBeforeDatabaseAccess(t *testing.T) {
	service, err := New(&sql.DB{}, &testExecutor{}, testConfig(), Target{Namespace: "test", InstallationID: "console-test"})
	require.NoError(t, err)
	for _, test := range []struct{ developer, template, key string }{
		{"", "http-test", "key"}, {"alice", "", "key"}, {"alice", "http-test", ""}, {"alice", "http-test", "key\n"}, {"alice", "http-test", "key\x00"},
	} {
		_, err := service.Create(context.Background(), test.developer, test.template, test.key)
		require.ErrorIs(t, err, ErrInvalid)
	}
}

func TestReconcileStatus(t *testing.T) {
	for _, test := range []struct {
		name        string
		desired     string
		observation Observation
		err         error
		status      string
		hasError    bool
	}{
		{"created", DesiredRunning, Observation{}, nil, StatusProvisioning, false},
		{"ready", DesiredRunning, Observation{Ready: true}, nil, StatusReady, false},
		{"failed", DesiredRunning, Observation{}, errors.New("secret image environment"), StatusFailed, true},
		{"deleting", DesiredDeleted, Observation{}, nil, StatusDeleting, false},
		{"deleted", DesiredDeleted, Observation{Deleted: true}, nil, StatusDeleted, false},
		{"delete retry", DesiredDeleted, Observation{}, errors.New("sensitive-detail"), StatusDeleting, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			status, reason := reconcileStatus(Workload{DesiredState: test.desired}, test.observation, test.err)
			require.Equal(t, test.status, status)
			require.Equal(t, test.hasError, reason != "")
			require.NotContains(t, reason, "secret")
			require.NotContains(t, reason, "sensitive")
		})
	}
}
