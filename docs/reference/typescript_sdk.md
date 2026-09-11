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

Login, signup, and logout also clear this client's cached realtime references,
dispose its old WebSocket client, and disconnect its old SSE client before waiting
for the authentication request. Create subscriptions again for a new session.
Independently constructed realtime clients require explicit owner cleanup.

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

#### Requests and errors

`AuthSessionChangedError` is exported by the package. It extends `Error`, has
`code === 'AUTH_SESSION_CHANGED'`, and has no HTTP status. It means the operation's
local session changed; applications should stop that old operation rather than
retrying it under a new identity.

HTTP requests capture their session at first authentication-interceptor admission.
Token waits and 401/403 refresh/retry preserve and check that version. An old request
cannot automatically refresh or retry using a new account. Missing access tokens
remove any existing Authorization header. Authentication retry remains limited to
one attempt.

This does not bind ownership at the instant an SDK method is called, cancel an
already admitted request, filter a successful old response, or undo server effects.
An admitted request may still send or finish using its old token. See the
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

## 4. Realtime (WS & SSE)

### WebSocket (default)

The convenience API starts or reuses one authenticated WebSocket per client.
Each subscription owns its callbacks:

```typescript
const subscription = client.subscribe('users', {
  onReady: () => schedulePull(),
  onEvent: (event) => console.log(event),
  onSnapshot: (snapshot) => console.log(snapshot),
  onError: (error) => console.error(error),
});

subscription.unsubscribe();
```

`onReady` runs once after initial registration and again after each reconnect's
registration ACK. It signals registration success, not historical delivery or
snapshot completion. Applications can use it to schedule their own reconciliation.
Events and snapshots may arrive before the ACK. Unsubscribing removes only that
subscription, including its callbacks; repeated unsubscribe calls are harmless.

The low-level API makes connection initiation explicit:

```typescript
const rt = client.realtime();
rt.on('onError', (error) => console.error(error));
const subId = rt.subscribe(
  { query: { collection: 'users' } },
  { onEvent: (event) => console.log(event) },
);
await rt.connect();

rt.unsubscribe(subId);
rt.disconnect();
```

Concurrent `connect()` calls share one attempt. The promise resolves only after
`auth_ack`; subscriptions wait locally until authentication succeeds. WebSocket
authentication sends an `auth` message containing the token and database.

| API or event | Behavior |
|---|---|
| `rt.subscribe(options, callbacks?)` | Return a `subId` synchronously; register immediately if authenticated, otherwise retain locally |
| `rt.on(name, callback)` | Set one global observer for that event; subscription callbacks remain independent |
| Event or snapshot | Route by `subId` to its active subscription and the global observer; ignore messages for removed subscriptions |
| Registration error | Notify the matching subscription and global error observer; retain the subscription for later reconnect |
| Connection or authentication failure | Notify active subscriptions and the global error observer once for that failed attempt |
| Last unsubscribe | Leave the client-owned connection open |
| `disconnect()` | Stop socket and timers, reject a pending connection, retain subscriptions and callbacks for explicit reconnect |
| `dispose()` | Stop transport work and permanently clear subscriptions and observers; reuse is an error |
| `client.login()`, `client.signup()`, `client.logout()` | Invalidate the old local session and dispose the cached WebSocket and disconnect cached SSE before awaiting authentication |

Disconnect and disposal are repeatable. Late socket messages, authentication
results, and reconnect timers cannot revive a stopped connection. Synchronous
callback exceptions are reported separately from message parsing to the global
error observer (or logged if absent) and do not interrupt other eligible callbacks.
An exception in an error callback is logged. Callbacks returning promises must
handle their own asynchronous failures.

`RealtimeClientOptions.activityTimeoutMs` defaults to 90,000 ms. It bounds the
whole connection/authentication attempt even if heartbeats arrive, and detects
inactivity after authentication. Reconnect uses exponential backoff with jitter
(`reconnectDelayMs` default 1,000; `maxReconnectAttempts` default 5); authentication
success resets the attempt count. Only a structured `unauthorized` error matching
the current auth request triggers one token refresh. Invalid authentication,
missing tokens, refresh errors, and a repeated auth rejection fail the attempt.

Each connection attempt also belongs to an authentication session. Token waits and
`auth_ack` validate that session. Automatic reconnect retains its original session
and stops if credentials are replaced; explicit `connect()` may end an obsolete
attempt and start under the current session. Disposal need not cancel HTTP
refresh requests: provider checks prevent obsolete credential writes and callbacks.
See [authentication sessions](#authentication-sessions) for the shared contract.

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
