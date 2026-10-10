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
		slog.Error("Error shutting down instance", "error", err)
	}
}

func (m *Manager) shutdown(ctx context.Context) error {
	m.shutdownMu.Lock()
	defer m.shutdownMu.Unlock()
	return m.shutdownResources(ctx)
}

func (m *Manager) shutdownResources(ctx context.Context) (shutdownErr error) {
	quiesced := true
	// Close streamer client if using remote connection
	if m.streamerClient != nil {
		if closer, ok := m.streamerClient.(io.Closer); ok {
			slog.Info("Closing Streamer gRPC client...")
			if err := closer.Close(); err != nil {
				shutdownErr = errors.Join(shutdownErr, fmt.Errorf("close Streamer client: %w", err))
			} else {
				m.streamerClient = nil
			}
		} else {
			m.streamerClient = nil
		}
	}

	// Stop Unified Server Service
	if s := server.Default(); s != nil && !m.serverStopped {
		slog.Info("Stopping Unified Server Service...")
		if err := s.Stop(ctx); err != nil {
			quiesced = false
			shutdownErr = errors.Join(shutdownErr, fmt.Errorf("stop unified server: %w", err))
		} else {
			m.serverStopped = true
		}
	}

	// Wait for background tasks (Trigger Watcher, Consumer)
	slog.Info("Waiting for background tasks to finish...")
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		slog.Info("Background tasks finished")
	case <-ctx.Done():
		quiesced = false
		slog.Warn("Timeout waiting for background tasks")
		shutdownErr = errors.Join(shutdownErr, ctx.Err())
	}

	// Close pubsub provider (handles both NATS and memory implementations)
	if m.pubsubProvider != nil {
		slog.Info("Closing pubsub provider...")
		if err := m.pubsubProvider.Close(); err != nil {
			shutdownErr = errors.Join(shutdownErr, fmt.Errorf("close pubsub provider: %w", err))
		} else {
			m.pubsubProvider = nil
		}
	}

	// Shutdown Puller gRPC Service
	if m.pullerGRPC != nil {
		slog.Info("Shutting down Puller gRPC Service...")
		m.pullerGRPC.Shutdown()
		m.pullerGRPC = nil
	}

	// Stop Change Stream Puller
	if m.pullerService != nil {
		slog.Info("Stopping Change Stream Puller...")
		if err := m.pullerService.Stop(ctx); err != nil {
			quiesced = false
			shutdownErr = errors.Join(shutdownErr, fmt.Errorf("stop Puller: %w", err))
		} else {
			m.pullerService = nil
		}
	}

	// Stop Indexer Service
	if m.indexerService != nil {
		slog.Info("Stopping Indexer Service...")
		if err := m.indexerService.Stop(ctx); err != nil {
			quiesced = false
			shutdownErr = errors.Join(shutdownErr, fmt.Errorf("stop Indexer: %w", err))
		} else {
			m.indexerService = nil
		}
	}
	if quiesced && m.backends != nil {
		if err := m.backends.Close(); err != nil {
			shutdownErr = errors.Join(shutdownErr, fmt.Errorf("close instance backends: %w", err))
		}
	}
	return shutdownErr
}
