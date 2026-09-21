# Syntrix TypeScript Client

## Installation

```bash
bun add @syntrix/client
```

## Building from source

Use pnpm 10.34.5 to apply the pinned dependency patch, and Bun to build and test:

```bash
pnpm install --frozen-lockfile --ignore-scripts
bun run build
bun test --coverage
bun run test:package
```

The build checks the patch and installed vendor files, then bundles the private
runtime with third-party licenses. The package test installs a tarball in an
isolated consumer and checks failure recovery without vendor dependencies.
Applications installing the published SDK can use their usual package manager.
Storage tests cover raw CAS, row budgets, identity lifetime, and native-seeded
compaction. Browser multi-tab checks complement these tests; they do not establish
power-loss durability or complete server synchronization.

## Usage

### SyntrixClient

```typescript
import { SyntrixClient } from '@syntrix/client';

const client = new SyntrixClient('http://localhost:8080', {
  token: 'my-token',
  refreshUrl: 'http://localhost:8080/auth/refresh',
  refreshToken: 'my-refresh-token'
});

const doc = await client.collection('users').doc('123').get();
```

### TriggerClient

```typescript
import { TriggerClient } from '@syntrix/client';

const client = new TriggerClient('http://localhost:8080', 'pre-issued-token');

await client.collection('users').doc('123').set({ name: 'Alice' });
```

## Query Pages

```typescript
const query = client.collection('messages')
  .where('version', '>=', 9007199254740993n)
  .orderBy('version', 'asc')
  .limit(20);

const page = await query.getPage();
console.log(page.documents, page.effectiveOrder);
if (page.nextCursor !== null) {
  const next = await query.startAfter(page.nextCursor).getPage();
  console.log(next.documents);
}
```

`getPage()` returns `{ documents, nextCursor, effectiveOrder }`. `get()` returns
only the selected page's documents; query `update()` and `delete()` likewise
operate on one page. Standard and Trigger clients use the same query contract.
Queries require a compatible index plan except for unordered listing and ID-only
lookups. See the [filter guide](../../docs/reference/filters.md).

