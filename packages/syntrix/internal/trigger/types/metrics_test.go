package types

import (
	"testing"
	"time"
)

func TestNoopMetrics(t *testing.T) {
	m := &NoopMetrics{}

	// Call all methods to ensure coverage
	m.IncPublishSuccess("database", "collection", true)
	m.IncPublishFailure("database", "collection", "reason")
	m.ObservePublishLatency("database", "collection", time.Second)

	m.IncConsumeSuccess("database", "collection", true)
	m.IncConsumeFailure("database", "collection", "reason")
	m.ObserveConsumeLatency("database", "collection", time.Second)
	m.IncHashCollision("database", "collection")

	m.IncDeliverySuccess("database", "collection")
	m.IncDeliveryFailure("database", "collection", 500, true)
	m.IncDeliveryRetry("database", "collection")
	m.ObserveDeliveryLatency("database", "collection", time.Second)
}
