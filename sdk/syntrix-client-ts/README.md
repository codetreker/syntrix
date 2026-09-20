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

## Realtime Subscriptions

```typescript
const client = new SyntrixClient('http://localhost:8080', {
  database: 'my-database',
  auth: { token: 'my-token' },
});

const subscription = client.subscribe('users', {
  onReady: () => schedulePull(),
  onEvent: (event) => console.log(event),
  onError: (error) => console.error(error),
});

subscription.unsubscribe();
client.realtime().dispose();
```

Convenience subscriptions share one automatically connected WebSocket and have
independent callbacks. `onReady` signals registration after authentication, both
initially and after reconnect; schedule reconciliation there when missed changes
must be fetched. Readiness does not mean historical data or a snapshot is complete.
After registration is acknowledged, `snapshot_failed` and `snapshot_limit` notify
`onError` while preserving the active subscription and subsequent live events.

The last unsubscribe leaves the connection open. Use `disconnect()` to stop it
while retaining subscriptions for explicit reconnect, or `dispose()` for permanent
cleanup. Login, signup, and logout invalidate the old local authentication session,
dispose the cached WebSocket, and disconnect cached SSE before awaiting the remote
operation. Failed replacement login leaves the client logged out; remote logout
failure rejects while local credentials remain cleared. Low-level
`realtime().subscribe()` requires an explicit `connect()`; its promise resolves
after authentication.

Authentication work and automatic retries remain bound to their original session.
HTTP requests capture that session before asynchronous interceptors run, and their
abort signals interrupt token and refresh waits during account replacement.
Obsolete operations reject with `AuthSessionChangedError` (`AUTH_SESSION_CHANGED`)
and cannot restore old credentials or retry under a new account. Custom
`TokenProvider` implementations must expose synchronous `getSessionVersion()` and
protect their credential mutations. See the
[authentication reference](../../docs/reference/typescript_sdk.md#authentication-sessions)
for setter ordering, ownership, and already admitted request limits, and the
[realtime reference](../../docs/reference/typescript_sdk.md#4-realtime-ws--sse)
for error routing and timeouts.

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

Use `openReplica` for local persisted CRUD/query/watch with automatic HTTP
replication. Existing `client.collection()` references keep direct REST behavior.
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
Replica synchronization uses authorized polling; automatic WebSocket hints await
matching query-source authorization. Local watch remains available independently.

The patched RxDB/Dexie/RxJS runtime loads lazily and is bundled with its licenses;
applications do not install vendor packages or receive RxDB objects. Manual Pull
retains application-owned page processing, and no public manual Push is added.
See the [replica reference](../../docs/reference/typescript_sdk.md#replica-availability)
for complete typed examples, recovery guards, configuration, limits and errors,
and the [replication design](../../docs/design/sdk/002_replication_client.md).
