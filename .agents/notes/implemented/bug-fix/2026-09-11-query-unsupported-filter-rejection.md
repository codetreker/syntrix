# Agent Note: Reject Unsupported Indexed Query Operators

Status: implemented

## Problem

An indexed query can return extra documents when planning discards a predicate
whose operator has no index translation. SDK query updates and deletes act on
the documents returned by that query, so a widened result can also cause
unintended writes. Shared filter syntax does not establish that every executor
can implement an operator.

## Decision

The [complete indexed-query decision](2026-09-07-indexed-query-filter-semantics.md)
supersedes the operator restriction recorded here. All eight operators now have
execution strategies for admitted plans; explicit rejection remains necessary
when no complete plan exists. The following records the earlier bug fix and its
original limits.

The Query planner returned an error for an operator it could not translate. The
indexed query failed before index search and returned no partial result.

| Query shape | Behavior at adoption |
|---|---|
| No filters and no ordering | Preserve direct Store execution |
| All filters target `id` with `==` or `in`, without ordering | Preserve direct Store execution |
| Indexed filters using `==`, `>`, `>=`, `<`, or `<=` | Translate using the existing index plan |
| Indexed `!=`, `in`, `contains`, or an unknown operator | Return `model.ErrInvalidQuery` |

The planner error maps to gRPC `InvalidArgument` and HTTP 400 `BAD_REQUEST`.
Its message identifies the unsupported operator and the indexed-query limitation,
without including the field, predicate value, or document payload. Error wrapping
preserves the existing invalid-query identity.
Existing request-validation and missing-Indexer checks retain their precedence
over planning errors. REST request validation rejects unknown operators with
HTTP 400 `BAD_REQUEST` and the generic message `Invalid query parameters`.

The check belongs to indexed Query planning. Shared model and SDK operator types,
write conditions, Store filtering, and realtime filtering retain their existing
contracts. The [Query integration design](../../../../docs/design/server/query/02.indexer-integration.md#filter-planning-and-page-coordination)
and [filter reference](../../../../docs/reference/filters.md#query-availability)
describe the execution rules.

## Alternatives

**Implement every shared operator in indexed queries.** Membership unions,
disjoint inequality ranges, array index entries, deduplication, and correct
ordering and limits require broader execution and index changes. The existing
[complete indexed-query decision](2026-09-07-indexed-query-filter-semantics.md)
now supplies those strategies; they were deliberately deferred from this repair.

**Reject the operators in shared validation.** This would also remove valid
ID-only membership queries and affect other consumers of the shared filter
syntax. Executor-specific rejection preserves those contracts.

**Fall back to unrestricted Store scans.** This changes the indexed-query cost
policy and can turn an unsupported request into an unbounded storage operation.
The existing explicit Store routes remain the only exceptions.

## Consequences

- The repair stopped silent predicate omission before full operator execution was
  available. Applications initially received explicit unsupported-query errors.
- Shared operators, ID-only membership, conditional writes, and realtime filtering
  retained their contracts; restriction at shared validation would have affected
  those independent consumers.
- Query-based SDK updates/deletes stopped when their initial query was rejected.
- [Same-field intersection](2026-09-11-indexed-filter-intersection.md) addressed
  repeated equality/range bounds separately. The
  [complete indexed-query decision](2026-09-07-indexed-query-filter-semantics.md)
  owns current access/residual execution, exact numeric values, pages, and storage
  changes. This note preserves why executor-specific errors were required.
