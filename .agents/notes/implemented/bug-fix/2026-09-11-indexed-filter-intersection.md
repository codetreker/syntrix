# Agent Note: Intersect Same-Field Indexed Filters

Status: implemented

## Problem

Multiple conditions on one indexed field can lose their AND semantics when
bound construction selects one equality, replaces a stronger range with a weaker
one, or ignores ranges after finding equality. Reordering equivalent predicates
can then change the result. Inconsistent equalities or disjoint bounds can also
return documents even though no value satisfies the query.

## Decision

The [complete indexed-query decision](2026-09-07-indexed-query-filter-semantics.md)
extends this intersection rule with exact int64/binary64 values, membership
strategies, and full residual evaluation. This note records the preceding local
bound repair and why conjunctions cannot use last-write-wins selection.

Indexer intersects supported predicates on each usable field. The original repair
used the then-existing encoded-value comparison semantics.

| Constraint | Rule |
|---|---|
| Repeated `==` | Encoded values must agree |
| `==` with ranges | The equality value must satisfy all ranges |
| Multiple `>` or `>=` | Keep the strongest lower bound; strict wins an equal-value tie |
| Multiple `<` or `<=` | Keep the strongest upper bound; strict wins an equal-value tie |
| Contradictory constraints | Return an empty result |
| Equal lower and upper values | Match that value only when both bounds are inclusive |

Predicate order does not change the intersection. Ascending and descending index
fields apply the same logical constraints; encoded key bounds preserve whether
all keys with the boundary value are included or excluded. Equality prefixes
continue into subsequent usable index fields. Index selection and cursor decoding
precede bound construction. A contradiction returns an empty result before
backend index search; backend readiness errors are not observed on that path.
Encoding errors on visited usable fields still propagate. Field placement,
limits, cursor protocol, and backend search for nonempty bounds remain unchanged.

The [index design](../../../../docs/design/server/indexer/02.index.md#same-field-constraints)
owns the bound rules; the [filter reference](../../../../docs/reference/filters.md#multiple-conditions-on-one-indexed-field)
owns caller examples. [Unsupported-operator rejection](2026-09-11-query-unsupported-filter-rejection.md)
was the preceding restriction; complete execution now supersedes it.

## Alternatives

**Fix only overwritten range bounds.** This leaves repeated equality and
equality/range contradictions able to broaden the same field's constraints.
All supported predicates on that field need one intersection rule.

**Complete all indexed predicate semantics together.** Membership unions, array
index entries, cross-field residual evaluation, and cursor correctness require
broader execution and index changes. The existing
[complete indexed-query decision](2026-09-07-indexed-query-filter-semantics.md)
now supplies those requirements; they were outside the original bound repair.

## Consequences

- Repeated equality and range conditions retain AND semantics independent of
  predicate order, strict endpoint ties, or descending encoding.
- The original repair did not change numeric encoding or add cross-field residual
  evaluation. It established a reusable intersection rule without claiming those
  broader guarantees.
- Current exact values, full predicate evaluation, generation checks, and page
  production are owned by the [complete indexed-query decision](2026-09-07-indexed-query-filter-semantics.md).
  The earlier repair remains relevant because additional execution strategies
  must retain same-field intersection before expanding branches.
