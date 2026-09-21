# Replication Client Design (RxDB + Syntrix replication/realtime)

**Status:** 公开 replica API、本地 CRUD/query/watch 与恢复已实现；下行使用私有 WS typed 数据页并自动 HTTP fallback，上行保留 HTTP Push，SSE 和手动 Pull 保留。

## Context & Why
- We need offline-first replication for web clients using RxDB as local store.
- 服务端提供有界 replica-data WS 及既有 HTTP Pull/Push；运输切换共用源页契约与本地应用器。
- Checkpoint is authoritative only in pull responses; realtime events are triggers, not state.

**Related:** Authentication flows and retry semantics are defined in [003_authentication.md](003_authentication.md); replication uses the same token/refresh handling and does not advance checkpoints on auth errors.

## Goals
- Reliable pull/push replication using RxDB, with typed wire values and flattened decoded documents that exclude storage internals.
- changed 仅调度新 read；实际数据页可走 WS 或 HTTP，checkpoint 只来自合法源响应。
- Conflict-safe push with server-returned conflicts written back or surfaced.
- Offline tolerance: durable local changes, resumable pulls, and native replication metadata.

## Non-Goals
- Owning server checkpoint or transport design, defined in the [replication reference](../../reference/replication.md).
- Rich conflict resolution UI/strategies (provide hooks only).
- Full-text search and application-defined persistent secondary indexes.

## Assumptions
- 私有 WS 使用 replica-data 模式和 typed 查询源页；普通 SSE 事件不进入副本应用器。
- HTTP replication follows the [replication reference](../../reference/replication.md).
- Token-based auth reusable for realtime channel; reconnect allowed.
- The SDK owns its pinned RxDB/Dexie runtime and loads it lazily; applications do not supply an RxDB instance.

## Data Model (flattened)
- Decoded fields: `id`, `collection`, optional deletion flag and server metadata, plus business fields. HTTP uses recursive typed values; int64 values decode to bigint.
- The private runtime accepts storage records and source adapters. Private alias storage supplies a lossless typed-value schema, local CRUD, identity fences, and clean compaction. Private query indexes evaluate this stored view; public replica types project business documents and synchronization controls without exposing RxDB objects.
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
or conflicts. 下行在原有 source adapter 下选择 WS/HTTP，上行使用 HTTP；原生 metadata
继续负责复制进度，不增加另一套应用器。

| Responsibility | Contract and rationale |
|---|---|
| Downstream persistence | Finish the current page before reading the next; slow storage cannot accumulate uncommitted pages |
| Metadata and checkpoints | Returned storage errors and rejected promises stop replication; a failed write cannot acknowledge progress |
| Opaque source checkpoint | Store the complete value under one stable `source` key so native shallow merging replaces it wholesale; adapter reads and completion hooks receive the unwrapped value, including after restart |
| Durable completion hook | Run after native page persistence and checkpoint completion; the owning source layer can finish activation before readiness |
| Durable upstream hook | Run after the native upstream metadata/checkpoint barrier, including no-op progress; pin settlement checks that frontier rather than treating an ACK as durable completion |
| Upstream persistence phase | Bound one native persist unit to at most four scanned batches; begin/complete/failed hooks cover sibling writes, metadata, checkpoint and settlement |
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
| Scanned batches per native persist unit | 4 |
| Business targets per upstream phase | 200 |

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
checks pinned dependency versions, patch identity/application across 17 source,
runtime and declaration files, and absence of
external vendor imports. An isolated packed-package consumer exercises failure
handling without workspace dependency resolution or consumer-installed patches.

The [runtime decision](../../../.agents/notes/implemented/architecture/2026-09-18-sdk-native-replication-runtime.md)
records the patch obligations, alternatives, and lifecycle costs. Passing native
or fake-IndexedDB tests does not establish complete browser synchronization or
power-loss guarantees.

## Private Replica Alias Storage

