package services

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/codetreker/syntrix/internal/server"
)

func (m *Manager) launch(ctx context.Context, role string, blocking bool, task func(context.Context) error) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		err := task(ctx)
		if ctx.Err() == nil && (err != nil || blocking) {
			m.fail(role, err)
		}
	}()
}

// Start launches owned tasks; terminal results are consumed through Wait.
func (m *Manager) Start(bgCtx context.Context) error {
	if m.opts.BootstrapIndexes && !m.bootstrapReady {
		return errors.New("cannot start services before index bootstrap readiness")
	}
	ctx := m.lifecycle(bgCtx)
	if s := server.Default(); s != nil && !m.serverStarted {
		m.serverStarted = true
		m.launch(ctx, "server", true, s.Start)
	}
	if m.pullerService != nil && !m.pullerStarted {
		if err := m.pullerService.Start(ctx); err != nil {
			return fmt.Errorf("start Change Stream Puller: %w", err)
		}
		m.pullerStarted = true
	}
	if m.pullerGRPC != nil && !m.pullerGRPCInitialized {
		m.pullerGRPC.Init()
		m.pullerGRPCInitialized = true
	}
	if m.indexerService != nil && !m.indexerStarted {
		if err := m.indexerService.Start(ctx); err != nil {
			return fmt.Errorf("start Indexer Service: %w", err)
		}
		m.indexerStarted = true
	}
	if m.streamerService != nil {
		if err := m.streamerService.Start(ctx); err != nil {
			return fmt.Errorf("start Streamer: %w", err)
		}
	}
	if m.rtServer != nil {
		m.launch(ctx, "realtime", false, func(ctx context.Context) error {
			ticker := time.NewTicker(50 * time.Millisecond)
			defer ticker.Stop()
			var last error
			for attempt := 0; attempt < 100; attempt++ {
				if ctx.Err() != nil {
					return nil
				}
				if last = m.rtServer.StartBackgroundTasks(ctx); last == nil {
					return nil
				}
				select {
				case <-ctx.Done():
					return nil
				case <-ticker.C:
				}
			}
			return last
		})
	}
	if !m.writersHeld() {
		if m.triggerService != nil {
			m.launch(ctx, roleEvaluator, true, m.triggerService.Start)
		}
		if m.triggerConsumer != nil {
			m.launch(ctx, roleWorker, true, m.triggerConsumer.Start)
		}
		if m.deletionWorker != nil {
			if err := m.deletionWorker.Start(ctx); err != nil {
				return fmt.Errorf("start deletion worker: %w", err)
			}
		}
	}
	if m.health != nil {
		m.launch(ctx, "readiness", false, func(ctx context.Context) error { m.sampleReadiness(ctx); return nil })
	}
	return nil
}
