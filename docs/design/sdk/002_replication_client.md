# Replication Client Design (RxDB + Syntrix replication/realtime)

**Status:** Manual Pull, WebSocket lifecycle, and a private native replication runtime are implemented. The public local database API and automatic HTTP synchronization remain planned.

## Context & Why
- We need offline-first replication for web clients using RxDB as local store.
- Server exposes scoped HTTP replication (`/replication/v1/databases/{database}/pull` and `/push`) and realtime change signals.
- Checkpoint is authoritative only in pull responses; realtime events are triggers, not state.

**Related:** Authentication flows and retry semantics are defined in [003_authentication.md](003_authentication.md); replication uses the same token/refresh handling and does not advance checkpoints on auth errors.

## Goals
- Reliable pull/push replication using RxDB, with typed wire values and flattened decoded documents that exclude storage internals.
- Realtime events only trigger pulls; checkpoint managed solely by pull responses.
- Conflict-safe push with server-returned conflicts written back or surfaced.
- Offline tolerance: durable local changes, resumable pulls, and native replication metadata.

## Non-Goals
- Owning server checkpoint or transport design, defined in the [replication reference](../../reference/replication.md).
- Rich conflict resolution UI/strategies (provide hooks only).
- Full-text/local secondary indexes beyond RxDB schema basics.

## Assumptions
- `/realtime/ws` (or `/realtime/sse`) can subscribe per collection and delivers at least `{ collection, id, action, updatedAt, deleted? }` plus some monotonic seq/lsn (used only for diagnostics; not trusted as checkpoint).
- HTTP replication follows the [replication reference](../../reference/replication.md).
- Token-based auth reusable for realtime channel; reconnect allowed.
- The SDK owns its pinned RxDB/Dexie runtime and loads it lazily; applications do not supply an RxDB instance.

## Data Model (flattened)
- Decoded fields: `id`, `collection`, optional deletion flag and server metadata, plus business fields. HTTP uses recursive typed values; int64 values decode to bigint.
- The private runtime accepts storage records and source adapters. The application-facing local schema, typed-value persistence codec, and query indexes remain part of the local database implementation.
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

The manual API does not schedule future pulls or persist checkpoints. It remains
independent of the private runtime below.

## Private Native Runtime

The SDK owns a pinned RxDB 17.5.0 replication protocol with caller-owned fork and
metadata stores. A source adapter supplies normalized records, an opaque
checkpoint, and a completion flag; a write adapter supplies remote acknowledgements
or conflicts. The runtime does not yet connect these adapters to Syntrix HTTP.

| Responsibility | Contract and rationale |
|---|---|
| Downstream persistence | Finish the current page before reading the next; slow storage cannot accumulate uncommitted pages |
| Metadata and checkpoints | Returned storage errors and rejected promises stop replication; a failed write cannot acknowledge progress |
| Durable completion hook | Run after native page persistence and checkpoint completion; the owning source layer can finish activation before readiness |
| Initial upload barrier | Every new instance waits for a fresh completed source round and its durable hook, including when saved metadata exists |
| Empty source page | Advancing progress requires an identifiable control record; a terminal page may be empty when its checkpoint matches the already persisted position |
| Failure | Cancel admission before the first diagnostic; recovery uses a new instance and retained durable metadata |
| Shutdown | Abort handlers, unsubscribe scheduling, and drain owned storage calls, hooks, and native queues; the caller closes the stores |

The completion flag is the adapter's claim about its source. The runtime does not
infer completion from page length or convert a document version into source
order. Source membership, generation activation, and HTTP checkpoint interpretation
remain the source adapter's responsibility.

### Bounded upstream scheduling

```text
real storage changes --> one dirty flag
                              |
                   native up/down idle
                              |
                           RESYNC
                              |
                    durable changed-doc scan
                              |
                    serialized remote writes
```

The protocol-facing fork has an empty live change feed. The real feed remains
available to local consumers and schedules work through the dirty flag, including
native conflict-resolution writes. The decorator cannot be unwrapped to bypass
its admission checks. Before source readiness, scans return no records and the
unchanged checkpoint; remote writes do not wait inside a native handler. This
avoids a cycle in which downstream waits for active upstream work while upstream
waits for source completion.

The dirty flag is a scheduling hint. Durable local records and native checkpoints
own pending work, so restarting does not depend on retaining in-memory events.

| Changed-document scan default | Bound |
|---|---:|
| Output records | 50 |
| Target encoded JSON payload | 8 MiB |
| Maximum encoded record | 16 MiB |
| Records per underlying read | 4 |
| Concurrent remote write adapter calls | 1 |

A legal record larger than the target travels alone. If only a prefix fits, the
scanner rereads a smaller batch from the same starting checkpoint and recalculates
sizes; it never truncates records while keeping the full batch's checkpoint.
Oversized records fail the read without returning partial progress. These limits
bound replication scan payloads, not total JavaScript heap or local query caches.
Write-time record admission and local query/cache budgets belong to their owning
layers. The existing HTTP Push request budget remains independently applicable.
Returned source document arrays also have a 16 MiB encoded JSON limit by default;
the source adapter must bound reads before allocating its response. Validation
after the adapter returns cannot limit that earlier allocation.

### Dependency delivery

The private lazy bundle includes patched RxDB, Dexie, and RxJS plus third-party
license notices. The remote client entry does not import it. Build validation
checks pinned dependency versions, patch identity/application, and absence of
external vendor imports. An isolated packed-package consumer exercises failure
handling without workspace dependency resolution or consumer-installed patches.

The [runtime decision](../../../.agents/notes/implemented/architecture/2026-09-18-sdk-native-replication-runtime.md)
records the patch obligations, alternatives, and lifecycle costs. Passing native
or fake-IndexedDB tests does not establish complete browser synchronization or
power-loss guarantees.

## Remaining Local Database Integration

The [offline replication proposal](../../../.agents/notes/proposed/feature/2026-09-07-sdk-offline-replication.md)
owns these unimplemented capabilities:

- Public local database creation and SDK-owned persistence with account/database
  identity isolation; public types do not expose RxDB objects.
- Query-based remote sources mapped to independent local collection aliases,
  membership exits, bounded windows, and durable generation activation.
- Local CRUD, lossless typed-value storage, local query results, and dynamic watch.
- Automatic typed HTTP Push, durable acknowledgement/conflict reconciliation,
  cancellation, and recovery across restarts and reconnects.
- Browser lifecycle, multi-tab ownership, storage cleanup, and end-to-end tests.

Direct reads and writes retain the REST API. Push is an internal replication
operation; a public manual Push method is not part of the local API. Native
replication metadata owns delivery progress; a separate SDK outbox is not required.
Legacy coordinator helpers are not connected to the new runtime or exported as a
supported local API.

Realtime notifications and registration `onReady` schedule authoritative source
reads. Their events do not become checkpoints or replace source reconciliation.
Tombstones convey deletion, and the same logical path ID may be recreated. Since
its version may reset, comparing document versions alone cannot order replication
history.

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

## Validation Boundaries

- Runtime regressions cover page backpressure, document/metadata/checkpoint
  failures, cancellation, fresh-source readiness, bounded scans, and recovery with
  retained metadata.
- Packed-package checks exercise the bundled runtime with no workspace fallback.
- Public local API, query-source membership, real HTTP Push integration, and
  browser end-to-end behavior require their own implementation and validation.
