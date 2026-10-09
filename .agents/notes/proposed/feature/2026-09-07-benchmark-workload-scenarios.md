# Agent Note: Complete Benchmark Workload Scenarios

Status: proposed

## Problem

The benchmark validates `crud`, `query`, `realtime`, and `mixed` in the
[configuration loader](../../../../packages/syntrix/pkg/benchmark/config/loader.go), but its
[CLI dispatch](../../../../packages/syntrix/cmd/syntrix-benchmark/main.go) only constructs a
scenario for `case "crud", "":`. The existing CRUD scenario, HTTP query client,
runner, and metrics collector are functional implementations. The missing work
is scenario integration: the [client](../../../../packages/syntrix/pkg/benchmark/client/http.go)
returns `"realtime subscriptions not yet implemented in HTTP client"` from
Subscribe and Unsubscribe. This finding follows static call-path inspection.

## Proposal

Complete the three accepted scenario types through the existing Scenario and
Client contracts. Query scenarios should seed controlled data and exercise
filtered, ordered, and paginated reads with result validation. Realtime scenarios
should measure connection setup, subscription setup, and write-to-event latency
separately, including filtered subscriptions and the configured number of
connections and subscriptions. Mixed scenarios should compose explicitly weighted
CRUD, query, and realtime activity without conflating request latency with event
delivery latency.

Validate operation-specific configuration before setup. A run owns its seeded
documents and subscriptions; cancellation, setup failure, and normal completion
must close connections and attempt bounded cleanup, reporting cleanup failures.
Use run-scoped identifiers to correlate writes and received events and report
timeouts, duplicates, and unexpected events without logging document payloads.
Keep fixtures scoped to the configured database so parallel database runs remain
independent. Update the [benchmark design](../../../../docs/benchmark/DESIGN.md)
and examples with the delivered workload contracts.

## Alternatives

**Restrict the accepted configuration to CRUD.** This would make validation
honest but leave the intended query and realtime performance questions unanswered.
It is appropriate only if those requirements are explicitly withdrawn.

**Use separate load-generation tools.** They can exercise HTTP and WebSocket
traffic, but maintaining database fixtures, subscription semantics, and comparable
metrics in separate tools adds operational work. Reconsider if an existing tool
can share these contracts without a second implementation.

## Acceptance Criteria

- Every accepted scenario executes its documented operations; invalid operation
  configurations fail before fixture writes.
- Query fixtures produce the expected result sets; realtime fixtures correlate
  writes with events and expose delivery failures within a bounded timeout.
- Mixed runs report each operation class separately and preserve configured
  weights; independent database runs neither consume nor delete each other's data.
- Cancellation and setup failure release subscriptions and report cleanup outcomes.

## Risks

Fixture preparation and client-side validation can dominate measured latency.
Separate setup and verification costs from operation measurements. Unbounded event
queues would hide slow consumers; cap queues and count overflow as a run failure.

## Dependencies

[Detailed results](../bug-fix/2026-09-07-benchmark-detailed-results.md) owns result
assembly; [load scheduling](2026-09-07-benchmark-load-scheduling.md) owns phases
and operation rates.
