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
and a bundled patched dependency. The public replica database API remains
outstanding; the runtime note owns the delivered mechanism. The
[query-source contract](../../implemented/feature/2026-09-18-query-replication-source.md)
now supplies matching-set events, complete result windows, generation identity,
and authoritative bound database checks. The [private downstream coordinator](../../implemented/architecture/2026-09-19-sdk-downstream-replication.md)
connects these HTTP sources with member activation, pins, polling, read retry and
native leadership. The
[private alias storage](../../implemented/architecture/2026-09-18-sdk-replica-storage.md)
now owns local identity, typed records, CAS CRUD, bounded storage access, and clean
physical compaction. The [private query client](../../implemented/architecture/2026-09-18-sdk-replica-query-watch.md)
owns exact local query/watch, bounded shared indexes and generation reconciliation.
[Private upstream and recovery](../../implemented/architecture/2026-09-20-sdk-upstream-replication.md)
now provide typed HTTP Push, whole-phase failure protection and explicit recovery.
This proposal retains the public facade, notifications with matching source
authorization, and complete browser-to-server validation.

## Proposal

Provide two explicit application paths: existing REST references for direct
remote reads/writes, and an SDK-owned replica database for durable replication and
local queries. RxDB remains an internal implementation detail. Push is internal
to replication; no public manual Push method is required.

| Remaining capability | Required behavior |
|---|---|
| Remote sources | Expose source builders and alias creation over delivered HTTP downstream; connect notifications only with matching source authorization |
| Local operations | Expose delivered private CRUD and query/watch through the public replica API |
| Lifecycle | Expose delivered pause/resume, inspection and recovery with their identity/token and explicit-authorization requirements |
| End-to-end integration | Validate the public composition against the real server across browser reload, reconnect, multiple tabs and recovery |

Local document identity is the logical document segment of its collection path.
Tombstones communicate deletion and must permit recreation with the same ID.
Versions may reset on recreation; they cannot serve as a global replication order
or a permanent document-lifecycle identity.

### Lossless Replica Storage and Write Transport

Manual Pull and Query decode int64 values as bigint. Ordinary SDK document
`set`/`update` still use JSON serialization, which rejects bigint. Converting to
Number loses precision and numeric type. Private alias storage preserves these
values as typed JSON strings. The implemented private upstream adapter retains
that representation across retry and conflicts; the public facade must preserve it.

The [typed HTTP Push transport](../../implemented/bug-fix/2026-09-18-http-push-typed-values.md)
accepts the recursive typed representation used by Pull. The private SDK adapter
now encodes and bounds those requests, with native success and explicit recovery
owned by the upstream decision. Ordinary CRUD retains its JSON format; a complete
Pull response is not a Push request.

Conflicts correlate by zero-based request position, including repeated IDs.
Preserve the reason and nullable current state; authoritative absence must not be
passed to a document upsert. An acknowledged write advances only the native
metadata for that acknowledged local state, not a later local edit.

### Bounded Persistence and Lifecycle

Use the delivered runtime's page backpressure, fresh-source readiness, dirty-bit
scheduling, and cancellation/drain contract. Durable local state and native
metadata own pending work; there is no additional SDK Outbox requirement.

Private alias storage supplies final-row admission, fixed records, paired metadata
cleanup, indexed bounded reads, and row/manifest invalidations. Private queries
provide their own serialized read pool, retained payload/order-key limits and
bounded generation rebuild across tabs. Public integration must preserve those
query guarantees. The public API also needs browser capability
checks and clear failure behavior for quota exhaustion or unavailable storage.

## Alternatives

**Application-owned synchronization:** keeps the SDK smaller but requires each
application to solve checkpoint ordering, durable acknowledgement, and account
isolation. The SDK-owned replica database supplies these recurring obligations.

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

The public facade and complete browser-to-server path must demonstrate these
delivered private guarantees together:

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
- The public package exposes replica database and query/watch capabilities without
  requiring application-managed RxDB or a public manual Push API.

## Dependencies

- [Private runtime](../../implemented/architecture/2026-09-18-sdk-native-replication-runtime.md)
  owns native protocol reliability and its publishable dependency bundle.
- [Private alias storage](../../implemented/architecture/2026-09-18-sdk-replica-storage.md)
  owns namespace/session isolation, typed records, raw CAS, bounded persistence,
  and clean physical compaction.
- [Private upstream and recovery](../../implemented/architecture/2026-09-20-sdk-upstream-replication.md)
  owns typed requests, true native acknowledgements, conditional retries, durable
  phase protection and explicit recovery intents.
- [Push version checks](../../implemented/bug-fix/2026-09-07-replication-push-version-checks.md),
  [create conflicts](../../implemented/bug-fix/2026-09-18-replication-push-create-conflict.md),
  and [typed HTTP Push](../../implemented/bug-fix/2026-09-18-http-push-typed-values.md)
  own existing server write guarantees.
- [Pull cursor progress](../../implemented/bug-fix/2026-09-07-replication-pull-cursor-progress.md)
  owns the existing source cursor and manual transport. The
  [query-source contract](../../implemented/feature/2026-09-18-query-replication-source.md)
  owns matching-set projection, complete result windows, and request identity
  checks. The [private downstream coordinator](../../implemented/architecture/2026-09-19-sdk-downstream-replication.md)
  supplies local membership, window refresh scheduling and HTTP source integration.
- [Realtime resume](2026-09-07-realtime-client-resume.md) owns transport recovery;
  notifications do not replace authoritative source reads.

## Risks

Local storage limits, cross-tab ownership, source initialization, and conflict
retries can stall synchronization. Adapter failures must remain visible with
pending work preserved. An uncertain remote write does not imply exactly-once
delivery, and clearing browser site data removes the local database.
