# TypeScript Client SDK Reference

The `@syntrix/client` package provides a type-safe interface for Syntrix. It includes `SyntrixClient` for external apps and `TriggerClient` for trigger workers.

## Installation

```bash
npm install @syntrix/client
# or
bun add @syntrix/client
```

## 1. SyntrixClient (Standard)

Use this client in external applications (Web, Mobile, Backend). Multi-database auth requires a database ID during login.

```typescript
import { SyntrixClient } from '@syntrix/client';

const client = new SyntrixClient('<URL_ENDPOINT>', {
  database: 'my-database',
});

await client.login('username', 'password');
```

### Methods

#### `doc<T>(path: string): DocumentReference<T>`

Creates a reference to a document.

#### `collection<T>(path: string): CollectionReference<T>`

Creates a reference to a collection.

#### `pull<T>(collection: string, options?: PullOptions): Promise<PullPage<T>>`

Fetches one authenticated replication page for the configured database, preserving
any base URL prefix. Options are `checkpoint?: string | null`, `limit?: number`,
and `signal?: AbortSignal`. Omitted/null checkpoint starts initialization; a supplied
checkpoint must be nonempty and at most 256 KiB. Limit defaults to 100 and must be
an integer from 1 through 1000.

```typescript
const page = await client.pull<{ name: string }>('users', {
  checkpoint: savedCheckpoint,
  limit: 100,
  signal: abortController.signal,
});
```

The result contains `documents`, opaque `checkpoint`, and boolean `caughtUp`.
Documents have flattened business fields and logical identity; int64 metadata and
nested values decode to bigint. A deletion may contain only `id`, `collection`,
and `deleted: true`. Process records in order, tolerating repeated states and
version reset after recreation. Continue with the checkpoint when `caughtUp` is
false, even on an empty page.

The application must atomically apply a whole page and persist its checkpoint.
Failures leave saved progress unchanged. `RESYNC_REQUIRED` means rebuilding the
server mirror from null while preserving pending local edits. This method does not
automatically fetch more pages or save progress.

