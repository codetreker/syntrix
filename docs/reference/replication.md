# Replication API Reference

Replication endpoints use an explicit database URL namespace. Pull returns
typed document states or query-membership events with an opaque continuation,
or a complete query window. Push accepts typed document objects and returns
conflicts with typed current state.

## Bound Database Identity

A client bound to a database incarnation sends the optional header
`X-Syntrix-Expected-Database-Identity: <database ID>` on Pull, Push, ordinary Query,
and document GET requests. The value is exactly one lowercase 16-digit hexadecimal
ID. An empty, repeated, or malformed header returns HTTP 400 `BAD_REQUEST`.

| Request | Database resolution and authorization |
|---|---|
| Any request carrying the header on these routes | Read the management store afresh; require an active database and its owner or matching `db_admin` grant before comparing identity |
| Query-source Pull without the header | Use the same authoritative resolution and full-scope authorization; return the real ID for initial binding |
| Existing requests without the header or a Pull source | Preserve their existing resolution and authorization behavior |

Status, permission, and identity use the same fresh database object. A cache hit
cannot replace this check, and management-store failure has no cache fallback.
Missing, suspended, and deleting databases retain their existing errors.
Unauthorized callers receive the permission error before identity comparison.
A different identity returns HTTP 409 `DATABASE_IDENTITY_MISMATCH` before any
document read, scan, Watch, or write. Push reports it as a request error, not a
per-document conflict. Bound ordinary Query and GET use this full-scope gate as
well; the header does not enable per-document-authorized replication reads.

The URL namespace still selects storage. This check does not rewrite a slug into
an ID address. It checks database identity when admitting the request; it does
not lock the database against concurrent deletion or slug reassignment for the
request's lifetime. Keep pending writes and uncertain earlier attempts after an
identity failure: rejecting this request says nothing about an earlier timed-out
Push.

All Gateway and Query nodes must support this contract before query-replication
protocol version 1 is enabled. Header presence alone cannot establish support
when an older node may ignore it.

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

## Query-Source Pull

The same Pull endpoint accepts a `source` to synchronize an entire matching set
or a bounded result window. Omitting `source` preserves the collection Pull
request and response above. The
SDK's existing public manual Pull method still exposes only collection Pull.

```json
{
  "collection": "users",
  "source": {
    "version": 1,
    "filters": [
      {"field": "active", "op": "==", "value": {"type": "bool", "value": true}}
    ]
  },
  "checkpoint": null,
  "limit": 100
}
```

