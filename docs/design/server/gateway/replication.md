# Replication Design

This document details the replication HTTP protocol used by Syntrix. It separates the client-visible contract from internal storage structures to avoid leaking backend details.

## Goals

- Support RxDB-style pull/push replication over HTTP.
- Keep the wire format storage-agnostic (no internal IDs, fullpaths, parents).
- Resume current-state synchronization using opaque committed source positions.
- Surface conflicts without exposing storage internals.

## Endpoint Summary

| Operation | Endpoint |
|---|---|
| Pull | `POST /replication/v1/databases/{database}/pull` |
| Push | `POST /replication/v1/databases/{database}/push` |

## Document Shape (Flattened)

Decoded documents use a flattened object with reserved metadata fields. Pull
encodes each complete object using recursive typed values; Push requests and
conflicts retain plain JSON. The
[API reference](../../../reference/replication.md) owns exact wire examples.

- `id` (string): required document ID.
- `version` (int64): optional version precondition on push; server-owned version on complete pull states/conflicts.
- `updatedAt` (int64, millis): server update timestamp when metadata is available.
- `createdAt` (int64, millis): server creation timestamp when metadata is available.
- `collection` (string): collection path (returned on pull/conflicts).
- `deleted` (bool): present and true if the document is a tombstone.
- All other fields are user data.

## Pull

Pull synchronizes one concrete collection. The client submits a JSON body:

```json
{
  "collection": "room/chatroom-1/messages",
  "checkpoint": null,
  "limit": 100
}
```

The response contains typed `documents`, an opaque `checkpoint`, and `caughtUp`.
POST keeps potentially large source positions out of URLs. Omitted, null, or
empty checkpoints begin bootstrap; an omitted or zero limit selects 100, with a
maximum of 1,000. Numeric timestamp checkpoints require explicit reset.

### Responsibility and flow

| Component | Responsibility |
|---|---|
| Gateway | Authenticate, resolve database entity for authorization, retain URL identifier as storage namespace, validate bounded JSON, encode HTTP response |
| Query | Validate source page, wrap source position, admit complete frames within transport limits |
| Store ReplicationSource | Pair committed scans with replay, validate source/history, materialize logical state |
| Client | Atomically apply page states and checkpoint; preserve local unsent edits during reset |

```text
Initial request
      |
Store establishes committed C0
      |
Scan current documents in logical ID order
      |
Replay changes from original C0
      |
Continue from completed source prefixes
```

