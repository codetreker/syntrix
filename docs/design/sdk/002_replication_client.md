# Replication Client Design (RxDB + Syntrix replication/realtime)

**Status:** Manual Pull and WebSocket lifecycle are implemented; the durable replication coordinator remains planned.

## Context & Why
- We need offline-first replication for web clients using RxDB as local store.
- Server exposes scoped HTTP replication (`/replication/v1/databases/{database}/pull` and `/push`) and realtime change signals.
- Checkpoint is authoritative only in pull responses; realtime events are triggers, not state.

**Related:** Authentication flows and retry semantics are defined in [003_authentication.md](003_authentication.md); replication uses the same token/refresh handling and does not advance checkpoints on auth errors.

## Goals
- Reliable pull/push replication using RxDB, respecting server wire format (flattened docs, no storage internals).
- Realtime events only trigger pulls; checkpoint managed solely by pull responses.
- Conflict-safe push with server-returned conflicts written back or surfaced.
- Offline tolerance: queued pushes (outbox), resumable pulls with checkpoint persistence.

## Non-Goals
- Owning server checkpoint or transport design, defined in the [replication reference](../../reference/replication.md).
- Rich conflict resolution UI/strategies (provide hooks only).
- Full-text/local secondary indexes beyond RxDB schema basics.

## Assumptions
- `/realtime/ws` (or `/realtime/sse`) can subscribe per collection and delivers at least `{ collection, id, action, updatedAt, deleted? }` plus some monotonic seq/lsn (used only for diagnostics; not trusted as checkpoint).
- HTTP replication follows the [replication reference](../../reference/replication.md).
- Token-based auth reusable for realtime channel; reconnect allowed.
- RxDB available (Dexie storage) in client environment.

## Data Model (flattened)
- Decoded fields: `id`, `collection`, optional deletion flag and server metadata, plus business fields. HTTP uses recursive typed values; int64 values decode to bigint.
- RxDB primary key: `id`. Indexes: `updatedAt`, `collection`, optionally business fields.
- Tombstones clear former business fields. Minimal logical deletions contain only identity and `deleted: true`; timestamps and version can be absent. Physical cleanup is not another business deletion. See [deletion semantics](../server/core/storage/03.stores.md#document-deletion-and-physical-cleanup).

## Implemented Manual Pull

`SyntrixClient.pull<T>(collection, {checkpoint, limit, signal})` performs one POST
to the client's configured database replication route, retaining a base URL prefix.
Omitted/null checkpoint initializes; supplied values must be nonempty opaque
strings. The default limit is 100, with SDK values restricted to 1–1000.

| SDK responsibility | Application responsibility |
|---|---|
| Validate bounded typed responses and decode int64 to bigint | Apply all document states/deletions and save the checkpoint in one local transaction |
| Preserve authentication session through request and retry; reject obsolete success | Invalidate pending local application when account ownership changes |
| Return `caughtUp` and source recovery errors | Drain pages, including empty pages with false; rebuild server mirror on `RESYNC_REQUIRED` |
| Support AbortSignal cancellation | Preserve unsent local changes during reset and isolate mirrors/checkpoints per account and scope |

Bootstrap performs a committed moving scan followed by overlapping Watch replay.
Duplicate, later, and temporarily regressing states are permitted before convergence.
Document version is not source order and must not suppress delete/recreate changes.
Full-scope access currently requires the database owner or matching `db_admin`;
this authorization profile remains provisional pending approval.

The manual API does not schedule future pulls, persist checkpoints, or maintain
an outbox. The remaining components and flows below describe the planned
coordinator owned by the [offline replication proposal](../../../.agents/notes/proposed/feature/2026-09-07-sdk-offline-replication.md).

## Planned Coordinator Components
- **ReplicationCoordinator**: high-level orchestrator per collection; owns pull/push workers, realtime trigger wiring, state, callbacks.
- **PullWorker**: executes `/replication/v1/databases/{database}/pull` with `{collection, checkpoint, limit}`; writes results into RxDB; updates CheckpointStore.
- **PushWorker**: drains Outbox to `/replication/v1/databases/{database}/push`; handles conflicts by writing server docs or invoking conflict hook.
- **RealtimeTrigger**: listens `/realtime/ws` (or `/realtime/sse`); enqueues pull requests (no checkpoint from event).
- **CheckpointStore**: persists opaque per-account/database/collection checkpoints in the same local transaction as page application.
- **Outbox**: local queue of pending writes (create/update/replace/delete), durable across reloads.

## SDK Architecture (public surface)
- The package exports existing clients and manual Pull types; coordinator types remain planned.
- Public clients remain:
	- `SyntrixClient` for CRUD/query and manual Pull over HTTP.
	- `TriggerClient` for trigger writes.
	- `TriggerHandler` wrapper for trigger payload execution.
- New replication surface (planned):
	- `createReplicationCoordinator(options): ReplicationCoordinator` factory.
	- Interfaces: `ReplicationOptions`, `PushOptions`, `RealtimeOptions`, `CheckpointStore`, `OutboxAdapter`, hooks types.
- Suggested layout under `src/replication/`:
	- `coordinator.ts` (orchestrator, public entry)
	- `pull.ts` (PullWorker)
	- `push.ts` (PushWorker)
	- `realtime.ts` (trigger wiring abstraction)
	- `checkpoint.ts`, `outbox.ts` (pluggable adapters)
	- `types.ts` (options, hooks, DTOs)

### High-level call graph (SDK)
```
App
 |- SyntrixClient (HTTP CRUD/query)
 |- createReplicationCoordinator({...})
			|- PullWorker -> /replication/v1/databases/{database}/pull -> RxDB collections
			|- PushWorker -> /replication/v1/databases/{database}/push -> Outbox mgmt
			|- RealtimeTrigger -> schedules PullWorker
			|- CheckpointStore / OutboxAdapter -> persistence
```

### Initialization flow
1) App constructs `SyntrixClient` (baseURL, token) and RxDB database with collections.
2) App calls `createReplicationCoordinator({ collection, client, rxdbCollection, checkpointStore, outboxAdapter, realtime, hooks, backoff, limits })`.
3) Coordinator wires realtime subscription (if enabled), starts a safety pull timer, and optionally runs an initial pull.
4) App writes go through RxDB and enqueue to Outbox (via helper we provide or explicit call). PushWorker drains automatically.

