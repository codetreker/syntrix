# Replication API Reference

Replication endpoints use an explicit database URL namespace. Pull returns
typed document states and an opaque continuation; Push accepts ordinary flattened
JSON documents and returns conflicts.

## Pull Changes

**Endpoint:** `POST /replication/v1/databases/{database}/pull`

```json
{
  "collection": "users",
  "checkpoint": null,
  "limit": 100
}
```

| Field | Contract |
|---|---|
| `collection` | Required concrete collection path; no wildcard selection |
| `checkpoint` | Omit, null, or empty string to initialize; otherwise reuse the returned string verbatim |
| `limit` | Optional integer, default 100; 0 also selects default; valid range 0–1000 |

Unknown fields, duplicate keys, invalid Unicode, and extra JSON values are rejected.
The request is bounded to 1 MiB and the checkpoint to 256 KiB.

Pull currently requires the validated database owner or a matching `db_admin`
grant for its ID or validated slug. A global `admin`/`user` role or authentication
alone is insufficient. This full-scope permission profile is provisional pending
approval; per-document authorization filtering is not supported.

### Response and Typed Values

```json
{
  "documents": [
    {
      "type": "object",
      "value": {
        "id": {"type": "string", "value": "alice"},
        "collection": {"type": "string", "value": "users"},
        "name": {"type": "string", "value": "Alice"},
        "version": {"type": "int64", "value": "2"},
        "createdAt": {"type": "int64", "value": "1700000000000"},
        "updatedAt": {"type": "int64", "value": "1710000000000"}
      }
    }
  ],
  "checkpoint": "opaque-continuation",
  "caughtUp": false
}
```

Each document is one recursive typed value. Objects and arrays recursively contain
typed values; scalar tags are `null`, `bool`, `string`, `int64`, and `float64`.
An int64 is a decimal string; float64 is a finite JSON number. The TypeScript SDK
decodes every int64 to bigint, including metadata and nested business values.
Decoded documents are flattened business fields plus reserved metadata:

| Metadata | Meaning |
|---|---|
| `id`, `collection` | Required logical document identity |
| `version` | Server document version, not replication order |
| `createdAt`, `updatedAt` | Server timestamps in milliseconds |
| `deleted` | When true, remove the server document state locally |

Decoded bigint values cannot be blindly passed to `JSON.stringify` or sent back to SDK
document `set`/`update`, whose current JSON serialization rejects them before HTTP
transmission. Number conversion can lose precision. Local storage needs a lossless
representation; a matching outbound codec and durable Pusher remain proposed.
The typed Pull envelope is not an accepted replacement for ordinary Push input.

A logical-delete event may return only `id`, `collection`, and `deleted: true`;
version and timestamps are then absent. Do not require or manufacture them.
Stored tombstones clear former business fields. Physical cleanup does not emit
another deletion; see [deletion semantics](../design/server/core/storage/03.stores.md#document-deletion-and-physical-cleanup).

### Continuation and Local Application

1. Initialize with a null checkpoint. The server scans committed documents and
   then replays overlapping changes from the original scan boundary.
2. Apply returned documents and deletions in order. Duplicate and later states
   are allowed; pages do not form a fixed snapshot. Never discard a record only
   because its document version is lower than a previously seen version.
3. Commit the whole page and its checkpoint in one local transaction. A failed
   transaction keeps the previous checkpoint.
4. Continue with the returned checkpoint, including after an empty page with
   `caughtUp: false`. True means a source watermark was processed, not that no
   future write exists.
5. Keep state and checkpoint isolated by account, database URL namespace, and
   collection. The cursor also binds the resolved database identity. Changing
   between ID and slug does not preserve the same continuation.
6. On `RESYNC_REQUIRED`, rebuild the server mirror with a null checkpoint while
   preserving unsent local edits for reconciliation.

Timestamp checkpoints, including numeric strings and JSON numbers, return an
explicit resynchronization error. Other malformed or scope-mismatched cursors
fail validation. Resuming on another service instance is supported against the
same retained source; source replacement or history expiry requires recovery.

### Budgets

| Budget | Limit |
|---|---:|
| Returned documents | 1000 |
| Source bytes | 16 MiB |
| Encoded JSON response | 16 MiB including checkpoint and envelope |
| Encoded protobuf response | 20 MiB |
| Incremental source frames | 10,000 |
| Incremental soft processing interval | 5 seconds |
| Hard request timeout | 30 seconds |

Source-byte accounting covers records visible to the adapter, not all database
work. A successful limited page retains the last fully accepted prefix; the next
request rereads any unreturned record. A single record exceeding the supported
budgets fails explicitly. Cancellation and source/encoding/cleanup errors fail
the request; retain the last saved checkpoint.

## Push Changes
- **Endpoint:** `POST /replication/v1/databases/{database}/push`
- **Request Body:**
```json
{
  "collection": "rooms/room-1/messages",
  "changes": [
    {
      "action": "create", // "create" | "update" | "delete"
      "document": {
        "id": "msg-2",
        "text": "Offline message",
        "version": 1 // optional version precondition; not stored metadata
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


## Errors

| HTTP status / code | Meaning and recovery |
|---|---|
| 400 `BAD_REQUEST` | Invalid request, cursor, or scope; correct the request |
| 401 / 403 | Authentication or full-scope access denied |
| 409 `RESYNC_REQUIRED` | Old timestamp cursor, source replacement, expired history, or unavailable required payload; rebuild from null |
| 413 `REQUEST_TOO_LARGE` | Request body or checkpoint exceeds its size limit |
| 422 `REPLICATION_BUDGET_EXCEEDED` | A response cannot satisfy work/size limits |
| 499 | Request canceled; keep the last saved checkpoint |
| 501 `REPLICATION_UNSUPPORTED` | Selected source lacks required capabilities |
| 503 `REPLICATION_UNAVAILABLE` | Transient source failure; retry the saved checkpoint |
| 504 `DEADLINE_EXCEEDED` | Request timeout; retry the saved checkpoint |
| 500 `INTERNAL_ERROR` | Invalid source output or other server failure; no progress returned |

Push continues to return version conflicts in a successful 200 response with a
`conflicts` array. Invalid supplied versions and other malformed Push parameters
return 400; the Pull recovery code is not a Push conflict format.