The source owns the overlapping scan/replay guarantee; Query never derives it
from timestamps or combines unrelated ordinary reads with a watch. Each request
processes one source page. There is no retained client session or instance
affinity. The [Store contract](../core/storage/03.stores.md#13-replicationsource)
owns adapter consistency, deletion identity, history checks, and source budgets.

### Page acceptance

- Query validates phase transitions, frame positions, logical state, and usage
  before accepting the page. A malformed source result fails as a whole.
- Each accepted frame supplies the continuation after all preceding work.
  Progress-only frames can advance without adding a document.
- The entire JSON envelope, cursor, and typed documents must fit 16 MiB, and the
  corresponding protobuf response must fit 20 MiB. If only a prefix fits, return
  that prefix's position and `caughtUp=false`; discard later prefetched work.
- A page-level terminal position or caught-up watermark is accepted only with
  the entire source page. A state too large for an empty page fails explicitly.
- Empty pages can advance without being caught-up. Only source proof establishes
  a watermark; continuous writes may keep caught-up false.
- Cancellation, source failure, and encoding failure publish no successful
  checkpoint. The client retains its previous durable position.

### State and authorization

Returned states may repeat or reflect writes newer than their triggering source
change. Applying pages yields eventual current-state convergence, not a fixed
historical snapshot or increasing document versions. Recreated documents can
restart their version sequence.

| Deletion condition | State |
|---|---|
| Retained tombstone | Logical identity, available metadata, `deleted: true`, no former business fields |
| Known identity but current record absent | Logical ID, collection, and `deleted: true`; omit unknown metadata |
| Physical cleanup event | Source progress only |
| Required identity unavailable | Resynchronization error without advancing past the unknown change |

Every request requires complete database access, established by database
ownership or a `db_admin` grant matching the canonical ID or validated slug.
Ordinary roles and per-document read permissions do not establish that grant.
The protocol has no document-permission membership/removal channel; filtered
replication would require an additional design. A cursor is never authorization.

The resolved database entity and the storage namespace are distinct. Existing
CRUD/Push use the URL's database identifier as their Store namespace, so Pull
preserves that identifier when selecting and reading its source. It does not
merge ID- and slug-keyed data or migrate either namespace. The public cursor
binds both namespace and entity ID: changing identifiers or reassigning a slug
to a different entity rejects continuation.

Native Puller checkpoints, capture buffers, CRUD, and Push write semantics remain
independent of this protocol. The
[Pull progress decision](../../../../.agents/notes/implemented/bug-fix/2026-09-07-replication-pull-cursor-progress.md)
records the native-source selection and alternatives.

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

## Checkpointing

| Layer | Ownership |
|---|---|
| Public version-2 cursor | Query binds URL storage namespace, resolved database entity ID, concrete collection, phase, and opaque source position |
| Native source position | Store binds source incarnation, continuation, and committed read context |
| Durable local checkpoint | Client commits it atomically with all applied states |

Clients retain cursors verbatim and never compare their encoded values for order.
Requests can move between Gateway/Query instances using the same source. Invalid
or cross-scope cursors fail; source replacement, unavailable history, or missing
identity requires a deliberate new bootstrap. Old timestamp checkpoints have no
implicit conversion. Reset rebuilds the server mirror while retaining unsent
local edits for reconciliation.

Requests are bounded to 1 MiB, public cursors to 256 KiB. Source work shares a
30-second hard deadline with a five-second soft budget; the
[source contract](../core/storage/03.stores.md#bounds-and-failures) defines
completed-prefix behavior and separately bounded cleanup.

## Conflict Handling

- Push may return `conflicts` containing the authoritative server documents in flattened form.
- Clients decide whether to retry, merge, or surface conflicts.
- The initial not-found path can enter Create before checking the version, including
  for deleted targets. Concurrent deletion can still leave incomplete conflict
  results when the authoritative lookup reports absence. Strict create/update/delete predicates, authoritative
  missing/tombstone results, and structured conflict reasons remain in the
  [version-check proposal](../../../../.agents/notes/proposed/bug-fix/2026-09-07-replication-push-version-checks.md).

## Error Handling

| Pull failure | HTTP outcome |
|---|---|
| Invalid request, cursor, or logical scope | 400 |
| Authentication or complete-scope access denied | 401 / 403 |
| Timestamp checkpoint, replaced source, lost history or identity | 409 `RESYNC_REQUIRED` |
| Request/cursor bytes exceed limits | 413 |
| Required source unit cannot fit supported bounds | 422 |
| Unsupported source capability | 501 |
| Source unavailable | 503 |
| Canceled / deadline exceeded | 499 / 504 |
| Invalid source state or encoding | 500 |

gRPC carries typed recovery categories so remote and local Query calls retain
the same public behavior. Diagnostics contain request ID, hashed scope and
checkpoint identities, phase, counts, end reason, and duration; raw positions,
source causes, and document payloads are excluded from transport errors.
Push validation failures use 400; conflicts remain HTTP 200 with `conflicts`.

## Notes for Implementers

- Do not include storage-internal fields in any response or request validation.
- Keep batch sizes modest (100–500) for IndexedDB performance when using RxDB Dexie.
- A minimal deletion requires only logical identity and `deleted: true`; clients
  must not require historical version or timestamps to remove a document.