### Public usage patterns
- **Online-first read**: keep using `SyntrixClient.query` for server truth.
- **Offline-first read**: read from RxDB directly; coordinator keeps it synced.
- **Write**: write to RxDB + Outbox helper; PushWorker syncs; conflicts surfaced via hook.
- **Control**: expose `start()`, `pause()`, `resume()`, `shutdown()` on coordinator for lifecycle (e.g., tab visibility, logout).
- **Metrics/diagnostics**: hooks emit pull/push timings, counts, last checkpoint for observability.

## Control Flow
```
[realtime event] -> [trigger queue] --(throttle 200-500ms)--> [PullWorker]
[interval timer]  ------------------------------------------^
[app writes] -> [Outbox] -> [PushWorker]
```

### Pull sequence (happy path)
1) Load the scope's opaque checkpoint; null starts initialization.
2) Call the manual Pull API with `{collection, checkpoint, limit}`.
3) Apply document states and deletions and save the checkpoint in one transaction.
4) Continue while `caughtUp` is false, including empty pages; retries keep saved progress.
5) Emit success callbacks only after local commit. On resynchronization, rebuild the server mirror while preserving pending local edits.

### Push sequence
1) Read batch from Outbox (bounded size).
2) Send `/replication/v1/databases/{database}/push` with `{collection, changes}`.
3) On success, remove sent entries from Outbox.
4) If `conflicts` returned, upsert them to RxDB and emit `onConflict(conflicts, locals?)`.
5) Errors: retry with backoff, keep Outbox intact.

### Realtime trigger policy
- Event arrival only schedules a pull; event seq/lsn is not persisted as checkpoint.
- Multiple events coalesced via throttle/debounce to a single pull.
- On each subscription's `onReady`, schedule a pull with the last checkpoint to
  reconcile changes missed before registration or during disconnection.
- Periodic safety pull (e.g., every N minutes) to cover missed events.

## Error Handling & Resilience
- Pull errors: exponential backoff with jitter; checkpoint unchanged until success.
- Push errors: retain Outbox, retry with backoff; optionally surface fatal 4xx to app.
- Network loss: pause realtime, keep Outbox; on regain, run pull then resume push.
- Idempotency: RxDB upserts keyed by `id`; `updatedAt/version` prevent regression (drop stale writes if local version < incoming version when applicable).

## Observability Hooks
- `onPullScheduled(reason)`, `onPullSuccess(stats)`, `onPullError(err)`
- `onPushSuccess(batchInfo)`, `onPushError(err)`
- `onConflict(conflicts, locals?)`

## Configuration Surface (draft)
- `collection`: string (required)
- `pullLimit`: number (default 200–500)
- `pullThrottleMs`: number (e.g., 200–500)
- `safetyPullIntervalMs`: optional periodic pull
- `backoff`: { baseMs, maxMs, factor, jitter }
- `checkpointStore`: pluggable (default RxDB key-value)
- `outboxAdapter`: pluggable (default RxDB collection)
- `realtime`: { enable: boolean, subscribe: fn, unsubscribe: fn }
- `hooks`: callbacks listed above

## Conflict Handling Options
- Default: server-wins (upsert conflicts, clear outbox entries for those ids).
- Custom: app-provided merge in `onConflict`, then enqueue merged doc back to Outbox for retry.