The lazy bundle owns Dexie-backed alias storage with raw revision CAS. These are
internal building blocks composed by the public replica facade and its replication
coordinator. The [replica-storage decision](../../../.agents/notes/implemented/architecture/2026-09-18-sdk-replica-storage.md)
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

Pending visibility excludes a native download whose origin hash and revision match
the current row, even if assumed metadata is missing or stale. This keeps staged
new members hidden through partial persistence and reopen. A later local edit
advances the revision and restores ordinary pending comparison; active membership,
pins and recovery protections remain independent reasons for visibility.

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
| Structural changes | 活动 physical epoch、source generation 或保护 ID 并集改变时重建；普通 manifest 控制字段只触发核对。活动和 shadow 共享资源上限，完整有效视图才能发布 |
| Missed notifications | Verify the manifest before publication, every 10 seconds while queries are active, and when the page becomes visible |
| Reads | Serialize query materialization across database handles; reserve the shared 64 MiB pool before indexed reads, including manifest reads, and decode only after cache admission |
| Retained state | Bound payload/cache, query configuration, ordering keys, nodes, queued invalidations and output throughout the query lifetime; fail explicitly rather than truncate |
| Ownership | A closing handle releases its observers; remaining handles rebind storage access. Managers retain budgets until owned work drains, including close/reopen overlap |
| Contention | 长 watch 在有界工作或重建尝试用尽后退让并保留订阅；单次读取仍有界失败。无关控制字段变化不打断当前语义视图 |
| Failure | 稳定视图、保留内存或输出实际超限及存储错误仍终止对应查询；不修改记录、pending 或复制进度，隔离应用回调错误 |

The [query decision](../../../.agents/notes/implemented/architecture/2026-09-18-sdk-replica-query-watch.md)
owns default quotas, algorithms and trade-offs. Public replica query references
delegate to this local view; they do not perform implicit remote reads.

扫描预算区分单个视图的硬上限和多次废弃尝试的累计工作额度。累计工作用尽只让出执行权，
不免除真实数据及持续保留对象的预算。查询视图身份从已有字段派生，不增加存储格式或
checkpoint；详细取舍见[视图竞争决定](../../../.agents/notes/implemented/bug-fix/2026-09-20-replica-watch-contention.md)。

## Private Downstream Coordination

The private source adapter connects authenticated matching-set and window requests
to alias storage through the native runtime. Successful envelopes are bounded and
validated before projection. Initial reads may be unbound; every later source
request, including empty-cursor resync, carries the immutable expected database
identity. The existing stricter write guard remains independent.

| Mechanism | Contract |
|---|---|
| Projection | Fold repeated event IDs in source order into independent d/m/c records; leave affects membership, while logical delete supplies a tombstone without invented metadata |
| Delivery | Fetch one source page at a time; each native chunk has at most 201 rows and 16 MiB, with c progress; advance the server cursor only on the last chunk |
| Replacement | Keep old active membership through window/rebuild staging; activate after record, assumed metadata and checkpoint persistence; recover ambiguous manifest completion by rereading |
| Pending protection | Preserve current edit token/pin on downloaded d writes under the original CAS; m still applies when native preserves pending d |
| Pin settlement | Require matching current token/business state and durable native up frontier, then await a later completed source round before clearing protection |
| Leadership | One coordinator owns source work per alias using the pinned native elector; followers derive readiness from durable manifest, and coordinator recreation uses fresh election ownership |
| Scheduling | Default 10s polling and 200ms hint coalescing; retry transient reads with capped exponential delay/jitter and Retry-After; do not retry persistence failures as network faults |
| Maintenance | Retain coordinator leadership when this or another handle retires native ownership; drain and recreate from the selected durable epoch, including not-clean compaction and verified safe rollback. Scope capture waits for local maintenance; a handoff race retries only after verifying a replacement owner in the same session and definition |
| Shutdown | Abort and drain before releasing election; preserve genuine I/O and cleanup failures, while normal cancellation and handled source rejection close safely |

