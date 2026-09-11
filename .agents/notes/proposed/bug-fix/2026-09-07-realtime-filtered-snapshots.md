# Agent Note: Complete Filtered Realtime Snapshots

Status: proposed

## Problem

A realtime subscription can request a filtered initial snapshot, but
[the WebSocket handler](../../../../internal/gateway/realtime/client.go) calls
replication Pull with `Checkpoint: 0` and `Limit: 1000`. It passes filters to
live matching but not the snapshot.
[Replication Pull](../../../../internal/query/core/engine.go) uses an update-time
predicate and `ShowDeleted: true`, so this snapshot can contain nonmatching
rows or tombstones and truncate results without a completeness indicator.
Snapshot failures are logged after acknowledgment without a client error.

In [Streamer processing](../../../../internal/streamer/service.go),
`doc := helper.FlattenStorageDocument(event.Document)` supplies only the current
document to subscription matching. A snapshot row changing from `status=pending`
to `status=completed` stops matching without a removal notification, leaving
stale client state. These static findings affect the snapshot-and-delta behavior
in [the realtime design](../../../../docs/design/server/gateway/realtime_watching.md).

## Proposal

Build snapshots through an authoritative snapshot operation in the Query
service, using the same validated database, collection, and predicate as live
matching. Read storage at a declared boundary so index lag cannot omit changes
before the snapshot cursor. Explicitly reject subscription query clauses whose
live semantics are unsupported. Honor accepted clauses and exclude deleted rows
unless explicitly supported and requested. Return bounded pages, continuation,
and explicit completion without an implicit 1,000-document cap.

This proposal owns membership transitions for snapshot-backed subscriptions
through handoff and subsequent live delivery. Streamer maintains a bounded
per-subscription set of member document identities, seeded from the snapshot.
Buffer collection changes from the read boundary before applying the predicate;
publish completion only after the seed and boundary are established, then drain
changes in order. Matching documents enter or update the result. A known member
that stops matching or is deleted produces a removal carrying its identity,
version/event identity, and reason, even when its current document no longer
matches. A filter-exit removal changes subscription membership, not stored data.

Reconcile duplicate versions so an older snapshot row cannot overwrite a newer
change. Deletes with a known identity remove prior members without requiring a
current matching document. If membership, deletion identity, or continuity cannot
be determined, invalidate the subscription with `resync-required`; never leave
it marked synchronized. Unknown membership without a snapshot follows the same
invalidation rule. Bound membership and change buffers and invalidate on overflow.

Tie queries, membership, and buffers to subscription cancellation and deadlines.
Surface correlated failures and release resources on unsubscribe or disconnect.
Document the snapshot, membership-removal, and resynchronization messages and
update SDK application of them together.

## Alternatives

**Filter the replication batch in memory.** This retains truncation and cannot
establish completeness or detect later filter exits.

**Evaluate both document images.** Before/after matching can avoid a member set,
but images are not guaranteed throughout the current event path. Reconsider
when their availability and deletion semantics are established end to end;
missing images would still require visible resynchronization.

**Fetch all matches in one response.** This avoids paging but makes memory and
frame size proportional to the result. It is appropriate only for an explicit
size bound and does not solve membership transitions.

## Acceptance Criteria

- More than 1,000 matching rows produce complete current state without unrelated
  or deleted rows; identical document IDs remain isolated by database.
- A snapshot member exiting its filter during paging, buffered replay, or live
  delivery is removed; entering the filter adds it once after deduplication.
- Deletes remove prior members even without a current matching document. Missing
  identity or membership evidence produces visible resynchronization.
- Concurrent changes cannot disappear at handoff or let an older snapshot row
  overwrite newer state; replayed removals remain idempotent.
- Unsupported clauses, query failures, timeouts, and membership/buffer overflow
  cannot report successful synchronization; cancellation releases all state.

## Risks

Membership tracking adds state proportional to subscription results and must
be rebuilt or invalidated after restart. Complete snapshots also increase
storage reads. Pagination and version reconciliation alone cannot preserve
filtered state; deferring transition tracking leaves snapshots stale after filter
exits. Consumers must not equate connection health with synchronized membership.

## Dependencies

[Query cursor pagination](../../implemented/feature/2026-09-07-query-cursor-pagination.md) and
[indexed filter semantics](../../implemented/bug-fix/2026-09-07-indexed-query-filter-semantics.md) supply
query behavior. [Client resume](../feature/2026-09-07-realtime-client-resume.md)
owns continuity cursors and transports this membership/removal/resynchronization
contract across reconnects.
