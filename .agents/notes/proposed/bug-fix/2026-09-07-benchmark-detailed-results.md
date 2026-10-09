# Agent Note: Preserve Detailed Metrics in Benchmark Results

Status: proposed

## Problem

The [collector](../../../../packages/syntrix/pkg/benchmark/metrics/collector.go) already records
operation-specific counts and latency samples and exposes GetOperationMetrics.
The [runner](../../../../packages/syntrix/pkg/benchmark/runner/runner.go) assembles timestamps,
duration, configuration, and `result.Summary = r.metrics.GetSnapshot()` only.
It does not populate the Result type's SessionID, Name, Operations, Errors, or
Timeline. The [console reporter](../../../../packages/syntrix/pkg/benchmark/reporter/console.go)
already renders operation metrics when present, so collected detail is lost at
the assembly boundary. Static inspection establishes this integration gap;
detailed error entries and timeline sampling still require collection work.

## Proposal

Make a coherent snapshot of summary and per-operation metrics available through
the collector contract, then assemble a complete Result for each run. Assign
session identity and name once, record sanitized effective configuration and
benchmark build provenance, and freeze the measured interval at completion.
Summary and operation counts must describe the same recorded operations and
time interval, including failures; percentiles remain independently computed
distributions and must not be added or averaged across operation groups.

Collect bounded error examples with timestamp, operation, worker identity, and
stable error category, plus bounded timeline samples at the configured interval.
Preserve exact aggregate error counts when examples are truncated and record the
truncation. Avoid raw response bodies and document payloads in messages. A final
snapshot must remain immutable after collector reset or later runs and must not
change throughput as the report is rendered later. Propagate fatal run failures
with a clearly labeled partial result so output cannot mistake partial data for
a completed run.

## Alternatives

**Let each reporter inspect the collector.** This exposes live mutable state and
allows console, JSON, and monitoring views to disagree about a completed run.

**Drop the unused Result fields.** This simplifies assembly but removes the
operation breakdown and diagnostic history promised by the
[benchmark design](../../../../docs/benchmark/DESIGN.md). Reconsider only if
those reporting requirements are explicitly removed.

## Acceptance Criteria

- A fixture containing successful and failed operations has complete identity,
  matching summary/operation counts, and expected per-operation percentiles.
- Snapshot mutation, collector reset, concurrent reads, and subsequent runs
  cannot change a previously completed result.
- Final throughput uses the frozen measured interval and is unchanged by later
  serialization; empty runs contain finite numerical values.
- Error and timeline retention obey documented bounds while aggregate counts
  remain correct; partial runs and truncated samples are explicitly labeled.
- Result examples contain neither credentials nor raw response/document payloads.

## Risks

Consistent snapshots can lengthen collector lock hold times. Bounded sampling and
copying must be measured so reporting does not materially distort the workload.
Error classification must preserve useful failure distinctions without allowing
unbounded categories from arbitrary response text.

## Dependencies

[Load scheduling](../feature/2026-09-07-benchmark-load-scheduling.md) defines the
measurement interval and phase ownership. Report export and monitoring consume
this result contract rather than defining separate aggregation semantics.
