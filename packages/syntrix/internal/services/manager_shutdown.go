package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/codetreker/syntrix/internal/server"
)

func (m *Manager) Shutdown(ctx context.Context) {
	if err := m.shutdown(ctx); err != nil {
		slog.Error("Instance shutdown failed", "error", &runtimeFailure{role: "shutdown", cause: err})
	}
}
func (m *Manager) shutdown(ctx context.Context) error {
	m.shutdownMu.Lock()
	defer m.shutdownMu.Unlock()
	return m.shutdownResources(ctx)
}

func (m *Manager) shutdownResources(ctx context.Context) (shutdownErr error) {
	m.lifecycleMu.Lock()
	if m.lifeCancel != nil {
		m.lifeCancel(nil)
	}
	m.lifecycleMu.Unlock()
	m.clearHealth()
	quiesced := true
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		quiesced = false
		shutdownErr = errors.Join(shutdownErr, ctx.Err())
	}
	if !quiesced {
		if s := server.Default(); s != nil && !m.serverStopped {
			if err := s.Stop(ctx); err != nil {
				quiesced = false
				shutdownErr = errors.Join(shutdownErr, fmt.Errorf("stop unified server: %w", err))
			} else {
				m.serverStopped = true
			}
		}

		return shutdownErr
	}
	for _, owner := range []any{m.triggerService, m.triggerConsumer} {
		if stopper, ok := owner.(interface{ Shutdown(context.Context) error }); ok {
			if err := stopper.Shutdown(ctx); err != nil {
				quiesced = false
				shutdownErr = errors.Join(shutdownErr, err)
			}
		}
	}
	if m.rtServer != nil {
		if stopper, ok := any(m.rtServer).(interface{ Shutdown(context.Context) error }); ok {
			if err := stopper.Shutdown(ctx); err != nil {
				quiesced = false
				shutdownErr = errors.Join(shutdownErr, err)
			}
		}
	}
	if m.deletionWorker != nil {
		if err := m.deletionWorker.Stop(ctx); err != nil {
			quiesced = false
			shutdownErr = errors.Join(shutdownErr, err)
		} else {
			m.deletionWorker = nil
		}
	}
	if m.streamerService != nil {
		if err := m.streamerService.Stop(ctx); err != nil {
			quiesced = false
			shutdownErr = errors.Join(shutdownErr, fmt.Errorf("stop Streamer: %w", err))
		} else {
			m.streamerService = nil
		}
	}
	if m.pullerGRPC != nil {
		m.pullerGRPC.Shutdown()
		m.pullerGRPC = nil
	}
	if s := server.Default(); s != nil && !m.serverStopped {
		if err := s.Stop(ctx); err != nil {
			quiesced = false
			shutdownErr = errors.Join(shutdownErr, fmt.Errorf("stop unified server: %w", err))
		} else {
			m.serverStopped = true
		}
	}
	if m.pullerService != nil {
		if err := m.pullerService.Stop(ctx); err != nil {
			quiesced = false
			shutdownErr = errors.Join(shutdownErr, fmt.Errorf("stop Puller: %w", err))
		} else {
			m.pullerService = nil
		}
	}
	if m.indexerService != nil {
		if err := m.indexerService.Stop(ctx); err != nil {
			quiesced = false
			shutdownErr = errors.Join(shutdownErr, fmt.Errorf("stop Indexer: %w", err))
		} else {
			m.indexerService = nil
		}
	}
	if !quiesced {
		return shutdownErr
	}
	if m.streamerClient != nil {
		if closer, ok := m.streamerClient.(io.Closer); ok {
			if err := closer.Close(); err != nil {
				shutdownErr = errors.Join(shutdownErr, fmt.Errorf("close Streamer client: %w", err))
			} else {
				m.streamerClient = nil
			}
		} else {
			m.streamerClient = nil
		}
	}
	retained := m.ownedClients[:0]
	for _, client := range m.ownedClients {
		if err := client.Close(); err != nil {
			shutdownErr = errors.Join(shutdownErr, err)
			retained = append(retained, client)
		}
	}
	m.ownedClients = retained
	if m.health != nil {
		m.health.mu.Lock()
		for address, connection := range m.health.connections {
			if err := connection.Close(); err != nil {
				shutdownErr = errors.Join(shutdownErr, err)
			} else {
				delete(m.health.connections, address)
			}
		}
		m.health.mu.Unlock()
	}
	if closer, ok := m.triggerService.(io.Closer); ok {
		if err := closer.Close(); err != nil {
			shutdownErr = errors.Join(shutdownErr, err)
		} else {
			m.triggerService = nil
		}
	}
	if m.pubsubProvider != nil {
		if err := m.pubsubProvider.Close(); err != nil {
			shutdownErr = errors.Join(shutdownErr, fmt.Errorf("close pubsub provider: %w", err))
		} else {
			m.pubsubProvider = nil
		}
	}
	if m.backends != nil {
		if err := m.backends.CloseContext(ctx); err != nil {
			shutdownErr = errors.Join(shutdownErr, fmt.Errorf("close instance backends: %w", err))
		}
	}
	return shutdownErr
}