| Field | Contract |
|---|---|
| `source.version` | Required integer `1` |
| `source.filters` | Required array, possibly empty; AND conjunction with the existing [filter semantics](filters.md#fields-and-operators); operands require recursive typed nodes |
| `source.orderBy` | Optional array of `{field, direction}` with `asc` or `desc`; orders a result window and participates in source identity; matching-set events retain source order |
| Top-level `limit` | Matching-set transfer event limit, default 100, range 0–1000; zero selects the default; forbidden for windows |
| `checkpoint` | For matching sets, omit, null, or empty string to initialize; otherwise reuse the returned opaque string; forbidden for windows |
| `source.limit` | Result-window size, integer 1–1000; omission selects the entire matching set |
| `requestId` | Required nonempty string for a window and echoed exactly; forbidden for matching sets |

Unknown or duplicate structural fields, null `source`, unsupported versions,
invalid Unicode, malformed typed nodes, and invalid field combinations return
HTTP 400 `INVALID_REPLICATION_SOURCE`. Window requests reject the presence of
top-level `limit` or `checkpoint`, including zero, null, and empty values where
otherwise valid for matching sets. HTTP and gRPC preserve this presence rule.

### Events and Source Identity

```json
{
  "protocolVersion": 1,
  "mode": "events",
  "databaseIdentity": "0123456789abcdef",
  "sourceHash": "server-computed-source-hash",
  "events": [
    {"type": "leave", "id": "alice"},
    {"type": "delete", "id": "bob"}
  ],
  "checkpoint": "opaque-query-continuation",
  "generationId": "server-generated-generation",
  "phase": "replay",
  "caughtUp": false,
  "bootstrapComplete": false
}
```

All response envelope fields are required, including an empty `events` array and
false boolean values. `databaseIdentity` comes from the authoritative database
check. `sourceHash` binds protocol version, actual database identity, collection,
normalized typed filters, effective ordering, and result limit. Filter order and
other equivalent normalized predicates produce the same identity. Effective
ordering appends logical ID ascending when not explicitly ordered; absent ordering
is ID ascending for this identity. Clients retain the hash rather than deriving it.

| Source state | Event | Consumer meaning |
|---|---|---|
| Live document matching every predicate | `upsert` with `document` in the existing recursive typed object format | Apply the current state and include its ID in this source |
| Live document failing a predicate during replay/live | `leave` with `id` | Remove this source's membership, even if that ID was never observed locally |
| Logical deletion | `delete` with `id` | Apply deletion without inventing a version, timestamps, or payload |
| Filtered scan candidate or progress-only frame | No event | Persist the returned checkpoint even when events are empty |

Delete events currently emit no `observedMetadata`. A leave is not a deletion of
the stored document or of its membership in another source. Full-scope owner or
`db_admin` authorization is required because stateless leaves may reveal IDs that
never matched. Query filters do not confer permissions.

### Generations and Recovery

```text
new generation -> scan -> replay -> live
                     original C0 ----^
```

| Phase | Completion contract |
|---|---|
| `scan` | Scan committed candidates in logical ID order; `caughtUp` and `bootstrapComplete` are false |
| `replay` | Replay from the scan's original C0; neither a short nor empty page proves completion |
| `live` | Enter only after Watch proves caught-up progress; `bootstrapComplete` remains true for the generation, while `caughtUp` describes the current page |

The opaque version-4 cursor binds source hash, generation, phase, database scope,
and existing Store progress. It can resume on another service instance. Changing
the source or database scope fails validation; old collection-mode cursors cannot
be used for query mode. Expired source history returns `RESYNC_REQUIRED`; an
explicit new initialization creates a new generation. Consumers activate rebuilt
membership only after the completed generation and its checkpoint are durable,
retaining unsent local edits throughout recovery.

The moving scan overlaps replay and is not a fixed historical snapshot. Current
state enrichment may yield a recreated document, then a historical deletion, then
the recreated document again. Apply source order and permit temporary regression;
maximum document version cannot establish delete/recreate order.

All existing Pull budgets apply to matching-set mode, including every scanned candidate,
filtered-out work, source bytes, Watch frames, and encoded envelope. The transfer
limit counts events; rejected scan candidates still consume the bounded source
page. An empty events page may therefore advance without completing bootstrap.
A cursor never advances beyond an event that did not fit the response. These
adapter-visible budgets do not promise a bound on all internal database work.

### Complete Result Windows

```json
{
  "collection": "users",
  "source": {
    "version": 1,
    "filters": [],
    "orderBy": [{"field": "score", "direction": "desc"}],
    "limit": 2
  },
  "requestId": "refresh-7"
}
```

A window runs one ordinary Query with the normalized filters, effective ordering,
and result limit. It starts without a Query continuation. Absent ordering means
explicit logical ID ascending; explicit ordering appends ID ascending unless ID
is already ordered. Execution uses that same ordering as `sourceHash`. The normal
index requirements apply, including to default ID ordering.

```json
{
  "protocolVersion": 1,
  "mode": "replace",
  "databaseIdentity": "0123456789abcdef",
  "sourceHash": "server-computed-source-hash",
  "requestId": "refresh-7",
  "generationId": "new-window-generation",
  "complete": true,
  "effectiveOrder": [
    {"field": "score", "direction": "desc"},
    {"field": "id", "direction": "asc"}
  ],
  "documents": []
}
```

All nine fields are required. Documents use the existing recursive typed object
format; an empty array is a complete empty result. Every successful request gets
a new generation. The response has no events, checkpoint, phase, caughtUp, or
bootstrapComplete fields. The client uses request/session identity to reject old
responses; generation IDs are not sortable source positions.

| Single Query result for requested N | Replication result |
|---|---|
| Exactly N documents, with or without continuation | Complete replacement |
| Fewer than N documents and no continuation | Complete exhausted replacement |
| Fewer than N documents with continuation | HTTP 503 `REPLICATION_WINDOW_INCOMPLETE`; no replacement |
| Query, encoding, or budget failure | Error; no partial replacement |

Independent Query pages are never concatenated into a purported snapshot. A
window must fit both the full 16 MiB JSON envelope and 20 MiB protobuf envelope,
including request ID, generation, ordering, and typed document overhead. Query
work limits still apply. Envelope limits fail explicitly rather than truncating
members; retain the prior active window on any failure.

Windows use ordinary Query consistency. Index lag can temporarily omit an existing
member that still matches, followed by reentry after indexing catches up; it does
not merely delay new members. A complete response certifies the single Query's
bounded result, not a strict source snapshot or index freshness fence. A later
complete refresh handles rank displacement and replacement members. Membership
exit is not a stored-document deletion.

The authoritative database identity and owner/`db_admin` gate run before window
Query execution, including first binding. Identity failure must not be interpreted
as an empty replacement. The SDK's automatic window adapter, member replacement,
and refresh scheduling remain unimplemented. Their contract treats realtime as a
refresh hint and requires polling so missed notifications do not freeze a window.

The [query-source decision](../../.agents/notes/implemented/feature/2026-09-18-query-replication-source.md)
records source projection, identity checking, and their guarantees.

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
| Create | Missing or tombstoned | Create/recreate; supplied valid version is ignored |
| Create | Live | `already_exists`; leave the document unchanged regardless of supplied version |

Explicit zero remains an equality precondition for update/delete. Create accepts
any otherwise valid version but does not use it as a precondition; the live-target
check takes precedence over version comparison. Even equal content and version
return `already_exists`. A retained tombstone communicates deletion and does not
reserve the logical ID: creation can immediately reuse that ID in the same
database and collection, without observing its deletion version or awaiting
physical cleanup. See the
[create-conflict decision](../../.agents/notes/implemented/bug-fix/2026-09-18-replication-push-create-conflict.md).

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
| `tombstoned` | Target is a retained tombstone |
| `version_mismatch` | Live target has a different version |
| `already_exists` | Create observed a live target, or a create/recreate attempt lost to one |
| `precondition_failed` | The write failed its condition, but the later read cannot identify a more specific cause |

Push uses the database's write source for initial and conflict reads and includes
retained tombstones. A conflict's `current` is the state observed when read; after
a failed write it may already differ from the state that caused the failure.
A failed create may therefore report `missing` or `tombstoned`; this records the
later observation and does not prohibit creation in that state. Push does not
automatically retry the failed creation. It is not a guarantee that retrying will
succeed. Failed conflict reads return an error, without a fabricated document or
an incomplete success response.

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
| 400 `BAD_REQUEST` | Invalid request, cursor, scope, or database identity header; correct the request |
| 400 `INVALID_REPLICATION_SOURCE` | Invalid source structure, version, or field combination |
| 400 `NO_MATCHING_INDEX` | No eligible complete index plan for the window Query |
| 401 / 403 | Authentication or full-scope access denied |
| 409 `DATABASE_IDENTITY_MISMATCH` | Bound database identity no longer matches; stop this binding and preserve pending or uncertain writes |
| 409 `RESYNC_REQUIRED` | Old timestamp cursor, source replacement, expired history, or unavailable required payload; rebuild from null |
| 413 `REQUEST_TOO_LARGE` | Request body or checkpoint exceeds its size limit |
| 422 `REPLICATION_BUDGET_EXCEEDED` | Collection/matching-set Pull or Push cannot satisfy replication work/size limits |
| 422 `QUERY_WORK_LIMIT` | Window Query or its complete replace envelope exceeded work/size limits; no replacement |
| 499 | Request canceled; keep the last saved checkpoint |
| 501 `REPLICATION_UNSUPPORTED` | Selected source lacks required capabilities |
| 503 `INDEX_UNAVAILABLE` | Window index is not ready or is rebuilding |
| 503 `REPLICATION_WINDOW_INCOMPLETE` | Window Query returned fewer than N documents with continuation; retain the active window and retry the complete request |
| 503 `REPLICATION_UNAVAILABLE` | Transient source failure or work budget exhausted without checkpoint progress; retry the saved checkpoint |
| 504 `DEADLINE_EXCEEDED` | Request timeout; retry the saved checkpoint |
| 500 `INTERNAL_ERROR` | Invalid source output or other server failure; no progress returned |

Push continues to return version conflicts in a successful 200 response with a
`conflicts` array. Invalid supplied versions and other malformed Push parameters
return 400; the Pull recovery code is not a Push conflict format.
