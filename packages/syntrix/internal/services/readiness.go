package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/codetreker/syntrix/internal/indexer"
	"github.com/codetreker/syntrix/internal/initialization"
	initializationconfig "github.com/codetreker/syntrix/internal/initialization/config"
	"github.com/codetreker/syntrix/internal/server"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

const (
	roleAPI       = "syntrix.api"
	roleQuery     = "syntrix.query"
	roleIndexer   = "syntrix.indexer"
	rolePuller    = "syntrix.puller"
	roleStreamer  = "syntrix.streamer"
	roleEvaluator = "syntrix.trigger_evaluator"
	roleWorker    = "syntrix.trigger_worker"
)

var runtimeRoles = []string{roleAPI, roleQuery, roleIndexer, rolePuller, roleStreamer, roleEvaluator, roleWorker}

type healthSnapshot struct {
	ready   bool
	checked time.Time
}
type runtimeHealth struct {
	mu          sync.RWMutex
	active      map[string]bool
	snapshots   map[string]healthSnapshot
	freshness   time.Duration
	connections map[string]*grpc.ClientConn
	inFlight    map[string]bool
}
type statusPublisher interface{ SetServingStatus(string, bool) }
type readinessSource interface{ Readiness(context.Context) error }
type terminalSource interface{ TerminalError() error }

func (m *Manager) initHealth() {
	h := &runtimeHealth{active: map[string]bool{}, snapshots: map[string]healthSnapshot{}, freshness: m.cfg.Services.Readiness.Interval + m.cfg.Services.Readiness.Timeout, connections: map[string]*grpc.ClientConn{}, inFlight: map[string]bool{}}
	standalone := m.opts.Mode.IsStandalone()
	h.active[roleAPI] = standalone || m.opts.RunAPI
	h.active[roleQuery] = standalone || m.opts.RunQuery
	h.active[roleIndexer] = standalone || m.opts.RunIndexer
	h.active[rolePuller] = standalone || m.opts.RunPuller
	h.active[roleStreamer] = standalone || m.opts.RunStreamer
	h.active[roleEvaluator] = !m.writersHeld() && (standalone || m.opts.RunTriggerEvaluator)
	h.active[roleWorker] = !m.writersHeld() && (standalone || m.opts.RunTriggerWorker)
	m.health = h
	if publisher, ok := server.Default().(statusPublisher); ok {
		for _, role := range runtimeRoles {
			publisher.SetServingStatus(role, false)
		}
		publisher.SetServingStatus("", false)
	}
	server.Default().RegisterHTTPHandler("/ready", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ready" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		status := http.StatusServiceUnavailable
		if h.ready() {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		if r.Method == http.MethodGet {
			if status == http.StatusOK {
				_, _ = w.Write([]byte("{\"ready\":true}\n"))
			} else {
				_, _ = w.Write([]byte("{\"ready\":false}\n"))
			}
		}
	}))
}

func (h *runtimeHealth) ready() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for role, active := range h.active {
		if active {
			snapshot := h.snapshots[role]
			if !snapshot.ready || time.Since(snapshot.checked) > h.freshness {
				return false
			}
		}
	}
	return true
}

func (m *Manager) sampleReadiness(ctx context.Context) {
	ticker := time.NewTicker(m.cfg.Services.Readiness.Interval)
	defer ticker.Stop()
	for {
		m.checkReadiness(ctx)
		select {
		case <-ctx.Done():
			m.clearHealth()
			return
		case <-ticker.C:
		}
	}
}

func (m *Manager) checkReadiness(ctx context.Context) {
	if m.health == nil {
		return
	}
	var checks sync.WaitGroup
	for role, active := range m.health.active {
		if !active {
			continue
		}
		m.health.mu.Lock()
		if m.health.inFlight[role] {
			m.health.snapshots[role] = healthSnapshot{checked: time.Now()}
			m.health.mu.Unlock()
			if publisher, ok := server.Default().(statusPublisher); ok {
				publisher.SetServingStatus(role, false)
			}
			continue
		}
		m.health.inFlight[role] = true
		m.health.mu.Unlock()
		checks.Add(1)
		go func(role string) {
			defer checks.Done()
			bounded, cancel := context.WithTimeout(ctx, m.cfg.Services.Readiness.Timeout)
			defer cancel()
			results := make(chan error, 1)
			m.wg.Add(1)
			go func() {
				defer m.wg.Done()
				defer func() { m.health.mu.Lock(); m.health.inFlight[role] = false; m.health.mu.Unlock() }()
				results <- m.checkRole(bounded, role)
			}()
			var err error
			select {
			case err = <-results:
			case <-bounded.Done():
				err = bounded.Err()
			}
			ready := err == nil && bounded.Err() == nil
			m.health.mu.Lock()
			previous := m.health.snapshots[role]
			m.health.snapshots[role] = healthSnapshot{ready: ready, checked: time.Now()}
			m.health.mu.Unlock()
			if previous.ready != ready || previous.checked.IsZero() {
				fields := m.diagnosticAttrs(role, "readiness", "role_observation")
				fields = append(fields, "ready", ready)
				slog.Info("Runtime role readiness changed", fields...)
			}
			if publisher, ok := server.Default().(statusPublisher); ok {
				publisher.SetServingStatus(role, ready)
			}
		}(role)
	}
	checks.Wait()
	if publisher, ok := server.Default().(statusPublisher); ok {
		publisher.SetServingStatus("", m.health.ready())
	}
}

