# Agent Note: Integrate the Benchmark Monitoring Server

Status: proposed

## Problem

The [benchmark configuration](../../../../packages/syntrix/pkg/benchmark/types/types.go) defines
Monitor.Enabled, Address, AutoOpen, and Interval. Static inspection of the
[CLI](../../../../packages/syntrix/cmd/syntrix-benchmark/main.go) shows console progress reporting
but no monitoring server or dashboard startup. The
[monitoring design](../../../../docs/benchmark/monitoring.md) specifies HTTP
status, metrics, workers, errors, timeline, and an embedded dashboard; these are
planned capabilities, not a working benchmark endpoint.

## Proposal

Give the benchmark process ownership of an optional HTTP server and embedded
dashboard. Publish immutable, session-scoped snapshots at Monitor.Interval so
HTTP requests and slow browsers cannot block operation recording. Preserve the
design's status, metrics, worker, error, and bounded timeline views. Distinct
runner sessions in one process must remain separately addressable; this does not
introduce distributed run orchestration.

Default to the existing local listen address and require explicit configuration
for network exposure. Bind before advertising a URL; AutoOpen should act only
after a successful bind. Monitoring startup or runtime failures should emit a
clear diagnostic and leave the workload running, as the design's graceful
degradation requirement intends. The workload owner cancels sampling and performs
bounded server shutdown on completion, failure, or cancellation. A terminal
snapshot must be observable before shutdown, with its availability policy
documented. Expose sanitized configuration, counts, and error categories without
credentials or full response bodies.

## Alternatives

**Console-only progress.** This has low overhead but cannot provide the requested
timeline and concurrent session inspection. It remains available when monitoring
is disabled.

**An external metrics dashboard.** This reuses operational infrastructure but
introduces deployment prerequisites and does not satisfy the embedded, local
dashboard requirement. Reconsider for long-term history across processes.

## Acceptance Criteria

- Enabling monitoring serves the documented endpoints and dashboard; disabling
  it creates no listener or sampling goroutine.
- Two simultaneous sessions have isolated status, metrics, workers, and timelines.
- A port conflict or disconnected browser produces a diagnostic while the run
  continues; shutdown releases the listener and sampler within a fixed timeout.
- Timeline retention is bounded and the documented overhead target is measured
  against the same workload with monitoring disabled.
- Rendered data and JSON contain neither credentials nor raw document payloads.

## Risks

Snapshot copying and histogram sorting may bias results under load. Bound sampling
frequency, retention, and response sizes. Network binding exposes operational
metadata and must remain an explicit choice.

## Dependencies

[Detailed results](../bug-fix/2026-09-07-benchmark-detailed-results.md) owns coherent
metric snapshots. [Application observability](../architecture/2026-09-07-application-observability.md)
owns production service telemetry; this note owns the benchmark process UI.
