# Replication Design

Replication Pull uses committed Store Watch progress to synchronize current
document state, or one ordinary Query to replace a bounded result window. Push
preserves explicit actions and optional version conditions
through atomic writes and structured conflict responses.
[Replication reference](../../../reference/replication.md) owns exact request,
response, typed-value, and error contracts.

## Endpoints and Responsibilities

| Boundary | Responsibility |
|---|---|
| `POST /replication/v1/databases/{database}/pull` | Current document states or membership events with continuation; complete bounded query windows |
| `POST /replication/v1/databases/{database}/push` | Apply requested changes and return conflicts |
| Gateway | Authenticate, resolve and authorize the database, enforce optional bound identity, validate and encode HTTP |
| Query | Bind source/cursor scope, sequence scan/replay or execute a complete window, and enforce response budgets |
| Store | Establish committed scan overlap, ordered Watch frames, source identity, and explicit failures |
| Client | Durably apply a page and its checkpoint, or activate a complete replacement window |

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

## Bound Identity and Query Sources

Query-source Pull returns an entire matching set through the existing committed
scan and Watch. It adds membership projection and generation completion without
introducing a parallel source or server-side client membership table. Ordinary
collection Pull retains its existing response. Result windows use one ordinary
Query and return a complete replacement, under the same database identity gate.

### Request identity

| Request | Gate before data access |
|---|---|
| Query-source Pull, including initial binding | Resolve the database directly from management storage; check active status and owner/`db_admin` authorization on that same object |
| Pull, Push, ordinary Query, or document GET with `X-Syntrix-Expected-Database-Identity` | Apply the same fresh resolution and full-scope authorization, then compare the expected ID |
| Existing unbound request | Keep existing resolution and authorization |

Fresh resolution bypasses cached database metadata. Status, authorization, and
identity cannot refer to different database objects. Management-store errors fail
the request; there is no cache fallback. Permission checks precede identity
mismatch reporting. A mismatch rejects the complete request before document
access, including every change in Push. Ordinary Query and GET carrying this
header adopt the replication full-scope gate.

```text
authenticate -> authoritative database object -> active + full-scope access
             -> expected ID comparison -> local or trusted remote Query
             -> existing URL storage namespace
```

The response and cursor use the verified database identity. The namespace remains
the URL value because changing it to an ID address would select a different
existing storage namespace. This admission check prevents an old binding from
accessing a replacement already visible in management storage. It does not lease
the database or serialize data requests against concurrent deletion/reassignment.
A rejection cannot settle an earlier timed-out write. All participating Gateway
and Query nodes require a coordinated upgrade before exposing query-replication
protocol version 1.

### Projection and source binding

| Input | Matching-set projection |
|---|---|
| Matching live scan candidate or Watch document | Typed `upsert` |
| Nonmatching scan candidate | Advance scan progress without an event |
| Nonmatching live Watch document | ID-only `leave`, regardless of prior client membership |
| Logical deletion | ID-only `delete`; no fabricated document metadata |
| Progress frame | Advance the cursor, possibly with an empty events page |

A stateless leave can disclose an ID that never matched, so source filters cannot
replace full-scope authorization. Physical cleanup retains its existing
progress-only behavior. Existing current-state enrichment can temporarily produce
recreated/deleted/recreated states; source order and eventual convergence remain
the contract, not maximum document version.

The source hash binds version, actual database identity, collection, normalized
typed filters, effective ordering, and result limit. The hash uses existing query
normalization, including numeric equality and logical-ID tie breaking. Ordering
is part of source identity but does not sort the matching-set event stream.
Version-4 query cursors additionally retain generation and public phase alongside
existing Store progress. They require no service-instance memory.

```text
C0 -> bounded scan -> replay from C0 -> Watch caught-up proof -> live
          bootstrapComplete = false             |               |
                                                 +-- true -------+
```

`generationId` remains stable through scan, replay, and live. Only the Watch proof
establishes the first `bootstrapComplete`; later pages preserve that fact while
`caughtUp` continues to describe their current source progress. An empty page does
not imply completion. Expired history fails with explicit resynchronization; a
new initialization creates a new generation. Clients own durable activation of
rebuilt membership while preserving pending local edits.

