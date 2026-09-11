# SyntrixClient Design

**Date:** December 22, 2025
**Status:** Planned
**Related:** [001_sdk_architecture.md](001_sdk_architecture.md), [003_authentication.md](003_authentication.md)

## Scope
Public HTTP client for application usage (web/mobile/backend) over `/api/v1/...` REST. Implements `StorageClient` to power the reference API (CollectionReference/DocumentReference/QueryBuilder) and is reused by replication.

## Responsibilities
- CRUD and query over REST.
- Token injection via shared auth surface (003); retry once on 401/403 if refresh is provided.
- Surface 404 as `null` for `get()`; propagate other HTTP errors.
- Provide reference API entry points `collection(path)` and `doc(path)`.
- Remain side-effect free for replication: replication workers can share auth but own their Axios instance.

## Non-Responsibilities
- Managing refresh token storage (caller responsibility).
- Trigger-specific batch writes (handled by TriggerClient).
- UI/session management.

## API Surface (current)
- `constructor(baseURL: string, token: string | TokenProvider)` (to be aligned with 003 for tokenProvider/refresh hooks).
- `collection<T>(path): CollectionReference<T>`
- `doc<T>(path): DocumentReference<T>`
- `get(path): Promise<T | null>`
- `create(collectionPath, data, id?): Promise<T>`
- `update(path, data): Promise<T>`
- `replace(path, data): Promise<T>`
- `delete(path): Promise<void>`
- `query(query: Query): Promise<T[]>` returns the selected page's documents
- `queryPage(query: Query): Promise<QueryPage<T>>` retains continuation and effective order

## Behavior Notes
- Base URL and auth headers are applied via Axios; content-type JSON.
- `get` treats 404 as `null`; other status codes propagate.
- `create` allows optional client-supplied `id`; payload merges if provided.
- All methods are promise-based; no retries beyond auth-refresh retry.

## Usage Examples
### Standard client
```typescript
const client = new SyntrixClient('https://api.synbase.tech', 'user-token');

// Fluent read
const user = await client.doc('users/alice').get();

// Fluent write
await client.collection('users/alice/posts').add({
	title: 'Hello World',
	createdAt: Date.now()
});
```

### Reference chaining
```typescript
const posts = await client
	.collection('posts')
	.where('status', '==', 'published')
	.orderBy('date', 'desc')
	.limit(10)
	.get();
```

## Query Values and Continuation

The query builder exposes `getPage()` and `startAfter()`; `get()` extracts one
page. Query updates/deletes also act on the selected page using individual writes.
Standard and Trigger query routes return typed documents, `nextCursor`, and
`effectiveOrder`. Int64 decodes to bigint and binary64 to number; ordinary CRUD/CAS
bodies remain JSON. Cursors bind query scope and index generation and do not create
a historical snapshot. See the [SDK query contract](../../reference/typescript_sdk.md#query-pages).

## Error Handling
- Network errors propagate; caller may wrap with their retry/backoff.
- Auth errors: follow 003 rules (single refresh + retry, then bubble). No checkpoint mutation here.

## Testing Plan
- `get` returns null on 404; throws on other errors.
- `create` with/without id forwards payload correctly.
- Auth: 401 triggers refresh once and retries; failure bubbles.
- Query maps to POST `/api/v1/databases/{database}/query` with typed filter values;
  malformed pages and legacy array responses reject.
- Reference API (`collection().doc().get/set/update/delete`) calls underlying methods correctly.
