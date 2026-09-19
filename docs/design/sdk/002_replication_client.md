# Replication Client Design (RxDB + Syntrix replication/realtime)

**Status:** Manual Pull, WebSocket lifecycle, a private native replication runtime, replica alias storage, and bounded private query/watch are implemented. The server also supports matching-set query sources, complete result windows, and bound database identity checks. Their SDK adapters, the public replica database API, and automatic HTTP synchronization remain planned.

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
- Full-text search and application-defined persistent secondary indexes.

## Assumptions
- `/realtime/ws` (or `/realtime/sse`) can subscribe per collection and delivers at least `{ collection, id, action, updatedAt, deleted? }` plus some monotonic seq/lsn (used only for diagnostics; not trusted as checkpoint).
- HTTP replication follows the [replication reference](../../reference/replication.md).
- Token-based auth reusable for realtime channel; reconnect allowed.
- The SDK owns its pinned RxDB/Dexie runtime and loads it lazily; applications do not supply an RxDB instance.

## Data Model (flattened)
- Decoded fields: `id`, `collection`, optional deletion flag and server metadata, plus business fields. HTTP uses recursive typed values; int64 values decode to bigint.
- The private runtime accepts storage records and source adapters. Private alias storage supplies a lossless typed-value schema, local CRUD, identity fences, and clean compaction. Private query indexes evaluate this stored view; public replica types remain part of the replica API integration.
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
| Opaque source checkpoint | Store the complete value under one stable `source` key so native shallow merging replaces it wholesale; adapter reads and completion hooks receive the unwrapped value, including after restart |
| Durable completion hook | Run after native page persistence and checkpoint completion; the owning source layer can finish activation before readiness |
| Initial upload barrier | Every new instance waits for a fresh completed source round and its durable hook, including when saved metadata exists |
| Empty source page | Advancing progress requires an identifiable control record; a terminal page may be empty when its checkpoint matches the already persisted position |
| Failure | Cancel admission before the first diagnostic; recovery uses a new instance and retained durable metadata |
| Partial fork persistence | Preserve the native download-origin metadata through inserts; replay recognizes downloaded rows by origin and revision even when assumed metadata was not committed, while later local edits invalidate that marker |
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

## Private Replica Alias Storage

The lazy bundle owns Dexie-backed alias storage with raw revision CAS. These are
internal building blocks; they do not expose `openReplica` or perform HTTP
synchronization. The [replica-storage decision](../../../.agents/notes/implemented/architecture/2026-09-18-sdk-replica-storage.md)
owns the persistence choices and their costs.

### Identity and lifetime

| Concern | Contract |
|---|---|
| Namespace | Hash the canonical endpoint, JWT subject, exact configured database string, local name, and alias; retain the original tuple for verification |
| Endpoint | Preserve URL path prefixes, normalize equivalent trailing slashes, reject credentials/query/fragment |
| Offline identity | Require a nonempty JWT `sub`, with matching `oid` if present; an expired token can open offline storage, while missing/malformed identity cannot select a fallback account |
| Source binding | Freeze the source definition; initially unbound storage can accept local edits; CAS binds the first database ID/source hash and rejects later reassignment |
| Session refresh | Same-subject refresh keeps ownership; a subject change invalidates admission before draining owned resources, including an opening alias |
| Owned cancellation | Alias lifetime follows its session; each native generation also has its own cancellation signal. Cancel that generation before alias close or maintenance revokes its scope, so queued operations use the exact reason recognized during native drain |
| Maintenance ownership | Retire the previous native generation and give future instances fresh ownership; seed work follows the alias lifetime rather than the retired normal runtime |
| Drain failure | Keep the failed owner's drain obligation observable; another account cannot proceed as if cleanup succeeded |
| Bundle boundary | Ownership belongs to the token provider through a shared versioned capability, so the remote entry and lazy bundle use the same owners |

JWT parsing chooses an offline namespace; it is not server authentication or a
security boundary against malicious same-origin JavaScript. Different database
URL spellings retain separate namespaces even if they resolve to the same ID.

Captured work binds subject, session version, source-definition hash, physical
epoch, native instance, and request ID. The network guard requires durable binding
and source readiness and supplies `X-Syntrix-Expected-Database-Identity`. It does
not send a request or replace the server's identity gate. Scope failure preserves
local edits and the old binding; automatic source and Push adapters still own
network orchestration.

### Durable records and local operations

