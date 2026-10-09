package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
)

func TestMetrics_Registration(t *testing.T) {
	// This test mainly ensures that init() runs without panic
	// and that metrics are registered.
	// Since prometheus.MustRegister panics on double registration,
	// and init() runs automatically, we just check if we can access them.

	assert.NotNil(t, EventsIngested)
	assert.NotNil(t, IngestionLatency)
	assert.NotNil(t, EventsPublished)
	assert.NotNil(t, PublishLatency)
	assert.NotNil(t, PublishErrors)
	assert.NotNil(t, CheckpointsSaved)
	assert.NotNil(t, CheckpointErrors)
	assert.NotNil(t, GapsDetected)
	assert.NotNil(t, BackpressureEvents)
	assert.NotNil(t, QueueDepth)

	// Try to use them to ensure no runtime errors
	EventsIngested.WithLabelValues("test").Inc()
	IngestionLatency.WithLabelValues("test").Observe(0.1)
	QueueDepth.WithLabelValues("test", "buffer").Set(10)
}

func TestMetrics_DoubleInit(t *testing.T) {
	// We can't really test double init easily because it happens at package load time.
	// But we can verify that the global registry contains our metrics.

	// Note: In a real test environment, we might want to use a custom registry
	// to avoid polluting the global one, but the code uses the global one.

	// Just a sanity check that we can collect from them.
	ch := make(chan prometheus.Metric, 100)
	EventsIngested.Collect(ch)
	assert.NotEmpty(t, ch)
}
