# Replication API Reference

Replication synchronizes current document state within one database and concrete
collection. Pull uses recursively typed JSON documents; Push uses plain flattened
JSON. Both expose logical identities without storage keys, fullpaths, or parents.

## Pull Changes

**Endpoint:** `POST /replication/v1/databases/{database}/pull`

The database path accepts an ID or validated slug. Authorization uses the
resolved database entity; storage routing retains the exact database identifier
from the URL, matching existing CRUD/Push namespace behavior. Use the same
identifier for reads, writes, and Pull: an ID and slug are not interchangeable
data namespaces. Every request requires authentication and full database access:
database ownership or a matching `db_admin` grant for the canonical ID or
validated slug. A general role or permission to read individual documents is
insufficient. Possessing a checkpoint does not grant access.

### Request

```json
{
  "collection": "rooms/room-1/messages",
  "checkpoint": null,
  "limit": 100
}
```

| Field | Contract |
|---|---|
| `collection` | Required concrete collection path; no wildcards or filters |
| `checkpoint` | Omit or use null/empty string to bootstrap; otherwise reuse the returned opaque string verbatim |
| `limit` | Integer from 0 through 1,000; omitted or zero selects 100 |

Unknown or duplicate fields, multiple JSON values, and malformed Unicode are
rejected. The request body is limited to 1 MiB and the checkpoint to 256 KiB.
Timestamp checkpoints, including numeric strings such as `"100"`, require an
explicit new bootstrap; they are not converted to source positions. The former
GET Pull route is unsupported.

### Response

HTTP 200 returns a page with `documents`, a nonempty `checkpoint`, and `caughtUp`.
Every document is a recursively tagged value; this preserves nested int64 values
without floating-point conversion:

```json
{
  "documents": [
    {
      "type": "object",
      "value": {
        "id": { "type": "string", "value": "msg-2" },
        "text": { "type": "string", "value": "Offline message" },
        "version": { "type": "int64", "value": "2" },
        "updatedAt": { "type": "int64", "value": "1710000000000" },
        "createdAt": { "type": "int64", "value": "1700000000000" },
        "collection": { "type": "string", "value": "rooms/room-1/messages" }
      }
    }
  ],
  "checkpoint": "<opaque server-issued position>",
  "caughtUp": false
}
```

| Value domain | Encoding |
|---|---|
| Null | `{"type":"null"}` |
| Boolean or string | `{"type":"bool","value":true}` or `{"type":"string","value":"text"}` |
| Signed int64 | `{"type":"int64","value":"9223372036854775807"}` |
| Finite float64 | `{"type":"float64","value":1.5}` |
| Array | `{"type":"array","value":[...typed values...]}` |
| Object | `{"type":"object","value":{"field":...typed value...}}` |

After decoding, documents have flattened business fields plus logical metadata.
Live documents have `id`, `collection`, `version`, `createdAt`, and `updatedAt`.
Deleted documents have `deleted: true` and no former business data. A retained
tombstone includes its metadata; a deletion whose historical metadata is no
longer available can decode to just:

```json
{ "id": "msg-3", "collection": "rooms/room-1/messages", "deleted": true }
```

Missing version/timestamps on such a deletion are intentional. Physical cleanup
does not generate another business deletion. If a required logical identity
cannot be recovered, the request fails with `RESYNC_REQUIRED`.

### Progress and recovery

```text
No checkpoint -> scan current committed documents
                         |
                replay from pre-scan boundary
                         |
                continue incremental pages
```

- Apply each page idempotently and persist all its documents/tombstones together
  with its checkpoint in one local transaction. On failure retain prior progress.
- Continue while `caughtUp` is false, even when the page contains no documents.
  True means the returned progress reached an observed source watermark; it
  does not cover every write made before the response arrived.
- Pages can repeat document states, and a state can be newer than its triggering
  source change. Version/timestamp comparisons are not a substitute for applying
  replication state, particularly after deletion and recreation.
- Source positions can resume through another service instance connected to the
  same authoritative source. The cursor binds both the URL's storage namespace
  and the resolved database entity. Switching identifiers or reassigning a slug
  to another entity rejects continuation with 400. Source replacement or expired
  history cannot be hidden by silently restarting at the current head.
- On `RESYNC_REQUIRED`, rebuild the server mirror using a new bootstrap while
  retaining unsent local edits separately for reconciliation.
- Serialized JSON pages are limited to 16 MiB, including checkpoint/envelope;
  the corresponding protobuf page is limited to 20 MiB. Count, work, time, or
  byte limits may return fewer documents than requested without being caught-up.
  No continuation advances over a document omitted by a page limit.

