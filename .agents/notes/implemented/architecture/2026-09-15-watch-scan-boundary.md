# Agent Note: Overlapping Committed Scans Through Store Watch

Status: implemented

## Problem

An independently opened change stream and document scan do not establish a
gap-free initial replica. A scan can omit a committed change before the stream's
start, or include an uncommitted primary state that later rolls back. Primary
read routing alone prevents neither failure. Timestamp continuation also cannot
order commits when timestamps tie or writes commit in a different order.

State consumers additionally need logical identity after a delete clears its
document payload, and an explicit distinction between filtered source progress
and reaching the current source watermark.

## Decision

Extend the existing Watch and ScanDocuments contracts. The original
[Store Watch decision](2026-09-08-store-watch-contract.md) continues to own portable
source checkpoints, ordered frames, failure categories, and subscription lifecycle.
This extension owns the scan boundary, logical deletion identity, and watermark.
The [Store design](../../../../docs/design/server/core/storage/03.stores.md)
defines the consumer contract.

| Capability | Delivered rule |
|---|---|
| Default Watch | `WatchStartCurrent` retains current-boundary tail behavior |
| Scan start | `WatchStartForScan` with empty checkpoint returns an initial C0 for overlapping scan/replay |
| Committed scan | `SourceScanRequest.AtLeast` accepts only that initial C0 for the same source and concrete scope |
| Resume | C0 replays inclusively; subsequent native event/progress checkpoints resume completed prefixes |
| Identity | Event copies StoredDoc database, collection, and logical document ID; StoredDoc and physical `Event.Id` are unchanged |
| Logical delete | `EventDelete`, nil `Document`, retained logical identity |
| Physical cleanup | Progress only, filtered before image-dependent logical routing |
| Current payload | Create/update Document is committed state at the event or later; consumers inspect its `Deleted` flag |
| Watermark | `CaughtUp` requires a real consumed prefix reaching a fixed committed target; an empty batch alone is insufficient |
| Work accounting | `SourceBytes` counts raw frames visible to the adapter, including filtered frames; consumers own page budgets |
| Polling | `MaxAwaitTime` preserves the one-second default; cancellation remains terminal |

Mongo obtains majority-committed time T and preserves the causal operation time
and signed cluster time in its private initial checkpoint. Scans restore that
context with majority reads and replay uses inclusive `startAtOperationTime=T`.
The overlap closes the scan/replay gap without retaining a cross-request snapshot.
Pages may observe later committed states. Source incarnation validation and ordered
index readiness remain mandatory; the scanner uses the existing logical-ID index.

```text
Watch(StartForScan) -> C0
                       |
                Scan(AtLeast=C0, AfterID)
                       |
                Watch(after=C0) -> ordered changes and progress
```

Source history is checked through actual replay consumption. Opening a cursor
alone is not proof that history remains readable. Each scan page does not open a
second stream merely to probe history. Missing required payload or identity
continues to return `WatchPayloadUnavailable`; no ordinary per-event GetMany
path or separate replication error family is introduced. Error display omits
backend cause text while preserving its inspectable cause chain.

### Watermark Proof

Mongo captures a majority-committed target when a resumed watch has no unfinished
target. Private checkpoints retain that target until it is reached, preventing
page boundaries from replacing it with a moving head. Initial scan C0 carries no
target: post-scan replay establishes its own, covering work during the scan.

| Topology | Required proof on a successful empty real source read |
|---|---|
| Direct replica set | Post-batch progress timestamp >= target; transaction expansion completes before the source can pause |
| Mongos | Progress timestamp > target; equal timestamps can leave transaction operations in pending shard batches |

Filtering can produce an empty batch without satisfying either proof. Native
periodic no-ops provide idle progress for the strict mongos boundary; no synthetic
application write is introduced. Disabling no-op progress can prevent completion.
Target establishment adds a majority read, topology discovery, and a zero-batch
native probe: H(T) for a replica set or H(T+1) for mongos. The probe is closed and
its native token is retained in the private checkpoint; no token field parser or
synthetic token encoder defines the boundary. Replay uses native batches of 100,
with driver wire-byte bounds preserved. Fresh default tail watches retain their
zero-batch start and use that initial native head as their target without the
additional capture/probe sequence.

### Research basis

