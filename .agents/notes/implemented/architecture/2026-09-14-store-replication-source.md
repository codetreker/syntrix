# Agent Note: Keep Replication Progress in the Authoritative Source

Status: implemented

## Problem

Current-state replication must join an initial document scan to subsequent
committed changes without losing writes made during the scan. Ordinary
authoritative read routing does not guarantee that a read covers a change
position. Wall-clock timestamps do not establish commit order, and a local
consumer's replay offset cannot survive replacement of that consumer.

The [Pull progress decision](../bug-fix/2026-09-07-replication-pull-cursor-progress.md)
owns the end-to-end defect and the research comparing native changes with
transactional revisions. This note owns the delivered Store capability and its
source-selection contract; the linked decision owns public Pull integration.

## Decision

Provide optional `ReplicationSource` operations alongside ordinary Store APIs:

| Operation | Responsibility |
|---|---|
| `BeginBootstrap` | Capture a committed overlapping start and bind source/scope |
| `ReadBootstrapPage` | Scan committed current state in logical ID order, retaining the original replay start |
| `ReadChangesPage` | Read an ordered native prefix and materialize current committed document state |

All operations route through `OpWatch` to the authoritative source. A selected
adapter without this capability fails explicitly. The adapter owns native
positions, history validation, source incarnation, causal context, and the
relationship between reads and changes. The public abstraction assumes none of
MongoDB's token or timestamp formats.

### Progress and consistency

```text
Committed boundary C0
         |
Moving logical-ID scan covering C0
         |
Inclusive replay from original C0
         |
Completed native source prefixes
```

- Positions contain a phase and opaque adapter data. Source and logical scope
  are validated across requests, including requests reaching another instance.
- Each scan page checks usable replay history through an operation that actually
  exercises resumability. A zero-sized initial stream batch alone is not proof.
- Each frame carries a continuation safe after that frame and all preceding
  work. Page-level `End` and `CaughtUp` are accepted only with the entire page;
  a consumer accepting a prefix uses its final frame's position.
- Filtered events advance progress without a document. Only an observed source
  watermark proves caught-up. Count, byte, work, and soft-time limits return a
  completed prefix; errors discard tentative results.
- Document reads cover consumed source progress but may see newer committed
  state. Replication converges to current state; it does not reconstruct a
  historical snapshot or use document versions as ordering positions.

The Mongo adapter pairs a majority-committed starting time with inclusive native
replay. It restores operation time and signed cluster time in fresh sessions,
preserving committed read coverage across instances. Source UUID binding detects
physical collection replacement. Bootstrap verifies existing source index
metadata and creates the required index only when missing. Unconditional DDL
could wait for unrelated writes to become majority committed, preventing a
bootstrap that can safely overlap those writes. These mechanics are private to
replication; ordinary read routing, Store Watch, and the native Puller checkpoint
retain their existing contracts.

### Identity and deletion

| Condition | Delivered meaning |
|---|---|
| Current live document | Complete replacement state |
| Retained tombstone | Logical deletion with retained metadata |
| Known logical identity, absent current record | Minimal deletion identity without invented historical metadata |
| Physical source deletion | Progress only; no second business deletion |
| Required identity unavailable | Explicit failure without advancing over that change |

Logical identity is independent of business data and the hashed physical key.
Physical cleanup is filtered before logical collection routing requires an
image. Remaining events must retain recoverable identity even if their current
document disappears. A source-history entry alone does not establish that
requirement.

### Ownership and bounds

Calls own their sessions and cursors, propagate cancellation, and release those
resources while provider connections remain shared. Positions do not depend on
server-side client state or instance affinity. Budgets bound states, source
frames, source bytes, probe bytes, retained pages including per-frame positions,
and elapsed time. Page source-byte usage includes admitted or decoded speculative
payloads; rejected raw records and driver batches retain native wire bounds.
This is not a total server-I/O limit. Boundary acquisition returns only a position
and shares absolute deadlines with page reads; byte usage is per page rather
than aggregated across calls. Errors preserve their causes and expose
storage-independent recovery categories; raw positions and document payloads
are not diagnostics.

The [Store contract](../../../../docs/design/server/core/storage/03.stores.md#13-replicationsource)
owns limits and page semantics; the
[routing design](../../../../docs/design/server/core/storage/04.routers.md)
owns source selection.

## Alternatives

**Compose ordinary authoritative reads with Watch in Query.** These operations
do not independently promise a shared committed boundary. Exposing adapter
causal context to Query would move source-specific correctness outside the
component responsible for it. A cohesive optional capability keeps that
guarantee enforceable without expanding every ordinary read contract.

**Maintain transactional revisions over current documents.** This is a valid
alternative when revision assignment and document changes commit together in
order, and tombstone cleanup coordinates a retained-progress floor. It would
change every write and cleanup path and introduce contention on hot collection
counters. Native changes keep synchronization coordination on the read side,
consistent with existing throughput and scaling goals. The owning proposal
preserves the full comparison and conditions for reconsidering measured costs.

**Use strict timestamp/ID continuation.** It repairs ties in a fixed dataset but
cannot recover delayed commits or later writes behind the saved tuple.

**Use Puller-local history or a persistent synchronization replica.** Local
history adds consumer lifetime and retention prerequisites to portable recovery.
A persistent replica adds derived data, a change log, and another recovery
lifecycle. Neither is required for this authoritative-source capability.

## Consequences

- Existing document writes avoid replication-counter coordination. Optional
  capability discovery leaves ordinary Store implementations and consumers
  independent of replication support.
- Source reads and stream opening add load. Native history and recoverable
  identity bound recovery; no fixed offline-retention interval is guaranteed.
  Long bootstrap scans can fail when history expires and require a new start.
- Logical deletion and recreation may yield repeated or newer states, including
  reset document versions. Consumers must apply state idempotently and persist
  applied data with progress atomically.
- Source replacement, unavailable history, or lost logical identity requires
  deliberate resynchronization. A source failure never silently selects another
  backend or discards the requested position.
- The Store capability owns source operations independently of public transport
  and client authorization. The
  [Pull decision](../bug-fix/2026-09-07-replication-pull-cursor-progress.md)
  owns protocol integration and complete-database authorization; authentication
  alone does not establish replication access. A durable SDK coordinator remains
  separate from the Store capability.

## Topology Validation

Delayed-majority and primary-stepdown integration tests require a dedicated
three-node replica set with `enableTestCommands`. Set
`SYNTRIX_REPLICATION_TOPOLOGY_URI` to that isolated deployment. These tests pause
replication and step down the primary; without the variable they skip before
connecting, allowing ordinary Mongo and Puller coverage to use their existing
test deployment independently.

```sh
SYNTRIX_REPLICATION_TOPOLOGY_URI='<isolated-replica-set-uri>' \
  go test ./internal/core/storage/mongo \
  -run 'TestMongoReplication(BootstrapExcludesDelayedMajorityWrite|ResumesOnFreshClientAfterPrimaryStepdown)' \
  -race -timeout 5m
```

The cases verify that bootstrap excludes an uncommitted write without waiting
for it to commit, then replays it after commitment, and that a fresh client can
resume the same source position after a primary stepdown.