Every candidate and frame consumes existing source work/byte limits, including
nonmatches. Encoded events and their generation/source envelope consume complete
response budgets. The cursor cannot pass an unreturned event. The original
collection Pull state machine below still owns source sequencing and failure
handling; matching-set mode projects its accepted prefix.

### Bounded result windows

`source.limit` selects a result window of 1–1000 documents. Each request carries
its own `requestId` and forbids a transfer limit or checkpoint. The existing
normalizer binds filters, effective order, and result size into sourceHash.
Default order is explicit logical ID ascending; other orders append that tie
breaker unless ID is already ordered. The same normalized order is executed,
including the ordinary Query requirement for a suitable index.

```text
authoritative identity + full-scope authorization
  -> one ordinary Query(filters, effective order, N)
  -> exactly N OR fewer than N with no continuation
  -> complete envelope budget validation
  -> replace(new generation, echoed requestId, complete=true, documents)
```

A short Query page with continuation returns `REPLICATION_WINDOW_INCOMPLETE`;
there is no successful incomplete window. Query pages are not combined into a
snapshot. Missing/unready indexes, Query work limits, source errors, and full
response encoding failures remain errors. The complete replace envelope observes
16 MiB JSON and 20 MiB protobuf limits; no documents are removed to make it fit.

Each success has a fresh generation and reports effectiveOrder. Window responses
have no event progress, source checkpoint, or bootstrap completion fields. The
consumer retains the active window on failure and rejects stale responses by
request/session identity. Replacing source membership does not delete documents
in the remote collection.

Window consistency is the ordinary Query model. Authoritative validation rejects
stale index candidates, but index lag can also omit an existing still-matching
member until its new posting arrives. Clients may observe exit followed by
reentry. No stricter source freshness fence or lifecycle lock is introduced; a
complete window is not a cross-document transaction snapshot.

Subsequent complete refreshes perform rank displacement and fill vacated slots.
SDK automatic refresh and durable membership activation remain part of local
replication integration. Realtime notifications serve as refresh hints alongside
polling; neither a live connection nor a notification is proof of a fresh window.

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
Push uses the same recursive value codec for request and conflict documents,
while retaining its action and version semantics.

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

| Contract | Rule |
|---|---|
| Route | `POST /replication/v1/databases/{database}/push` |
| Scope | One concrete collection; nonempty ordered changes |
| Action | Explicit `create`, `update`, or `delete`; missing/unknown values fail |
| Document | Typed object decoding to flattened data with required logical `id`; protected metadata is stripped |
| Version | Optional nonnegative typed int64 `document.version`, extracted before stripping |
| Validation | HTTP and Query validate the complete request before any storage operation |
| Batch | Sequential, nontransactional; conflicts do not stop later changes |

### Version and Action Semantics

| Request | Target | Result |
|---|---|---|
| Versioned update/delete, including zero | Live and exact version | Atomic conditional write |
| Versioned update/delete | Missing, tombstoned, or different version | Conflict |
| Unversioned update | Live | Update |
| Unversioned update | Missing or tombstoned | Existing create/recreate behavior |
| Unversioned delete | Live | Delete |
| Unversioned delete | Missing or tombstoned | Idempotent success |
| Create | Live | `already_exists`, regardless of supplied version; no write |
| Create | Missing or tombstoned | Existing create/recreate behavior; supplied valid version is ignored |

The shared recursive typed-value codec preserves nested int64 and finite float64
values across HTTP and gRPC. HTTP request documents must be typed objects, with
no ordinary-JSON fallback. A supplied version must be a nonnegative int64 encoded
as a canonical decimal string; null, float64, other scalar types, noncanonical
strings, negative values, and overflow reject the whole batch. Version extraction
precedes protected-field stripping; storage assigns resulting metadata.

Local and gRPC calls retain action and an optional int64 precondition, with no
negative sentinel or unspecified-action fallback. Ordinary CRUD formats remain
unchanged. The SDK's [private upstream adapter](../../sdk/002_replication_client.md#private-upstream-and-recovery)
now supplies typed encoding and native acknowledgement/recovery; the public
replica facade remains separate work.

Create checks for a live target before version comparison, so a create cannot
become an update even when content and version match. A supplied valid create
version is ignored. A retained tombstone conveys deletion rather than ownership
of the logical ID; it allows immediate same-ID recreation.

