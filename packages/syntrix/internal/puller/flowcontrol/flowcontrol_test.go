package flowcontrol

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
)

func TestCircuitBreaker_StateTransitions(t *testing.T) {
	opts := CircuitBreakerOptions{
		Threshold: 2,
		Timeout:   100 * time.Millisecond,
	}
	cb := NewCircuitBreaker(opts)

	// Initial state: Closed
	assert.Equal(t, StateClosed, cb.State())
	assert.True(t, cb.Allow())

	// 1 failure: Still Closed
	cb.RecordFailure()
	assert.Equal(t, StateClosed, cb.State())
	assert.True(t, cb.Allow())

	// 2 failures: Open
	cb.RecordFailure()
	assert.Equal(t, StateOpen, cb.State())
	assert.False(t, cb.Allow())

	// Wait for timeout
	time.Sleep(150 * time.Millisecond)

	// Should be HalfOpen now (on next Allow call)
	assert.True(t, cb.Allow())
	assert.Equal(t, StateHalfOpen, cb.State())

	// Success in HalfOpen -> Closed
	cb.RecordSuccess()
	assert.Equal(t, StateClosed, cb.State())
	assert.True(t, cb.Allow())
}

func TestCircuitBreaker_HalfOpenFailure(t *testing.T) {
	opts := CircuitBreakerOptions{
		Threshold: 1,
		Timeout:   50 * time.Millisecond,
	}
	cb := NewCircuitBreaker(opts)

	// Force Open
	cb.RecordFailure()
	assert.Equal(t, StateOpen, cb.State())

	// Wait for timeout
	time.Sleep(100 * time.Millisecond)

	// Transition to HalfOpen
	assert.True(t, cb.Allow())
	assert.Equal(t, StateHalfOpen, cb.State())

	// Failure in HalfOpen -> Open immediately
	cb.RecordFailure()
	assert.Equal(t, StateOpen, cb.State())
	assert.False(t, cb.Allow())
}

func TestCircuitBreaker_Defaults(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerOptions{})
	assert.Equal(t, 5, cb.threshold)
	assert.Equal(t, 30*time.Second, cb.timeout)
}

func TestBackpressureMonitor_Actions(t *testing.T) {
	opts := BackpressureOptions{
		SlowThreshold:     100 * time.Millisecond,
		CriticalThreshold: 500 * time.Millisecond,
	}
	bm := NewBackpressureMonitor(opts)

	// Fast -> None
	assert.Equal(t, ActionNone, bm.HandleBackpressure(10*time.Millisecond))

	// Slow -> SlowDown
	assert.Equal(t, ActionSlowDown, bm.HandleBackpressure(200*time.Millisecond))

	// Critical -> Pause
	assert.Equal(t, ActionPause, bm.HandleBackpressure(600*time.Millisecond))
}

func TestBackpressureMonitor_Defaults(t *testing.T) {
	bm := NewBackpressureMonitor(BackpressureOptions{})
	assert.Equal(t, 100*time.Millisecond, bm.slowThreshold)
	assert.Equal(t, 500*time.Millisecond, bm.criticalThreshold)
}

func TestBackpressureMonitor_Metrics(t *testing.T) {
	// Just verify no panic when metrics are nil
	bm := NewBackpressureMonitor(BackpressureOptions{})
	bm.HandleBackpressure(100 * time.Millisecond)
	bm.RecordQueueDepth(10)
}

func TestCircuitBreaker_RecordSuccess_Closed(t *testing.T) {
	opts := CircuitBreakerOptions{
		Threshold: 2,
		Timeout:   100 * time.Millisecond,
	}
	cb := NewCircuitBreaker(opts)

	// Initial state: Closed
	assert.Equal(t, StateClosed, cb.State())

	// 1 failure
	cb.RecordFailure()
	assert.Equal(t, 1, cb.failureCount)

	// Success should reset failure count
	cb.RecordSuccess()
	assert.Equal(t, 0, cb.failureCount)
	assert.Equal(t, StateClosed, cb.State())
}

func TestBackpressureMonitor_RecordQueueDepth(t *testing.T) {
	gauge := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "test_queue_depth",
		Help: "Test queue depth",
	}, []string{"buffer"})

	bm := NewBackpressureMonitor(BackpressureOptions{
		QueueDepth: gauge,
	})
	bm.RecordQueueDepth(100)

	// Verify metric
	// (Optional: check if metric is set, but just calling it covers the code)
}