## Cleanup
- Tombstone GC (optional): app can provide policy (e.g., delete tombstones older than N days after last checkpoint synced) to keep local store small.

## Connection Health & Keepalive

### Server-side (WebSocket)
- **Ping interval:** 54 seconds (`pongWait * 9/10`)
- **Pong timeout:** 60 seconds - connection closed if no pong received
- **Write timeout:** 10 seconds per message

The server sends WebSocket Ping frames; browsers automatically respond with Pong. If the server doesn't receive a Pong within 60 seconds, it closes the connection.

### Server-side (SSE)
- **Heartbeat interval:** 15 seconds - server sends `: heartbeat\n\n` comments
- Client should monitor incoming data; if no data (including heartbeats) arrives for an extended period, consider reconnecting.

### WebSocket Ownership and Keepalive (implemented SDK)

- One `RealtimeClient` owns one socket. Convenience subscriptions start or reuse
  its connection; low-level subscriptions leave `connect()` explicit.
- Concurrent connection attempts share one promise. Socket open begins auth;
  `auth_ack` completes connection and permits pending subscription registration.
- Subscription callbacks are keyed by `subId`; global observers remain separate.
  Each registration ACK triggers `onReady` once for that connection. Events can
  precede the ACK; readiness guarantees neither replay nor snapshot completion.
- For an active acknowledged subscription, `snapshot_failed` and `snapshot_limit`
  notify subscription and global error observers without disabling live delivery.
  Registration rejection and other error codes retain their failure behavior;
  a later snapshot error cannot revive a failed or removed subscription.
- Unsubscribe releases one subscription, leaving the shared connection open.
  `disconnect()` stops transport work but retains logical subscriptions;
  `dispose()` clears them permanently. Logout disposes the WebSocket client.
- Timers and asynchronous handlers are bound to their connection attempt so
  late completion cannot revive a stopped or disposed client.
- Browser WebSocket API automatically responds to server Ping frames
- Client tracks `lastMessageTime` on every incoming message (including server heartbeats)
- **Activity timeout:** `activityTimeoutMs` (default: 90s) bounds the entire
  connection/authentication attempt independently of heartbeats; after
  authentication it detects inactivity and triggers reconnect.
- Reconnect uses exponential backoff with jitter: base delay × 2^(attempt-1),
  bounded by `maxReconnectAttempts`. Successful authentication resets attempts.

Client-owned connection lifetime lets explicit connection users and convenience
subscriptions coexist without one subscriber closing another's transport. See the
[lifecycle decision](../../../.agents/notes/implemented/bug-fix/2026-09-07-sdk-realtime-subscription-lifecycle.md)
for alternatives and the separate authentication-provider limitation.

### Reconnect Flow
1. On disconnect detected (via `onclose` or activity timeout), set state to `disconnected`.
2. Attempt reconnect with exponential backoff.
3. On successful reconnect:
   - Re-authenticate (send auth message with fresh token)
   - Re-subscribe to all active subscriptions
   - Notify each subscription with `onReady` after its registration ACK
   - The application or planned coordinator schedules a pull to reconcile missed changes
4. Connection and authentication failures notify active subscriptions and the
   global error observer; automatic attempts stop at the configured limit.

### WebSocket Configuration (implemented)
```typescript
interface RealtimeClientOptions {
  maxReconnectAttempts?: number;  // default: 5
  reconnectDelayMs?: number;      // base delay, default: 1000
  activityTimeoutMs?: number;     // handshake deadline and inactivity, default: 90000
}
```

## Security
- Reuse bearer token for HTTP and realtime; refresh hooks must be supported before retry.
- Validate collection names client-side before requests (defensive against misuse).

## Open Questions / Decisions
- Exact realtime payload shape and subscribe API (event source vs websocket vs SSE). We only require it can signal per-collection changes.
- Should push include client `updatedAt/version` always, or only when available? (current protocol allows optional version).
- Outbox persistence format: per-collection vs global queue; proposed per-collection for simpler retries.

## Testing Plan (to implement with the code)
- Pull: checkpoint advance, tombstone handling, throttle coalescing, backoff on failures.
- Push: outbox drain, retry/backoff, conflict upsert, idempotent duplicate suppression.
- Realtime trigger: event-driven pull scheduling, debounce, reconciliation after
  registration ACK, safety interval coverage.
- Concurrency: simultaneous pull/push without corrupting checkpoint or outbox.
- Persistence: checkpoint/outbox survive reload, resume correctly.
- Error paths: auth failure, 4xx on push, transient network failures on pull.

## Next Steps
- Define TypeScript interfaces for the components above.
- Add ASCII sequence diagrams to code comments when implementing orchestrator.
- Implement and unit-test orchestrator, pull/push handlers, outbox/checkpoint adapters.