Without an internal write adapter, upstream scanning is disabled and pending edits
remain unsent. No synthetic ACK is generated. With the private upstream adapter,
the same coordinator owns durable phase handling and explicit recovery. 私有源运输由活动
native owner 取得租约，WS changed 调度现有 hint；整页完成回执在最后分块与必要
manifest/pin 持久化之后发送，owner 清理前不释放仍在应用的页面。

The [downstream decision](../../../.agents/notes/implemented/architecture/2026-09-19-sdk-downstream-replication.md)
owns source/delivery generation distinctions, pin transitions, retry classes and
maintenance recovery conditions. The Store/Puller checkpoint mechanism is unchanged.

## Private Upstream and Recovery

The private adapter maps native assumed/desired pairs to typed HTTP Push. A
successful write returns the real native `[]` result, so RxDB stores the submitted
desired state as assumed without inventing a server version or timestamp. A newer
local edit remains distinct from an earlier acknowledgement.

| Assumed state | Desired state | Remote operation |
|---|---|---|
| Live with version | Changed live / deleted | Conditional update / delete |
| Live without version | Changed live / deleted | Authoritative single-ID read, then conditional write only if current business state still equals assumed |
| Absent or tombstone | Live | Create without version |
| Absent or tombstone | Deleted | No remote mutation |
| Possibly committed but unsettled | Any | Pause for explicit uncertain-result recovery |

Each handler freezes assumed and desired. Business equality includes identity,
existence and exact typed payload; it ignores server metadata and pins, so int64
and float64 remain distinct. A conflict already equal to desired is satisfied.
For update/delete only, a live current state equal to assumed supplies a new CAS
version; at most three attempts are made. Known successful indices are not sent
again within that handler. A changed or missing baseline becomes a durable issue;
a stale update never becomes create.

Missing-version preflight uses the authoritative ID Query with deleted records
included and no source filter. It supplies only a CAS candidate: it does not
advance membership or source progress, and ordinary read failure cannot authorize
an unconditional mutation. Successful ACKs do not otherwise trigger ID reads.

| Transport boundary | Contract |
|---|---|
| Identity | Every Push, retry, preflight and recovery read carries the original bound `X-Syntrix-Expected-Database-Identity`; preserve the configured URL namespace |
| Dispatch | One upstream wire request per alias at a time; each request retains its captured session and binding through authentication waits and retries |
| Request | At most 50 changes, split by the actual 10 MiB HTTP body and a conservative 20 MiB protobuf budget; preserve original change indices, including repeated IDs |
| Response | Push has a 32 MiB client resource cap before decoding; oversized or malformed postdispatch results remain unknown, not proof of nonexecution |

Database identity mismatch blocks new dispatch and retains the binding and any
earlier uncertain work. It is never interpreted as authoritative absence.

### Durable phase

One phase is one native persist-to-master unit, bounded to four scanned batches
and at most 200 business targets. A manifest marker records phase, session,
physical epoch, target IDs/tokens and possible dispatch. Its persistence must be
confirmed before wire admission. Sibling callbacks share it; it is not an ACK
journal or a copy of native assumed/checkpoint data.

```text
persist marker -> serialized wire work -> native assumed/conflict metadata
  -> native up checkpoint (including no-op) -> matching-token pin settlement
  -> clear this phase's marker
```

A phase failure closes dispatch and drains siblings. Automatic retry is allowed
only when the whole phase sent no mutation or every sent mutation is proven not
executed. Any accepted, already-satisfied or possibly committed result without
reliable settlement makes the whole phase uncertain. Native persistence failure
stops the runtime; restart preserves the dirty marker and pauses automatic
application. Pause/resume does not authorize replaying uncertain effects.

### Explicit recovery

