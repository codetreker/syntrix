# Agent Note: Preserve Every Predicate in Indexed Query Pages

Status: implemented

## Problem

An accepted predicate must not disappear during index planning or materialization.
The original planner omitted unsupported operators, while later access planning
could omit fields outside a usable prefix. Candidate limits, stale postings, and
array membership expansion also affect which source documents can be returned.
A correct filter evaluator alone cannot establish complete ordered pages.

The initial [unsupported-operator rejection](2026-09-11-query-unsupported-filter-rejection.md)
prevented silent omission, and [same-field intersection](2026-09-11-indexed-filter-intersection.md)
fixed contradictory and overwritten equality/range bounds. Full execution also
requires exact values, complete predicate accounting, stable document identity,
atomic multikey replacement, and a reconstructible index generation.

## Decision

### Values and Predicate Semantics

| Contract | Decision |
|---|---|
| Operators | Support `==`, `!=`, `>`, `>=`, `<`, `<=`, `in`, and `contains` under explicit operand rules |
| Conjunction | Every normalized predicate remains in full source evaluation, including access predicates |
| Field grammar | Top-level business names and reserved metadata; reject dotted paths and malformed names |
| Missing/null | Distinct; missing matches no operator, including `!=`; explicit null participates in equality/membership |
| Membership | `in` compares the field itself; `contains` examines immediate scalar/null array members |
| Numeric domain | Exact signed int64 plus finite binary64; compare magnitude without rounding integers into doubles |
| Scalar order | Missing, null, booleans, numbers, then UTF-8 byte-ordered strings; descending reverses a field |
| Wire values | Recursively typed nodes, including complete objects; int64 uses decimal strings |
| SDK values | Decode int64 as bigint and binary64 as number, including nested fields and metadata |

No implicit scalar coercion, deep object equality, arbitrary decimal arithmetic,
or recursive array flattening is introduced. Scalar index fields reject arrays
and objects. Invalid values fail visibly; an early contradiction does not hide
malformed later operands. The [filter reference](../../../../docs/reference/filters.md)
owns the complete truth table and typed wire shape.

### Plans, Projections, and Source Validation

| Component | Responsibility |
|---|---|
| Indexer planner | Assign each predicate to access/residual execution; prove one common order across every branch |
| Access strategies | Intersect typed ranges; expand distinct `in` points and present-scalar `!=` intervals; fix explicit membership probes |
| Candidate service | Pin one view, merge branches, return complete equal-position groups with posting provenance, and bound work |
| Query coordinator | Read authoritative source documents; validate scope, visibility, every predicate, current ordering, and current posting membership |
| Page production | Deduplicate within the page, refill rejected candidates, and advance only through fully consumed groups |
| Direct routes | Bounded authoritative ID-ordered listing and ID-only equality/membership lookups |

Residual filtering is allowed only when the selected access/order plan supplies
complete candidates. It does not permit an unrestricted Store-scan fallback.
Physical dimensions left unfixed must agree with the effective ordering. With no
explicit order, the selected template's scalar order plus ID is exposed; explicit
order appends ID ascending unless ID is already specified.

Indexes are partitioned by logical database, concrete collection, full template
fingerprint, and generation. Postings always include the logical ID derived from
the source envelope/path, never a storage hash or business `data.id`. At most one
membership dimension is allowed per template. Arrays produce distinct postings;
all matching-template posting sets are replaced atomically with existing applied
progress. Empty replacements remove obsolete postings completely.

Memory views pin stable ordered trees. Pebble snapshots capture immutable pending
and flushing replacements under state synchronization. Pending supersedes flushing,
which supersedes persisted state; an out-of-range replacement still suppresses all
old postings. Prefix successors and cursor/filter bound intersection prevent
range widening. Expansion and overlay limits reject excess work rather than
truncating projections or bypassing pending visibility.

The [index design](../../../../docs/design/server/indexer/02.index.md) and
[storage design](../../../../docs/design/server/indexer/04.storage.md) own these
mechanisms. Source reads use the Store abstraction with explicit authoritative
consistency and deletion visibility. The Mongo adapter provides bounded ID scans
with a `{database, collection, fullpath}` index and simple collation in both its
data and system namespaces; other adapters must satisfy the same logical contract.

Materialization uses at most 128 documents and a 16 MiB source-byte bound per
batch across indexed candidates, direct ID lookups, and source listing. Read
options enforce source-byte admission before accumulating decoded rows; this is
separate from the typed HTTP/protobuf page limits. Batch size is also bounded by
remaining result slots, so prefetched candidates never advance continuation past
unconsumed groups.

