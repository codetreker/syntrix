# Replication Design

Replication Pull uses committed Store Watch progress to synchronize current
document state. Push preserves explicit actions and optional version conditions
through atomic writes and structured conflict responses.
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

| Contract | Rule |
|---|---|
| Route | `POST /replication/v1/databases/{database}/push` |
| Scope | One concrete collection; nonempty ordered changes |
| Action | Explicit `create`, `update`, or `delete`; missing/unknown values fail |
| Document | Flattened data with a required logical `id`; protected metadata is stripped |
| Version | Optional exact nonnegative int64 `document.version`, extracted before stripping |
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
| Create | Live | Existing update behavior; supplied version is an equality condition |
| Create | Missing or tombstoned | Existing create/recreate behavior; supplied valid version is ignored |

The raw JSON decoder preserves exact version presence and value before ordinary
business-number decoding. Null, non-integer, negative, or out-of-range versions
reject the batch. Ordinary business numbers retain their existing representation;
storage assigns resulting metadata. Local and gRPC calls retain action and an
optional int64 precondition, with no negative sentinel or unspecified-action
fallback. Push-specific protobuf document data uses recursive typed values in
requests and conflict responses, preserving nested int64 values separately from
float64. HTTP keeps ordinary flattened JSON; this internal encoding is not an
additional HTTP request format or an SDK bigint write contract.

A versioned update/delete must not enter Create when its target disappears.
Push reads from the authoritative write source with tombstones included, then
applies the optional version condition in the atomic live-document mutation.
The read itself does not lock data or provide a linearizable snapshot.

```text
Validate complete batch
    -> Read target, including tombstones
    -> Apply action/version rule
         -> Conflict: retain request position and observed state
         -> Write: enforce live/version predicate atomically
              -> Failed condition: reread authoritative state and report conflict
              -> Other storage error: fail request; earlier writes may remain
    -> Continue next change
```

Tombstone replacement during creation atomically requires a still-deleted target.
If another writer has recreated it, the write reports a conflict; the newly live
document cannot be overwritten by the stale replacement attempt. Explicit zero
and create/version 1 retain their accepted meanings. New insert-only rules remain
[proposed](../../../../.agents/notes/proposed/feature/2026-09-07-replication-push-insert-only.md).

### Encoded Message Budgets

| Boundary | Maximum |
|---|---|
| HTTP Push body | 10 MiB |
| Encoded protobuf request | 20 MiB, including typed data and envelope |
| Encoded protobuf conflict response | 20 MiB, including typed data and envelope |

Local and remote Query execution enforce the same encoded message budgets;
production gRPC receive limits admit messages within them. Typed-value expansion
means the HTTP body cap does not guarantee that every body below it is accepted.
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
| `current` | Real flattened live document or tombstone; null for absence |

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

The [Push conditional-write decision](../../../../.agents/notes/implemented/bug-fix/2026-09-07-replication-push-version-checks.md)
records action semantics, conflict observations, and deferred insert-only creation.
The [HTTP decoder decision](../../../../.agents/notes/implemented/bug-fix/2026-09-07-http-push-version-preconditions.md)
records why raw version extraction remains local to replication decoding.
