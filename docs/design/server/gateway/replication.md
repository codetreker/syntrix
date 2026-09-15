# Replication Design

Replication Pull uses committed Store Watch progress to synchronize current
document state. Push retains its existing conditional-write protocol.
[Replication reference](../../../reference/replication.md) owns exact request,
response, typed-value, and error contracts.

## Endpoints and Responsibilities

| Boundary | Responsibility |
|---|---|
| `POST /replication/v1/databases/{database}/pull` | Bounded page of current document states or logical deletions with an opaque continuation |
| `POST /replication/v1/databases/{database}/push` | Apply requested changes and return conflicts |
| Gateway | Authenticate, validate the active database, authorize full-scope Pull, validate and encode HTTP |
| Query | Bind public cursor scope, sequence scan/replay, and enforce complete-page budgets |
| Store | Establish committed scan overlap, ordered Watch frames, source identity, and explicit failures |
| Client | Atomically apply a whole page and persist its checkpoint |

The request's database URL value remains the storage namespace used by ordinary
CRUD. The resolved database identity is additionally bound into the public cursor;
an alias resolving to a different database cannot reuse it. A canonical ID and its
slug are not interchangeable continuations. This change does not migrate existing
document namespaces or redefine CRUD database selection.

### Authorization Profile

The implemented full-scope profile requires the validated database owner or a
matching `db_admin` grant for its ID or validated slug. Authentication, a global
role, and a valid cursor alone do not grant Pull access. Authorization is checked
on every request. This profile is provisional pending approval; it does not
implement per-document permission filtering or document-removal notifications
when a permission set changes.

## Pull State Machine

```text
no checkpoint
      |
Watch(StartForScan) -> C0 -> close stream
      |
ScanDocuments(AtLeast=C0, AfterID)
      |
scan exhausted -> changes cursor at original C0
      |
Watch(after=C0) -> document/progress pages
      |
resume from last accepted Watch checkpoint
```

| Phase | Cursor state | Advancement |
|---|---|---|
| Scan | Database URL namespace, resolved identity, collection, original C0, last logical ID | Exclusive logical-ID continuation through returned documents |
| Changes | Same scope, last accepted Watch checkpoint | Source order through completed frames, including filtered progress |

The initial committed boundary overlaps inclusive replay, covering changes during
the moving scan. Pages need not show one historical snapshot. Create/update
enrichment may show a later committed document; pages can repeat states and
temporarily regress before converging. Document timestamps and versions do not
order replication, and deletion/recreation can reset a document version.

The [Watch scan-boundary decision](../../../../.agents/notes/implemented/architecture/2026-09-15-watch-scan-boundary.md)
owns source consistency. Pull directly reuses Watch and ScanDocuments; it adds no
parallel replication source, ordinary event GetMany, persistent client session,
or dependency on a Puller instance's buffer. Original C0 is retained throughout
scanning. History is checked by actual replay reads, so expiry can require
repeating a completed scan.

### State and Deletion

| Source result | Public logical document |
|---|---|
| Scan candidate or create/update Document | Flattened current state with server metadata |
| Tombstone Document | Metadata and `deleted: true`, without former business fields |
| Logical delete event with nil Document | `{id, collection, deleted: true}`; unknown version/timestamps are absent |
| Physical cleanup | Source progress only |
| Missing required identity or payload | Explicit resynchronization error |

