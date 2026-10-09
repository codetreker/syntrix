package flowcontrol

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Action represents the action to take based on backpressure.
type Action int

const (
	// ActionNone means no action is needed.
	ActionNone Action = iota
	// ActionSlowDown means reduce batch size or increase wait time.
	ActionSlowDown
	// ActionPause means pause publishing and alert.
	ActionPause
)

// BackpressureMonitor monitors system health and suggests actions.
type BackpressureMonitor struct {
	publishLatency    *prometheus.HistogramVec
	queueDepth        *prometheus.GaugeVec
	slowThreshold     time.Duration
	criticalThreshold time.Duration
}

// BackpressureOptions configures the backpressure monitor.
type BackpressureOptions struct {
	SlowThreshold     time.Duration
	CriticalThreshold time.Duration
	PublishLatency    *prometheus.HistogramVec
	QueueDepth        *prometheus.GaugeVec
}

// NewBackpressureMonitor creates a new backpressure monitor.
func NewBackpressureMonitor(opts BackpressureOptions) *BackpressureMonitor {
	slow := opts.SlowThreshold
	if slow == 0 {
		slow = 100 * time.Millisecond
	}
	critical := opts.CriticalThreshold
	if critical == 0 {
		critical = 500 * time.Millisecond
	}

	return &BackpressureMonitor{
		publishLatency:    opts.PublishLatency,
		queueDepth:        opts.QueueDepth,
		slowThreshold:     slow,
		criticalThreshold: critical,
	}
}

// HandleBackpressure analyzes latency and returns the recommended action.
func (b *BackpressureMonitor) HandleBackpressure(latency time.Duration) Action {
	if b.publishLatency != nil {
		b.publishLatency.WithLabelValues("grpc").Observe(latency.Seconds())
	}

	switch {
	case latency < b.slowThreshold:
		return ActionNone
	case latency < b.criticalThreshold:
		return ActionSlowDown
	default:
		return ActionPause
	}
}

// RecordQueueDepth records the current queue depth.
func (b *BackpressureMonitor) RecordQueueDepth(depth int) {
	if b.queueDepth != nil {
		b.queueDepth.WithLabelValues("buffer").Set(float64(depth))
	}
}
