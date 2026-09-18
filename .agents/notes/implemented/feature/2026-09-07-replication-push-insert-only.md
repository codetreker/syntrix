# Agent Note: Explicit Replication Create Conditions

Status: implemented

## Problem

Push's default create policy can replace a retained tombstone or update an
existing live target. A caller could not express either an atomic insert into an
unoccupied identity or intentional recreation of a particular retained tombstone.
The [conditional-write repair](../bug-fix/2026-09-07-replication-push-version-checks.md)
protected versioned update/delete, but did not supply those creation guarantees.

## Decision

**Scope: Stage 1B — explicit server-side Push create conditions.** Each change
may carry `createCondition` alongside its action and typed document. HTTP and
local/gRPC Query calls retain the condition through the existing Store.Create
operation. No public manual Push API, internal SDK Pusher, or RxDB integration is
included in this decision.

| `createCondition` | Required action/version | Atomic storage behavior |
|---|---|---|
| Omitted | Existing action/version rules | Preserve default create, update, and delete behavior |
| `absent` | Create, version omitted | Insert only; a live record or retained tombstone occupies the identity |
| `tombstone` | Create, nonnegative version supplied | Replace only a retained tombstone with that exact version; never insert if absent |

HTTP null, empty, unknown, or non-string conditions fail validation. A condition
on update/delete is invalid. `absent` with any version is invalid; `tombstone`
requires a nonnegative typed int64 version. Query validates the whole request
before storage access. Protobuf uses an optional enum: omission retains default
behavior; explicitly unspecified and unknown enum values are invalid.

Omitted conditions preserve all previously accepted inputs, including create
with version 0 or 1 and update/delete with version 0. The original proposal's
version-zero overload and newly forbidden action/version combinations were not
adopted. The explicit condition carries the additional guarantee without changing
what an existing version value means.

### Store enforcement

`Create` accepts at most one `CreateOptions{Condition, ExpectedVersion}` value.
Omission or the zero value preserves its default insert-or-replace-tombstone
behavior. `ExpectedVersion` is allowed and required only with `tombstone`.
Routing preserves the options to the selected database write source.

- `absent` performs a pure atomic insert. Duplicate occupancy is a conflict, with
  no tombstone replacement fallback.
- `tombstone` performs an atomic replacement matching database, document identity,
  `deleted=true`, and the expected version. It has no upsert/insert fallback.
- The expected version constrains the existing tombstone; storage assigns the
  recreated document's metadata according to its existing initialization rules.
- These predicates apply in the actual write even after an authoritative initial
  read. The read does not lock the target.

### Conflict observations

The existing structured conflict shape and reasons remain in use. Initial and
failed-write reads use the authoritative source with tombstones visible:

| Current observation for a rejected conditional create | Reason/current |
|---|---|
| Missing | `missing` / raw JSON null |
| Retained tombstone, including a version mismatch | `tombstoned` / actual typed tombstone |
| Live document | `already_exists` / actual typed live document |

A later read can differ from the state that caused a failed write. It does not
turn the failure into success or authorize a retry. Non-conflict write errors
and conflict-read failures propagate; no document is fabricated. Request position
continues to distinguish multiple changes to one logical ID. Batches remain
ordered and nontransactional, with a possibly committed prefix on runtime error.

## Alternatives

**Interpret create/version zero as insert-only and reject positive create
versions or zero update/delete versions.** This was the original proposal. It
would provide an insertion signal but reinterpret already accepted inputs and
remove unrelated version combinations. Explicit conditions preserve those inputs
and distinguish absence from an intentional tombstone recreation.

**Preserve only default create semantics.** This avoids a new option but leaves
callers unable to require absence or a particular tombstone at the write instant.

**Require versions for every push.** This would change unconditional update/delete
use cases. The chosen conditions express the two creation guarantees without
changing that wider policy.

## Consequences

- Concurrent absent creates cannot overwrite the winning live document or a
  retained tombstone. Conditional recreation cannot overwrite a live target or
  a tombstone with a different version at the write instant.
- Gateway and Query must support the condition end to end before consumers use
  it; sending to an older peer that ignores the new field loses the guarantee.
- Physical purge removes deletion history. After purge, absent creation may
  succeed, while tombstone recreation conflicts as missing.
- Version equality is not a generation identifier. Delete/recreate/delete cycles
  can reuse a version, so the tombstone condition does not prevent ABA across
  distinct document lifetimes.
- A lost response remains ambiguous; replaying a successful request may conflict.
  This change adds no global revision, idempotency key, or exactly-once guarantee.
- The [SDK offline proposal](../../proposed/feature/2026-09-07-sdk-offline-replication.md)
  remains open. Its internal Pusher must preserve explicit creation intent through
  its durable queue and retries; server predicates alone do not implement it.

The [replication reference](../../../../docs/reference/replication.md#create-conditions)
owns the HTTP validation rules and examples. The
[typed transport decision](../bug-fix/2026-09-18-http-push-typed-values.md) owns numeric
encoding; this decision preserves those codecs and byte budgets.