func (m *Manager) clearHealth() {
	if publisher, ok := server.Default().(statusPublisher); ok {
		for _, role := range runtimeRoles {
			publisher.SetServingStatus(role, false)
		}
		publisher.SetServingStatus("", false)
	}
	if m.health != nil {
		m.health.mu.Lock()
		for role := range m.health.snapshots {
			m.health.snapshots[role] = healthSnapshot{}
		}
		m.health.mu.Unlock()
	}
}

func (m *Manager) checkTerminal(role string, source any) error {
	if terminal, ok := source.(terminalSource); ok {
		if err := terminal.TerminalError(); err != nil {
			m.fail(role, err)
			return err
		}
	}
	return nil
}

func (m *Manager) checkSource(ctx context.Context, role string, source any) error {
	if err := m.checkTerminal(role, source); err != nil {
		return err
	}
	readiness, ok := source.(readinessSource)
	if !ok {
		return fmt.Errorf("%s does not expose readiness", role)
	}
	return readiness.Readiness(ctx)
}

func (m *Manager) storageReady(ctx context.Context) error {
	if m.initialization == nil {
		return errors.New("storage initialization has not been observed")
	}
	observed, err := m.initialization.Inspect(ctx)
	if err != nil {
		return err
	}
	if observed.State == initialization.Finalized {
		return nil
	}
	if observed.State == initialization.Prepared && (m.writersHeld() || m.cfg.Initialization.Mode != initializationconfig.Managed) {
		return nil
	}
	return errors.New("storage initialization is incomplete")
}

func (m *Manager) brokerReady(ctx context.Context) error {
	if checker, ok := m.pubsubProvider.(interface{ Check(context.Context) error }); ok {
		return checker.Check(ctx)
	}
	if m.opts.Mode.IsStandalone() && m.pubsubProvider != nil {
		return nil
	}
	return errors.New("broker does not expose readiness")
}

func (m *Manager) checkRole(ctx context.Context, role string) error {
	switch role {
	case rolePuller:
		return m.checkSource(ctx, role, m.pullerService)
	case roleIndexer:
		if err := m.checkTerminal(role, m.indexerService); err != nil {
			return err
		}
		if m.indexerService == nil {
			return errors.New("indexer not initialized")
		}
		observed, err := m.indexerService.Health(ctx)
		if err != nil {
			return err
		}
		if observed.Status != indexer.HealthOK {
			return errors.New("indexer not ready")
		}
		if m.opts.Mode.IsDistributed() {
			return m.remoteReady(ctx, m.cfg.Indexer.PullerAddr, rolePuller)
		}
		return m.checkSource(ctx, rolePuller, m.pullerService)
	case roleQuery:
		if err := m.storageReady(ctx); err != nil {
			return err
		}
		if m.opts.Mode.IsDistributed() {
			return m.remoteReady(ctx, m.cfg.Query.IndexerAddr, roleIndexer)
		}
		return m.checkRole(ctx, roleIndexer)
	case roleStreamer:
		if err := m.checkSource(ctx, role, m.streamerService); err != nil {
			return err
		}
		if m.opts.Mode.IsDistributed() {
			return m.remoteReady(ctx, m.cfg.Streamer.Server.PullerAddr, rolePuller)
		}
		return m.checkSource(ctx, rolePuller, m.pullerService)
	case roleEvaluator:
		if err := m.checkTerminal(role, m.triggerService); err != nil {
			return err
		}
		if err := m.storageReady(ctx); err != nil {
			return err
		}
		if err := m.brokerReady(ctx); err != nil {
			return err
		}
		return m.checkSource(ctx, role, m.triggerService)
	case roleWorker:
		if err := m.checkTerminal(role, m.triggerConsumer); err != nil {
			return err
		}
		if m.identityModule == nil {
			return errors.New("identity not initialized")
		}
		if err := m.storageReady(ctx); err != nil {
			return err
		}
		if err := m.brokerReady(ctx); err != nil {
			return err
		}
		return m.checkSource(ctx, role, m.triggerConsumer)
	case roleAPI:
		if m.identityModule == nil || m.gatewayServer == nil {
			return errors.New("api not initialized")
		}
		if err := m.storageReady(ctx); err != nil {
			return err
		}
		if m.opts.Mode.IsDistributed() {
			if err := m.remoteReady(ctx, m.cfg.Gateway.QueryServiceURL, roleQuery); err != nil {
				return err
			}
			address := m.cfg.Gateway.StreamerServiceURL
			if address == "" {
				address = m.cfg.Streamer.Client.StreamerAddr
			}
			if err := m.remoteReady(ctx, address, roleStreamer); err != nil {
				return err
			}
		} else {
			if err := m.checkRole(ctx, roleQuery); err != nil {
				return err
			}
			if err := m.checkRole(ctx, roleStreamer); err != nil {
				return err
			}
		}
		return m.checkSource(ctx, role, m.rtServer)
	}
	return errors.New("unknown runtime role")
}

func (m *Manager) remoteReady(ctx context.Context, address, role string) error {
	if address == "" {
		return errors.New("required role endpoint missing")
	}
	m.health.mu.Lock()
	connection := m.health.connections[address]
	if connection == nil {
		var err error
		connection, err = grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			m.health.mu.Unlock()
			return err
		}
		m.health.connections[address] = connection
	}
	m.health.mu.Unlock()
	response, err := healthpb.NewHealthClient(connection).Check(ctx, &healthpb.HealthCheckRequest{Service: role})
	if err != nil {
		return err
	}
	if response.Status != healthpb.HealthCheckResponse_SERVING {
		return errors.New("required remote role is not serving")
	}
	return nil
}