Source byte-budget errors halve the batch and retry while retaining candidate
order; a single document that cannot fit fails. Other source failures propagate
immediately. Successful sub-batches are processed before their retained remainder,
and only consumed groups advance public continuation. Later failure still discards
the page. Read counters include retry attempts; validated-document counters include
only non-nil results from successful reads.

### Pages and Transport

Query returns `documents`, `nextCursor`, and `effectiveOrder`. HTTP documents are
complete typed objects; Query gRPC uses wire version 2. The SDK exposes `getPage()`;
`get()` returns that page's documents. Query updates/deletes also act on one page
and remain individual document writes. Trigger queries use the same page route
and codec. Ordinary CRUD/CAS bodies remain JSON and do not gain bigint writes.

The [pagination decision](../feature/2026-09-07-query-cursor-pagination.md) owns
the opaque versioned cursor, scope/generation binding, consumed-position rules,
and changing-data consistency costs. Errors discard the entire page and preserve
no success continuation.

### Bootstrap and Breaking Upgrade

Event wire version 2 carries exact business values and independent database,
collection, fullpath, and logical ID through native capture, durable buffering,
replay, and RPC. Decoding validates identity agreement and rejects incompatible
records. Existing native resume-token checkpoints, EventID, and BufferKey retain
their meanings. The opaque progress marker adds per-backend buffer lineages and
retention validation needed to prove bootstrap/replay continuity.