| Record | Responsibility |
|---|---|
| `d` | Desired business state, logical ID, live/deleted/absent existence, edit token, settlement pin, and known wire metadata |
| `m` | At most two source-generation membership slots and the last observed source metadata |
| `c` | Source progress, generation, completion, and partial-delivery state |
| Manifest | Namespace/source binding, active physical epoch, active/staged source generations, recovery intent, bounded issues, and upstream marker |
| Native metadata | Assumed business state and durable replication progress; no separate outbox |

Business payloads are recursive typed values stored as JSON strings. Int64 stays
lossless and returns as bigint. Logical IDs are retained beside hashed physical
keys and checked on access. Logical deletion and absence keep native
`_deleted:false`; nonlive payloads are empty, and absence is not a tombstone shown
by `showDeleted`. Recreating the same logical ID is allowed. Physical row order
is not public logical-ID query order.

Local reads combine business state from `d` with the latest source metadata from
`m`; that metadata is not the revision of an unsent local edit. Visibility retains
members and protected local work. Reads hide deletions unless requested and always
hide absence. `set` creates or replaces, `update` shallow-merges only a live
document, and deleting a missing/deleted document is idempotent. Generated IDs are
available alongside explicit logical IDs. Reserved metadata cannot be written as
business fields.

```text
alias shared lock -> current physical epoch -> view-write exclusive lock
  -> read d + m -> evaluate frozen ifMatch -> desired + edit token + pin
  -> raw revision CAS -> success, or reread and recompute on 409
```

CAS retries are limited to eight. All native fork/control writes and maintenance
state changes use the same lock order and admission fence. Reads take the shared
view lock. Conditions on version/time use the latest observed metadata, including
metadata-only changes; other failures propagate. Locks cover local persistence,
not HTTP waits. Pin and edited state commit together.

Manifest and row feeds expose invalidation hints. A source-generation or physical
epoch change requires rereading the manifest and rebuilding affected views.
Cross-tab consumers observe persisted manifest changes; a hint is not authoritative
state. Query evaluation and dynamic query watches are not implemented by this feed.

### Admission and materialization limits

| Default bound | Limit |
|---|---:|
| Encoded `d`/`m`/`c` row, including native system fields | 16 MiB |
| Encoded manifest | 34 MiB |
| Native metadata row, including its nested record and envelope | 17 MiB |
| Raw data-read reservation pool | 64 MiB |
| Separate control-read pool | Twice the manifest row limit: 68 MiB |
| One underlying indexed seek/ID read | At most 4 rows, reduced to fit its pool |
| Native handoff result | 128 MiB |
| Known logical IDs per alias | 100,000, configurable |

Every persistence entry checks the final stored row, including source, seed,
recovery, and control writes. Recovery intents hold exactly two typed data-record
snapshots for one target. Reads reserve capacity before materializing rows;
retained snapshots remain charged within their owning scope. Physical scans must
use an index-satisfied primary-key seek without a blocking sort. Bulk writes also
reserve capacity for the storage engine's implicit reads of current documents and
split work into bounded chunks. The native handoff budget includes retained write
inputs and conflict results; each next chunk must fit before it is dispatched.
If admission fails, earlier successful writes remain durable, the operation fails,
and replication must not advance its checkpoint. Retry reconciles those partial
results through the existing per-row CAS rules. These encoded byte limits do not
bound total JavaScript heap, native runtime queues, or future query caches.

The storage uses raw collection storage rather than RxDocument/RxQuery views.
Unused high-level event history and lazy document-cache tasks are disabled or
drained against the pinned RxDB internals, while replication and invalidation
feeds remain active. Upgrading RxDB requires checking that retained-buffer behavior.

Capacity accounts for data, membership, control, manifest, and native metadata,
including inactive storage awaiting cleanup. The default relies on browser quota
and the known-ID cap; it does not add a 512 MiB alias cap. A configured byte cap
requires authoritative accounting before writes, including cross-tab changes;
these bounded rescans can add work. Capacity failures preserve pending state.

### Clean physical compaction

Source generation describes a remote member set; physical epoch describes local
storage replacement. Compaction preserves the former and its source checkpoint.
Statistics recommend maintenance under capacity pressure or when at least 1,000
retired IDs form at least 25% of known IDs; callers schedule the attempt.

```text
stop native admission -> cancel/drain -> exclusive alias lock -> clean check
  -> one shadow epoch -> native seed + assumed metadata + checkpoints
  -> verify -> manifest CAS flip -> remove old fork and paired native metadata
```

