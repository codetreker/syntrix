# Agent Note: Complete Query Cursor Pagination across Storage and Indexes

Status: implemented

## Problem

A query API that accepts a continuation but returns only documents cannot expose
the consumed index position reliably. Missing documents, residual predicates,
multiple branches, and equal sort values make the last returned document an
insufficient traversal boundary. Direct source routes also need deterministic
ordering and a continuation that cannot be reused for another query.

## Decision

Public Query returns `documents`, `nextCursor`, and `effectiveOrder` through local
interfaces, gRPC, HTTP, and both SDK clients. Documents use exact typed values as
specified by the [indexed-query decision](../bug-fix/2026-09-07-indexed-query-filter-semantics.md).

| Route | Position and ordering |
|---|---|
| Unordered listing | Bounded authoritative source scanner, logical ID ascending |
| Unordered ID-only equality/membership | Authoritative point lookups, logical ID ascending |
| Other queries | Complete Indexer candidate plan, effective scalar order plus unique ID tie-break |

Ordered public queries use Indexer. The older internal `DocumentStore.Query`
method is not the public continuation API. No arbitrary source-sort translation
or fallback scan is required for this page contract.

The opaque version-2 cursor binds database, concrete collection, normalized
predicates, requested/effective ordering, deletion visibility, route, applicable
template fingerprint and generation, branch manifest, and last consumed position.
Page size may change. Malformed/old cursors and cross-query reuse fail explicitly;
definition/generation changes produce a stale-cursor error.

Candidate branches merge into complete equal-position groups before a page
boundary. The cursor advances after consumed groups, including stale, missing,
or predicate-rejected candidates; prefetch never advances public continuation.
Source listing likewise advances on consumed candidates, including tombstones.
Refill continues until the final document limit or exhaustion. A non-null cursor
may lead to an empty terminal page and does not promise another visible document.

The SDK exposes `getPage()` and explicit `startAfter()`. `get()` returns only the
selected page's documents. Query `update()` and `delete()` also act on that page
using individual document writes. The [API reference](../../../../docs/reference/api.md#continuation)
owns transport/error behavior and the [SDK reference](../../../../docs/reference/typescript_sdk.md#query-pages)
owns client usage.

## Alternatives

**Use only the last document ID.** An ID is sufficient for the direct ID-order
routes, but cannot continue composite or descending order without recovering
mutable source fields. It also loses positions consumed without emitting a result.

**Expose raw Indexer order keys.** Raw keys do not bind database, predicates,
deletion visibility, branch strategy, or generation, and provide no common direct
source contract. The opaque envelope preserves those required scope checks.

## Consequences

- Stable datasets traverse deterministic ordered pages without losing equal-key
  groups at branch boundaries. Skipped candidates advance or terminate traversal.
- Cursors are versioned and generation-bound; old formats and rebuilt definitions
  cannot silently resume. A stale cursor requires restarting the query.
- Index views are request-scoped, while source reads and successive pages are not
  a historical snapshot. Index lag and concurrent sort-key changes can cause
  omissions or repeats across pages. Materialized results still satisfy the full
  query and current candidate position; deduplication within a page is bounded.
- Page envelopes and typed document values require coordinated client/server
  upgrades. Failed queries return no partial page or success continuation.
- Candidate accounting, cancellation, and resource limits remain owned by the
  [indexed-query decision](../bug-fix/2026-09-07-indexed-query-filter-semantics.md).
  [Online recovery](../../proposed/architecture/2026-09-07-indexer-recovery-lifecycle.md)
  can use bounded source scanning but still requires its own concurrent handoff
  and job-lifecycle guarantees.
