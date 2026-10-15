package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/codetreker/syntrix/internal/config"
	"github.com/codetreker/syntrix/internal/core/storage"
	storageconfig "github.com/codetreker/syntrix/internal/core/storage/config"
	"github.com/codetreker/syntrix/internal/initialization"
)

type initializationAction struct {
	Inspect, Finalize, QueryAccepted bool
	Expected, Output                 string
	Timeout                          time.Duration
}

func (a initializationAction) enabled() bool { return a.Inspect || a.Finalize }
func (a initializationAction) validate() error {
	if a.Inspect && a.Finalize {
		return errors.New("initialization inspection and finalization are mutually exclusive")
	}
	if !a.enabled() {
		if a.Expected != "" || a.QueryAccepted || a.Output != "" {
			return errors.New("initialization arguments require inspection or finalization")
		}
		return nil
	}
	if a.Output != "json" {
		return errors.New("initialization commands require --output json")
	}
	if a.Timeout <= 0 || a.Timeout > time.Hour {
		return errors.New("initialization timeout must be positive and at most 1h")
	}
	if a.Finalize && (a.Expected == "" || !a.QueryAccepted) {
		return errors.New("finalization requires --expected-preparation and --indexed-query-accepted")
	}
	if a.Inspect && (a.Expected != "" || a.QueryAccepted) {
		return errors.New("inspection does not accept finalization attestations")
	}
	return nil
}

type initializationOperations interface {
	Inspect(context.Context) (initialization.Status, error)
	Finalize(context.Context, string) (initialization.Receipt, error)
}
type physicalInitializationBackends interface{ CloseContext(context.Context) error }

var openInitializationBackends = func(ctx context.Context, cfg storageconfig.Config) (physicalInitializationBackends, error) {
	return storage.NewBackends(ctx, cfg)
}
var newInitializationOperations = func(ctx context.Context, cfg storageconfig.Config, backends physicalInitializationBackends) (initializationOperations, error) {
	physical, ok := backends.(*storage.Backends)
	if !ok {
		return nil, errors.New("physical backends do not expose initialization handles")
	}
	return initialization.New(ctx, cfg, physical)
}

type initializationCommandError struct {
	cause error
	phase string
}

func (e *initializationCommandError) Error() string {
	return "initialization " + e.phase + " failed (initialization_failure)"
}
func (e *initializationCommandError) Unwrap() error { return e.cause }

func runInitialization(parent context.Context, cfg *config.Config, action initializationAction, out io.Writer) (resultErr error) {
	if err := action.validate(); err != nil {
		return err
	}
	if !action.enabled() {
		return errors.New("initialization action is required")
	}
	ctx, cancel := context.WithTimeout(parent, action.Timeout)
	defer cancel()
	backends, err := openInitializationBackends(ctx, cfg.Storage)
	if err != nil {
		return &initializationCommandError{cause: err, phase: "connect"}
	}
	phase := "inspection"
	if action.Finalize {
		phase = "finalization"
	}
	closed := false
	defer func() {
		if !closed {
			resultErr = errors.Join(resultErr, closeInitializationBackends(parent, action.Timeout, backends))
		}
		if resultErr != nil {
			resultErr = &initializationCommandError{cause: resultErr, phase: phase}
		}
	}()
	operations, err := newInitializationOperations(ctx, cfg.Storage, backends)
	if err != nil {
		return err
	}
	var receipt initialization.Receipt
	if action.Inspect {
		receipt, err = operations.Inspect(ctx)
	} else {
		receipt, err = operations.Finalize(ctx, action.Expected)
	}
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	closeErr := closeInitializationBackends(parent, action.Timeout, backends)
	closed = true
	if closeErr != nil {
		return closeErr
	}
	return json.NewEncoder(out).Encode(receipt)
}

func closeInitializationBackends(parent context.Context, timeout time.Duration, backends physicalInitializationBackends) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(parent), min(timeout, 10*time.Second))
	defer cancel()
	return backends.CloseContext(cleanup)
}

func (e *initializationCommandError) LogValue() slog.Value {
	return slog.GroupValue(slog.String("subsystem", "initialization"), slog.String("phase", e.phase), slog.String("category", "initialization_failure"))
}