Clean means source completion is active, no staged/partial generation exists, and
there are no pending business differences, pins, issues, dirty markers, or recovery
intents. The shadow keeps current live members and source control. Native seed
builds assumed metadata with the new epoch's normal identifier; source checkpoint
is preserved, while the local upstream checkpoint is regenerated. Any business
Push during seed is an error, preventing copied rows from echoing upstream.

A private maintenance capability reuses the exclusive lock for seed writes rather
than reacquiring shared ownership. Thirty seconds without scan or durable seed
progress aborts maintenance; this is an inactivity timeout, not a duration limit
for a large collection. Quota must accommodate both epochs. After an ambiguous
manifest write, reread the active epoch before removing either copy; startup removes
confirmed inactive orphans and their explicitly paired native metadata. An unreadable
manifest retains both copies. Only one shadow exists at a time.

## Private Replica Queries and Watch

The private query client evaluates authoritative alias projections using the
[filter and ordering contract](../../reference/filters.md). It preserves exact
bigint/number comparisons, UTF-8 order, missing/null distinction, and logical ID
tie-breaking. Page reads default to 100 documents with a maximum of 1000; typed
cursors bind the alias and normalized query, with no cross-page snapshot promise.
Watch returns complete matching results or a limited ordered window and does not
accept a continuation cursor. Returned values are isolated from the internal cache.

| Mechanism | Contract |
|---|---|
| Sharing | One resource manager per replica database in each execution context; identical canonical queries share a matcher and full candidate AVL |
| Ordinary changes | Coalesce d/m/assumed changes to document keys and update candidates incrementally; retain candidates outside a limited window for refill |
| Structural changes | Rebuild a shadow view after manifest revision or physical epoch changes; active and shadow resources share the same limits, and only a complete current view can publish |
| Missed notifications | Verify the manifest before publication, every 10 seconds while queries are active, and when the page becomes visible |
| Reads | Serialize query materialization across database handles; reserve the shared 64 MiB pool before indexed reads, including manifest reads, and decode only after cache admission |
| Retained state | Bound payload/cache, query configuration, ordering keys, nodes, queued invalidations and output throughout the query lifetime; fail explicitly rather than truncate |
| Ownership | A closing handle releases its observers; remaining handles rebind storage access. Managers retain budgets until owned work drains, including close/reopen overlap |
| Failure | Terminate the affected canonical query and release its resources without altering records, pending edits or replication progress; isolate application callback errors |

The [query decision](../../../.agents/notes/implemented/architecture/2026-09-18-sdk-replica-query-watch.md)
owns default quotas, algorithms and trade-offs. Private query availability does
not expose a public replica database or connect a network replication adapter.

## Remaining Replica Database Integration

The [offline replication proposal](../../../.agents/notes/proposed/feature/2026-09-07-sdk-offline-replication.md)
owns these unimplemented capabilities:

- Public `openReplica()` creation over the implemented private alias storage,
  returning a `ReplicaDatabase` with `ReplicaCollection` handles; public types do
  not expose RxDB objects.
- HTTP adapters for the server's matching-set and result-window sources and bound
  database identity header; map sources to independent local aliases, schedule
  window refreshes using realtime hints plus polling, and durably activate
  membership generations.
- Public CRUD, query and dynamic watch facades over the delivered private storage
  and query clients.
- Automatic typed HTTP Push, durable acknowledgement/conflict reconciliation,
  cancellation, and recovery across restarts and reconnects.
- Automatic synchronization ownership and scheduling across tabs, and complete
  browser-to-server end-to-end tests.

Direct reads and writes retain the REST API. Push is an internal replication
operation; a public manual Push method is not part of the replica API. Native
replication metadata owns delivery progress; a separate SDK outbox is not required.
Legacy coordinator helpers are not connected to the new runtime or exported as a
supported replica API.

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
- Private storage regressions cover identity, typed persistence, raw CAS,
  metadata conditions, admission budgets, and clean compaction recovery. Real
  browser multi-tab checks cover storage and locks; they do not establish power-loss
  durability or complete browser-to-server synchronization.
- Private queries: typed semantics, bounded materialization, incremental window
  refill, manifest-only changes, shared ownership and continuous resource limits.
- Public replica API, query-source membership application, and real HTTP Push
  integration require their own implementation and validation.