Deletion identity is copied from existing StoredDoc metadata. Physical storage IDs,
fullpaths, and native resume tokens are not public document fields. A later
tombstone in a create/update payload is applied as deleted state. Source order,
including delete/recreate events, is preserved without version-based suppression.
The [deletion lifecycle](../core/storage/03.stores.md#document-deletion-and-physical-cleanup)
defines retained tombstones and physical cleanup.

### Page Limits and Ownership

| Limit | Value |
|---|---:|
| Documents | Default 100; maximum 1000 |
| HTTP request / public cursor | 1 MiB / 256 KiB |
| Source bytes | 16 MiB per accepted page; visible raw records, not all DB work |
| Encoded JSON / protobuf response | 16 MiB / 20 MiB, including envelopes |
| Watch frames | 10,000 per request |
| Incremental soft work interval / hard request timeout | 5 seconds / 30 seconds |
| Incremental Watch poll | 100 milliseconds |
| HTTP socket write deadline | Processing deadline plus 10 seconds |

Scans shrink candidate batches after a source-byte limit and fail explicitly if
one candidate cannot fit. A prefetched record that does not fit the response
cannot advance its public cursor; it is read again. A successful page may stop
on count, source work, or response size and return `caughtUp: false`. Errors,
cancellation, invalid source data, and stream-close failures fail the whole
request without returning partial progress.

The soft interval ends a successful page only after an accepted source checkpoint
advances or Watch proves caught-up progress. Watch setup and repeated empty frames
at the same checkpoint cannot consume the soft interval into a successful no-op:
that would let every retry repeat the same position. Without progress, source-byte
or frame exhaustion returns a retryable unavailable error; the hard deadline
returns a timeout. The 30-second processing deadline also covers response encoding.

Gateway sets Pull's socket write deadline to the processing deadline plus ten
seconds, allowing the result or timeout error to be transmitted. Response wrappers
must expose the underlying writer's deadline control; unavailable control fails
before querying. The configured write timeout continues to govern other routes.

Only Watch's successful watermark proof produces `caughtUp: true`. Filtered
empty pages can advance while false; sustained writes may prevent true. The
watermark describes consumed source progress, not an absence of future writes.
Every request closes its stream and owns its cancellation lifetime.

### Transport and Recovery

Pull uses POST because opaque source continuations can exceed practical URL
limits. HTTP and gRPC encode each flattened document using the shared recursive
typed-value representation, preserving nested int64 values. Protobuf carries an
explicit wire version and the same complete logical document representation.
Push retains its ordinary flattened JSON decoding and version preconditions.

Clients persist opaque cursor strings verbatim without parsing or comparing
them. Missing/null/empty HTTP checkpoint starts a bootstrap. Old timestamp
positions explicitly require resynchronization; malformed or cross-scope
positions fail validation. Source replacement, unavailable history, or missing
required payload require rebuilding the server mirror while retaining unsent
local changes for reconciliation. Transient errors retry the last saved cursor.

The manual SDK requests one page, decodes int64 as bigint, and binds requests to
the current authentication session. It does not own local storage or automatically
drain pages. The [SDK coordinator design](../../sdk/002_replication_client.md)
remains planned; application state and checkpoint application must remain atomic.

### Diagnostics

Request IDs propagate across Gateway and Query. Pull completion logs include
hashed scope and input/output checkpoint identifiers, phase, returned count,
duration, and bounded error/end reason. Raw checkpoints, source errors containing
payloads, and document data are excluded.

## Push

- Method: `POST /replication/v1/databases/{database}/push`
- Request body (flattened documents):

```json
{
  "collection": "room/chatroom-1/messages",
  "changes": [
    {
      "action": "create",
      "document": {
        "id": "m1",
        "text": "hello",
        "version": 1
      }
    },
    {
      "action": "delete",
      "document": { "id": "m2" }
    }
  ]
}
```

- Rules:
  - `action` ∈ {"create", "update", "delete"}.
  - `document.id` is required for every change.
  - `document.version` is optional and case-sensitive; preserve its exact nonnegative int64 integer value and presence as the version precondition before stripping protected metadata.
  - No storage-layer fields (e.g., `_id`, `fullpath`, `parent`) are accepted or returned.
- Response (conflicts only):

```json
{
  "conflicts": [
    {
      "id": "m1",
      "text": "server-copy",
      "version": 3,
      "updatedAt": 1710000001000,
      "createdAt": 1700000000000,
      "collection": "room/chatroom-1/messages"
    }
  ]
}
```

### Version Preconditions

| Supplied `document.version` | Request handling |
|----------------------------|------------------|
| Omitted | Preserve an absent precondition |
| Nonnegative int64 integer literal, including zero | Forward the exact value separately from document data |
| Null, string, boolean, negative value, fraction, exponent notation, or out-of-range integer | Reject the request before any Engine call |

Extract the reserved field from raw JSON so values beyond floating-point integer
precision remain exact. Ordinary business numbers keep their existing decoding
behavior. Protected fields are still removed from document data, and new stored
documents retain server initialization at version 1. The supplied value is not
assigned to stored version metadata. The existing gRPC encoding preserves absence
as `-1` and retains explicit zero and supported positive int64 values.

Push requests the database's write source for its initial and conflict lookups.
Non-not-found conflict-read errors propagate as server errors. These reads do not
lock data or establish transactions or linearizable reads. Existing live-target
writes compare the version and apply it in the atomic write predicate. Explicit zero is an equality precondition, not an insert-only request;
`create` with version 1 remains accepted. Omission retains the current optional,
unconditional behavior. A malformed version anywhere in a batch prevents all
Engine calls for that request; valid batches remain nontransactional.

The [HTTP precondition decision](../../../../.agents/notes/implemented/bug-fix/2026-09-07-http-push-version-preconditions.md)
records why extraction is local to replication decoding and preserves the
existing document-number representation.

## Conflict Handling

- Push may return `conflicts` containing the authoritative server documents in flattened form.
- Clients decide whether to retry, merge, or surface conflicts.
- The initial not-found path can enter Create before checking the version, including
  for deleted targets. Concurrent deletion can still leave incomplete conflict
  results when the authoritative lookup reports absence. Strict create/update/delete predicates, authoritative
  missing/tombstone results, and structured conflict reasons remain in the
  [version-check proposal](../../../../.agents/notes/proposed/bug-fix/2026-09-07-replication-push-version-checks.md).


## Decision Ownership

The [Pull checkpoint decision](../../../../.agents/notes/implemented/bug-fix/2026-09-07-replication-pull-cursor-progress.md)
records native-source selection, alternatives, and accepted retention and client
integration limits. Public Pull and source capabilities have separate owners.
