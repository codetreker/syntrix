# Agent Note: SDK Offline Replication

Status: proposed

## Problem

The SDK provides manual `SyntrixClient.pull` without a working durable
synchronization loop. Internal coordinator scaffolding remains incomplete:
[Puller](../../../../sdk/syntrix-client-ts/src/replication/pull.ts) returns `{}`,
[Pusher](../../../../sdk/syntrix-client-ts/src/replication/push.ts) performs no
operation, [Outbox](../../../../sdk/syntrix-client-ts/src/replication/outbox.ts)
always reads an empty queue, and
[CheckpointManager](../../../../sdk/syntrix-client-ts/src/replication/checkpoint.ts)
never persists progress. The
[coordinator](../../../../sdk/syntrix-client-ts/src/replication/coordinator.ts)
starts work without applying returned documents or saving checkpoints. Manual
transport alone does not deliver the durable offline behavior in the
[replication design](../../../../docs/design/sdk/002_replication_client.md).

## Proposal

Complete collection-scoped pull, push, persistence, and lifecycle coordination through an explicit public API. Namespace local documents, checkpoints, and queued mutations by endpoint, authenticated account, database, and collection so switching accounts cannot replay another account's writes.

Pull applies documents and tombstones durably before committing the server-issued checkpoint. Push retains each queued mutation and its base version until an acknowledged result is recorded; conflicts remain available to the documented resolution hook. A local edit and its outbox entry must become durable together. Define the adapter's transactional requirements and stored schema before implementation; any existing application-owned local data requires an explicit migration, without inferred format fallbacks.

Use one bounded pull worker and one bounded push worker per coordinator, coalesce realtime signals, retry transient failures with cancellation, and stop automatic retries for actionable authentication or validation failures. Shutdown cancels timers and requests and waits for local persistence. RxDB integration is already intended by the design, but its dependency and adapter packaging require an explicit implementation decision.

## Alternatives

**Application-owned synchronization:** keeps the SDK smaller but requires every application to solve checkpoint ordering, durable outbox acknowledgement, and account isolation. The shared coordinator is proposed because these obligations recur across clients.

**Realtime events as the local source of truth:** lowers pull traffic but depends on a durable delivery contract and cannot recover omitted events using the HTTP checkpoint. Realtime should schedule authoritative pulls.

## Acceptance Criteria

- Offline writes and pull progress survive reload, including crashes before and after local persistence and server acknowledgement.
- Conflicts, tombstones, duplicate retries, authentication failure, and a full page sharing timestamps complete according to the documented protocol without silently losing queued changes.
- Two accounts and two databases sharing collection names remain isolated; shutdown prevents further writes and callbacks.
- Diagnostic hooks expose operation, counts, duration, and error category without tokens or document payloads.

## Dependencies

[Push version checks](../bug-fix/2026-09-07-replication-push-version-checks.md),
[implemented Pull progress](../../implemented/bug-fix/2026-09-07-replication-pull-cursor-progress.md),
and [realtime resume](2026-09-07-realtime-client-resume.md) own server and transport
guarantees. The manual Pull API supplies typed pages and portable progress;
this proposal owns their durable application and automatic coordination.

## Risks

Local storage limits, multi-tab ownership, and conflict retries can stall synchronization. Adapter failures must remain visible with pending work preserved; retrying an uncertain push cannot imply exactly-once delivery.
