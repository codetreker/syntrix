# Agent Note: Make Replication Pull Checkpoints Advance Reliably

Status: proposed

## Problem

The [Pull implementation](../../../../internal/query/core/engine.go) filters `updatedAt >= checkpoint`, sorts by timestamp and ID, and returns only the last timestamp as the checkpoint. A page filled by documents sharing that timestamp can repeat indefinitely. Simply changing the comparison to strict greater-than would omit remaining documents at the same timestamp.

The [replication design](../../../../docs/design/server/gateway/replication.md) promises a deterministic monotonic checkpoint. Existing millisecond wall-clock values also cannot establish committed-write order when a later write has an equal or earlier timestamp. These are static findings; an end-to-end replication run has not been performed.

## Proposal

Separate replication position from user-visible `updatedAt`. Use a versioned opaque checkpoint over a durable committed change position, with an explicit bootstrap-scan phase and live-replay phase. Prefer the existing Puller event history as that source, subject to its durability and history-gap guarantees. The checkpoint must carry enough scope and generation information to reject use for another database or collection.

Bootstrap captures a replay fence before scanning current documents using exclusive ID continuation, then replays changes from that fence before switching to incremental delivery. Incremental pages advance after the last consumed committed event, including events excluded by collection filtering. Duplicate document states are permitted and clients apply them idempotently; no mutation may disappear merely because a timestamp equals or precedes the prior page. Bound page work, honor cancellation, and report expired history as a resynchronization requirement.

Update HTTP, storage-facing types, protobuf, SDK persistence, and reference documents together. The wire field remains a string, but its documented stringified-int64 restriction changes. Define explicit reset or migration for persisted old checkpoints; do not silently reinterpret them. Diagnostics identify phase, generation, counts, and expiration cause without exposing raw checkpoints or document payloads.

## Alternatives

**Use strict `(updatedAt, id)` continuation.** This repairs tied pages in a fixed dataset with limited changes, but later equal-timestamp writes behind the tuple and clock regression still cause omissions.

**Assign a new durable sequence in storage.** This could preserve scalar checkpoints, but requires atomic publication ordering with document writes and a retained change representation. Reuse of the existing event stream avoids selecting a second ordering authority.

## Acceptance Criteria

- More than one page of equal-timestamp documents terminates with every document synchronized.
- Writes during bootstrap, equal-timestamp updates, clock regression, deletes, and restart converge to current server state without checkpoint regression.
- Empty filtered pages advance safely; canceled or failed application does not commit a client checkpoint.
- Expired, malformed, old-format, and cross-database checkpoints produce explicit documented outcomes.

## Risks

This changes replication ordering and checkpoint format. Bootstrap can outlive retained history and require restart; durable history and bounded scan duration must be sized together. Replication remains asynchronous and replay may deliver duplicates.

## Dependencies

[History-gap recovery](../architecture/2026-09-07-puller-history-gap-recovery.md), [local replay](../architecture/2026-09-07-local-puller-subscription-replay.md), and [SDK replication](../feature/2026-09-07-sdk-offline-replication.md) track the proposed source and checkpoint persistence. [Query pagination](../../implemented/feature/2026-09-07-query-cursor-pagination.md) supplies public query traversal; it does not change replication Pull checkpoints. The [publication proposal](../../rejected/architecture/2026-09-07-puller-persist-before-publish.md) is rejected; relying on Puller history for this replication design remains unconfirmed.