A versioned update/delete must not enter Create when its target disappears.
Push reads from the authoritative write source with tombstones included, then
applies the optional version condition in the atomic live-document mutation.
The read itself does not lock data or provide a linearizable snapshot.

```text
Validate complete batch
    -> Read target, including tombstones
    -> Apply action/version rule
         -> Conflict: retain request position and observed state
         -> Create: atomically insert or replace a still-deleted target
         -> Update/delete: enforce live/version predicate atomically
              -> Failed condition: reread authoritative state and report conflict
              -> Other storage error: fail request; earlier writes may remain
    -> Continue next change
```

Tombstone replacement during creation atomically requires a still-deleted target.
If another writer has recreated it, the write reports a conflict; the newly live
document cannot be overwritten by the stale replacement attempt. A failed
creation is reread once; `missing` or `tombstoned` describes that later state
and is not a ban on recreation. There is no automatic creation retry. Explicit
zero remains an equality condition for update/delete; create accepts valid
versions without using them as a condition.

### Encoded Message Budgets

| Boundary | Maximum |
|---|---|
| HTTP Push body | 10 MiB, including typed-value tags and the request envelope |
| Encoded protobuf request | 20 MiB, including typed data and envelope |
| Encoded protobuf conflict response | 20 MiB, including typed data and envelope |

Local and remote Query execution enforce the same encoded message budgets;
production gRPC receive limits admit messages within them. HTTP and protobuf
encoded sizes are checked independently; fitting the HTTP body cap does not waive
the protobuf budget.
An oversized request fails validation before storage access. An oversized conflict
response returns HTTP 422 `REPLICATION_BUDGET_EXCEEDED` without truncating
outcomes; earlier changes may already have committed.

## Conflict Handling

Each successful Push response contains a `conflicts` array of objects:

```json
{
  "conflicts": [
    { "changeIndex": 1, "id": "m2", "reason": "missing", "current": null }
  ]
}
```

| Field | Meaning |
|---|---|
| `changeIndex` | Zero-based request position, preserving duplicate-ID operations |
| `id` | Logical document ID |
| `reason` | `version_mismatch`, `missing`, `tombstoned`, `already_exists`, or `precondition_failed` |
| `current` | Typed object for a real live document or tombstone; raw JSON null for absence |

Conflict rendering takes logical ID and deletion state from validated storage
metadata; business-data keys cannot override either value.

After a failed mutation, the reread may observe a later state. A matching version
at reread does not convert failure into success: report `precondition_failed`
when no more specific reason applies. Non-conflict write errors and conflict-read
errors propagate. An unversioned delete followed by absence or a tombstone is
idempotent success.

Clients correlate outcomes by request position, then choose retry, merge, or
application-visible conflict handling. Missing targets have no fabricated
metadata. A lost response or a runtime error after earlier changes leaves an
ambiguous completed prefix; batches offer no exactly-once guarantee.

Structured conflicts replace the former document-only response. Gateway, Query,
and response consumers require a coordinated upgrade. The
[replication reference](../../../reference/replication.md#push-changes) owns full
request and response examples.

## Decision Ownership

The [Pull checkpoint decision](../../../../.agents/notes/implemented/bug-fix/2026-09-07-replication-pull-cursor-progress.md)
records native-source selection, alternatives, and accepted retention and client
integration limits. Public Pull and source capabilities have separate owners.

The [query-source decision](../../../../.agents/notes/implemented/feature/2026-09-18-query-replication-source.md)
owns matching-set events, complete query windows, generation completion, and
authoritative request identity. It extends public Pull without replacing its source guarantees.

The [Push conditional-write decision](../../../../.agents/notes/implemented/bug-fix/2026-09-07-replication-push-version-checks.md)
records conditional mutation semantics and conflict observations. The
[create-conflict decision](../../../../.agents/notes/implemented/bug-fix/2026-09-18-replication-push-create-conflict.md)
owns live-target rejection and required same-ID recreation after deletion.
The [HTTP decoder decision](../../../../.agents/notes/implemented/bug-fix/2026-09-07-http-push-version-preconditions.md)
records the original version-extraction and authoritative-read rationale.
The [HTTP typed-value decision](../../../../.agents/notes/implemented/bug-fix/2026-09-18-http-push-typed-values.md)
owns lossless HTTP document transport and the strict typed version encoding.