Private `pause`, `resume`, `inspect` and `resolve` are coordinator controls.
Pause drains replication while retaining leadership and local CRUD/watch.
Resolve requires the elected owner and stays paused until explicit resume.
Unresolved issues or markers prevent resume; a pending recovery intent blocks edits to its target,
while other IDs remain locally editable outside the bounded persistence lock.

Inspection is offline by default: it returns bounded issue/target metadata, with
at most one requested ID's desired/assumed pair under the existing row budgets.
Explicit `readCurrent: true` requires `logicalId`, a configured transport and a
bound identity. It adds `current: {source: 'authoritative-read', document}`;
omitted `current` means unknown, `document: null` means confirmed missing, and a
tombstone remains distinct. The request retains the original bound database
identity/session and runs outside the alias lock. After it returns, issue, edit
token, physical epoch, binding and the desired/assumed pair are rechecked; a stale
inspection fails. This observation is advisory: adopt/merge resolution
independently rereads current. Inspection does not persist a payload journal or
add automatic reads after ACKs.

| Decision | Effect |
|---|---|
| Adopt server | Use an authoritative current state as both desired and assumed; missing maps to explicit absence |
| Merge local | Use current as assumed and the chosen typed business content as new desired with a new edit token/pin |
| Retry uncertain | Require explicit acknowledgement of potentially repeated effects before clearing the phase for native retry |
| Reset alias | Require explicit pending-data discard and an unchanged inspection token; replace local physical state while retaining the original database binding |

Adopt/merge stop normal replication, read current without holding an alias lock,
then recheck issue, edit token and physical epoch under exclusive ownership. A
stale decision fails. A confirmed, bounded recovery intent saves current
(including absence), desired and the preallocated result token before fork or
assumed metadata writes. Replay finishes that exact decision after a crash;
it does not fetch a replacement current state. The two stores have no shared
transaction, so verification precedes clearing the intent and corresponding
issue/phase target. Other IDs and ordinary up/down checkpoints remain unchanged
by adopt/merge; unresolved sibling targets keep the phase blocked.

The [upstream decision](../../../.agents/notes/implemented/architecture/2026-09-20-sdk-upstream-replication.md)
owns failure classification and recovery costs. Unknown create may have succeeded
and then been deleted; automatic retry could recreate it. Explicit retry can
permit that effect. Same-version ABA and repeated external side effects remain
outside this contract; no outbox, idempotency service or source receipt is added.

## Public Replica Database