### Manual TypeScript API

```typescript
const page = await client.pull<Message>('rooms/room-1/messages', {
  checkpoint: savedCheckpoint, // null or omitted starts bootstrap
  limit: 100,
  signal: abortController.signal,
});
```

`SyntrixClient.pull` returns decoded `PullPage<T>`; int64 metadata and business
values are `bigint`. Its limit accepts 1–1,000 and defaults to 100. The request is
bound to the authentication session that started it; a session change rejects
the result with `AUTH_SESSION_CHANGED`. The method performs one request.
Persistence, repeated pulling, reset handling, and a durable offline coordinator
remain application responsibilities; see the
[SDK replication design](../design/sdk/002_replication_client.md).

## Push Changes
- **Endpoint:** `POST /replication/v1/databases/{database}/push`
- **Request Body:**
```json
{
  "collection": "rooms/room-1/messages",
  "changes": [
    {
      "action": "create",
      "document": {
        "id": "msg-2",
        "text": "Offline message",
        "version": 1
      }
    },
    {
      "action": "delete",
      "document": { "id": "msg-3" }
    }
  ]
}
```
- **Response 200 (conflicts only):**
```json
{
  "conflicts": [
    {
      "id": "msg-2",
      "text": "Server copy",
      "version": 3,
      "updatedAt": 1710000001000,
      "createdAt": 1700000000000,
      "collection": "rooms/room-1/messages"
    }
  ]
}
```

### Version Preconditions

`document.version` is optional and case-sensitive. It is a precondition on the
existing live-target write path; storage assigns the resulting document version.
It does not replace server-managed metadata.

| JSON value | Behavior |
|------------|----------|
| Field omitted | No version precondition |
| Integer literal from `0` through `9223372036854775807` | Preserve the exact value as a version equality precondition |
| Null, string, boolean, negative value, fraction, exponent notation, or out-of-range integer | HTTP 400 before any change in the request reaches the Engine |

For an existing live target, a stale version returns the server document in
`conflicts`; a matching version proceeds subject to the atomic write predicate
and other storage outcomes. Push uses the database's write source for its initial
and conflict lookups, rather than the ordinary replica read route. These reads
do not lock the document or make the batch transactional. Non-not-found
conflict-read errors return a server error. Explicit zero is preserved and does not mean
insert-only. The existing `create` example with version 1 remains accepted.
Omitting the version retains unconditional behavior. Invalid-version rejection
covers the whole request, but a valid batch is not transactional.

Current limits: a target missing from the initial read, including a deleted target,
can still enter Create before version checking. Concurrent deletion can leave
incomplete conflict results when the conflict lookup reports absence. Strict insert-only
create, missing/tombstone conflicts, and structured conflict reasons remain
[proposed](../../.agents/notes/proposed/bug-fix/2026-09-07-replication-push-version-checks.md).
The [HTTP precondition decision](../../.agents/notes/implemented/bug-fix/2026-09-07-http-push-version-preconditions.md)
records the delivered fix and its limits.

## Validation & Errors

| Pull HTTP status | Code / meaning |
|---|---|
| 400 | `BAD_REQUEST`: invalid request, cursor, or scope |
| 401 / 403 | Authentication missing or full database access denied |
| 409 | `RESYNC_REQUIRED`: timestamp checkpoint, lost source history/identity, or replaced source |
| 413 | `REQUEST_TOO_LARGE`: request body or cursor exceeds its limit |
| 422 | `REPLICATION_BUDGET_EXCEEDED`: a required source unit or state cannot fit supported work/page bounds |
| 499 | Request canceled |
| 501 | `REPLICATION_UNSUPPORTED`: selected source lacks replication support |
| 503 | `REPLICATION_UNAVAILABLE`: source unavailable; retain and retry the old checkpoint |
| 504 | `DEADLINE_EXCEEDED`: request deadline elapsed; retain prior progress |
| 500 | Internal source-state or encoding failure |

Push validation failures use 400, including invalid collection, action,
document ID, or supplied version. Valid Push conflicts remain an HTTP 200
`conflicts` array; other failures use 500.

## Notes
- Document fields are flattened; do not send storage-layer fields like `_id`, `fullpath`, or `parent`.
- No fixed offline-recovery duration is guaranteed: retained source history and
  recoverable logical identity must both remain available. See
  [deletion semantics](../design/server/core/storage/03.stores.md#document-deletion-and-physical-cleanup).
