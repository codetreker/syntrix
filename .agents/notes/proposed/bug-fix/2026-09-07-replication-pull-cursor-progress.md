# Agent Note: Make Replication Pull Checkpoints Advance Reliably

Status: proposed

## Problem

The [Pull implementation](../../../../internal/query/core/engine.go) filters
`updatedAt >= checkpoint`, sorts by timestamp and ID, and returns only the last
timestamp as its checkpoint. A page filled by documents sharing that timestamp
can repeat indefinitely. Strict greater-than would omit the remaining tied
documents.

The [replication design](../../../../docs/design/server/gateway/replication.md)
requires deterministic monotonic progress. Wall-clock timestamps do not
establish committed-write order: a write can obtain timestamp 100, stall, and
commit after another write at 101 has already been pulled. Tuple continuation
still loses that write. Replication must converge to current document state
through concurrent writes, logical deletion, restart, and routing to another
service instance.

## Proposal

The native source change-stream direction was confirmed on 2026-09-11. The
runtime protocol and supporting capabilities remain unimplemented; this note
retains proposed status until delivery. The earlier suggestion to prefer Puller
event history was conditional and never selected. The chosen source is the
authoritative storage adapter, using its native committed change history.

### Responsibility and progress

| Owner | Required responsibility |
|---|---|
| Authoritative storage | Remain the business source of truth and define committed source positions |
| Storage adapter | Provide a cohesive, source-neutral replication capability covering bootstrap, document reads, change pages, history validity, and progress |
| Query and Gateway | Bind scope and authorization, expose Pull, and enforce transport budgets |
| Client | Apply documents and tombstones durably with the returned checkpoint; retain prior progress if application fails |

The proposed capability, provisionally `ReplicationSource`, groups
`BeginBootstrap`, `ReadBootstrapPage`, and `ReadChangesPage`. Native resume
tokens, causal context, and adapter-specific read guarantees stay inside that
capability. The public contract must not assume MongoDB. Ordinary authoritative
read routing alone does not establish that a read covers a committed change
position.

Use versioned opaque checkpoints bound to database, collection, source
incarnation, and phase. They must survive requests routed to another instance
using the same authoritative source. No server-side client registry, instance
affinity, or persistent synchronization replica is required. The native
Puller's capture checkpoint and buffer architecture remain unchanged.

### Bootstrap and incremental delivery

```text
Capture committed overlapping boundary C0
                  |
Scan committed current documents in logical ID order
                  |
Replay native changes from the original C0
                  |
Continue incremental current-state delivery
```

- Establish a committed boundary before scanning. Every scan page must cover
  that boundary; overlapping replay must include writes that the moving scan
  could miss. A primary read without a committed lower bound is insufficient.
- Keep C0 fixed throughout the scan and validate usable history through a
  bounded source operation that actually checks resumability. Merely opening
  an empty native batch is insufficient evidence.
- Materialize stable logical identities from ordered source changes using
  committed reads covering the consumed position. Returned state may be newer
  than its triggering event. This is asynchronous state convergence, not an
  event audit or a historical snapshot.
- Preserve logical identity independently of document data, including empty
  logical tombstones. Physical cleanup is progress only, not another business
  deletion. An unrecoverable required identity produces an explicit
  resynchronization requirement without advancing over the unknown change.
- Advance only over a fully handled source prefix. Filtered frames may advance
  without documents; they do not prove caught-up. Require a successful source
  watermark for caught-up, and bound counts, bytes, time, and source work.
- Permit duplicate states and require idempotent application. Document version
  is not the ordering authority; deletion and recreation can reset version.
  Continuous writes may keep caught-up false.

Update HTTP, storage-facing contracts, protobuf, the manual SDK Pull API, and
reference documents with implementation. Checkpoints become opaque strings;
old numeric checkpoints require explicit reset without silent reinterpretation.
Document numbers remain lossless across transports. Cancellation and source
failures must not expose successful progress for incomplete work.

### Research findings and selection rationale

The [requirements](../../../../docs/design/server/00.requirements.md) emphasize
high write throughput, horizontal scaling, and a public single-document
atomicity contract. That contract does not prohibit internal multi-document
transactions. It makes additional coordination on every write a material
trade-off.

| Dimension | Native committed changes: selected | Transactional revision: valid alternative |
|---|---|---|
| Ordering authority | Existing source commit history | Scope counter committed atomically with document writes |
| Main cost | Source reads, materialization, and bootstrap consistency | Transactions and contention on a hot scope's counter |
| Initialization | Committed moving scan with overlapping replay | Scan up to captured head, then read larger revisions |
| Incremental reads | Ordered identities and current-state reads | Revision-index queries over current documents and tombstones |
| Deletion recovery | Usable history and recoverable identity | Tombstones coordinated with a durable retention floor |

The selected direction keeps replication coordination on the read side and
preserves the current write path. Each adapter must provide reliable native CDC
and suitable committed reads. There is no established product requirement for
arbitrary business writes bypassing Syntrix; such a requirement is not a reason
for this selection. Physical maintenance and TTL are distinct from supported
business writes.

