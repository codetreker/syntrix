// Package health provides health checking and bootstrap functionality.
package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Status represents the health status of the puller.
type Status string

const (
	// StatusOK indicates the puller is healthy.
	StatusOK Status = "ok"

	// StatusDegraded indicates the puller is running but with issues.
	StatusDegraded Status = "degraded"

	// StatusUnhealthy indicates the puller is not working properly.
	StatusUnhealthy Status = "unhealthy"
)

// BackendHealth represents the health of a single backend.
type BackendHealth struct {
	Name         string     `json:"name"`
	Status       Status     `json:"status"`
	LastEvent    *time.Time `json:"lastEvent,omitempty"`
	EventsTotal  int64      `json:"eventsTotal"`
	Errors       int        `json:"errors"`
	GapsDetected int        `json:"gapsDetected"`
}

// Report is the full health report.
type Report struct {
	Status     Status          `json:"status"`
	Uptime     string          `json:"uptime"`
	StartedAt  time.Time       `json:"startedAt"`
	Backends   []BackendHealth `json:"backends"`
	Consumers  int             `json:"consumers"`
	BufferSize int             `json:"bufferSize,omitempty"`
}

// Checker provides health check functionality.
type Checker struct {
	startedAt time.Time
	logger    *slog.Logger

	// mu protects health state
	mu sync.RWMutex

	// backendHealth maps backend name to health
	backendHealth map[string]*BackendHealth

	// consumerCount tracks active consumers
	consumerCount int

	// bufferSize tracks total buffer size
	bufferSize int
}

// NewChecker creates a new health checker.
func NewChecker(logger *slog.Logger) *Checker {
	if logger == nil {
		logger = slog.Default()
	}
	return &Checker{
		startedAt:     time.Now(),
		logger:        logger.With("component", "health"),
		backendHealth: make(map[string]*BackendHealth),
	}
}

// RegisterBackend registers a backend for health tracking.
func (h *Checker) RegisterBackend(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.backendHealth[name] = &BackendHealth{
		Name:   name,
		Status: StatusOK,
	}
}

// RecordEvent records an event for a backend.
func (h *Checker) RecordEvent(backend string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if bh, ok := h.backendHealth[backend]; ok {
		now := time.Now()
		bh.LastEvent = &now
		bh.EventsTotal++
		bh.Status = StatusOK
	}
}

// RecordError records an error for a backend.
func (h *Checker) RecordError(backend string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if bh, ok := h.backendHealth[backend]; ok {
		bh.Errors++
		if bh.Errors > 5 {
			bh.Status = StatusDegraded
		}
	}
}

// RecordGap records a gap detection for a backend.
func (h *Checker) RecordGap(backend string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if bh, ok := h.backendHealth[backend]; ok {
		bh.GapsDetected++
	}
}

// SetConsumerCount sets the active consumer count.
func (h *Checker) SetConsumerCount(count int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.consumerCount = count
}

// SetBufferSize sets the buffer size.
func (h *Checker) SetBufferSize(size int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.bufferSize = size
}

// GetReport returns the current health report.
func (h *Checker) GetReport() Report {
	h.mu.RLock()
	defer h.mu.RUnlock()

	report := Report{
		Status:     StatusOK,
		Uptime:     time.Since(h.startedAt).Round(time.Second).String(),
		StartedAt:  h.startedAt,
		Backends:   make([]BackendHealth, 0, len(h.backendHealth)),
		Consumers:  h.consumerCount,
		BufferSize: h.bufferSize,
	}

	// Check backend health
	for _, bh := range h.backendHealth {
		report.Backends = append(report.Backends, *bh)

		// Aggregate status
		if bh.Status == StatusUnhealthy {
			report.Status = StatusUnhealthy
		} else if bh.Status == StatusDegraded && report.Status == StatusOK {
			report.Status = StatusDegraded
		}
	}

	return report
}

// Check returns the overall health status.
func (h *Checker) Check() Status {
	return h.GetReport().Status
}

// ServeHTTP implements http.Handler for health endpoint.
func (h *Checker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	report := h.GetReport()

	w.Header().Set("Content-Type", "application/json")

	switch report.Status {
	case StatusOK:
		w.WriteHeader(http.StatusOK)
	case StatusDegraded:
		w.WriteHeader(http.StatusOK) // Still return 200 for degraded
	case StatusUnhealthy:
		w.WriteHeader(http.StatusServiceUnavailable)
	}

	json.NewEncoder(w).Encode(report)
}

// StartServer starts an HTTP health server.
func StartServer(ctx context.Context, addr string, checker *Checker) error {
	mux := http.NewServeMux()
	mux.Handle("/health", checker)

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdownCtx)
	}()

	checker.logger.Info("health server starting", "address", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
