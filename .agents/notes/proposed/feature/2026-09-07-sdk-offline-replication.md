# Agent Note: SDK Offline Replication

Status: proposed

## Problem

The SDK exposes replication components without a working durable synchronization loop. [Puller](../../../../sdk/syntrix-client-ts/src/replication/pull.ts) returns `{}`, [Pusher](../../../../sdk/syntrix-client-ts/src/replication/push.ts) performs no operation, [Outbox](../../../../sdk/syntrix-client-ts/src/replication/outbox.ts) always reads an empty queue, and [CheckpointManager](../../../../sdk/syntrix-client-ts/src/replication/checkpoint.ts) never persists progress. The [coordinator](../../../../sdk/syntrix-client-ts/src/replication/coordinator.ts) starts work without applying returned documents or saving checkpoints. These static findings leave the offline behavior in the [replication design](../../../../docs/design/sdk/002_replication_client.md) unimplemented.

## Proposal

Complete collection-scoped pull, push, persistence, and lifecycle coordination through an explicit public API. Namespace local documents, checkpoints, and queued mutations by endpoint, authenticated account, database, and collection so switching accounts cannot replay another account's writes.

Pull applies documents and tombstones durably before committing the server-issued checkpoint. Push retains each queued mutation and its base version until an acknowledged result is recorded. Structured conflicts correlate by zero-based request position, including repeated document IDs; the nullable current state and reason remain available to the resolution hook. Absence must not be passed to a document upsert. A local edit and its outbox entry must become durable together. Define the adapter's transactional requirements and stored schema before implementation; any existing application-owned local data requires an explicit migration, without inferred format fallbacks.

### Lossless Local Storage and Write Transport

Manual Pull and Query decode int64 values as bigint. Ordinary SDK document
`set`/`update` requests still use JSON serialization, which throws on bigint before
HTTP transmission. The proposed Pusher remains a stub. Consequently a pulled
document cannot be blindly passed to JSON.stringify, persisted in JSON, or sent
back through ordinary document writes. Converting bigint to Number loses values
outside the safe-integer range and is not an acceptable synchronization codec.

The coordinator needs a lossless persisted representation and a matching write
transport contract for int64 metadata and nested business values. Preserve numeric
type and value across local edits, outbox persistence, retry, and server decoding;
the typed Pull envelope is not automatically a supported CRUD/Push input format.
This is deferred write-side integration, not a guarantee supplied by manual Pull.

### Worker Lifecycle

Use one bounded pull worker and one bounded push worker per coordinator, coalesce realtime signals, retry transient failures with cancellation, and stop automatic retries for actionable authentication or validation failures. Shutdown cancels timers and requests and waits for local persistence. RxDB integration is already intended by the design, but its dependency and adapter packaging require an explicit implementation decision.

## Alternatives

**Application-owned synchronization:** keeps the SDK smaller but requires every application to solve checkpoint ordering, durable outbox acknowledgement, and account isolation. The shared coordinator is proposed because these obligations recur across clients.

**Realtime events as the local source of truth:** lowers pull traffic but depends on a durable delivery contract and cannot recover omitted events using the HTTP checkpoint. Realtime should schedule authoritative pulls.

**Convert bigint to Number before persistence or writes:** avoids the serialization
exception but loses integer precision and can corrupt version preconditions or
business values. A lossless encoding must be agreed across both ends.

## Acceptance Criteria

- Offline writes and pull progress survive reload, including crashes before and after local persistence and server acknowledgement.
- Conflicts, tombstones, duplicate retries, authentication failure, and a full page sharing timestamps complete according to the documented protocol without silently losing queued changes.
- Two accounts and two databases sharing collection names remain isolated; shutdown prevents further writes and callbacks.
- Diagnostic hooks expose operation, counts, duration, and error category without tokens or document payloads.
- Int64 metadata and nested business values survive local persistence and outbound writes exactly, including values beyond Number's safe-integer range; no implicit Number conversion is accepted.

## Dependencies

implemented [Push version checks](../../implemented/bug-fix/2026-09-07-replication-push-version-checks.md), implemented [Pull cursor progress and manual transport](../../implemented/bug-fix/2026-09-07-replication-pull-cursor-progress.md), and [realtime resume](2026-09-07-realtime-client-resume.md) own server and transport guarantees. Manual Pull does not persist local documents/checkpoints or implement this proposal's coordinator and outbox.

## Risks

Local storage limits, multi-tab ownership, and conflict retries can stall synchronization. Adapter failures must remain visible with pending work preserved; retrying an uncertain push cannot imply exactly-once delivery.
