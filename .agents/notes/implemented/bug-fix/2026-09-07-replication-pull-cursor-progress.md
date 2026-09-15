# Agent Note: Make Replication Pull Checkpoints Advance Reliably

Status: implemented

## Problem

The former Pull query filtered `updatedAt >= checkpoint` and saved only the last
timestamp. Equal-timestamp full pages could repeat indefinitely. A strict tuple
would still miss delayed commits whose timestamp/ID sorts behind saved progress.
Independent scans also need a committed replay overlap to cover concurrent writes.

## Decision

Pull directly composes Store Watch and ScanDocuments. The
[Watch scan-boundary decision](../architecture/2026-09-15-watch-scan-boundary.md)
owns committed overlap, source checkpoints, logical event identity and watermark
proof. Query owns the public two-phase cursor and response budgets. The
[replication design](../../../../docs/design/server/gateway/replication.md) and
[reference](../../../../docs/reference/replication.md) own the architecture and
client contract.

| Concern | Behavior |
|---|---|
| Bootstrap | Capture C0 with WatchStartForScan, close that stream, and scan committed ID-ordered pages with AtLeast=C0 |
| Phase transition | Exhausted scan returns a changes cursor at original C0, with caughtUp false |
| Incremental | Consume Watch in source order, returning its current-or-later Document or a minimal logical deletion |
| Progress | Advance only through accepted documents and filtered frames; reread prefetched records outside that prefix |
| Watermark | Return caughtUp only from Watch's explicit source proof |
| Failure | Fail a request on source, encoding, cancellation, or close errors; return no partial progress |
| Source independence | Original Puller ingestion, buffer and checkpoint mechanisms are unchanged |

The opaque versioned cursor binds the requested database namespace, resolved
database identity, collection, phase, source checkpoint, and scan continuation.
Storage keeps the namespace supplied in the URL, matching current CRUD behavior.
The additional resolved identity rejects alias reassignment; it does not migrate
documents or make ID and slug continuations interchangeable.

### Protocol and Client

Pull uses POST at the database-scoped route. Missing/null/empty HTTP checkpoint
starts initialization; old timestamp positions produce `RESYNC_REQUIRED`.
Malformed or scope-mismatched cursors fail validation. HTTP and gRPC reuse
recursive typed values for complete flattened documents, preserving nested int64
values and metadata. Pull conversions do not alter Push encoding or write semantics.

The manual SDK exposes `SyntrixClient.pull`, validates one page, decodes int64 to
bigint, supports cancellation, and preserves authentication-session ownership
through request and retry. It leaves state application and checkpoint persistence
to the caller. A local transaction must commit both together; account changes must
invalidate pending application. The SDK's automatic coordinator and outbox remain
owned by the [offline replication proposal](../../proposed/feature/2026-09-07-sdk-offline-replication.md).

| Limit | Bound |
|---|---:|
| Document count | Default 100; maximum 1000 |
| Request / public cursor | 1 MiB / 256 KiB |
| Accepted source bytes | 16 MiB |
| JSON / protobuf response | 16 MiB / 20 MiB including envelopes |
| Incremental frames | 10,000 |
| Incremental soft interval / hard timeout | 5 seconds / 30 seconds |
| HTTP socket write deadline | Processing deadline plus 10 seconds |

Scan candidate batches shrink on byte-budget errors. A single oversized record
fails explicitly. Page limits return a completed prefix without skipping a fetched
record. Source-byte accounting describes adapter-visible records, not total DB
work. Request logs retain request ID, hashed scope/checkpoint identities, phase,
count, duration and bounded reason without raw payloads or source tokens.

The soft interval permits success only after an accepted source checkpoint
advances or Watch proves caught-up progress. A slow Watch open or repeated empty
frames at the same position must not produce indefinitely repeatable successful
pages. Without progress, exhausting source-byte or frame budgets returns retryable
unavailability; the hard deadline returns a timeout. Processing includes response
encoding. Pull extends its HTTP socket write deadline ten seconds past that
deadline, so the configured general write timeout cannot discard a valid result.
The handler requires write-deadline control before calling Query.

### Realtime Snapshot Consumer

The existing snapshot message consumes opaque Pull pages through `caughtUp`,
applying upserts and logical deletions by document ID. Only the completed live
state is sent as a snapshot. One Pull page cannot establish completeness because
source-byte and encoded-page limits can stop it below the document-count limit.

