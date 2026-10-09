package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	// Ingestion
	EventsIngested = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "puller_events_ingested_total",
		Help: "The total number of events ingested",
	}, []string{"backend"})

	IngestionLatency = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "puller_ingestion_latency_seconds",
		Help: "The latency of event ingestion",
	}, []string{"backend"})

	// Publishing
	EventsPublished = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "puller_events_published_total",
		Help: "The total number of events published",
	}, []string{"backend"})

	PublishLatency = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "puller_publish_latency_seconds",
		Help: "The latency of event publishing",
	}, []string{"backend"})

	PublishErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "puller_publish_errors_total",
		Help: "The total number of publish errors",
	}, []string{"backend"})

	// Checkpoints
	CheckpointsSaved = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "puller_checkpoints_saved_total",
		Help: "The total number of checkpoints saved",
	}, []string{"backend"})

	CheckpointErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "puller_checkpoint_errors_total",
		Help: "The total number of checkpoint errors",
	}, []string{"backend"})

	// Gaps
	GapsDetected = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "puller_gaps_detected_total",
		Help: "The total number of gaps detected",
	}, []string{"backend"})

	// Backpressure
	BackpressureEvents = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "puller_backpressure_events_total",
		Help: "The total number of backpressure events",
	}, []string{"backend", "level"})

	QueueDepth = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "puller_queue_depth",
		Help: "The current depth of the event queue",
	}, []string{"backend", "component"})
)

func init() {
	prometheus.MustRegister(EventsIngested)
	prometheus.MustRegister(IngestionLatency)
	prometheus.MustRegister(EventsPublished)
	prometheus.MustRegister(PublishLatency)
	prometheus.MustRegister(PublishErrors)
	prometheus.MustRegister(CheckpointsSaved)
	prometheus.MustRegister(CheckpointErrors)
	prometheus.MustRegister(GapsDetected)
	prometheus.MustRegister(BackpressureEvents)
	prometheus.MustRegister(QueueDepth)
}
