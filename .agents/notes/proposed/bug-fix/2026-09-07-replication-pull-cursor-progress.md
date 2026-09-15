# Agent Note: Make Replication Pull Checkpoints Advance Reliably

Status: proposed

## Problem

Pull filters `updatedAt >= checkpoint`, sorts by timestamp and ID, and returns
only the last timestamp as its checkpoint. A page filled by equal timestamps
can repeat indefinitely. Strict greater-than would omit remaining documents at
that timestamp. Wall-clock values also cannot establish commit order: a write
can receive an earlier timestamp and commit after Pull has passed it.

The [replication design](../../../../docs/design/server/gateway/replication.md)
requires a deterministic continuation. Initial scanning and incremental replay
must cover concurrent mutations without relying on timestamp order or one
service instance's lifetime.

## Proposal

Use storage-native committed history through the existing Store Watch, with an
explicit scan phase followed by ordered replay. The
[Watch scan-boundary extension](../../implemented/architecture/2026-09-15-watch-scan-boundary.md)
provides the source capabilities; public Pull integration remains proposed.

```text
Watch(StartForScan) -> initial C0
                       |
        ScanDocuments(AtLeast=C0, AfterID)
                       |
              Watch(after=C0)
                       |
           ordered incremental Pull pages
```

| Concern | Required behavior |
|---|---|
| Public position | Versioned opaque cursor binds database, collection, phase, and source checkpoint |
| Scan | Retain original C0, advance exclusive logical-ID continuation only past returned candidates |
| Replay | Resume C0 inclusively, then advance only past completed frames |
| State | Use Watch's committed current-or-later document enrichment; allow duplicates and eventual convergence |
| Logical delete | Preserve nil event Document and use separate logical identity copied from StoredDoc |
| Physical cleanup | Advance source progress without a second business deletion |
| Budgets | Query owns frame, source-byte, document and encoded-response limits; unreturned events cannot advance the public cursor |
| Caught-up | Require explicit Watch watermark proof; an empty filtered page may advance without being caught up |
| Recovery | Malformed/scope-mismatched cursors fail; expired history, replaced source or missing required identity/payload require resynchronization |
| Client application | Atomically apply page state and persist checkpoint; failed application retains the previous checkpoint |

Update HTTP, protobuf, the manual SDK Pull API, and their reference contracts
together. The public field remains a string but ceases to be a stringified int64;
old numeric positions require an explicit reset, without runtime reinterpretation.
Preserve pending local edits during a reset. Phase and progress diagnostics must
exclude raw checkpoint and document content.

Permission scope remains unresolved. Full-scope replication needs an explicit
authorization rule on every request; authentication alone does not grant database
access. Per-document authorization additionally requires membership exits and
permission-change recovery, and must not be claimed by a full-scope protocol.

## Alternatives

**Strict `(updatedAt, id)` continuation.** Repairs static tied pages, but delayed
commits and equal-timestamp writes behind the tuple can still disappear.

**Transactional replication revision.** Atomically committing a scope counter
with each document can order current-state scans without a full event journal.
It adds transaction retries and scope-local counter contention to writes, and
requires tombstone cleanup coordinated with a retained revision floor. Native
committed history preserves the current write model. This choice does not assume
that arbitrary direct writes to the source are a supported product feature.

**Parallel ReplicationSource.** Separate bootstrap/scan/change methods duplicate
existing Watch lifecycle, checkpoints, source errors, and scanning mechanisms.
The selected Watch extension gives these existing owners the missing consistency
guarantees while Query owns public phases and response budgets.

**Puller-local history as the replication source.** It adds dependencies on local
buffer continuity, retention, replay and instance replacement. Source-owned Watch
checkpoints allow cross-instance requests directly; Puller-local history is not
a prerequisite of this proposal, and native Puller checkpoints stay unchanged.

## Acceptance Criteria

- More than one page of equal-timestamp documents terminates with all state synchronized.
- Delayed commits, concurrent scan writes, deletion/recreation and rollback converge without permanent omissions.
- Source checkpoints resume across service/client replacement; replaced sources fail explicitly.
- Empty filtered pages advance safely without claiming caught-up; responses never skip unreturned records.
- Expired, malformed, old-format and cross-scope checkpoints have explicit documented outcomes.
- HTTP/gRPC and manual SDK preserve typed document values and bounded response semantics.
- Authorization follows the approved permission scope; no unresolved policy is presented as implemented.

## Risks

The public checkpoint and request protocol change. Scanning can outlive retained
history; actual replay may discover expiry after substantial scan work. Required
logical identity can become unavailable before source history expires. Neither
condition permits silently restarting at the present. Watch opens and committed
reads add source load; no fixed offline recovery period or performance equivalence
is promised. Continuous writes can prevent a caught-up watermark.

## Dependencies and Scope

The implemented Watch extension is the source dependency.
[Query pagination](../../implemented/feature/2026-09-07-query-cursor-pagination.md)
provides public query traversal but does not itself repair Pull checkpoints.
The [SDK offline replication proposal](../feature/2026-09-07-sdk-offline-replication.md)
owns the durable local coordinator/outbox; manual Pull transport alone does not
deliver those features. These client additions must retain page-atomic state and
checkpoint application. Puller
[history-gap recovery](../architecture/2026-09-07-puller-history-gap-recovery.md)
and [local replay](../architecture/2026-09-07-local-puller-subscription-replay.md)
remain independent owners. The
[publication redesign](../../rejected/architecture/2026-09-07-puller-persist-before-publish.md)
remains rejected.