| Finding | Source and implication |
|---|---|
| Primary selection alone is insufficient for a committed scan | [Mongo majority read concern](https://www.mongodb.com/docs/manual/reference/read-concern-majority/); require committed reads covering T |
| Resumed clients need causal session context | [Causal consistency specification](https://github.com/mongodb/specifications/blob/master/source/causal-consistency/causal-consistency.md); retain operation time and signed cluster time across requests |
| Lookup may return later state or no document | [Mongo update events](https://www.mongodb.com/docs/manual/reference/change-events/update/); do not claim historical after-images or reinterpret absence as deletion |
| Transaction revision changes the write path | [Mongo transaction production considerations](https://www.mongodb.com/docs/manual/core/transactions-production-consideration/); ordering requires coordinated commits, not merely allocating numbers |
| Replica-set transaction expansion completes before pause | [Mongo transaction expansion](https://github.com/mongodb/mongo/blob/b41cda4fe697dce6fd9b83b3805362ccc02fbeb3/src/mongo/db/pipeline/document_source_change_stream_unwind_transaction.cpp#L218-L259); a successful empty real read can prove timestamp equality after the transaction drains |
| Mongos may retain same-timestamp shard results | [Mongo async results merger](https://github.com/mongodb/mongo/blob/b41cda4fe697dce6fd9b83b3805362ccc02fbeb3/src/mongo/s/query/async_results_merger.cpp#L295-L316); require progress strictly past the target |
| Sharded idle progress uses native no-ops | [Mongo periodic no-op writer](https://github.com/mongodb/mongo/blob/b41cda4fe697dce6fd9b83b3805362ccc02fbeb3/src/mongo/db/repl/noop_writer.cpp#L200-L226); completion does not require application writes |

These source contracts justify the mechanism; they do not establish performance
results or a real sharded runtime test. The mongos comparison follows the cited
source analysis. No requirement to support arbitrary external writes is assumed.

## Alternatives

**A parallel ReplicationSource interface.** Separate bootstrap, scan-page, and
change-page methods centralized synchronization, but duplicated Watch cursors,
lifecycle, source codecs, errors, scanning, and normal state materialization.
Extending the existing owners retains the necessary consistency guarantees with
one stream and scanning implementation. Query owns public phases and page limits.

**Transactional replication revision.** A revision counter committed atomically
with each document could support indexed state scanning without full event history.
It also serializes counter updates within a synchronization scope, adds transaction
retries to every mutation, and requires tombstone cleanup coordinated with a
retention floor. Native committed history preserves the current write model and
places the additional work on synchronization reads. Reconsider revisions only
with evidence that their write contention and cleanup costs fit workload goals.

**Timestamp plus document-ID continuation.** It fixes tied pages in static data,
but a delayed commit with a smaller tuple still disappears behind saved progress.

**Primary scans without an overlapping committed boundary.** Routing selects the
right source but does not prevent rollback images or prove coverage of the replay
start. The committed lower bound is required for correctness.

**Retain deletion Document or infer identity from its hash.** Tombstones already
retain identity and clear business data. Copying that identity before producing
the intentional nil payload suffices; the physical hash is not reversible.

**Probe source history on every scan page.** This discovers expiry sooner but adds
cursor creation and source reads to every page. Replay detects expiry explicitly;
the accepted cost is discovering the need to restart after a long scan.

## Consequences

- Store Watch checkpoints remain source-owned and portable across service/client
  replacements. Source recreation invalidates them explicitly.
- The private Mongo envelope advances to version 2; earlier encodings are rejected
  without inference. Persisted older positions require explicit reinitialization.
- Ordinary document reads, writes, versions, and native Puller ingestion, buffers,
  and checkpoints retain their mechanisms. Native Puller does not depend on Watch.
- Change retention and recoverable logical identity limit replay. Missing images
  can require resynchronization even while source history remains retained.
- Current-state consumers allow duplicate and later images, including delete and
  recreation. Document versions do not replace source ordering.
- Sustained writes may prevent `CaughtUp`; an empty filtered result is not proof
  that synchronization completed. A retained target prevents reopening pages from
  moving the required watermark. Byte accounting does not bound hidden DB work.
- Scans with AtLeast select the authoritative route and restore causal context;
  unsupported capabilities, source changes, permissions, and index failures remain
  visible. No silent replica or fresh-head fallback is allowed.

### Integration scope and costs

| Owner | Remaining work and constraint |
|---|---|
| [Replication Pull](../bug-fix/2026-09-07-replication-pull-cursor-progress.md) | Query connects the two phases, public opaque checkpoint, bounded responses, HTTP/gRPC and manual SDK. It retains C0 through scanning and never advances beyond accepted frames. |
| [SDK offline replication](../feature/2026-09-07-sdk-offline-replication.md) | The public replica API owns durable local application and automatic coordination; manual Pull remains application-managed. |
| Pull authorization | The permission profile remains unresolved in the Pull proposal; source scope validation does not grant access or handle per-document membership changes. |

The public integration is delivery sequencing, not removal of its accepted
correctness requirements. The source contracts keep that integration possible
without a second source abstraction or changes to business write ordering.
