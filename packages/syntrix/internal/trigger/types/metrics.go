package types

import "time"

// Metrics defines the interface for trigger engine telemetry.
type Metrics interface {
	// Publisher metrics
	IncPublishSuccess(database, collection string, subjectHashed bool)
	IncPublishFailure(database, collection string, reason string)
	ObservePublishLatency(database, collection string, duration time.Duration)

	// Consumer metrics
	IncConsumeSuccess(database, collection string, subjectHashed bool)
	IncConsumeFailure(database, collection string, reason string)
	ObserveConsumeLatency(database, collection string, duration time.Duration)
	IncHashCollision(database, collection string)

	// Worker metrics
	IncDeliverySuccess(database, collection string)
	IncDeliveryFailure(database, collection string, status int, fatal bool)
	IncDeliveryRetry(database, collection string)
	ObserveDeliveryLatency(database, collection string, duration time.Duration)
}

// NoopMetrics is a no-op implementation of Metrics.
type NoopMetrics struct{}

func (m *NoopMetrics) IncPublishSuccess(database, collection string, subjectHashed bool) {
	_ = database
}
func (m *NoopMetrics) IncPublishFailure(database, collection string, reason string) {
	_ = database
}
func (m *NoopMetrics) ObservePublishLatency(database, collection string, duration time.Duration) {
	_ = database
}

func (m *NoopMetrics) IncConsumeSuccess(database, collection string, subjectHashed bool) {
	_ = database
}
func (m *NoopMetrics) IncConsumeFailure(database, collection string, reason string) {
	_ = database
}
func (m *NoopMetrics) ObserveConsumeLatency(database, collection string, duration time.Duration) {
	_ = database
}
func (m *NoopMetrics) IncHashCollision(database, collection string) {
	_ = database
}

func (m *NoopMetrics) IncDeliverySuccess(database, collection string) {
	_ = database
}
func (m *NoopMetrics) IncDeliveryFailure(database, collection string, status int, fatal bool) {
	_ = database
}
func (m *NoopMetrics) IncDeliveryRetry(database, collection string) {
	_ = database
}
func (m *NoopMetrics) ObserveDeliveryLatency(database, collection string, duration time.Duration) {
	_ = database
}