MongoDB's [change-stream documentation](https://www.mongodb.com/docs/manual/changeStreams/)
describes resume-history requirements and `updateLookup` semantics: lookup can
return a later majority-committed document or no document after deletion. Its
[performance guidance](https://www.mongodb.com/docs/manual/changeStreams/#change-stream-performance-considerations)
identifies connection and sharded-stream costs. Event payloads are therefore
not historical state, and source capacity must be measured under concurrent
Pull requests.

The research consists of source inspection, official documentation, and
concurrency analysis. No performance benchmarks establish either option's
throughput or latency. Measure concurrent Pull source work and connections for
the selected direction. Transaction retry rate, throughput, and P99 latency
under hot-collection writes are comparison evidence for reconsideration.

## Alternatives

**Use strict `(updatedAt, id)` continuation.** This repairs tied pages in a
fixed dataset but misses delayed commits and later equal-timestamp writes
behind the saved tuple. It cannot satisfy committed progress.

**Maintain a transactional replication revision over current state.** This is
a correct alternative without an append-only event journal. Every write updates
a scope counter and its document or tombstone in one transaction; counter-write
conflicts must serialize publication through commit. Preallocated numbers or
ordinary sequence allocation alone do not establish commit order. Initialization
scans revisions up to a captured committed head, then consumes higher revisions.
Each incremental page reads source generation, head, retention floor, and rows
in one short committed consistent view. Cleanup must publish a floor atomically
or before removal, conditionally delete the intended tombstone revision, and
reject checkpoints below the floor. Independent TTL cannot safely provide this
protocol by itself. MongoDB [transaction constraints](https://www.mongodb.com/docs/manual/core/transactions-production-consideration/)
and [snapshot reads](https://www.mongodb.com/docs/manual/reference/read-concern-snapshot/)
provide relevant adapter mechanisms. This direction was not selected because
it changes every business write and cleanup path and creates contention for a
hot collection. Reconsider it if measured source-read costs dominate and
measured transaction costs meet write-throughput requirements.

**Serve Pull from Puller-local event history.** The original conditional
suggestion would couple client recovery to local replay, retention, and ordering
guarantees that are separately unresolved. Native source positions provide
cross-instance recovery without making those buffer changes a prerequisite.
Storage remains authoritative if a Puller is rebuilt.

**Build a persistent synchronization replica.** Sharing source capture and
separating client-history retention would require derived documents, a change
log, and another recovery lifecycle. That additional architecture was explicitly
excluded from the selected direction.

## Acceptance Criteria

- More than one page of equal-timestamp documents terminates with every
  document synchronized; delayed commits and clock regression cause no loss.
- Writes during bootstrap, deletion, recreation, and restart converge to
  current committed state. Source rollback cannot publish uncommitted state.
- Scan and change requests can move between service instances; source
  replacement or unavailable history produces explicit resynchronization.
- Logical deletion retains recoverable identity; physical cleanup adds no
  business deletion; unrecoverable required identity cannot advance progress.
- Empty filtered pages advance safely without claiming caught-up. Page budgets
  never skip an unreturned state; canceled or failed application does not commit
  a client checkpoint.
- Expired, malformed, old-format, and cross-scope checkpoints produce distinct
  documented outcomes through local and remote transports.
- Authorization is enforced on every request under the separately confirmed
  scope; checkpoint possession never grants access.

## Risks

- Bootstrap can outlive usable history and require restart. An oplog entry's
  presence alone does not guarantee recoverable identity. The current
  [storage default](../../../../internal/core/storage/config/config.go) retains
  soft deletes for five minutes, and [Mongo cleanup](../../../../internal/core/storage/mongo/document_store.go)
  uses TTL. A needed identity may become unavailable before oplog expiry.
  No fixed offline-recovery duration is promised by this choice.
- Per-request source streams and committed reads add load and latency. Stream
  opening, materialization, and cancellation cleanup require explicit limits
  and representative load measurements.
- The wire format requires coordinated server/manual-client delivery.
  Diagnostics identify phase, source generation, counts, budget end reason,
  and expiration cause without raw checkpoints or document payloads.

## Dependencies and Remaining Decisions

| Item | Ownership and consequence |
|---|---|
| Full-scope authorization | Pending. Database-wide readable scope is recommended, but not confirmed. Per-document replication also needs membership exits and permission-change recovery; authentication alone is insufficient. |
| Durable client coordinator | [SDK offline replication](../feature/2026-09-07-sdk-offline-replication.md) owns persistence, outbox, and automatic lifecycle. Manual Pull can be delivered separately, but its protocol must preserve atomic apply/checkpoint and reset without erasing pending local edits. |
| Native Puller recovery | [History-gap recovery](../architecture/2026-09-07-puller-history-gap-recovery.md) and [local replay](../architecture/2026-09-07-local-puller-subscription-replay.md) remain separate consumer concerns, not prerequisites for the selected native-source Pull path. |
| Query pagination | [Query pagination](../../implemented/feature/2026-09-07-query-cursor-pagination.md) owns public query traversal; it does not repair replication checkpoints. |
| Rejected publication mechanism | The [publication proposal](../../rejected/architecture/2026-09-07-puller-persist-before-publish.md) remains rejected. Its local sequence and generation mechanism is not restored here. |

This selection records architecture and research conclusions. Active design and
reference contracts change with eventual implementation; the detailed runtime
contract remains a draft until confirmed.
