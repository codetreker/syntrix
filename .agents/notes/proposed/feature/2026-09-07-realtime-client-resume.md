# Agent Note: Realtime Client Resume and Resynchronization

Status: proposed

## Problem

The [realtime design](../../../../docs/design/server/gateway/realtime_watching.md)
calls for brief-disconnect replay and resynchronization outside retention.
The [client protocol](../../../../packages/syntrix/internal/gateway/realtime/protocol.go) has no
replay cursor, resume request, or resynchronization status. In
[the Hub](../../../../packages/syntrix/internal/gateway/realtime/hub.go), the public event ID uses
collection and document ID, so separate changes to one document are not uniquely
identified. Its empty `case <-time.After(50 * time.Millisecond):` branch discards
an event when a client queue stays full without invalidating the subscription.

WebSocket reconnection and internal Gateway-to-Streamer re-registration already
exist. Static inspection shows that they restore connections and subscriptions,
not missed deliveries or lost query membership.

## Proposal

Define a resume contract across the public protocol, Streamer RPC, Gateway, and
SDK. Deliver a stable event identity and opaque cursor bound to the database,
subscription query, and stream generation. Resume after the last applied cursor,
reauthorize, and replay retained subscription changes before live delivery.
Repeated delivery is allowed; deduplication must distinguish separate changes
to one document.

Replay uses the membership contract owned by
[filtered snapshots](../bug-fix/2026-09-07-realtime-filtered-snapshots.md): entries,
updates, and removals for filter exits or deletes. Retain these outcomes with
their identities and ordering, rather than reconstructing them by matching only
current documents. SDKs apply removals to subscription results without treating
a filter exit as a storage delete. The applied cursor advances only after the
corresponding membership change is applied. Lost membership state or uncertain
transition history requires `resync-required`, even if some events remain in
the replay window.

Use a bounded replay window with one owner and time, count, and byte limits,
starting from the existing design's window requirement. Restart loss, expired
cursors, and queue overflow invalidate continuity. If resynchronization status
cannot be sent, terminate the stream so reconnection cannot falsely confirm
continuity. Failed resumes must never silently start at the current head.

Map SSE event IDs and `Last-Event-ID` to the same semantics. Expose SDK resume
and resynchronization outcomes, cancel replay on disconnect, bound per-client
work, and isolate slow subscribers. Document the coordinated server/SDK wire
change. Diagnostics need subscription, generation, replay count, and reason
without raw document data. These mechanisms provide bounded replay or explicit
resynchronization, not exactly-once delivery.

## Alternatives

**Reconcile on every reconnect.** A query or replication pull avoids retaining
transitions but increases storage load and cannot return every intermediate
change. Reconciliation remains the explicit recovery path when replay or
membership continuity is unavailable.

**Durable per-client queues.** Longer retention can survive process replacement,
but needs persistent subscription ownership and cleanup. Reconsider if the
required outage window exceeds bounded replay plus resynchronization.

## Acceptance Criteria

- WS and SSE replay retained entries, updates, filter exits, and deletes without
  losing their order; duplicates do not corrupt the subscription result.
- Reconnection after a snapshot member exits its filter or is deleted removes
  stale state, or explicitly requires resynchronization when evidence is lost.
- Expiry, generation or membership loss, changed query, and unauthorized database
  cursors cannot receive a successful continuity acknowledgment.
- Slow-client saturation invalidates continuity without indefinitely delaying
  other clients; cancellation and restart release replay resources.

## Risks

Protocol, retained transitions, and ownership must change together. An event
window alone cannot reconstruct unavailable membership. Retrofitting this after
consumers assume lossless live events increases migration cost. The SDK's
replication checkpoint stays separate from its realtime cursor, as required by
[the replication design](../../../../docs/design/sdk/002_replication_client.md).

## Dependencies

[Durable Streamer progress](../architecture/2026-09-07-streamer-durable-progress.md)
does not substitute for client replay.
[History-gap recovery](../architecture/2026-09-07-puller-history-gap-recovery.md)
propagates continuity loss, and the linked filtered snapshot proposal owns
membership semantics and the complete-state recovery path.
