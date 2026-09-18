# Agent Note: Preserve Replication Push Version Preconditions

Status: implemented

## Problem

The [HTTP precondition repair](2026-09-07-http-push-version-preconditions.md)
preserved exact optional versions and selected the write source for initial and
conflict reads. Query still took its not-found/Create branch before checking the
precondition. Ordinary reads hid tombstones, which Create could replace. A stale
conditional update or delete could recreate a missing or deleted target.

After a failed write predicate, actual absence could disappear from the
document-only conflict array. The transport also lost the requested action, so
Query could not distinguish an intentional create from an update or delete.
Replacing a tombstone without an atomic deletion predicate could overwrite a
concurrently recreated live document.

## Decision

### Action and optional versions

HTTP, local Query calls, and gRPC preserve an explicit create/update/delete action.
Missing and unknown actions fail validation. The existing exact nonnegative
`document.version` precondition remains optional; protobuf uses `optional int64`, with
no negative sentinel or omitted-action fallback. Query validates the entire
batch before its first storage operation, including direct callers.

Push-specific protobuf document data uses the existing recursive typed-value
codec in both requests and current conflict documents. This preserves nested
int64 values and their distinction from integral float64 values across gRPC;
ordinary JSON decoding would otherwise round values or change runtime types.
Nil data is encoded as typed null. Untyped legacy Push data is rejected as part
of the coordinated protocol change. The later
[typed HTTP Push decision](2026-09-18-http-push-typed-values.md) uses this codec for
HTTP documents and non-null conflict state as well. The decoded document remains
flattened; SDK outbound bigint encoding remains separate work.

| Request | Target | Result |
|---|---|---|
| Versioned update/delete, including zero | Live with matching version | Atomic conditional mutation |
| Versioned update/delete | Missing, tombstoned, or different version | Conflict; never create |
| Unversioned update | Live | Unconditional update |
| Unversioned update | Missing or tombstoned | Existing create/recreate behavior |
| Unversioned delete | Live | Delete |
| Unversioned delete | Missing or tombstoned | Idempotent success |
| Explicit create | Missing or tombstoned | Existing create/recreate behavior; supplied valid version is ignored |
| Explicit create | Live | Existing update behavior, conditional when a version is supplied |

Storage assigns resulting versions. Explicit zero remains an equality condition
on live targets. Strict insert-only create and new invalid action/version
combinations are separately [proposed](../../proposed/feature/2026-09-07-replication-push-insert-only.md).

### Encoded message limits

Push requests and conflict responses each have a 20 MiB encoded protobuf budget,
including the message envelope and typed document data. Local Query calls apply
the same budget as gRPC calls; production gRPC receive limits admit those messages.
This prevents deployment mode from changing which otherwise valid Push messages
can be processed. The HTTP request body retains its independent 10 MiB limit, including its typed
JSON envelope. Encoded HTTP and protobuf sizes are checked independently; the
body limit does not waive the protobuf budget.

Oversized encoded requests fail validation with HTTP 400 before any storage
operation. An oversized conflict response fails with a work-limit error, mapped
to HTTP 422 `REPLICATION_BUDGET_EXCEEDED`, without truncating conflicts. As with other runtime errors, earlier changes may already have committed.

### Atomic writes and conflict observations

Initial and conflict reads use the database's authoritative write source with
`ShowDeleted: true`. Update/Delete retain atomic scope, live-document, and optional
version predicates. Reads do not lock the document or establish linearizability.
A failed predicate or missing write target triggers another authoritative read;
non-conflict storage errors and failed conflict reads propagate.

Create retains tombstone replacement, but replacement must atomically match
`deleted=true` and verify that a record matched. A competing live recreation
returns `ErrExists`; Push reads actual current state before reporting its conflict.
It never fabricates a tombstone or document after a failed write.

Each conflict contains the zero-based request `changeIndex`, logical document
`id`, `reason`, and nullable `current`. Request position distinguishes repeated
changes to the same ID. The HTTP current document now uses a typed object with
flattened decoded fields, as defined by the later typed transport decision.
Tombstones contain their real retained metadata, and absence is raw JSON null.
Conflict rendering takes ID and deletion state from validated storage metadata,
preventing business-data keys from overriding their authoritative values.

| Reason | Observed outcome |
|---|---|
| `missing` | Target absent |
| `tombstoned` | Retained tombstone |
| `version_mismatch` | Live target differs from the supplied version |
| `already_exists` | Create/recreate lost to an existing live target |
| `precondition_failed` | Mutation failed, but the subsequent read cannot identify a more specific cause |

The current document is an observation made after the failure, not an atomic
snapshot of the failure instant. In particular, a subsequent matching version
does not turn a failed write into success or authorize a retry. An unversioned
delete whose failed write is followed by absence or a tombstone succeeds
idempotently.

## Alternatives

**Introduce a separate public `baseVersion` field.** This separates metadata from
payload, but changes the established flattened protocol when its existing
version field already has the necessary meaning.

**Require versions for every push.** This strengthens unconditional-write
protection but changes documented optional behavior and requires a separate
client-contract decision.

**Represent missing targets as fabricated tombstones.** This preserves the old
conflict-array shape, but invents authoritative version and deletion metadata.
Structured conflict outcomes preserve absence and retained deletion separately.

**Introduce strict insert-only create together with the repair.** This changes
accepted create/version combinations and tombstone recreation policy. The
separate proposal retains that decision and its future atomic-write requirements.

## Consequences

- Conditional update/delete cannot enter Create after an absent or tombstoned
  initial read. Read/write races still meet the atomic write predicate, and
  missing conflict observations remain visible.
- Valid changes execute in request order and continue past per-item conflicts.
  Batches are nontransactional: a later runtime error can leave a completed
  prefix, even though invalid input prevents all writes.
- The structured conflict response and explicit action/version presence change
  the HTTP/gRPC contract. Gateway, Query services, and consumers require a
  coordinated upgrade; there is no legacy-message fallback.
- Exact precondition presence remains separate from stored document metadata.
  The durable SDK Pusher and its lossless outbound bigint encoder remain owned by
  [SDK offline replication](../../proposed/feature/2026-09-07-sdk-offline-replication.md).
- A lost success response may produce a conflict on retry. Version equality
  does not provide exactly-once execution or document-generation identity.

The [replication reference](../../../../docs/reference/replication.md#version-preconditions)
owns the consumer contract. The earlier HTTP decision retains the rationale for
local raw-field extraction and authoritative read routing.