Snapshot collection retains at most 1000 live documents and 16 MiB of serialized
SnapshotPayload, including its envelope. Collection has a ten-second deadline;
delivery to the connection queue has a separate ten-second bound. Snapshot enqueue
shares per-client synchronization with Hub-owned channel closure. The Hub signals
pending senders before waiting for the send lock, preventing a full queue from
holding up deregistration or shutdown. Late results after closure are discarded.
Collection or encoding failure produces `snapshot_failed`; exceeding either size bound produces
`snapshot_limit`. No successful prefix is sent. The ordinary JSON message schema
is unchanged. Filter matching and coordination with live delivery remain owned by
the [filtered snapshot proposal](../../proposed/bug-fix/2026-09-07-realtime-filtered-snapshots.md).

### Authorization Profile

The implemented full-scope gate requires the validated database owner or a matching
`db_admin` grant for its ID or validated slug. Authentication and global roles alone
are insufficient. The policy remains provisional pending approval. Source scope
validation does not authorize access; the check runs on each request. Per-document
permission filtering would also require membership exits and permission-change
recovery, which this gate does not implement.

### Pending Database Lifecycle Decision

Continuous replication assumes an active database is not physically purged and
then returned to active under the same logical identity. Existing lifecycle paths
can mark a database deleting, partially purge records, and permit activation again.
Those physical removals produce no business deletion events, so a retained Pull
checkpoint could miss them even when its source identity still matches.

This is an unresolved merge blocker for the Pull change. The lifecycle policy
decision remains pending; neither the cursor binding nor the current active-status
check fixes that transition. No terminal-deletion guarantee is claimed here.

## Alternatives

**Hold the Hub registry lock while enqueueing snapshots.** A slow connection could
block registration, deregistration and broadcasts for unrelated clients until the
delivery deadline. Registry membership also cannot distinguish an in-progress
registration from a closed client. A per-client stop signal and send lock preserve
the existing Hub close owner without making registry progress depend on the queue.

**Timestamp and ID continuation.** Repairs static tied pages but cannot represent
commit order; a delayed write can still disappear behind the saved tuple.

**Transactional replication revision.** A scope counter committed with each document
could serve indexed current-state replication without a complete event journal.
It introduces transaction retries and counter contention on writes, and requires
tombstone cleanup coordinated with a retention floor. Native history preserves
the existing write model. No supported arbitrary-external-write requirement is
assumed in this choice.

**Parallel ReplicationSource.** Separate bootstrap, scan and change-page APIs
duplicate Watch lifecycle, source codecs, errors and scanning. Existing Watch and
ScanDocuments own the required guarantees, while Query owns public pages. Ordinary
events use their existing document payloads without a second GetMany path.

**Puller-local history.** This would add dependencies on buffer retention, continuity
and instance replacement. Source-issued checkpoints support cross-instance recovery
directly; local Puller replay and history-gap proposals remain independent.

**One timestamp cursor with implicit reset.** Silently reopening at current time
would report success while omitting state. Explicit resynchronization preserves
the distinction between retry and rebuilding the server mirror.

**Canonicalize all database namespaces in Pull.** Resolving only Pull storage to a
canonical ID can select a different namespace than existing CRUD requests using a
slug. This change preserves URL namespaces and binds resolved identity for cursor
safety. A system-wide namespace decision requires separate analysis across reads,
writes, routing and existing data.

## Consequences

- Equal timestamps and delayed commit ordering no longer determine Pull progress.
  Current-state convergence allows duplicates, later images and temporary regression;
  versions must not suppress delete/recreate states.
- Logical deletion returns identity even with nil Document. Physical cleanup only
  advances progress. Missing required identity/payload or expired source history
  produces resynchronization, not an inferred deletion or silent skip.
- Actual Watch replay checks history. A long scan can complete before expiry is
  discovered; rebuilding costs additional source reads and client work.
- Source replacement invalidates its continuation. Service or client replacement
  remains recoverable against the same retained source.
- POST and opaque cursors deliberately replace the old GET/timestamp protocol.
  HTTP/gRPC require matching typed-value support; no legacy-format inference exists.
- No fixed offline recovery duration is promised. Source retention and available
  identity both limit replay, and source Watch opens/reads impose workload costs.
- Manual Pull does not deliver local durability automatically. Applications preserve
  unsent changes when resetting and separate state by account and scope.
- Pull's bigint results cannot blindly round-trip through existing JSON-based SDK
  writes. The offline replication proposal owns lossless persistence and outbound
  encoding; conversion to Number would corrupt supported int64 values.
- Required server and SDK CI applies to stacked PR bases as well as mainline bases;
  existing path filters, jobs and thresholds remain intact.
