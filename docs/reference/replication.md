# Replication API Reference

Replication endpoints use an explicit database URL namespace. Pull returns
typed document states and an opaque continuation; Push accepts typed document
objects and returns conflicts with typed current state.

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
representation. HTTP Push accepts the same document typed-value representation
inside its change envelope; the SDK outbound encoder and durable Pusher remain
proposed. The complete Pull response is not a Push request, and ordinary CRUD
still uses its existing JSON format.

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
| Hard processing timeout, including response encoding | 30 seconds |
| HTTP socket write deadline | Processing deadline plus 10 seconds |

Source-byte accounting covers records visible to the adapter, not all database
work. A successful limited page retains the last fully accepted prefix; the next
request rereads any unreturned record. A single record exceeding the supported
budgets fails explicitly. Cancellation and source/encoding/cleanup errors fail
the request; retain the last saved checkpoint.

The soft interval permits a successful stop only after the source checkpoint
advances or a caught-up watermark is proved. Opening a Watch slowly or receiving
an empty frame with the same checkpoint does not count as progress. Exhausting
the frame or source-byte budget without progress returns retryable
`REPLICATION_UNAVAILABLE`; reaching the hard deadline returns `DEADLINE_EXCEEDED`.
The HTTP write deadline leaves time to transmit the encoded result or error after
processing ends. Other routes retain the configured server write timeout.

## Push Changes

**Endpoint:** `POST /replication/v1/databases/{database}/push`

The request contains one concrete `collection` and a nonempty `changes` array.
Each change requires `action` (`create`, `update`, or `delete`) and a `document`
encoded as one recursive typed object. Its decoded fields are flattened and must
include a nonempty logical `id`. Missing or unknown actions are invalid.
Ordinary untyped documents, typed null, arrays, and scalar document roots are
rejected; there is no legacy decoding fallback.

```json
{
  "collection": "rooms/room-1/messages",
  "changes": [
    {
      "action": "create",
      "document": {
        "type": "object",
        "value": {
          "id": { "type": "string", "value": "msg-2" },
          "text": { "type": "string", "value": "Offline message" },
          "version": { "type": "int64", "value": "1" }
        }
      }
    },
    {
      "action": "delete",
      "document": {
        "type": "object",
        "value": {
          "id": { "type": "string", "value": "msg-3" },
          "version": { "type": "int64", "value": "5" }
        }
      }
    }
  ]
}
```

A successful HTTP 200 response contains only conflicts; an empty array means
all changes succeeded, including idempotent deletes. `changeIndex` is the
zero-based position in the request, so repeated IDs remain distinguishable.

```json
{
  "conflicts": [
    {
      "changeIndex": 1,
      "id": "msg-3",
      "reason": "missing",
      "current": null
    }
  ]
}
```

When present, `current` is a recursive typed object using the same value codec
as the request document and Pull documents. For example:

```json
{
  "type": "object",
  "value": {
    "id": { "type": "string", "value": "msg-3" },
    "collection": { "type": "string", "value": "rooms/room-1/messages" },
    "text": { "type": "string", "value": "Server copy" },
    "version": { "type": "int64", "value": "6" },
    "createdAt": { "type": "int64", "value": "1700000000000" },
    "updatedAt": { "type": "int64", "value": "1710000001000" }
  }
}
```

A retained tombstone includes typed `deleted: true` and its real metadata, with
former business fields cleared. Decoded `current.id` and `current.deleted` reflect
validated document identity and stored deletion state; business data cannot
override them. An absent target uses raw JSON null for `current`, not a typed-null
object. The outer `changeIndex`, `id`, and `reason` fields keep their existing shape.

### Version Preconditions

`document.version` is optional and case-sensitive. Storage assigns the resulting
document version; the supplied value is never copied into stored metadata.

| Typed `version` field | Behavior |
|---|---|
| Field omitted | No version precondition |
| `{"type":"int64","value":"0"}` through `{"type":"int64","value":"9223372036854775807"}` | Preserve exact value and presence |
| Null, string, bool, float64, negative/out-of-range int64, or noncanonical int64 string | HTTP 400 before any change reaches the Engine |

Int64 strings use canonical decimal notation: no leading plus, leading zeros,
negative zero, fraction, or exponent. A float64 value of `1` is not a valid version.
Nested business values retain their declared numeric type.

| Request | Target | Result |
|---|---|---|
| Versioned update/delete, including zero | Live, matching version | Atomic conditional mutation |
| Versioned update/delete | Missing, tombstoned, or different version | Conflict; target is not recreated |
| Unversioned update | Live | Unconditional update |
| Unversioned update | Missing or tombstoned | Create/recreate |
| Unversioned delete | Live | Delete |
| Unversioned delete | Missing or tombstoned | Idempotent success |
| Create without `createCondition` | Missing or tombstoned | Create/recreate; supplied valid version is ignored |
| Create without `createCondition` | Live | Update; enforce equality if a version is supplied |

Explicit zero remains an equality precondition on live targets. Without a
`createCondition`, create with version 0 or 1 and update/delete with version 0
retain their existing behavior.

### Create Conditions

