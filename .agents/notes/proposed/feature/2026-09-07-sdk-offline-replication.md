# Agent Note: SDK Offline Replication

Status: proposed

## Problem

The SDK's manual Pull can fetch a validated page, but applications still own local
state application, checkpoint persistence, account isolation, and pending writes.
The original coordinator helpers do not provide a working durable synchronization
loop. These obligations recur across applications and need a supported local
API with lossless values, crash recovery, and dynamic local query results.

The [private native runtime](../../implemented/architecture/2026-09-18-sdk-native-replication-runtime.md)
provides bounded replication inputs, durable completion hooks, failure isolation,
and a bundled patched dependency. It is not yet connected to Syntrix source/Push
HTTP adapters or a public local database API. This proposal owns that remaining
integration; the runtime note owns the delivered mechanism. The
[query-source contract](../../implemented/feature/2026-09-18-query-replication-source.md)
now supplies matching-set events, completed generations, and authoritative bound
database checks. Its SDK adapters and result-window execution remain outstanding.

## Proposal

Provide two explicit application paths: existing REST references for direct
remote reads/writes, and an SDK-owned local database for durable replication and
local queries. RxDB remains an internal implementation detail. Push is internal
to replication; no public manual Push method is required.

| Remaining capability | Required behavior |
|---|---|
| Remote sources | Connect the delivered matching-set HTTP source to a named local collection alias; add bounded result-window execution and its adapter |
| Membership | Apply delivered source upsert/leave/delete events and activate durable generations without treating every membership exit as a document deletion |
| Local operations | Persist offline CRUD and support dynamic local query/watch results |
| Identity | Isolate endpoint, account, bound database identity, source, and local alias; obsolete work cannot apply or send across reassignment |
| Initialization | Activate a complete source generation durably before uploading, while preserving pending local edits |
| Upstream | Use native durable changed-document scanning and acknowledgement metadata; keep newer local edits when an earlier write is acknowledged |
| Recovery | Restart from reliable metadata, reconcile uncertain outcomes, and retain pending edits during source rebuild |
| Lifecycle | Coordinate browser ownership, cancellation, cleanup, and bounded retention |

Local document identity is the logical document segment of its collection path.
Tombstones communicate deletion and must permit recreation with the same ID.
Versions may reset on recreation; they cannot serve as a global replication order
or a permanent document-lifecycle identity.

### Lossless Local Storage and Write Transport

Manual Pull and Query decode int64 values as bigint. Ordinary SDK document
`set`/`update` still use JSON serialization, which rejects bigint. Converting to
Number loses precision and numeric type. The local representation and the internal
Push encoder must preserve both across edits, restart, retry, and conflicts.

The [typed HTTP Push transport](../../implemented/bug-fix/2026-09-18-http-push-typed-values.md)
already accepts the recursive typed representation used by Pull. The SDK's
outbound encoder and HTTP replication adapter remain unimplemented. Ordinary CRUD
retains its JSON format; a complete Pull response is not a Push request.

Conflicts correlate by zero-based request position, including repeated IDs.
Preserve the reason and nullable current state; authoritative absence must not be
passed to a document upsert. An acknowledged write advances only the native
metadata for that acknowledged local state, not a later local edit.

### Bounded Persistence and Lifecycle

Use the delivered runtime's page backpressure, fresh-source readiness, dirty-bit
scheduling, and cancellation/drain contract. Durable local state and native
metadata own pending work; there is no additional SDK Outbox requirement.

The local storage layer still needs explicit row admission, schema and cleanup
rules. Query scans and retained payload/order-key caches need independent byte
limits and generation invalidation across tabs. Replication scan bounds alone do
not establish those guarantees. The public API also needs browser capability
checks and clear failure behavior for quota exhaustion or unavailable storage.

## Alternatives

**Application-owned synchronization:** keeps the SDK smaller but requires each
application to solve checkpoint ordering, durable acknowledgement, and account
isolation. The SDK-owned local database supplies these recurring obligations.

**Realtime events as the local source of truth:** lowers pull traffic but cannot
recover omitted events using authoritative source progress. Realtime schedules
source reconciliation.

**A separate SDK Outbox:** was part of the original coordinator proposal. The
selected native runtime already persists changed-document delivery and
acknowledgement metadata; maintaining a second queue duplicates those obligations.

**Convert bigint to Number:** avoids a serialization exception but loses integer
precision and the int64/float64 distinction. A lossless codec must be used through
persistence, wire encoding, and conflict application.

## Acceptance Criteria

- Local writes and replication progress survive reload, including crashes around
  local persistence and remote acknowledgement; newer edits remain pending.
- Query sources maintain membership and local aliases; generation activation
  updates local watches without requiring a subsequent business write.
- Logical deletion, same-ID recreation, conflicts, duplicate retries, and source
  reset preserve the defined state without silently losing pending edits.
- Endpoint/account/database/source identities remain isolated across reconnects,
  alias reassignment, and concurrent tabs; shutdown prevents new admission and
  drains owned work.
- Int64 metadata and nested business values round-trip exactly, including values
  outside Number's safe-integer range.
- Persistent reads, replication work, local query caches, and retained metadata
  enforce their own budgets and expose failures without truncating data.
- The public package exposes local database and query/watch capabilities without
  requiring application-managed RxDB or a public manual Push API.

## Dependencies

- [Private runtime](../../implemented/architecture/2026-09-18-sdk-native-replication-runtime.md)
  owns native protocol reliability and its publishable dependency bundle.
- [Push version checks](../../implemented/bug-fix/2026-09-07-replication-push-version-checks.md),
  [create conflicts](../../implemented/bug-fix/2026-09-18-replication-push-create-conflict.md),
  and [typed HTTP Push](../../implemented/bug-fix/2026-09-18-http-push-typed-values.md)
  own existing server write guarantees.
- [Pull cursor progress](../../implemented/bug-fix/2026-09-07-replication-pull-cursor-progress.md)
  owns the existing source cursor and manual transport. The
  [query-source contract](../../implemented/feature/2026-09-18-query-replication-source.md)
  owns matching-set projection and request identity checks; local membership,
  bounded windows, and automatic SDK adapters remain additional work.
- [Realtime resume](2026-09-07-realtime-client-resume.md) owns transport recovery;
  notifications do not replace authoritative source reads.

## Risks

Local storage limits, cross-tab ownership, source initialization, and conflict
retries can stall synchronization. Adapter failures must remain visible with
pending work preserved. An uncertain remote write does not imply exactly-once
delivery, and clearing browser site data removes the local database.