`replicate(path)` constructs an immutable, client-owned source without opening
storage or sending requests. `openReplica` freezes the complete configuration,
opens all requested aliases locally, then starts their replication coordinators. It
returns before network convergence so existing and new local state remain usable
offline. The [public reference](../../reference/typescript_sdk.md#replica-availability)
owns signatures, runnable examples, defaults, errors and browser requirements.

| Public responsibility | Contract and reason |
|---|---|
| Access distinction | REST references operate remotely; replica references operate locally without fallback or child-collection discovery |
| Source and alias | Same-client immutable builders define matching sets or complete windows; each alias has independent state, even for identical sources |
| Reopen | The frozen definition must match; omitted aliases remain stored but unopened. A new source needs a new alias or clean remove/recreate; reset preserves the current definition/binding |
| Local values | Preserve exact typed values and logical IDs; source metadata is optional and does not describe an unsent edit. Tombstones permit same-ID recreation |
| Query/watch | Reuse bounded exact queries and complete local result publication; callbacks stop with their owning handle |
| Status | Combine durable source readiness/generation/round/pending/pins with current leader and lifecycle state; native idle is not a source watermark |
| Recovery | Project private issues, durable phase/intent facts and advisory actions with bounded document inspection, including explicit authoritative current; preserve nullable edit tokens and physical-epoch guards |
| Ownership | Browser capability checks precede lazy open; account changes invalidate old handles and pending opens, and close drains all owned work while retaining failures |
| Diagnostics | Correlate facade lifetime, operation, session and available request/epoch/count/duration fields; exclude payloads, filter values, credentials and raw errors, without claiming distributed server tracing |
| Diagnostic reentrancy | Recheck ownership and subscription activity after application diagnostics before delivering results or errors; callbacks may synchronously close, unsubscribe or change accounts |

One elected coordinator per alias owns network work; followers use durable
manifest state for readiness and all tabs retain local CRUD/watch. Query managers
share budgets within the replica database, while each public handle retains its
own close responsibility. Open requires window/document, IndexedDB, Web Locks and
Web Crypto; the remote-only API does not inherit these requirements.

### Alias removal and recreation

Removal cancels/drains the caller's coordinator and takes exclusive alias
ownership. It checks actual pending differences, pins, issues and unfinished
phases/intents before persisting terminal removal. It can address an omitted
historical alias from its namespace without reopening its source. It removes
local physical stores, not remote documents.
An empty `collections` configuration creates no source coordinator and supports
this historical-only removal path.

A small terminal manifest retains the lifetime fence through cleanup failure.
Reopen completes owned cleanup before allocating a new lifecycle ID and physical
epoch. Old handles reject the new lifetime even after missed notifications;
stable alias locks serialize the transition, while election/query ownership is
scoped to the lifetime. Compaction preserves that lifetime. These boundaries
prevent a delayed old close or write from operating on a recreated alias.
Manifest notifications are hints, including removal and lifetime changes. Verify
the current durable manifest under the stable alias lock before invalidating a
handle, so delayed notifications from a prior lifetime cannot retire its valid
replacement. Per-operation lifetime checks still fence genuinely obsolete handles
when notifications are missed.

### 私有 WS 数据与 HTTP fallback

`ReplicaDatabase` 句柄拥有至多一条惰性私有 WS，只有活动 leader alias 取得源运输
租约；follower 继续观察持久状态。同一 collection 的 alias 只共享 socket，各自保留
源定义、绑定、cursor、subId 和应用 owner。最后一个租约释放时关闭连接与重连 timer；
独立句柄不使用全局连接池。

```text
现有 source.read -> 私有 WS read/page
               \-> WS 不可用：有限 HTTP Pull
                           |
                  唯一 downstream 应用器
                           |
          最后一块 doc/meta/checkpoint + manifest/pin
                           |
                 committed -> ACK / 归还页额度
```

两种运输共用严格 request 编码和 typed page decoder，绑定、source hash、generation、
窗口和源水位语义保持一致。只有合法数据页建立首次绑定，auth/subscribe ACK 不建立
数据位置。已绑定后注册、read、重连和 HTTP fallback 都保留原 expectedDatabaseIdentity，
不改写 URL namespace。Matching set 使用已有持久 cursor；窗口仍完整重取并在整页
持久化后激活。

| 边界 | 契约与原因 |
|---|---|
| 有限 read | 每次返回一页或错误；空进度页可结束轮次，让原生上下行重新 idle，避免阻塞本地 Push/settlement |
| 源租约 | 绑定 session、native physical epoch/instance；pause、维护、移除或账号切换先失效请求归属 |
| 页额度 | 句柄最多四页、同 alias 一页，WS 与 HTTP 共用；等待有界到每 alias 一个排队请求并可取消 |
| 已接纳页 | socket 失效不释放它的额度；现有应用器完成整页或 owner 清理完成后归还 |
| 整页 ACK | 最后分块的文档、assumed、checkpoint、manifest 激活和必要 pin 处理完成后发送；不等待 token 或重连 |
| ACK 失败 | 仅影响运输可用性，不能反向拒绝已经完成的本地提交；HTTP receipt 只归还页额度 |
| 新鲜轮次 | changed 与注册完成只调度现有 hint；新的 read 才产生页面，不能把 settlement 前的旧页重新标为新 round |
| 关闭 | 先退休源租约，再 drain 原生队列及应用器，最后释放未完成页与句柄运输；真实存储错误保留 |
| 订阅退休 | 在所属连接上发送原 subId 的 unsubscribe；发送失败则关闭连接，触发服务端清理。订阅级限流不遗留无人持有的服务端注册 |

| 切换点 | 处理 |
|---|---|
| WS 未交付页 | 撤销旧请求/订阅归属，以同一已提交状态请求 HTTP |
| WS 页已交给应用器 | 完成当前页后下一次 read 才换运输；不并发应用 HTTP 页 |
| HTTP 读取/应用期间 WS 恢复 | 后台只连接/认证；等当前 read 退出且整页完成，再注册并走 WS |
| 维护或 native owner 替换 | 旧 alias 租约失效，新 owner 取得新 subId；其它 alias 可以保留共享连接 |

运输/协议损坏可撤销 WS 后使用独立校验的 HTTP 数据。明确的权限、绑定身份、非法源、
本地持久化错误走既有阻塞/恢复流程；REPLICATION_SOURCE_BUSY/429 的 retryAfter 和
源 retryAt 跨运输保留。重连、changed 和 fallback 都不能绕过源退避。
权限拒绝等终止连接错误保持阻塞，直到显式 resume 在旧应用任务排空、存储校验通过后
重新允许认证。该入口只恢复连接准入，不清除源退避，也不跨越会话失效边界。
WS 失败不决定正在发送的 HTTP Push 成功与否。

默认 10 秒源核对与 200ms hint 合并保留。WS 健康时，周期核对请求和真实页面均走 WS，
不另发 HTTP Pull；fallback 才用 HTTP。通知遗漏和窗口索引滞后仍需要这些核对，不能把
WS 当成没有 Query 读取成本的 raw-event 路径。完整取舍由
[SDK WS 复制决定](../../../.agents/notes/implemented/architecture/2026-09-21-sdk-replica-websocket.md)维护。

## Connection Health & Keepalive

| 边界 | 当前值 |
|---|---|
| WS 建连/认证 | 10 秒 |
| 每次源注册 | 10 秒 |
| SDK WS read / HTTP fallback read | 各 45 秒；取消整个有限读取，给服务端 30 秒 Query 及授权准入留出余量 |
| SDK 共用页池等待 | 45 秒且可取消；超时进入额度退避，不释放其它仍在应用的页 |
| WS 重连 | 退避基数 1 秒增长至 30 秒，带 jitter；有效租约存在时持续尝试，不使用旧公开 API 的五次上限 |
| 服务端 WS ping / pong | 54 秒 / 60 秒；浏览器自动应答 |
| 服务端 WS 单帧写 | 10 秒 |
| 服务端 SSE heartbeat | 15 秒，沿用普通 SSE 契约 |

重连探测只做连接/认证，不提前建立第二套源读取。凭据取消监听先于共享 provider 调用；
退休尝试的包装立即结束，底层共享 refresh 可供其它 HTTP 调用继续使用，迟到结果仍
受会话 fence 限制。重新注册使用新 subId；同 requestId 的重复页不重复应用，旧代消息
直接丢弃，当前代未知请求页作为协议错误处理。

公开 raw WS API、配置和协议类型已经移除；没有新的公共 transport 选择开关。
SSE 与手动 Pull 保留。运输诊断通过现有 onDiagnostic 报告 mode、transportEpoch、
subId/requestId 和固定 phase；接纳页面、本地整页完成与上行确认保持不同含义。

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
- Private downstream: ordered event projection, bounded window activation,
  durable pin settlement, read retries and native leader/follower lifecycle.
- Private upstream: real native success, CAS attempt limits, request identity,
  accepted-prefix/whole-phase failures, explicit recovery and crash replay.
- Public replica integration covers source freezing, offline local readiness,
  typed query/CRUD and recovery projection, lifecycle removal and package exports.
  Browser/server tests and packed-package checks validate their respective paths;
  none establish browser power-loss durability. WS 主路径、HTTP fallback、整页回执与运输
  归属需要各自的故障和真实浏览器验证，不能由静态协议检查代替。