A change may include `createCondition` alongside `action` and `document`. It is
valid only with `action: "create"`:

| `createCondition` | Version requirement | Result |
|---|---|---|
| Omitted | Existing optional-version rules | Default create behavior |
| `absent` | Version must be omitted | Atomic insert only when no live record or retained tombstone exists |
| `tombstone` | Version required as a nonnegative typed int64 | Atomic replacement only of a retained tombstone at that version |

Example changes, placed inside the request's `changes` array:

```json
[
  {
    "action": "create",
    "createCondition": "absent",
    "document": {
      "type": "object",
      "value": { "id": { "type": "string", "value": "new-message" } }
    }
  },
  {
    "action": "create",
    "createCondition": "tombstone",
    "document": {
      "type": "object",
      "value": {
        "id": { "type": "string", "value": "removed-message" },
        "version": { "type": "int64", "value": "7" },
        "text": { "type": "string", "value": "Recreated message" }
      }
    }
  }
]
```

Null, empty, unknown, or non-string conditions return HTTP 400. So do conditions
on update/delete, any supplied version with `absent`, or a missing/invalid version
with `tombstone`. The entire request is validated before any storage operation.
Explicit typed-int64 zero is valid for the tombstone version and matches only
that version; it is not an insert-only sentinel.

A rejected conditional create returns the existing conflict shape: `missing`
with null for absence, `tombstoned` with the real tombstone even when its version
mismatches, or `already_exists` with the live document. The actual storage write
must enforce the condition after any initial read. A tombstone condition never
falls back to inserting a missing target; an absent condition never replaces a
tombstone. Storage assigns the recreated document's resulting metadata.

Conditions describe current stored state, not a permanent document generation.
Physical cleanup makes an identity eligible for absent creation again. Reused
versions after delete/recreate cycles can satisfy a later tombstone condition
(ABA); there is no generation or exactly-once guarantee. Deploy condition-aware
Gateway and Query services before sending the new field. The
[create-condition decision](../../.agents/notes/implemented/feature/2026-09-07-replication-push-insert-only.md)
records these guarantees and limits.

### Push Size Limits

| Boundary | Limit |
|---|---|
| HTTP request body, including all typed-value tags and the outer envelope | 10 MiB |
| Encoded protobuf request, including typed data and envelope | 20 MiB |
| Encoded protobuf conflict response, including typed data and envelope | 20 MiB |

The protobuf budgets also apply to local Query execution. Production gRPC
receive limits admit messages within these budgets. The HTTP body limit counts
the encoded typed JSON, not only business data. The HTTP and protobuf budgets
apply independently; fitting the body limit does not waive the protobuf check. Such a request returns HTTP 400 before any storage operation.
An oversized conflict response returns HTTP 422 `REPLICATION_BUDGET_EXCEEDED`
without truncation; earlier changes in the batch may already have committed.

### Conflict Results and Batch Execution

| Reason | Meaning |
|---|---|
| `missing` | Target is absent; `current` is null |
| `tombstoned` | Target is a retained tombstone, including a conditional recreation with the wrong version |
| `version_mismatch` | Live target has a different version |
| `already_exists` | A create condition rejects a live target, or a create/recreate attempt lost to one |
| `precondition_failed` | The write failed its condition, but the later read cannot identify a more specific cause |

Push uses the database's write source for initial and conflict reads and includes
retained tombstones. A conflict's `current` is the state observed when read; after
a failed write it may already differ from the state that caused the failure.
It is not a guarantee that retrying will succeed. Failed conflict reads return
an error, without a fabricated document or an incomplete success response.

Query validates the entire request before the first storage operation. Valid
changes execute in order, continuing after individual conflicts. The batch is
nontransactional: a later runtime error may leave earlier writes committed.
A lost response is ambiguous, and retrying does not provide exactly-once effects.

The conflict object replaces the document-only response. The internal gRPC
contract requires explicit action and optional int64 version presence, with no
legacy negative sentinel or unspecified-action fallback. Push document data also
uses recursive typed values internally over gRPC; old untyped gRPC data is invalid.
HTTP uses the same typed-value codec for documents, with no ordinary-JSON
fallback. Upgrade Push request and response consumers together. The
[HTTP typed-value decision](../../.agents/notes/implemented/bug-fix/2026-09-18-http-push-typed-values.md)
owns this encoding change. See the
[conditional-write decision](../../.agents/notes/implemented/bug-fix/2026-09-07-replication-push-version-checks.md)
for rationale and the [HTTP decoder decision](../../.agents/notes/implemented/bug-fix/2026-09-07-http-push-version-preconditions.md)
for exact version extraction.

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
| 503 `REPLICATION_UNAVAILABLE` | Transient source failure or work budget exhausted without checkpoint progress; retry the saved checkpoint |
| 504 `DEADLINE_EXCEEDED` | Request timeout; retry the saved checkpoint |
| 500 `INTERNAL_ERROR` | Invalid source output or other server failure; no progress returned |

Push continues to return version conflicts in a successful 200 response with a
`conflicts` array. Invalid supplied versions and other malformed Push parameters
return 400; the Pull recovery code is not a Push conflict format.