Consumer deduplication distinguishes EventIDs within each backend's current
cluster timestamp. [MongoDB change events](https://www.mongodb.com/docs/manual/reference/change-events/insert/)
can share that timestamp across distinct changes. Delivery
records identities and advances progress only after sending succeeds. A resume
replays the complete boundary timestamp group, including previously delivered
events, because the EventID hash suffix does not establish source order. Within
one subscription, retained group identities suppress replay/live overlap; they
are discarded when a newer timestamp is delivered. Verified resume rejects any
pruning within its boundary group. Native checkpoint and progress formats remain
unchanged; consumers must tolerate boundary-group redelivery.

The complete encoded durable event is capped at 64 MiB and each complete Puller
protobuf response at 65 MiB. Typed expansion means some legal source document
shapes can exceed those limits. Capture fails visibly before admission/progress;
operators must reduce the data or coordinate a budget change and rebuild capture.
The Mongo adapter converts known top-level metadata dates to Unix-millisecond
int64 values while leaving unsupported business date types explicit.

An explicitly supplied documentless physical delete performs no posting mutation
and advances applied progress. It cannot reconstruct logical identity from a
storage hash. Source validation rejects remaining orphan candidates, and a later
maintenance rebuild reclaims them; unrelated indexes continue consuming events.

Database removal publishes an explicit retirement disposition with deletion of
its derived partitions/catalog. Projection and retirement are serialized. Queued
full-document updates for a retired database skip projection while preserving
applied progress, preventing resurrection or failure of unrelated databases.
The marker survives Pebble restart and blocks generation resolution until a
complete bootstrap inventory deliberately includes the database again.

The supported rebuild is write-quiesced maintenance. Enumerate all configured,
template, and metadata databases, including system scopes and tombstone-only
collections. Default capture includes both physical data and system namespaces;
custom capture configuration must include the corresponding configured names.
Establish a source-confirmed boundary in explicitly reset empty
buffers, scan authoritative pages through the shared projection, flush, validate
the boundary and unchanged inventory, then atomically publish every database
catalog with one common generation/progress. Empty and future collections under
covered templates inherit catalog completeness.

Startup validates the complete catalog, template fingerprints, saved progress,
backend set, lineages, and retained replay. Serving readiness follows application
and flush of the ordered Puller readiness barrier. Temporary transport failures
clear readiness and retry from flushed applied progress after boundary validation,
with a one-second delay and at most ten consecutive attempts. Invalid lineage,
history, or codec state requires maintenance; there is no silent reset to current
head. Bootstrap stays in the same runtime so
memory indexes can serve after maintenance. External writers, including physical
cleanup/TTL, must remain fenced until readiness.

This is a breaking upgrade of derived indexes, buffers, query pages, and clients.
`--bootstrap-indexes --writes-quiesced --reset-derived` archives only configured
validated derived-store directories before rebuilding; source documents remain
intact. The [maintenance procedure](../../../../docs/design/server/indexer/04.storage.md#offline-bootstrap-and-startup)
owns flags, limits, and operational requirements.

### Administration and Request Diagnostics

`GetState` reports active concrete generations and uses `DocCount=-1` when the
count is unknown. Broad invalidation is bounded to the existing partition
inventory; an explicit concrete collection can also invalidate its catalog-backed
empty generation. Storage failures propagate. Full template reload, online
reconciliation, and rebuild-job administration remain in the
[management proposal](../../proposed/feature/2026-09-07-indexer-management-rpcs.md).

Query completion and candidate gRPC admission/completion emit structured debug
records with propagated request ID, hashed identities, counters, elapsed time,
and sanitized reason categories. They exclude predicate values, raw cursors,
document payloads, and error messages. These records do not establish aggregate
metric exporters or transactional consistency.

Projection failure logs correlate event/backend and hashed document scope with
template/generation and a sanitized budget or failure category. Bootstrap logs
identify the operation, generation, phase/state, scanned/projected documents, and
postings. Counts are observations; readiness still requires the validated applied
barrier and successful publication/flush.

### Reproducible Cost Fixture

```bash
go test ./internal/query/core -run '^$' -bench '^BenchmarkIndexedQueryPage$' -benchmem
```

The fixture uses 10,000 stable documents, a 50-result page, memory/Pebble indexes,
and an in-memory source that enforces canonical source-byte accounting. It includes
planning, candidate traversal, source validation, posting checks, cursor creation,
and typed page encoding. Source database/network latency is excluded.

| Local observation (2026-09-11) | Memory | Pebble |
|---|---|---|
| Common first-page median | 0.873 ms | 0.819 ms |
| Common source reads per page | 1 | 1 |
| 90% residual rejection | 491 source documents / 43 reads | 491 source documents / 43 reads |
| 99% residual rejection | 4,901 source documents / 446 reads | 4,901 source documents / 446 reads |

Residual-heavy queries illustrate the cost of refilling to the final result limit.
These are local fixture observations, not production-load latency, p99, or peak
memory guarantees. The earlier document-array path omitted source validation,
source-byte bounds, complete cursors, and typed encoding, so its timing is not an
equivalent-contract comparison.

## Alternatives

**Keep rejecting unsupported indexed operators.** The earlier repair prevented
silent widening but left documented membership and inequality queries unavailable.
Complete access/residual execution replaces that restriction while preserving
explicit failures for inadmissible plans.

**Use unrestricted source scans for missing index strategies.** This weakens the
required-index cost policy. Source scans remain limited to the deliberate
unordered listing route; other queries need complete index candidates.

**Use binary64 for all numbers.** Converting int64 first loses equality and ordering
precision above 2^53. Exact bounded int64/binary64 values require typed transport
but preserve metadata and source values without arbitrary decimal support.

**Expose raw index positions as public cursors.** This cannot bind query scope,
branch strategy, route, or generation. A versioned opaque envelope gives all page
routes one validation contract.

**Deduplicate consumer events by cluster timestamp or resume by hash order.**
MongoDB can emit distinct changes at the same cluster timestamp. Timestamp-only
deduplication drops them; an exclusive hash-ordered resume can also skip a later
source event with a smaller hash. Identity-aware delivery and complete boundary
group replay preserve those changes at the cost of possible redelivery on resume.

**Use automatic online rebuilding for this initialization.** A concurrent scan and
replay handoff needs additional fencing, history-gap recovery, and job lifecycle
ownership. Explicit write quiescence makes complete initialization available while
those broader requirements remain in their existing recovery proposal.

## Consequences

- All eight operators and complete conjunctions are executable for admitted plans;
  no predicate disappears. Unsupported plan shapes fail rather than returning a
  broader result.
- Index lag can omit recent writes. Source checks reject stale positions and
  postings but do not create a historical snapshot. Concurrent sort-key moves may
  cause omissions or repeats across pages; per-page deduplication is bounded.
- Authoritative materialization adds write-source read load. Branch, projection,
  overlay, candidate, and response budgets cap work and can reject broad queries.
- Multikey expansion and exact numeric keys increase index storage/write costs.
  Projection failure occurs after the original source write and stops affected
  indexing; it does not roll back that write.
- Query response and SDK numeric types require coordinated release. Legacy derived
  stores and cursors are rejected; maintenance preserves source data but requires
  downtime and explicit reset.
- Logical deletion clears business data; tombstones cannot retain former array
  membership. Physical cleanup remains filtered from native business events.
- [Query pagination](../feature/2026-09-07-query-cursor-pagination.md) owns the
  delivered public page and cursor contract. [Online recovery](../../proposed/architecture/2026-09-07-indexer-recovery-lifecycle.md)
  and [nested fields](../../proposed/feature/2026-09-07-indexed-nested-fields.md)
  retain their distinct remaining requirements. Offline initialization does not
  claim automatic online rebuilds or nested-path support.
