# Agent Note: Complete the Indexer Recovery Lifecycle

Status: proposed

## Problem

The [completed indexed-query decision](../../implemented/bug-fix/2026-09-07-indexed-query-filter-semantics.md)
provides write-quiesced offline bootstrap, complete database catalogs, validated
native Puller replay boundaries, and applied/flush readiness. Persistent complete
generations can resume after validation; memory-index loss requires maintenance
rebuilding. Source scanning and collection enumeration now use Store abstractions.

Automatic online recovery remains incomplete. Production does not own the full
reconciler/rebuild job lifecycle needed for concurrent source writes, new template
reconciliation, obsolete-job fencing, history-gap reconstruction, and resumable
jobs. Offline bootstrap requires writer downtime and explicit derived reset, so it
does not fulfill these online guarantees. The original call-site inspection found
no production scan/replay integration; the bounded maintenance path now resolves
initialization while leaving this broader lifecycle open.

## Proposal

Make the Indexer service own reconciliation, online rebuild jobs, and their
shutdown. Extend the supplied source scanning, collection enumeration, and verified
Puller replay boundaries to concurrent rebuilds in both deployment modes. Discover
desired database/collection indexes from configuration and source metadata,
including collections with no recent events.

For each rebuild, capture a durable replay position before scanning, scan with stable pagination, apply replay through a known boundary, and switch to live consumption without a gap. Apply one document-ID, pattern-matching, ordering-key, and tombstone policy across scan, replay, and live updates. Fence obsolete jobs when templates change. Mark an index ready only after every required write and checkpoint succeeds; persist enough generation and state information to resume safely or deliberately restart reconstruction.

Bound scan batches, concurrent jobs, and replay backlog. Cancellation must release iterators and stop owned workers before the store closes. Preserve the [Query integration](../../../../docs/design/server/query/02.indexer-integration.md) behavior of explicit index-unavailable errors during rebuilding. Emit database, template, job generation, progress counts, and failure cause without document bodies.

## Alternatives

**Rebuild every index on every start.** This simplifies checkpoint validation but imposes full scans and query downtime even when persistent data is intact. Retain full rebuild as recovery from invalid state.

**Only replay retained events.** This avoids scanning, but cannot reconstruct documents older than retained history or guarantee a complete newly added index.

## Acceptance Criteria

- Pre-existing documents appear after initial startup and memory-index restart in both deployment modes.
- Concurrent writes, deletes, and duplicate replay converge to storage state across multiple databases and matching collection patterns.
- Persistent restart, missing history, template replacement, scan/write failure, and cancellation never expose a partial index as ready.
- Configured scan and concurrency limits hold, and shutdown leaves no owned workers running.

## Risks

Long scans may outlive retained replay history and require a visible retry or failure. Rebuilds consume storage capacity and temporarily remove affected query availability. Recovery metadata requires an explicit migration or index rebuild when its format changes.

## Dependencies

[History-gap recovery](2026-09-07-puller-history-gap-recovery.md) and
[local replay](2026-09-07-local-puller-subscription-replay.md) retain their proposed
recovery guarantees. [Query pagination](../../implemented/feature/2026-09-07-query-cursor-pagination.md)
supplies the public page contract; offline bootstrap uses bounded source scans.
The [publication proposal](../../rejected/architecture/2026-09-07-puller-persist-before-publish.md)
is rejected. Verified retained replay boundaries are supplied by the completed
query decision; a gap-free concurrent scan/replay handoff remains required.
[Management RPCs](../feature/2026-09-07-indexer-management-rpcs.md) expose the
broader lifecycle.
