# Agent Note: Application Telemetry Across Service Boundaries

Status: proposed

## Problem

The deployment includes Prometheus and Grafana configuration, but the application
does not expose a metrics handler. The [metrics design](../../../../docs/design/monitor/003.metrics_collection.md)
describes collection across services. Current [trigger counters](../../../../packages/syntrix/internal/trigger/types/metrics.go#L28)
only execute `_ = database`; normal factories select that no-op implementation.
Puller registers collectors, while many events never update them. Shared
[gRPC interceptors](../../../../packages/syntrix/internal/server/middleware.go#L255) contain only
`recoveryUnaryInterceptor` and `loggingUnaryInterceptor`. The proposed tracing
pipeline has no corresponding runtime instrumentation.

These static findings leave operators unable to distinguish stopped ingestion,
delayed indexing, failed delivery, and disconnected clients using the supplied
monitoring stack.

## Proposal

Give each service-manager instance an owned metrics registry and telemetry
lifecycle. Register an operationally restricted metrics endpoint on the shared
HTTP server, including processes that run without Gateway. Connect bounded
request, query, indexing, ingestion, subscription, and delivery counters and
histograms to their actual success, failure, retry, and cancellation paths.
Implement the trigger metrics interface and reconcile existing Puller collectors
with the registry so multiple instances do not collide.

Propagate request/trace identity across HTTP, gRPC, and asynchronous trigger work;
link background operations to their originating event when one exists. Capture
structured error category, component, phase, and correlation identifiers without
tokens, secrets, or document payloads. Keep identifiers out of unbounded metric
labels. Metric dimensions and database cardinality need explicit limits.

Use the existing Prometheus dependency for scraping. Evaluate tracing dependencies
and exporters against the [monitoring architecture](../../../../docs/design/monitor/001.architecture.md)
before implementation. Export work must have bounded queues, cancellation,
observable drops, and bounded shutdown; exporter outages must not change data
operation results. [Puller health reporting](../feature/2026-09-07-puller-health-reporting.md)
owns ingestion continuity reports. [Console health probes](../bug-fix/2026-09-07-console-health-probes.md)
owns the authenticated backend readiness aggregation/API for Gateway, identity,
and database dependencies. This proposal instruments those reports and their
failures without determining readiness semantics.

## Alternatives

**Instrument each component independently.** This allows local delivery but
duplicates registration, label policy, and exporter lifecycle, making cross-service
correlation harder to maintain.

**Use logs alone.** Logs preserve individual failures, but do not supply bounded
aggregate lag, latency, and queue measurements for the supplied dashboards.

## Acceptance Criteria

- Standalone and each supported distributed service expose real metrics; repeated
  construction and shutdown do not cause duplicate registration or leaked workers.
- Controlled successful, failed, retried, and canceled operations change the
  corresponding measurements without counting the same event twice unintentionally.
- A known request trace can be followed through Query and its asynchronous event
  delivery, with documented links where direct parentage is inappropriate.
- Exporter unavailability keeps data results unchanged, bounds telemetry memory,
  and reports dropped telemetry. Tests reject secret/payload leakage and unbounded labels.
- An operations guide shows how to scrape the endpoint and search a controlled
  failure by its trace or event identifier in the chosen deployed backend.

## Risks

Cardinality, sampling, and exporter work can consume the resources being measured.
Instrumentation must not imply stronger event-delivery guarantees than the data
path provides. Metrics naming changes also require synchronized dashboards and
alerts; runtime health checks remain a separate correctness signal.
