package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/config"
	"github.com/codetreker/syntrix/internal/core/storage"
	storageconfig "github.com/codetreker/syntrix/internal/core/storage/config"
	"github.com/codetreker/syntrix/internal/initialization"
	initializationconfig "github.com/codetreker/syntrix/internal/initialization/config"
	"github.com/stretchr/testify/require"
)

type phaseInitialization struct {
	fakeInitialization
	prepared, fresh, inspected int
	state                      string
	err                        error
}

func (s *phaseInitialization) Prepare(context.Context) (initialization.Receipt, error) {
	s.prepared++
	return initialization.Receipt{State: initialization.Prepared}, s.err
}
func (s *phaseInitialization) PrepareFresh(context.Context) (initialization.Receipt, error) {
	s.fresh++
	return initialization.Receipt{State: initialization.Prepared}, s.err
}
func (s *phaseInitialization) Inspect(context.Context) (initialization.Status, error) {
	s.inspected++
	return initialization.Status{State: s.state}, s.err
}

func TestInitializationPhaseIsValidatedBeforeAccountComposition(t *testing.T) {
	cases := []struct {
		name                       string
		mode                       initializationconfig.Mode
		opts                       Options
		state                      string
		prepared, fresh, inspected int
		wantError                  bool
	}{
		{"operator", initializationconfig.Operator, Options{RunPuller: true}, initialization.Prepared, 1, 0, 0, false},
		{"managed-bootstrap", initializationconfig.Managed, Options{RunPuller: true, BootstrapIndexes: true}, initialization.Prepared, 0, 1, 0, false},
		{"managed-acceptance", initializationconfig.Managed, Options{RunPuller: true, InitializationAcceptance: true}, initialization.Prepared, 1, 0, 0, false},
		{"managed-normal", initializationconfig.Managed, Options{RunPuller: true}, initialization.Finalized, 0, 0, 1, false},
		{"managed-unfinalized", initializationconfig.Managed, Options{RunPuller: true}, initialization.Prepared, 0, 0, 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupManagerFactories(t)
			cfg := config.DefaultConfig()
			cfg.Initialization.Mode = tc.mode
			operation := &phaseInitialization{state: tc.state}
			initializationFactory = func(context.Context, storageconfig.Config, *storage.Backends) (initializationCoordinator, error) {
				return operation, nil
			}
			mgr := NewManager(cfg, tc.opts)
			err := mgr.prepareInitialization(context.Background())
			if tc.wantError {
				require.ErrorIs(t, err, initialization.ErrNotPrepared)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.prepared, operation.prepared)
			require.Equal(t, tc.fresh, operation.fresh)
			require.Equal(t, tc.inspected, operation.inspected)
			require.Nil(t, mgr.identityModule)
		})
	}
}

func TestInitializationFailuresStopBeforeFactoriesAndPreserveCause(t *testing.T) {
	for _, stage := range []string{"backends", "coordinator", "prepare"} {
		t.Run(stage, func(t *testing.T) {
			setupManagerFactories(t)
			cause := errors.New("initialization backend failure")
			cfg := config.DefaultConfig()
			operation := &phaseInitialization{state: initialization.Prepared}
			initializationFactory = func(context.Context, storageconfig.Config, *storage.Backends) (initializationCoordinator, error) {
				if stage == "coordinator" {
					return nil, cause
				}
				return operation, nil
			}
			if stage == "backends" {
				storageBackendsFactory = func(context.Context, *config.Config) (*storage.Backends, error) { return nil, cause }
			}
			if stage == "prepare" {
				operation.err = cause
			}
			mgr := NewManager(cfg, Options{RunPuller: true})
			require.ErrorIs(t, mgr.prepareInitialization(context.Background()), cause)
			require.Nil(t, mgr.identityModule)
			require.Nil(t, mgr.storageFactory)
		})
	}
}

func TestInitializationAcceptanceRequiresMaintenanceAttestation(t *testing.T) {
	for _, test := range []struct {
		opts  Options
		valid bool
	}{
		{Options{InitializationAcceptance: true}, false},
		{Options{InitializationAcceptance: true, WritesQuiesced: true}, true},
		{Options{InitializationAcceptance: true, WritesQuiesced: true, BootstrapIndexes: true, BootstrapTimeout: time.Minute}, false},
	} {
		mgr := NewManager(config.DefaultConfig(), test.opts)
		err := mgr.PrepareIndexBootstrap()
		if test.valid {
			require.NoError(t, err)
		} else {
			require.Error(t, err)
		}
	}
}

func TestDefaultInitializationFactoryBorrowsOpenedPhysicalBackends(t *testing.T) {
	backends := managerTestBackends(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	coordinator, err := initializationFactory(ctx, config.DefaultConfig().Storage, backends)
	require.NoError(t, err)
	require.NotNil(t, coordinator)
	require.NoError(t, backends.PostgresDB().PingContext(ctx))
}
