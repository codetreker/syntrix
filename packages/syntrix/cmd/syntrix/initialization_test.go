package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/config"
	storageconfig "github.com/codetreker/syntrix/internal/core/storage/config"
	"github.com/codetreker/syntrix/internal/initialization"
	"github.com/stretchr/testify/require"
)

type commandBackends struct {
	calls      int
	closeError error
	closeCheck func(context.Context)
}

func (b *commandBackends) CloseContext(ctx context.Context) error {
	b.calls++
	if b.closeCheck != nil {
		b.closeCheck(ctx)
	}
	return b.closeError
}

type commandOperations struct {
	inspect  func(context.Context) (initialization.Status, error)
	finalize func(context.Context, string) (initialization.Receipt, error)
}

func (o commandOperations) Inspect(ctx context.Context) (initialization.Status, error) {
	return o.inspect(ctx)
}
func (o commandOperations) Finalize(ctx context.Context, expected string) (initialization.Receipt, error) {
	return o.finalize(ctx, expected)
}

type rejectedCommandOutput struct{ err error }

func (w rejectedCommandOutput) Write([]byte) (int, error) { return 0, w.err }

func commandFixture(t *testing.T, backend *commandBackends, operations commandOperations) {
	t.Helper()
	oldOpen, oldNew := openInitializationBackends, newInitializationOperations
	t.Cleanup(func() { openInitializationBackends = oldOpen; newInitializationOperations = oldNew })
	openInitializationBackends = func(ctx context.Context, _ storageconfig.Config) (physicalInitializationBackends, error) {
		require.NoError(t, ctx.Err())
		return backend, nil
	}
	newInitializationOperations = func(ctx context.Context, _ storageconfig.Config, got physicalInitializationBackends) (initializationOperations, error) {
		require.Same(t, backend, got)
		require.NoError(t, ctx.Err())
		return operations, nil
	}
}
func inspectAction() initializationAction {
	return initializationAction{Inspect: true, Output: "json", Timeout: 50 * time.Millisecond}
}

