# Agent Note: Define Insert-Only Replication Push

Status: rejected - Tombstones must permit same-ID recreation; tombstone occupancy and version-zero insertion semantics contradict that requirement.

## Problem

Push preserves its existing create policy: create can replace a retained
tombstone and update an existing live target. Applications cannot express an
atomic insert-only operation through the current replication action/version
contract. Version zero currently means equality on an existing live target;
`create` with version 1 remains accepted.

The [conditional-write repair](../../implemented/bug-fix/2026-09-07-replication-push-version-checks.md)
fixes unintended recreation by versioned update/delete. It does not supply this
additional creation guarantee.

## Proposal

Define the action/version combinations as a coordinated consumer-contract change:

- `create` with version zero inserts only when no live document or retained
  tombstone exists. The absence check and insertion must be atomic.
- Positive versions on create and zero on update/delete become invalid input,
  rejected before any write in the batch.
- Unversioned operations preserve their documented unconditional policy unless
  a separate decision changes it.
- Conflicts retain request position, logical identity, reason, and real nullable
  current state. A retained tombstone conflicts; physical cleanup can make the
  same logical identity eligible for a later insert.

This is a deferred guarantee, with an intentional future implementation cost:
the tombstone-replacing Create operation cannot implement insert-only safely.
Delivery requires a storage write predicate or insertion capability that treats
both live records and tombstones as occupied, plus coordinated Query, HTTP/gRPC,
and consumer validation updates. The existing explicit action and optional
version fields preserve the ability to make that decision without overloading
absence or a negative sentinel. They do not already provide insert-only behavior.

## Alternatives

**Preserve current create semantics.** This keeps accepted create/version inputs
and recreation behavior, but provides no insert-only guarantee. It remains the
implemented behavior until the new contract is approved.

**Require versions for every push.** This would also change unconditional
update/delete use cases; the proposed insert-only rule does not need that broader
policy change.

## Acceptance Criteria

- Two concurrent version-zero creates cannot overwrite each other.
- Existing live records and retained tombstones return explicit conflicts.
- Physically absent records can be created without an intervening overwrite race.
- Invalid action/version combinations reject the whole request before any write.
- Local and gRPC routes preserve action and presence; consumers agree on the new
  semantics, with no silent reinterpretation of old messages.

## Risks

Existing clients using create/version 1 or update/delete/version zero would be
rejected. Deployment must coordinate that semantic change. Tombstone expiration
removes deletion history, so insert-only does not imply a permanently unused ID.
A lost response remains ambiguous; this proposal does not add exactly-once effects.
