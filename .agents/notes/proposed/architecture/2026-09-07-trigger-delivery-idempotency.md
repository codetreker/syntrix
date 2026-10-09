# Agent Note: Preserve Trigger Delivery Identity Across Retries

Status: proposed

## Problem

The [publisher design](../../../../docs/design/server/trigger/evaluator/03.publisher.md)
calls for an idempotency key derived from original document and event identity.
[The active publisher](../../../../packages/syntrix/internal/trigger/evaluator/publisher.go) sends
only subject and serialized task bytes, while
[the worker](../../../../packages/syntrix/internal/trigger/delivery/worker/worker.go) sends each
received task without a stable idempotency header or delivery ledger. Broker
redelivery, replay, or a lost acknowledgement can repeat a webhook effect.
Memory queue acceptance also cannot preserve a task across process restart.

## Proposal

Define a deterministic delivery ID from database, trigger ID and version,
original document identity, and backend-qualified canonical source event
identity. Use a structured encoding or a digest of that encoding; never derive
it from a hashed routing subject.

In both deployment modes, persist a scheduling record containing immutable rule
selection before evaluation and fan-out. Persist evaluation outcomes and durable
outbox records for all matched tasks, then mark scheduling complete. A successful
no-match outcome also completes scheduling. Use existing storage facilities with
explicit conditional creation and ownership semantics; partial recovery reuses
the recorded selection and outcomes, and conflicting immutable content is an
error. Only complete durable scheduling confers checkpoint eligibility.

A bounded dispatcher publishes pending records and recovers them after restart.
Publication acknowledgement records broker dispatch. Consumer acknowledgement
follows the proposed [durable task handoff](2026-09-07-trigger-acknowledgement-window.md):
verify or establish execution responsibility in the same task record before Ack,
without a second inbox or resetting an existing task. Neither acknowledgement
proves HTTP delivery. Retain nonterminal task records for redrive after queue loss,
including a standalone restart. Delivery workers claim tasks with fenced
ownership, preserve attempt history, and record acknowledged success or terminal
failure. Durable execution state owns attempts and retry scheduling; duplicate
broker receipts do not consume attempts. Duplicate completed tasks can be
acknowledged without another request.
Claims and dispatch waits observe cancellation. Define outbox admission bounds,
record schema, indexes, lease recovery, and retention covering replay/retry
horizons before implementation; full storage must backpressure scheduling.

Send the same delivery ID to the receiver on every HTTP attempt and document how
the receiver atomically records it with its business effect. A timeout or lost
HTTP response remains ambiguous: local deduplication cannot prove the receiver
committed. The guarantee is at-least-once delivery with a stable deduplication key,
not exactly-once external effects.

## Alternatives

**Broker-only deduplication** reduces duplicate publication within the broker's
window but cannot preserve partially scheduled rule outcomes, resolve replay
outside that window, or determine an ambiguous HTTP result.

**Receiver-only deduplication** protects cooperative business endpoints but does
not make the standalone queue durable or expose local task outcomes. It remains
required for the final external side-effect boundary.

## Acceptance Criteria

- Replaying a scheduled event or redelivering a message preserves its selected
  rule versions, outcomes, and delivery IDs; database and source identity
  differences cannot accidentally share scheduling or outbox records.
- Incomplete evaluation or fan-out remains checkpoint-ineligible; complete
  scheduling survives a broker outage and permits source progress in both modes.
- Crashes around scheduling, publication, claim, HTTP response, and success
  recording leave work recoverable or explicitly ambiguous, with bounded retries.
- Completed-task duplicates avoid another HTTP call within the declared retention
  horizon; a receiver fixture deduplicates ambiguous repeated calls atomically.
- Restart, lease expiry, cancellation, and cleanup preserve documented ownership,
  redrive, and retention behavior without unbounded queues or storage growth.

## Risks

The outbox adds writes, indexes, retention cost, and dispatcher ownership. Losing
retained identities weakens deduplication; migrating existing queued tasks cannot
invent missing event identities. Task diagnostics must exclude secrets and full
sensitive payloads.

## Dependencies

[Event envelopes](../bug-fix/2026-09-07-trigger-delivery-event-envelope.md) define
canonical identity; [checkpoint ordering](../bug-fix/2026-09-07-trigger-publish-checkpoint-ordering.md)
owns source progress after durable scheduling; [application observability](2026-09-07-application-observability.md)
owns shared telemetry infrastructure.