Plain JSON serialization rejects bigint, including values in decoded Pull and
Query documents. Existing SDK document `set`/`update` therefore cannot blindly
round-trip such documents. Converting to Number can lose precision. Use a lossless
local persistence representation. The server's
[HTTP Push](replication.md#push-changes) accepts typed document objects, and the
SDK's private upstream adapter preserves this representation. It is available
only to internal replication; ordinary CRUD formats remain unchanged and a
complete Pull response is not a Push request.

Pull binds the session before scheduling the request. Login, signup, or logout
invalidates a successful old-session response with `AuthSessionChangedError`;
applications must also invalidate pending local application after account changes.
Keep mirrors and checkpoints separated by account, database URL scope, and collection.

The implemented access profile requires the database owner or matching `db_admin`
grant; that full-scope policy remains provisional pending approval. See the
[replication reference](replication.md) for transport encoding, limits and recovery.

### Replica availability

`client.replicate<T>(path)` builds a source definition.
`client.openReplica(options)` opens an account-scoped `ReplicaDatabase` with
local CRUD, query/watch and automatic replication. 正常下行通过私有 WS 数据页传输，
不可用时自动使用 HTTP fallback，上行仍是 HTTP Push。It returns after local
initialization, without waiting for the network or source convergence.

| Access | Behavior |
|---|---|
| `client.collection(path)` / `client.doc(path)` | Existing direct REST reads and writes |
| `replica.collection(alias)` | Local persisted state; no implicit remote fallback or child-collection navigation |
| `client.pull()` | One manual page; the application owns local application and checkpoint persistence |

The runtime loads lazily on open. Patched RxDB, Dexie and RxJS are bundled with
their licenses; applications do not install them, apply patches or receive RxDB
objects. Importing the remote client and constructing a source do not load that
bundle. There is no public manual Push method.

#### Browser and identity requirements

Open requires a browser window/document, IndexedDB, Web Locks and Web Crypto
(`subtle.digest` and `randomUUID`). Missing capabilities fail with
`ReplicaUnsupportedEnvironment`; Node/SSR and workers are not supported replica
hosts. Direct REST APIs retain their existing environment support.

A token must identify a nonempty JWT `sub`; `oid`, when present, must match.
An expired token can open existing or new local storage offline. Opening does
not authenticate that token with the server; online operations still require
valid authentication and the source's database-owner or matching `db_admin`
authorization. Invalid/missing local identity fails rather than choosing a shared
anonymous namespace. Same-subject refresh retains storage; account replacement
invalidates old handles and pending opens.

Endpoint (including its path prefix), subject, exact configured database string,
local `name` and alias identify storage. Distinct database URL spellings remain
separate even if they resolve to one server ID. A source's first accepted identity
is bound durably; later Push, Pull, preflight and recovery requests retain that
identity. Database reassignment blocks synchronization without rebinding or
discarding local edits.

#### Typed quick start

Run this from a browser application with a configured endpoint and JWT. The
server must provide the indexes required by the source filter/window.

```typescript
import { SyntrixClient, type ReplicaDocument } from '@syntrix/client';

type Task = {
  projectId: string;
  title: string;
  status: 'open' | 'closed';
  estimate: bigint;
};

export const openTasks = async (endpoint: string, token: string) => {
  const client = new SyntrixClient(endpoint, {
    database: 'app',
    auth: { token },
  });
  const source = client.replicate<Task>('projects/p1/tasks')
    .where('projectId', '==', 'p1');
  const replica = await client.openReplica({
    name: 'task-cache',
    collections: {
      projectTasks: source,
      recentTasks: source.orderBy('updatedAt', 'desc').limit(100),
    },
  });
  const tasks = replica.collection<Task>('projectTasks');
  const created = await tasks.add({
    projectId: 'p1', title: 'Review', status: 'open', estimate: 2n,
  });
  await created.ifMatch('status', '==', 'open').update({ status: 'closed' });

  const query = tasks.where('status', '==', 'open').orderBy('id').limit(20);
  const page = await query.getPage();
  if (page.nextCursor !== null) {
    const next = await query.startAfter(page.nextCursor).getPage();
    console.log(next.documents);
  }
  const render = (documents: ReplicaDocument<Task>[]) => {
    for (const document of documents) {
      if (!document.deleted) console.log(document.id, document.title);
    }
  };
  const stopWatch = query.watch(render, console.error);
  const stopStatus = replica.sync.subscribe(status => {
    console.log(status.aliases.projectTasks?.state);
  }, console.error);

  return {
    replica,
    close: async () => {
      stopWatch();
      stopStatus();
      await replica.close();
    },
  };
};
```

Source builders are immutable and belong to the client that created them.
`where` adds typed filters; `orderBy` adds unique fields; `limit(1..1000)`
selects a complete remote window. Without a limit, the source tracks all matching
members. Sources have no `startAfter`, direct read or write methods.
Configuration and query arguments are copied before asynchronous work.

Aliases are independent, including aliases with identical sources. A local edit
in one alias reaches another through server synchronization, not shared local
records. Reopening an alias requires its frozen source definition to match.
Omitting an old alias from `collections` preserves it but does not open it.
Use a new alias, or remove a clean alias and reopen it, to change its source.
`reset-alias` rebuilds the current definition and retains the database binding.

#### Local references and queries

| Method | Result and semantics |
|---|---|
| `collection.doc(id?)` | Reference to a logical document ID; omission generates an ID without writing |
| `collection.add(data)` | Persist a generated-ID document and return its reference |
| `doc.get({showDeleted?})` | Local document or null; deletions are hidden by default, absence is always hidden |
| `doc.set(data)` | Create or replace business content; permits recreation of the same logical ID |
| `doc.update(partial)` | Shallow-merge into a live document; missing/deleted targets fail |
| `doc.delete()` | Persist a logical tombstone; missing/deleted targets are idempotent |
| `doc.ifMatch(field, op, value)` | Freeze a local condition for the next write; multiple conditions combine |
| `query.get()` / `getPage()` | One local page; default 100, maximum 1000; page adds cursor and effective order |
| `query.startAfter(cursor)` | Continue the same normalized query; no cross-page snapshot promise |
| `query.showDeleted()` | Include tombstones, whose former business content is absent |
| `query.watch(onResult, onError?)` | Complete local matches or ordered limited window; returns unsubscribe and rejects continuation cursors |

Writes resolve after local persistence, not remote ACK. They remain available
offline or while synchronization is paused/blocked, except a target protected
by an unfinished recovery intent. `set`, `update` and `delete` return void.

Values preserve bigint and number distinctly, including nested fields. Reserved
metadata is read-only. Version/timestamps are optional and reflect the last
source observation, not the revision of an unsent local edit; document versions
may reset after recreation. Narrow `deleted` before reading business fields.

Local storage permits broader logical IDs than HTTP Push. IDs intended for
upload must match ASCII `[A-Za-z0-9_.-]{1,64}`; the adapter rejects other IDs
before dispatch and does not remap them. Queries preserve exact numeric types,
UTF-8 order, missing/null distinction and logical-ID ties under the
[filter contract](filters.md). Resource failure ends the affected query instead
of publishing truncated results.

#### Replica 数据运输

每个已打开的 replica database 句柄至多拥有一条私有 WS，只供活动 leader alias
使用。相同 collection 的 aliases 仍是独立 source/subId/进度；follower 不建立数据
订阅。最后一个活动租约释放后，连接与重连 timer 关闭。

WS 使用 [replica-data 协议](replication.md#replica-websocket-data)，真实 typed 数据页
通过 WS 返回，不是仅收通知后继续 HTTP Pull。默认每 10 秒的源核对仍保留：WS 健康
时核对也走 WS，以恢复遗漏通知和窗口索引滞后；应用不需要管理连接或选择运输。

| 情况 | 行为 |
|---|---|
| WS 建连、注册或 read 运输失败 | 有限等待后以相同已提交状态使用 HTTP；后台重连的延迟有上限并带 jitter |
| WS 页面已交给本地应用 | 完成该页后下一次 read 再换运输；socket 失效不释放仍在应用的页额度 |
| HTTP 期间 WS 恢复 | 后台仅连接/认证，当前 HTTP read 退出、整页完成后才切回 |
| 源限流 / REPLICATION_SOURCE_BUSY | 保留 retryAfter 和 source retryAt，不用 HTTP、changed 或重连绕过退避 |
| Query/Store 的 REPLICATION_UNAVAILABLE、DEADLINE_EXCEEDED 或索引不可用 | 保留源错误与既有源退避，不立即改 HTTP 重试 |
| 权限、身份、非法源、本地持久化错误 | 保留既有阻塞/恢复处理，不用运输切换掩盖 |

两种运输共用同一个下行应用器和四页池；同 alias 最多一页，等待可取消且最长 45 秒。
额度等待超时以 `REPLICA_PAGE_CAPACITY` 进入退避，不提前释放旧页。页面额度
在整页持久化或 owner 清理完成后释放，fallback 不新增一套额度。WS ACK 只在最后
本地分块的文档/assumed/checkpoint、必要 manifest 激活和 pin 处理完成后发送。
ACK 不等待 token 或重连，发送失败不撤销已完成提交，也不改变进行中的 Push 结果。

私有 WS 连接/认证及单次注册各限 10 秒；WS read 与 HTTP fallback read 各限 45 秒。
WS 重连的退避基数从 1 秒增加至 30 秒并带 jitter，只要租约有效就继续尝试；没有旧公开
WS 的五次上限。后台 probe 不提前读取数据。没有新的公共 transport/deadline 选项。

首次绑定只来自共同 decoder 验证的源页。所有运输保留固定 expected database identity，
重连使用原有持久 cursor；窗口继续整页 replacement。requestId、subId、运输代和
ACK 都不进入存储 checkpoint。pause、维护、移除和账号切换使旧租约/迟到消息失效。

#### Synchronization, status and recovery

`replica.sync.pause(alias?)` cancels/drains synchronization while preserving local
CRUD/watch. `resume(alias?)` restarts runnable aliases; omission selects all
currently configured aliases. Unresolved conflict/uncertain state rejects resume.
Pause/resume does not authorize repeating a possibly committed mutation.

When `navigator.onLine === false`, a new Push is refused locally before dispatch
with retryable `OFFLINE`; whole-phase retry rules still apply. Going offline
after dispatch does not prove nonexecution: a possibly committed write remains
uncertain and is not automatically resent.

`sync.subscribe(onStatus, onError?)` immediately supplies a snapshot and returns
unsubscribe. Each alias reports:

| Fields | Meaning |
|---|---|
| `mode`, `state`, `leader` | Source mode `events`/`replace`; state `waiting`, `syncing`, `idle`, `retrying`, `paused`, `blocked` or `closed`; current network ownership |
| `ready`, `sourceReady` | Durable source initialization/activation, independent of native idle |
| `generation`, `physicalEpoch`, `checkpoint` | Activated source generation, local physical storage generation, and opaque events cursor; windows have no event cursor |
| `lastCompleteRound`, `pending`, `pins` | Last durably completed source round and local work/protection counts |
| `issues`, `error?`, `retryAt?` | Current issue identifiers/codes, original failure and scheduled retry time |

Snapshots may be coalesced; follower readiness comes from durable shared state.
A source-ready alias can still be offline, paused or blocked. Multiple tabs share
one elected network owner per alias; all can use local CRUD/watch. A replacement
leader checks unfinished phases before dispatching writes.

`sync.inspect<T>(alias, {id?, readCurrent?})` defaults to offline inspection.
It returns `issueId`, `stateToken`, `physicalEpoch`, bounded issue/target metadata
and, when `id` is supplied, one desired/assumed document pair and
`editToken: string | null`. Explicit `readCurrent: true` requires an ID and
bound identity. It performs a guarded authoritative read outside the alias lock
and then rechecks identity, issue and local state.

| Inspection field | Meaning |
|---|---|
| `phase` | Null, or `{id, state: 'prepared' \| 'dispatched'}` from the durable phase marker; dispatched records possible dispatch, not proof of remote execution |
| `recovering` | An unfinished durable recovery intent is present |
| `availableActions` | Advisory `{kind, issueId}` choices from the same snapshot; resolve revalidates all guards |

No issue means no recovery action. Explicit retry is not offered to bypass an
unresolved content conflict; adopt/merge or reset must address that protected work.

An omitted `document.current` is unknown. A returned observation has
`source: 'authoritative-read'` and `state`: `existence: 'absent'` with
`document: null` means confirmed missing; `deleted` and `live` remain distinct.
This is a current observation, not proof of whether an earlier request executed.

Resolve requires the elected owner, drains replication and remains paused until
explicit resume. Adopt/merge independently reread current; an old issue, token or
physical epoch fails without overwriting later edits.

```typescript
import type { ReplicaDatabase } from '@syntrix/client';

export const adoptInspectedServerState = async (
  replica: ReplicaDatabase,
  alias: string,
  id: string,
) => {
  await replica.sync.pause(alias);
  const inspected = await replica.sync.inspect(alias, { id, readCurrent: true });
  const action = inspected.availableActions.find(value => value.kind === 'adopt-server');
  if (action === undefined || inspected.document === undefined) {
    throw new Error('Server adoption is not available for this document');
  }

  // Invoke this function only after the application authorizes server adoption.
  await replica.sync.resolve(alias, {
    kind: 'adopt-server',
    issueId: action.issueId,
    id,
    editToken: inspected.document.editToken,
    physicalEpoch: inspected.physicalEpoch,
  });
  await replica.sync.resume(alias);
};
```

| Recovery decision | Required fields and effect |
|---|---|
| `adopt-server` | `issueId, id, editToken, physicalEpoch`; adopt actual current, including absence |
| `merge-local` | Same guards plus typed `data`; current becomes assumed, chosen data becomes a new local edit |
| `retry-uncertain` | `issueId, acknowledgeRepeatedEffects: true`; explicitly permit repeated effects |
| `reset-alias` | `issueId, stateToken, discardPending: true`; discard inspected local state and rebuild the same source/binding |

An unknown create may have succeeded and then been deleted; retry can recreate
that ID. Recovery intents protect partial fork/assumed writes across reopen.
Adopt/merge leave other IDs, membership and ordinary checkpoints unchanged.
Reset rejects an inspection made before any later local edit.

#### Close and remove

`replica.close()` stops admission/callbacks, cancels and drains owned work, and
preserves persisted data. It attempts all cleanup and reports failures; closing
one handle does not close other database handles in the same account. Clearing
browser site data removes that account's local state independently of the SDK.

`replica.removeCollection(alias)` removes local data only. It also accepts a
persisted alias omitted from this open's `collections`; its original identity
selects the store without needing the old source builder. A missing historical
alias is a no-op. Pending changes, pins, issues or unfinished phases/intents
reject removal; a failed attempt leaves the configured alias paused for explicit
handling. Removal never deletes remote documents.

An empty configuration opens only the named local database handle, so historical
aliases can be removed without creating or starting another source:

```typescript
import type { SyntrixClient } from '@syntrix/client';

export const removeHistoricalAlias = async (
  client: SyntrixClient,
  name: string,
  alias: string,
) => {
  const replica = await client.openReplica({ name, collections: {} });
  try {
    await replica.removeCollection(alias);
  } finally {
    await replica.close();
  }
};
```

Removal persists a terminal lifetime fence before deleting paired physical
stores. A cleanup failure retains retry/reopen information. Recreating the alias
uses a new lifetime and physical epoch; stale handles cannot write into it, even
after missed notifications. Clean compaction retains the alias lifetime.
Close cancels queued removal work; once terminal removal is durable, owned
physical cleanup finishes before its cancellation is reported.

#### Configuration and budgets

`OpenReplicaOptions` requires nonempty `name` and a `collections` object, which
may be empty for historical-only removal. Optional `storageLimits`, `queryLimits`, `sync` and `onDiagnostic`
are frozen at open. Numeric options are positive safe integers; unknown options
fail validation.

| Option | Default |
|---|---:|
| `sync.pollIntervalMs` | 10,000 |
| `sync.hintDelayMs` | 200 |
| `sync.retryBaseMs` / `retryMaxMs` | 1,000 / 30,000, with jitter; base must not exceed maximum |
| `sync.maintenanceBackoffMs` | 30,000 |
| `storageLimits.maxRecordBytes` / `maxMetadataBytes` | 16 MiB / 17 MiB; metadata must exceed record budget |
| `storageLimits.maxManifestBytes` | 34 MiB |
| `storageLimits.maxKnownIds` | 100,000 per alias |
| `storageLimits.maxStoredBytes` | `Number.MAX_SAFE_INTEGER`; browser quota still applies |
| `queryLimits.readBytes` / `payloadBytes` / `keyBytes` | 64 MiB / 128 MiB / 64 MiB |
| `queryLimits.nodes` / `scanCandidates` | 1,000,000 / 100,000 |
| `queryLimits.scanBytes` | 128 MiB |
| `queryLimits.queuedKeys` / `queuedBytes` | 100,000 / 16 MiB |
| `queryLimits.outputBytes` / `rebuildAttempts` | 16 MiB / 8 |

Query budgets are shared by handles of the same replica database in an execution
context. Retained candidates outside a limited window still count. Lowering a
limit never authorizes dropping pending work. Limits describe encoded records
and controlled materialization, not total browser heap or power-loss durability.

`scanCandidates` / `scanBytes` 限制一个有效视图的扫描；废弃重建尝试的累计工作量
单独决定何时让出调度。`rebuildAttempts` 是单轮尝试上限：长 watch 遇到视图竞争会以
25–200ms 退让保留订阅，稳定后继续；get/getPage 仍有界失败。真实保留内存、扫描或
输出超限仍终止对应查询。正常发送阶段和分页进度更新不会单独改变查询可见性身份。

Transport bounds remain independent: source pages use 100 events; windows allow
at most 1000 documents; native delivery uses at most 201 rows/16 MiB; Push uses
50 changes, 10 MiB HTTP and 20 MiB protobuf request budgets, plus a 32 MiB client
response cap. Upstream dispatch concurrency is one per alias; conditional writes
have at most three CAS attempts, local writes eight. See the
[replication design](../design/sdk/002_replication_client.md) for persistence and
recovery boundaries.

#### Errors and diagnostics

Method failures reject with the original error or a preserved `cause`. Query
watch and status subscriptions expose `onError`; synchronization failures also
appear in alias status. Uncertain mutation results are not ordinary read retries.

| Error/code | Meaning and handling |
|---|---|
| `ReplicaUnsupportedEnvironment` | Required browser capabilities are unavailable |
| `OFFLINE` | Browser reported offline before Push dispatch; retry follows whole-phase safety rules |
| `REPLICA_PAGE_CAPACITY` | 共用页额度等待超过 45 秒；保留仍在应用的页并按退避重试 |
| `AUTH_SESSION_CHANGED`, `ReplicaScopeChanged` | Old account/database ownership ended; retain its pending data in the original namespace |
| `ReplicaSourceMismatch` | Reopened alias definition differs; use a new alias or clean remove/recreate |
| `ReplicaWriteConflict`, `ReplicaUpstreamUncertain`, `ReplicaRecoveryRequired` | Inspect and explicitly resolve before resume |
| `ReplicaRecoveryStale` | Issue, token, epoch or inspected state changed; obtain a new inspection |
| `ReplicaStorageLimit`, `ReplicaRecordTooLarge`, `ReplicaReadBudgetExceeded`, `QueryBudgetExceeded` | Budget/storage admission failed; pending state is preserved |
| `ReplicaRemovalBlocked` | Resolve protected work or finish synchronization before removal |
| `ReplicaAliasUnknown`, `ReplicaAliasRemoved`, `ReplicaRemoved`, `ReplicaDatabaseClosed` | Reference no longer identifies an open usable alias/handle |
| `TypeError`, `RangeError` | Invalid public configuration, query or write input |

`onDiagnostic` receives `replicaId` (one facade-open lifetime), `operationId`,
`sessionVersion`, `alias`, `operation`, `phase` and `timestamp`. Optional fields
are `physicalEpoch`, `durationMs`, `count`, `requestId`, `subId`, `transportEpoch`,
`mode: 'ws' | 'http'` and an allowlisted `code`.
Operation IDs correlate start/completion/failure and watch lifetimes; Pull events
include the observed request ID and returned event/document count when available.
These are local correlations, not a distributed server tracing guarantee.
查询竞争通过 `operation: query` 的 `contended` / `recovered` 阶段报告，同一竞争阶段
共享 operationId。固定分类为 `ReplicaQueryViewContention` 或 `ReplicaQueryWorkContention`；
它们是等待/恢复诊断，不代表 watch 已失败，不包含查询条件或文档 ID。
Diagnostics exclude credentials, payloads, filter values and raw error objects.
`operation: 'transport'` 的 connect/subscribed/read/accepted/committed/fallback/
recovered/closed 阶段使用 alias、requestId、subId 和 transportEpoch 关联。accepted
表示合法页已接纳；committed 按实际 receipt 的 WS/HTTP mode 报告本地整页完成并释放额度，
不保证远端收到 WS ACK，也不是上行确认。HTTP 不发送 WS ACK。
The callback does not introduce a telemetry service.
It runs synchronously and may close the database, unsubscribe or change accounts.
Ownership and subscription checks run again afterward: obsolete successful
operations reject, and inactive watches receive neither results nor errors.
Thrown diagnostic exceptions are isolated; returned asynchronous work is not
awaited by the SDK.

私有 WS 已使用与查询源匹配的授权数据协议。周期核对仍支付 Query 读取成本；运输
改变不保证固定同步延迟。Local `watch` 继续只消费已应用的本地状态。

### Authentication sessions

`login(username, password)` and `signup(username, password)` immediately end the
current local session before making their request. The last session operation
started owns its result. Successful authentication installs the new credential
pair; failure leaves the client logged out. A superseded operation rejects with
`AuthSessionChangedError` and cannot overwrite the current credentials.

`logout()` immediately clears local credentials, then uses the saved old refresh
token with the existing server logout endpoint. Remote failure rejects the promise
while the client remains locally logged out. A late logout response cannot clear
a subsequent login. Server token expiration and revocation rules still apply;
local logout does not immediately revoke every issued access or derived refresh
token.

登录、注册和退出还会失效旧 replica 会话及其私有运输租约，并在等待认证请求前断开
客户端缓存的 SSE。新会话重新打开副本或连接 SSE；独立创建的 SSE client 仍需显式清理。

#### Token providers

The `TokenProvider` interface requires synchronous `getSessionVersion(): number`
alongside `getToken(): Promise<string | null>`, `refreshToken(): Promise<string>`,
`setToken(token)`, and `setRefreshToken(token)`. A session version is local to one
provider, changes on explicit session replacement, and must not be reused for a
later session. Normal token rotation retains the version. Custom providers must
also prevent obsolete operations from mutating credentials or firing hooks.

For the default provider:

| Operation | Behavior |
|---|---|
| `setToken(access)` | Advance version, replace access token, and clear old refresh token |
| `setRefreshToken(refresh)` | Advance version and attach refresh token to current access credentials |
| Install a complete pair through setters | Call `setToken()` before `setRefreshToken()` |
| Refresh-token-only initial configuration | Refresh may obtain an access token within the same session |
| Concurrent refresh | Share one operation per session; rotation preserves the session version |
| Login/signup | Advance once when clearing credentials and again when installing a successful pair |

The second login version change prevents requests admitted while login was pending
from retrying under the newly installed account. Obsolete refresh results neither
write credentials nor emit `onTokenRefresh`/`onAuthError`. If a hook synchronously
changes sessions, refresh waiters reject rather than receiving a stale token.

When private replica storage is attached, refresh also checks JWT subject before
installing credentials. Same-subject rotation retains its offline namespace;
subject replacement invalidates old storage admission and drains owned work.
This includes storage still opening when refresh completes. Expected cancellation
of old native work does not prevent account replacement after that work drains;
real storage and cleanup failures remain observable and prevent credential
installation. The provider carries this ownership across the separately loaded
replica bundle.

#### Requests and errors

`AuthSessionChangedError` is exported by the package. It extends `Error`, has
`code === 'AUTH_SESSION_CHANGED'`, and has no HTTP status. It means the operation's
local session changed; applications should stop that old operation rather than
retrying it under a new identity.

HTTP requests capture their session synchronously when the Axios request is
constructed, before asynchronous interceptors run.
Token waits and 401/403 refresh/retry preserve and check that version. An old request
cannot automatically refresh or retry using a new account. Missing access tokens
remove any existing Authorization header. Authentication retry remains limited to
one attempt. A request's `AbortSignal` is checked before credential waits and can
interrupt those waits, including a wait for shared refresh. Canceling one waiter
does not cancel the shared refresh or release the provider's credential barrier.

SDK methods may capture ownership earlier; generic HTTP ownership does not cancel
an already dispatched request, filter a successful old response, or undo server
effects. A dispatched request may still finish using its old token. See the
[authentication design](../design/sdk/003_authentication.md) for provider and
transport ownership.

## 2. TriggerClient (Internal)

Use only within Syntrix Trigger Workers. It requires the `preIssuedToken` from the webhook payload.

```typescript
import { TriggerHandler, WebhookPayload } from '@syntrix/client';

const payload = req.body as WebhookPayload;
const handler = new TriggerHandler(payload, process.env.SYNTRIX_API_URL);
const client = handler.syntrix; // TriggerClient
```

### Exclusive Methods

#### `batch(writes: WriteOp[]): Promise<void>`

Performs an atomic batch of write operations.

```typescript
await client.batch([
  { type: 'create', path: 'users/123', data: { name: 'Alice' } },
  { type: 'update', path: 'stats/daily', data: { count: 1 } },
]);
```

## 3. Fluent API (Shared)

Both clients return `DocumentReference` and `CollectionReference` objects with the same API.

### DocumentReference `<T>`

- **`get(): Promise<T | null>`** — fetch the document.
- **`set(data: T): Promise<T>`** — overwrite the document.
- **`update(data: Partial<T>): Promise<T>`** — partial update.
- **`delete(): Promise<void>`** — logically delete the document; see [tombstone semantics](api.md#delete-document).
- **`collection(path: string): CollectionReference`** — sub-collection reference.

### CollectionReference `<T>`

- **`doc(id: string): DocumentReference<T>`** — reference by ID.
- **`add(data: T, id?: string): Promise<DocumentReference<T>>`** — create (auto-ID for standard client).
- **`get(): Promise<T[]>`** — list documents.
- **`where(field, op, value): QueryBuilder<T>`** — start a query.
- **`orderBy(field, direction): QueryBuilder<T>`** — sort.
- **`limit(n: number): QueryBuilder<T>`** — limit results.

### Query Pages

Both clients expose the same query builder and page contract:

```typescript
interface QueryPage<T> {
  documents: T[];
  nextCursor: string | null;
  effectiveOrder: { field: string; direction: 'asc' | 'desc' }[];
}

const query = client.collection('messages')
  .where('version', '>=', 9007199254740993n)
  .orderBy('version', 'asc')
  .limit(20);

let page = await query.getPage();
for (;;) {
  console.log(page.documents);
  if (page.nextCursor === null) break;
  page = await query.startAfter(page.nextCursor).getPage();
}
```

- `getPage()` returns one decoded page and its continuation.
- `get()` returns only that page's documents; it does not traverse subsequent
  pages. Query `update()` and `delete()` also act only on the selected page, using
  per-document writes. They are not an atomic query-wide mutation.
- `where()` supports `==`, `!=`, `>`, `>=`, `<`, `<=`, `in`, and `contains` under
  the [query filter contract](filters.md). Queries need a compatible complete
  index plan except for the documented direct Store routes.
- Pass the opaque `nextCursor` to `startAfter()` while keeping query scope and
  ordering unchanged. A non-null cursor may lead to an empty terminal page.
- SDK `bigint` values encode as signed int64; SDK `number` values encode as finite
  binary64. All decoded int64 fields become `bigint`, including metadata and
  nested values; float64 fields remain `number`. Already-rounded JavaScript
  numbers cannot recover lost integer precision.
- Query transport uses [typed values](filters.md#typed-values). Document CRUD,
  conditional writes, and Trigger writes retain ordinary JSON bodies; query
  decoding does not add bigint support to those write methods.
- Malformed pages, legacy array responses, invalid typed values, and execution
  errors reject. A stale cursor requires restarting the query. See
  [query errors](api.md#query-errors) and [consistency](api.md#continuation).

Trigger collection queries use `/trigger/v1/databases/{database}/query` with the
same page and value codec as standard queries. This query contract does not
change conditional-write or realtime filter execution.

## 4. Realtime

复制 WebSocket 由每个 `ReplicaDatabase` 句柄私有管理。应用使用
`openReplica()`、本地 `watch()` 和 `replica.sync.subscribe()`；公开
`client.realtime()`、`client.subscribe()`、`RealtimeClient`、`RealtimeListener`
以及原 WS 配置/消息协议类型已移除，不提供兼容包装。
`client.pull()` 仍是独立的单页手动 HTTP API。

`realtimeSSE()`、`RealtimeSSEClient`、`RealtimeSSEOptions` 和 SSE 使用的
`RealtimeCallbacks`、`RealtimeEvent`、`SnapshotEvent`、`ConnectionState`
继续公开。普通 SSE 不成为副本的数据应用路径。

### Server-Sent Events (SSE)

```typescript
const sse = client.realtimeSSE();
await sse.connect(
  {
    onEvent: (evt) => console.log(evt),
    onSnapshot: (snap) => console.log(snap),
  },
  { collection: 'users' }
);
```

Notes:

- SSE authentication is sent via Authorization header (sourced from the SDK token provider); query-string tokens are rejected.
- `disconnect()` invalidates pending token acquisition and stops the active fetch.
  For an established connection in the same session, it emits `onDisconnect`
  exactly once before abort listeners can create a replacement. Pending or
  obsolete-session connections do not emit this explicit-disconnect notification.
  Authentication, response, and read callbacks check both controller ownership and
  session version. Stale cleanup cannot clear a new connection, and stopped reads
  cannot deliver buffered events afterward.
- SSE does not automatically reconnect across session changes. Independently
  constructed clients remain responsible for explicit disconnect; session checks
  guard subsequent asynchronous work, not immediate remote token revocation.
