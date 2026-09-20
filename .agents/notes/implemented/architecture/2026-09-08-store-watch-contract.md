# Agent Note: Store Watch Contract

Status: implemented

## Problem

A caller that consumes a document change stream needs to distinguish document
identity, source-change identity, and processed progress. A channel of events
with an untyped resume token does not establish a durable encoding, an initial
position on an idle source, ordered progress past filtered events, or a place to
observe failures after the channel opens.

Shared physical collections and database routing add another requirement: a
continuation must not be accepted for a different logical scope or replaced
source. Ordinary read routing also need not select the authoritative change
source. These ambiguities make it possible to report a resumed stream whose
history or scope differs from the caller's saved state.

## Decision

The [DocumentStore API](../../../../internal/core/storage/types/types.go) exposes
`Watch(ctx, database, collection, after, opts) (WatchStream, error)`. Its
[shared Watch types](../../../../internal/core/storage/types/watch.go) carry an
opaque string `WatchCheckpoint`, an initial checkpoint, ordered
`WatchFrame` results, and terminal errors. The
[Store design](../../../../docs/design/server/core/storage/03.stores.md#2-document-watch)
owns the complete consumer contract.
The [scan-boundary extension](2026-09-15-watch-scan-boundary.md) adds committed
scan/replay overlap, logical identity on documentless deletes, and explicit
watermarks. It changes the original physical-delete exposure while preserving
this note's checkpoint and lifecycle decisions.

### Scope, identity, and progress

A watch requires a logical database. An empty collection selects ordinary
logical collections in the selected data namespace; system collections require
an explicit selection. One call selects one source through the existing
routing facade. It does not enumerate logical databases or aggregate backends.

A default fresh watch establishes a nonempty current checkpoint before returning,
including on an idle source. A resumed watch confirms exactly the requested
checkpoint. Each successful frame carries a checkpoint for a completed source
prefix. A nil event is progress without a document change. A consumer persists
the checkpoint only after all required work through that frame succeeds.
Checkpoints are opaque to consumers and cannot be ordered by their byte values.

An explicit `WatchStartForScan` instead establishes an initial C0 usable by
`ScanDocuments.AtLeast` and inclusive replay. C0 is a lower bound, not an already
consumed event prefix. `CaughtUp` distinguishes a successfully observed source
watermark from filtered progress. An empty source batch alone is insufficient;
the [extension's watermark proof](2026-09-15-watch-scan-boundary.md#watermark-proof)
retains an unfinished committed target across page checkpoints and accounts for
replica-set versus mongos ordering. `SourceBytes` counts visible raw frame bytes.

`Event.Id` preserves its existing meaning as the document storage key.
`Event.ChangeID` identifies one source change stably across watches and retries.
A change identity is independent of the consumer's processed checkpoint.
Backend-native token fields are absent from the shared Event type.
Event collection and document ID copy existing StoredDoc metadata, preserving
logical identity when a deletion has nil Document.

The Mongo implementation's private versioned envelope binds the physical
namespace and collection UUID, logical database, collection selection,
`IncludeBefore`, and complete BSON resume token or explicit committed scan-start
context. It validates the encoding
strictly. Source identity comes from the Mongo collection, so replacing the
Store, client, or Puller does not invalidate a retained continuation. A new
physical collection incarnation does invalidate it. Native event tokens mark
event boundaries; idle native-cursor tokens provide ordered progress without
requiring another document change. This keeps a batch's later resume position
from being saved before its last event is processed.

An unfinished native watermark target is also retained privately until its proof
is delivered. The target describes catch-up work; it never replaces the completed
prefix in the token used for resuming source events.

### Opening, routing, and failures

Mongo Watch requires distinct physical data and system collection names. An
identical mapping returns `WatchUnsupported` before opening a cursor: a shared
namespace would allow an ordinary-collections watch to include system records.
This guard belongs to Watch and leaves CRUD configuration handling unchanged.

Mongo reads the collection UUID before opening a cursor and verifies it again
after opening. A fresh watch creates a missing selected physical collection to
establish its identity and an initial server position. It surfaces metadata,
change-stream, and collection-creation permission errors. A resumed missing or
replaced source returns `WatchSourceMismatch`.

The [routing facade](../../../../internal/core/storage/router/routed_store.go)
uses `OpWatch`. The [split router](../../../../internal/core/storage/router/split.go)
sends this operation to the primary while ordinary `OpRead` operations retain
the replica. Source/scope mismatches and unavailable history propagate to the
caller; Store and router never discard the checkpoint and open a fresh stream
implicitly.

`WatchError` exposes scope, checkpoint, source, history, payload, malformed-event,
capability, permission, and availability categories while preserving the
original error chain. Decode failures and native cursor errors terminate the
stream. Cancellation of the watch or a read is terminal. Callers serialize
reads; closing a stream can interrupt a blocked read, is idempotent, and gives
Mongo cursor cleanup a five-second timeout. Cleanup failures are reported, and
other watches and Store operations retain their shared connection.

### Payload and collection routing

The existing document key `database:hash(fullpath)` remains unchanged. The
prefix establishes database identity even on a physical delete, but its hash
cannot recover a logical collection path. Mongo requests available source
pre-images for specific-collection routing even when `IncludeBefore` is false.
If neither source pre-image nor current enrichment can establish membership,
the stream returns `WatchPayloadUnavailable`. It neither leaks an event from
another collection nor skips an event whose membership is unknown.

Physical deletion produces progress before image-dependent collection routing.
Non-delete events require available document enrichment. `Document` may
reflect a later committed lookup state; `Before` is exposed only when requested and
available from the source. This API does not promise retained historical payloads
or enable Mongo pre-images. Source capability and retention remain operational
prerequisites for a scope that needs those images or routing metadata.

The [deletion lifecycle](../../../../docs/design/server/core/storage/03.stores.md#document-deletion-and-physical-cleanup)
distinguishes logical tombstone updates from physical removal. Raw Puller events
retain that distinction; business conversion ignores physical deletion. The
original Store Watch conversion exposed both as `EventDelete` with nil Document.
The scan-boundary extension preserves that payload convention for logical deletion
and filters physical cleanup into progress. Deletion identity comes from existing
StoredDoc metadata, not from retained business data or a reversible hash.

## Alternatives

**Keep the event channel and untyped raw tokens.** This retains the earlier call
shape but provides no Store-owned durable codec, idle initial anchor, ordered
progress-only result, or terminal read error. Adding these as unrelated callbacks
would divide subscription ownership and processing order across interfaces.

**Identify the source by a Store, client, or Puller instance.** This avoids source
metadata reads, but replacing a process or connection would reject valid retained
history. The physical collection UUID survives those replacements and changes
when the collection is recreated.

**Add a Store-owned durable change journal.** A journal could own replay retention
and richer event records, but requires write-path atomicity, retention, recovery,
and operational ownership across CRUD operations. The selected implementation
uses Mongo's retained source history and reports its limits. No independent
Store journal is introduced.

**Change document keys to encode logical collections.** This could identify a
collection from a hard-delete key, but would change existing document identity
and every CRUD key derivation. Source metadata supplies routing when available;
missing metadata is an explicit error under the preserved key scheme.

**Integrate the whole Puller pipeline in the same change.** Doing so would combine
a source API change with buffer durability, progress migration, local/gRPC
replay, and consumer recovery decisions. Those have separate proposal owners
and validation obligations. This decision delivers the Store source contract
while making the remaining integration costs explicit.

## Consequences

Callers can persist an opaque continuation, restart against the same retained
source, and distinguish event processing from source progress. The returned
change identity supports deduplication without reusing the document key or
claiming exactly-once processing. Primary watch routing does not make replica
CRUD reads current with a received event.

The API is a deliberate source-level change from the former channel and raw
`interface{}` token. That API had no public durable token codec. New checkpoints
use strict scope/source binding without runtime legacy inference. Callers with
persisted raw tokens require an explicit migration or a deliberate new starting
boundary that accounts for omitted history. Existing document keys and their
public `Event.Id` meaning remain intact.

Deployments that map ordinary data and system records to the same physical
collection cannot use Mongo Watch; opening a watch fails explicitly without
changing CRUD behavior. Fresh watches may create an empty selected physical
namespace, requiring additional collection-creation permission. Existing
watches also need metadata
access to obtain the UUID. Scope isolation can stop a specific-collection watch
when a non-cleanup change or missing lookup lacks routing metadata, even if the
caller did not request `Before`. Operators must provide source history and
capabilities appropriate to their selected watch scope. Optional images do not
guarantee historical snapshots.

### Deferred integration and its costs

This note owns the implemented Store API, Mongo adapter, routing, and associated
validation. Puller ingestion still obtains Mongo clients through
[`StorageFactory.GetMongoClient`](../../../../internal/services/manager_init.go)
and bypasses `DocumentStore.Watch`. Its ingestion, batching, caches, pending
writes, persisted buffers, and local/gRPC delivery are unchanged by this
contract. The broader Puller design remains separate proposed work.

Puller [write-admission bounds](../bug-fix/2026-09-07-puller-pending-write-bound.md)
are implemented separately. The [publication redesign](../../rejected/architecture/2026-09-07-puller-persist-before-publish.md)
is rejected; Store checkpoints remain portable and source-owned, and cache
completion cannot define consumer checkpoint validity.

| Deferred work and owner | Cost and constraint retained here |
|---|---|
| Native Puller ingestion, described by the [Puller architecture](../../../../docs/design/server/puller/01.architecture.md) | Continues its direct native capture; routing it through Store Watch is not a prerequisite of replication Pull. Store source codecs remain private to their adapters. |
| [Local replay](../../proposed/architecture/2026-09-07-local-puller-subscription-replay.md) and [history-gap recovery](../../proposed/architecture/2026-09-07-puller-history-gap-recovery.md) | Requires durable continuity and retention boundaries plus consumer-visible failures; Store errors provide a source failure without implementing Puller generations or recovery |
| Multi-source discovery and aggregation in Puller, and [dedicated read/write routing](../../proposed/architecture/2026-09-07-dedicated-backend-read-write-routing.md) | Requires source inventory, ownership, and progress aggregation; one Store watch remains explicitly scoped, and separate source checkpoints must not be compared or combined as a scalar |
| [Indexer rebuild](../../proposed/architecture/2026-09-07-indexer-recovery-lifecycle.md) and [filtered subscription snapshots](../../proposed/bug-fix/2026-09-07-realtime-filtered-snapshots.md) | Requires consumer recovery integration; the scan-boundary extension supplies overlap, but neither an ordinary current checkpoint nor C0 creates a snapshot or performs a rebuild |
| [Trigger before-images](../../proposed/feature/2026-09-07-trigger-before-images.md) and [delivery idempotency](../../proposed/architecture/2026-09-07-trigger-delivery-idempotency.md) | Requires retained payloads, transport changes, durable delivery/outbox decisions, and consumer state; optional Store images and ChangeID do not provide those guarantees |
| [Streamer durable progress](../../proposed/architecture/2026-09-07-streamer-durable-progress.md) | Requires progress persistence and delivery/acknowledgment policy; receiving or closing a Store frame is not an end-client acknowledgment |

The [SDK replica API](../feature/2026-09-07-sdk-offline-replication.md) now owns its
local progress and recovery policy. A Store frame still does not acknowledge
application processing or an external side effect.

These gaps can still expose current Puller consumers to the failures described
by their proposal owners. Their statuses remain proposed; this implementation
does not establish a consolidated Puller redesign or an end-to-end durability
guarantee.