Query values preserve int64 as `bigint` and finite binary64 as `number`, including
nested document fields and metadata. Query pages replace legacy array responses.
CRUD and conditional-write methods still use ordinary JSON bodies. Cursors are
opaque; they do not establish a snapshot across pages. See
[query pages](../../docs/reference/typescript_sdk.md#query-pages) for continuation,
errors, and numeric limits.

## SSE 与身份

复制数据连接由 `openReplica()` 内部管理。公开 `client.realtime()`、
`client.subscribe()`、WS 构造器及其协议类型已移除；本地 `watch()` 和
`replica.sync.subscribe()` 保留。现有 SSE 入口继续用于普通服务端事件：

```typescript
import type { SyntrixClient } from '@syntrix/client';

export const observeUsers = (client: SyntrixClient) => {
  const sse = client.realtimeSSE();
  void sse.connect({
    onEvent: event => console.log(event),
    onSnapshot: snapshot => console.log(snapshot),
    onError: console.error,
  }, { collection: 'users' }).catch(console.error);
  return () => sse.disconnect();
};
```

SSE 使用 Authorization header，不把 token 放入 URL。它不自动把事件写入本地副本，
也不提供复制页的 checkpoint 或整页完成语义。登录、注册和退出会使旧认证会话失效，
关闭其 replica 运输租约并断开缓存的 SSE；独立创建的 SSE client 仍由调用方清理。

认证工作与自动重试保留原会话；旧结果以 `AUTH_SESSION_CHANGED` 拒绝，不能恢复旧凭据
或借新账号重试。自定义 TokenProvider 需要同步的 `getSessionVersion()` 和受保护的凭据
变更。具体边界见[认证参考](../../docs/reference/typescript_sdk.md#authentication-sessions)
和 [SSE 参考](../../docs/reference/typescript_sdk.md#server-sent-events-sse)。

## Manual Replication Pull

```typescript
const page = await client.pull<{ name: string }>('users', {
  checkpoint: savedCheckpoint, // string | null; null starts bootstrap
  limit: 100,
  signal: abortController.signal,
});

// This transaction belongs to the application's local database.
await localDatabase.transaction(async tx => {
  for (const document of page.documents) {
    if (document.deleted) await tx.removeServerDocument(document.id);
    else await tx.replaceServerDocument(document.id, document);
  }
  await tx.saveCheckpoint(page.checkpoint);
});
```

`pull<T>(collection, options?)` performs one authenticated POST to
`/replication/v1/databases/{database}/pull`, using the client's configured database
and retaining any base URL prefix. `limit` defaults to 100 and accepts integers
from 1 through 1000. Omit `checkpoint` or pass `null` to start bootstrap; retain
returned checkpoints verbatim and reuse them only for the same database,
collection, and account. `AbortSignal` cancels the request.

Bootstrap scans committed documents and then replays Store Watch from the
original scan boundary. Later pages continue Watch progress. This overlap can
repeat document states; it does not provide a fixed snapshot across pages.

Pull requires full database access through a matching `db_admin` grant for the
requested database's canonical ID or validated slug, or the existing database-owner
grant. An `admin` or `user` role alone does not authorize Pull.

Documents contain flattened business fields plus `id`, `collection`, `version`,
`createdAt`, and `updatedAt`; int64 values decode as `bigint`, including nested
business fields. Logical deletions have `deleted: true`. A minimal deletion can
contain only `id`, `collection`, and `deleted`; unknown metadata remains absent.
Apply pages in order and tolerate duplicate states. Document versions are not a
global replication order and may reset after deletion and recreation.

Decoded bigint values need a lossless local storage representation. Plain
`JSON.stringify` and existing SDK document `set`/`update` serialization reject
bigint, so a pulled document cannot be blindly saved as JSON or sent back through
those methods. Converting to `Number` can lose precision.
[HTTP Push](../../docs/reference/replication.md#push-changes) accepts typed document
objects. The SDK's private upstream replication adapter preserves this encoding;
the complete Pull response is not a Push request. Ordinary CRUD formats remain
unchanged.

The caller must atomically apply the entire page and persist its checkpoint.
An empty page can advance progress while `caughtUp` remains false; continue using
its checkpoint. `caughtUp: true` means the source proved a processed watermark,
not that no write can occur after that watermark. The SDK does not automatically
fetch subsequent pages or save progress.

Transport failures, cancellation, malformed responses, and failed local
transactions leave the caller's saved checkpoint unchanged. `RESYNC_REQUIRED`
requires rebuilding the server mirror from a null checkpoint while preserving
unsent local edits for reconciliation. Keep mirrors and checkpoints isolated per
account. Login, signup, or logout during a Pull invalidates a successful old-session
response with `AuthSessionChangedError` (`AUTH_SESSION_CHANGED`); account changes
after the response returns must also invalidate the application's pending local
apply operation. See the [replication reference](../../docs/reference/replication.md).

## Replica Database

使用 `openReplica` 获得本地持久 CRUD/query/watch；正常下行通过私有 WS 数据页，
不可用时自动 HTTP fallback，上行继续 HTTP Push。Existing `client.collection()` references keep direct REST behavior.
Open returns when local storage is ready, without waiting for network convergence.
The [replica demo](../../example/realtime-demo/README.md) runs two independent
browser replicas with local query watch, offline writes and synchronization controls.

```typescript
import { SyntrixClient } from '@syntrix/client';

type Task = { title: string; status: 'open' | 'closed'; estimate: bigint };

export const openTaskReplica = async (endpoint: string, token: string) => {
  const client = new SyntrixClient(endpoint, {
    database: 'app',
    auth: { token },
  });
  const replica = await client.openReplica({
    name: 'task-cache',
    collections: { tasks: client.replicate<Task>('tasks') },
    onDiagnostic: event => console.log(event.operationId, event.alias, event.phase),
  });
  const tasks = replica.collection<Task>('tasks');
  const task = await tasks.add({ title: 'Review', status: 'open', estimate: 2n });
  await task.update({ status: 'closed' });
  const stopWatch = tasks.where('status', '==', 'open').limit(20)
    .watch(documents => console.log(documents), console.error);
  const stopStatus = replica.sync.subscribe(status => {
    console.log(status.aliases.tasks?.state);
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

Run in a browser with IndexedDB, Web Locks and Web Crypto, supplying an endpoint
and JWT with a nonempty subject. An expired token permits offline local open;
network synchronization still requires valid source authorization. Account changes
invalidate old handles. Local writes resolve after persistence and remain usable
offline or while synchronization is paused. A known-offline Push is refused
before dispatch and can retry under whole-phase rules; going offline after a
request was dispatched does not make an uncertain result safe to resend.
Bigint values remain lossless.

Sources are immutable and client-owned. Two aliases retain independent state,
even for the same source. Source `limit(1..1000)` selects a complete remote window;
local query limits select a local view. Omitting an old alias preserves its data.
`removeCollection(alias)` explicitly removes safe local state, including an alias
omitted from the current configuration; pending or protected work blocks removal.
Closing preserves data, and removal does not delete remote documents.
`openReplica({ name: 'task-cache', collections: {} })` supports historical-only
removal without starting any source.

`sync.pause/resume`, `inspect` and explicit `resolve` expose conflict and uncertain
result handling. Ordinary resume does not authorize repeating an unknown write.
Inspection reports the durable phase, any recovery intent and advisory available
actions; a dispatched marker does not prove remote execution. Content conflicts
cannot be bypassed by blind uncertain retry.
同一 replica 句柄最多共享一条私有 WS，只有活动 leader alias 建立数据订阅。
默认 10 秒源核对和 changed 都使用当前运输；WS 正常时不会再发 HTTP Pull。
断线后已接纳页面仍完成原有本地应用，整页提交后才 ACK/切换；WS 与 HTTP 共用
四页额度池。源退避、身份和本地持久化错误不能被 fallback 绕过。Local watch remains available independently.

本地 watch 遇到正常视图竞争会有界退让并继续订阅；无关同步控制字段更新不会要求重建。
稳定查询的扫描、内存或输出真正超限仍会终止，单次 get/getPage 也保持有界失败。

The patched RxDB/Dexie/RxJS runtime loads lazily and is bundled with its licenses;
applications do not install vendor packages or receive RxDB objects. Manual Pull
retains application-owned page processing, and no public manual Push is added.
See the [replica reference](../../docs/reference/typescript_sdk.md#replica-availability)
for complete typed examples, recovery guards, configuration, limits and errors,
and the [replication design](../../docs/design/sdk/002_replication_client.md).