func TestInitializationActionsRequireBoundedExplicitPrerequisites(t *testing.T) {
	cases := []struct {
		name   string
		action initializationAction
		valid  bool
	}{
		{"normal", initializationAction{}, true},
		{"inspect", inspectAction(), true},
		{"finalize", initializationAction{Finalize: true, Expected: "1:sha256:fixture", QueryAccepted: true, Output: "json", Timeout: time.Second}, true},
		{"exclusive", initializationAction{Inspect: true, Finalize: true}, false},
		{"json-required", initializationAction{Inspect: true, Timeout: time.Second}, false},
		{"unbounded", initializationAction{Inspect: true, Output: "json"}, false},
		{"too-long", initializationAction{Inspect: true, Output: "json", Timeout: time.Hour + time.Nanosecond}, false},
		{"no-expected", initializationAction{Finalize: true, QueryAccepted: true, Output: "json", Timeout: time.Second}, false},
		{"no-query", initializationAction{Finalize: true, Expected: "fixture", Output: "json", Timeout: time.Second}, false},
		{"inspection-query", initializationAction{Inspect: true, QueryAccepted: true, Output: "json", Timeout: time.Second}, false},
		{"inspection-expected", initializationAction{Inspect: true, Expected: "fixture", Output: "json", Timeout: time.Second}, false},
		{"orphan-output", initializationAction{Output: "json"}, false},
		{"orphan-expected", initializationAction{Expected: "fixture"}, false},
		{"orphan-query", initializationAction{QueryAccepted: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.action.validate()
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestInitializationCommandClosesOnceBeforePublishingOneReceipt(t *testing.T) {
	for _, finalize := range []bool{false, true} {
		t.Run(map[bool]string{false: "inspect", true: "finalize"}[finalize], func(t *testing.T) {
			backend := &commandBackends{closeCheck: func(ctx context.Context) {
				require.NoError(t, ctx.Err())
				deadline, ok := ctx.Deadline()
				require.True(t, ok)
				require.LessOrEqual(t, time.Until(deadline), 50*time.Millisecond)
			}}
			expected := "1:sha256:fixture"
			receipt := initialization.Receipt{Version: 1, Specification: expected, State: initialization.Prepared}
			operations := commandOperations{inspect: func(context.Context) (initialization.Status, error) { return receipt, nil }, finalize: func(_ context.Context, spec string) (initialization.Receipt, error) {
				require.Equal(t, expected, spec)
				receipt.State = initialization.Finalized
				return receipt, nil
			}}
			commandFixture(t, backend, operations)
			action := inspectAction()
			if finalize {
				action.Inspect = false
				action.Finalize = true
				action.Expected = expected
				action.QueryAccepted = true
			}
			var output bytes.Buffer
			require.NoError(t, runInitialization(context.Background(), config.DefaultConfig(), action, &output))
			require.Equal(t, 1, backend.calls)
			decoder := json.NewDecoder(&output)
			var actual initialization.Receipt
			require.NoError(t, decoder.Decode(&actual))
			require.Equal(t, receipt, actual)
			require.ErrorIs(t, decoder.Decode(&actual), io.EOF)
		})
	}
}

func TestInitializationFailureClosesOnceAndPreservesSecretSafeCause(t *testing.T) {
	for _, phase := range []string{"construct", "inspect", "finalize", "close", "output", "deadline", "cancelled-receipt"} {
		t.Run(phase, func(t *testing.T) {
			cause := errors.New("postgres://admin:fixture-private-password@db/private")
			backend := &commandBackends{closeCheck: func(ctx context.Context) { require.NoError(t, ctx.Err()) }}
			operations := commandOperations{inspect: func(context.Context) (initialization.Status, error) {
				return initialization.Status{State: initialization.Prepared}, nil
			}, finalize: func(context.Context, string) (initialization.Receipt, error) {
				return initialization.Receipt{State: initialization.Finalized}, nil
			}}
			commandFixture(t, backend, operations)
			action := inspectAction()
			switch phase {
			case "construct":
				newInitializationOperations = func(context.Context, storageconfig.Config, physicalInitializationBackends) (initializationOperations, error) {
					return nil, cause
				}
			case "inspect":
				operations.inspect = func(context.Context) (initialization.Status, error) { return initialization.Status{}, cause }
			case "finalize":
				action.Inspect = false
				action.Finalize = true
				action.Expected = "fixture"
				action.QueryAccepted = true
				operations.finalize = func(context.Context, string) (initialization.Receipt, error) { return initialization.Receipt{}, cause }
			case "close":
				backend.closeError = cause
			case "deadline", "cancelled-receipt":
				action.Timeout = time.Millisecond
				operations.inspect = func(ctx context.Context) (initialization.Status, error) {
					<-ctx.Done()
					if phase == "cancelled-receipt" {
						return initialization.Status{State: initialization.Prepared}, nil
					}
					return initialization.Status{}, ctx.Err()
				}
				cause = context.DeadlineExceeded
			}
			if phase != "construct" {
				newInitializationOperations = func(context.Context, storageconfig.Config, physicalInitializationBackends) (initializationOperations, error) {
					return operations, nil
				}
			}
			var output bytes.Buffer
			var writer io.Writer = &output
			if phase == "output" {
				writer = rejectedCommandOutput{cause}
			}
			err := runInitialization(context.Background(), config.DefaultConfig(), action, writer)
			require.ErrorIs(t, err, cause)
			require.NotContains(t, err.Error(), "fixture-private-password")
			require.Empty(t, output.String())
			require.Equal(t, 1, backend.calls)
		})
	}
}

func TestInitializationInvalidActionsOpenNoBackend(t *testing.T) {
	old := openInitializationBackends
	t.Cleanup(func() { openInitializationBackends = old })
	openInitializationBackends = func(context.Context, storageconfig.Config) (physicalInitializationBackends, error) {
		t.Fatal("invalid action opened a backend")
		return nil, nil
	}
	require.Error(t, runInitialization(context.Background(), config.DefaultConfig(), initializationAction{}, io.Discard))
	require.Error(t, runInitialization(context.Background(), config.DefaultConfig(), initializationAction{Inspect: true}, io.Discard))
}

func TestInitializationBackendFailureHasNoReceipt(t *testing.T) {
	cause := errors.New("mongodb://admin:fixture-private-password@mongo/private")
	old := openInitializationBackends
	t.Cleanup(func() { openInitializationBackends = old })
	openInitializationBackends = func(context.Context, storageconfig.Config) (physicalInitializationBackends, error) { return nil, cause }
	var output bytes.Buffer
	err := runInitialization(context.Background(), config.DefaultConfig(), inspectAction(), &output)
	require.ErrorIs(t, err, cause)
	require.NotContains(t, err.Error(), "fixture-private-password")
	require.Empty(t, output.String())
}

func TestInitializationCLIChild(t *testing.T) {
	if os.Getenv("SYNTRIX_INITIALIZATION_TEST_CHILD") != "1" {
		return
	}
	flag.CommandLine = flag.NewFlagSet("syntrix", flag.ExitOnError)
	os.Args = append([]string{"syntrix"}, os.Args[3:]...)
	main()
}

func TestInitializationCLIRejectsMalformedSecretConfigBeforeEffects(t *testing.T) {
	dir := t.TempDir()
	secret := "fixture-private-password"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yml"), []byte("server:\n  http_port: "+secret), 0600))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestInitializationCLIChild$", "--", "--inspect-initialization", "--config-dir", dir, "--output", "json")
	child.Env = append(os.Environ(), "SYNTRIX_INITIALIZATION_TEST_CHILD=1")
	var stdout, stderr bytes.Buffer
	child.Stdout = &stdout
	child.Stderr = &stderr
	err := child.Run()
	require.Error(t, err)
	require.NoError(t, ctx.Err())
	require.Empty(t, stdout.String())
	require.Contains(t, stderr.String(), "configuration decode failed")
	require.NotContains(t, stderr.String(), secret)
	require.NoDirExists(t, filepath.Join(dir, "data"))
}
