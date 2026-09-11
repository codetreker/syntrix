package services

import (
	"context"
	"log/slog"
	"time"

	"github.com/syntrixbase/syntrix/internal/server"
)

func (m *Manager) Start(bgCtx context.Context) {
	if m.opts.BootstrapIndexes && !m.bootstrapReady {
		slog.Error("Cannot start services before index bootstrap readiness")
		return
	}
	// Start Unified Server Service
	if s := server.Default(); s != nil && !m.serverStarted {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			slog.Info("Starting Unified Server Service...")
			if err := s.Start(bgCtx); err != nil {
				slog.Error("Unified Server Service error", "error", err)
			}
		}()
	}

	// Start Streamer Service (only for local service, not when using gRPC client)
	if m.streamerService != nil {
		go func() {
			if err := m.streamerService.Start(bgCtx); err != nil {
				slog.Error("Failed to start Streamer Service", "error", err)
			}
		}()
	}

	// Start Realtime Background Tasks with retry
	if m.rtServer != nil {
		go func() {
			// Give servers a moment to start
			time.Sleep(50 * time.Millisecond)

			maxRetries := 100
			for i := 0; i < maxRetries; i++ {
				// Check context before trying
				select {
				case <-bgCtx.Done():
					return
				default:
				}

				if err := m.rtServer.StartBackgroundTasks(bgCtx); err != nil {
					// Log every 10th attempt to reduce noise
					if (i+1)%10 == 0 {
						slog.Warn("Failed to start realtime background tasks", "attempt", i+1, "max_attempts", maxRetries, "error", err)
					}

					// Wait with context check
					select {
					case <-bgCtx.Done():
						return
					case <-time.After(50 * time.Millisecond):
						continue
					}
				}
				slog.Info("Realtime background tasks started successfully")
				return
			}
			slog.Error("Failed to start realtime background tasks after multiple attempts")
		}()
	}

	// Start Trigger Evaluator Service
	if m.triggerService != nil {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			slog.Info("Starting Trigger Evaluator Service...")
			if err := m.triggerService.Start(bgCtx); err != nil {
				slog.Error("Failed to start trigger watcher", "error", err)
			}
		}()
	}

	// Start Trigger Consumer (Delivery Worker)
	if m.triggerConsumer != nil {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			slog.Info("Starting Trigger Consumer...")
			if err := m.triggerConsumer.Start(bgCtx); err != nil {
				slog.Error("Trigger Consumer stopped with error", "error", err)
			}
		}()
	}

	// Start Change Stream Puller
	if m.pullerService != nil {
		if !m.pullerStarted {
			slog.Info("Starting Change Stream Puller...")
			if err := m.pullerService.Start(bgCtx); err != nil {
				slog.Error("Failed to start Change Stream Puller", "error", err)
			}
		}

		// Initialize gRPC Server event handler (server is registered with unified server)
		if m.pullerGRPC != nil && !m.pullerGRPCInitialized {
			slog.Info("Initializing Puller gRPC Service...")
			m.pullerGRPC.Init()
		}
	}

	// Start Indexer Service
	if m.indexerService != nil && !m.indexerStarted {
		slog.Info("Starting Indexer Service...")
		if err := m.indexerService.Start(bgCtx); err != nil {
			slog.Error("Failed to start Indexer Service", "error", err)
		}
	}

	// Start Deletion Worker
	if m.deletionWorker != nil {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			if err := m.deletionWorker.Start(bgCtx); err != nil {
				slog.Error("Failed to start Deletion Worker", "error", err)
			}
		}()
	}
}
