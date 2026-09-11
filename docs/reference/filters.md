# Query Filters Guide

Structured queries use the rules below. Shared operator names remain available to
write conditions and realtime subscriptions, whose execution contracts are
separate. See the [Query API](api.md#query-operations) for page transport.

## Fields and Operators

Filters form an AND conjunction. Fields are top-level names; dotted paths, empty
names, control characters, and invalid Unicode are rejected. Reserved metadata
(`id`, `collection`, `version`, `createdAt`, `updatedAt`, `deleted`) comes from the
document envelope and cannot be shadowed by business data.

| Operator | Operand | Meaning |
|---|---|---|
| `==` | Scalar or null | Exact equality |
| `!=` | Scalar or null | Field is present and unequal |
| `>` / `>=` | Number or string | Greater / greater or equal in the same value family |
| `<` / `<=` | Number or string | Less / less or equal in the same value family |
| `in` | Array of scalars/null | The field itself equals at least one candidate |
| `contains` | Scalar or null | An immediate array member equals the operand |

Scalars are booleans, strings, signed int64, and finite binary64 numbers. Ranges
compare numbers with numbers or strings with strings. There is no string/number/
boolean coercion, deep object equality, or recursive array flattening.

| Source field | Equality | Not equal | Range | `in` | `contains` |
|---|---|---|---|---|---|
| Missing | False | False | False | False | False |
| Null | Equals null | True for a non-null operand | False | Matches a null candidate | False |
| Scalar | Typed equality | Inverse of equality | Same number/string family | Equality with any candidate | False |
| Array | False | True | False | False | Immediate scalar/null members only |
| Object | False | True | False | False | False |

All operand shapes are validated before execution. An empty `in` list matches
nothing. Duplicate `in` values collapse under numeric equality; at most 256
distinct values are accepted. Multiple `contains` predicates require every
specified member.

```text
missing != null                         -> false
["news"] == "news"                     -> false
["news"] in ["news"]                   -> false
["news", "news"] contains "news"       -> one document
[["news"], {"tag":"news"}] contains "news" -> false
```

## Typed Values

Query values preserve signed int64 and finite binary64 as distinct transport
types while comparing their exact numeric magnitudes. Integer `1` equals double
`1.0`; int64 `9007199254740993` remains distinct from double `9007199254740992`.
Both zero signs compare equally. NaN, infinities, invalid Unicode, and integers
outside the signed int64 range are rejected.

| Type | JSON node |
|---|---|
| Null | `{"type":"null"}` |
| Boolean | `{"type":"bool","value":true}` |
| String | `{"type":"string","value":"news"}` |
| Int64 | `{"type":"int64","value":"9007199254740993"}` |
| Float64 | `{"type":"float64","value":1.5}` |
| Array | `{"type":"array","value":[{"type":"string","value":"news"}]}` |
| Object | `{"type":"object","value":{"tag":{"type":"string","value":"news"}}}` |

Int64 uses canonical decimal text in the range -9223372036854775808 through
9223372036854775807. Every node, including objects, is tagged: business properties
named `type` or `value` cannot be mistaken for codec metadata. Typed null omits
`value`; other nodes require their payload. Unknown tags or extra node properties
are invalid.

HTTP filter operands accept typed nodes or ordinary JSON scalar/null/array values.
Ordinary integer tokens decode as int64; fractional or exponent tokens decode as
binary64. An absent operand is invalid. Query response documents always use
complete typed object nodes. The TypeScript SDK maps int64 to `bigint` and float64
to `number`, recursively; see [SDK query pages](typescript_sdk.md#query-pages).

## Query Availability

| Query shape | Execution |
|---|---|
| No filters and no ordering | Bounded authoritative Store scan in logical ID ascending order |
| Every filter is `id ==` or `id in`, without ordering | Authoritative ID lookups in logical ID ascending order |
| Other queries | A complete Indexer plan with authoritative source validation |
| No eligible complete index plan | HTTP 400 `NO_MATCHING_INDEX` |

Each indexed predicate is assigned to access, residual evaluation, or both. Every
returned document passes the complete conjunction against its authoritative
source value. Residual evaluation does not authorize an unrestricted Store scan.
An index traversal needs an access predicate or compatible explicit ordering.

### Multiple Conditions on One Indexed Field

Same-field equality/range constraints are intersected independently of their
order: equality values must agree, the strongest endpoints win, strict bounds win
equal-value ties, and contradictions yield no documents. `in` uses distinct point
probes; `!=` can use disjoint intervals; `contains` requires an explicit membership
index when used for access. Additional predicates may be evaluated as residuals.

## Ordering

Scalar order is `missing < null < false < true < numbers < strings`. Strings use
UTF-8 byte order without locale folding or Unicode normalization. Descending
reverses a field's order. Arrays and objects cannot supply scalar ordering fields.

Explicit ordering appends logical ID ascending unless ID is explicitly ordered.
Without explicit ordering, an indexed query uses its selected template's scalar
order plus ID. Membership dimensions do not order documents. The response reports
`effectiveOrder`; continuation pins it and the selected index generation.

## Pages and Consistency

- `limit` bounds returned documents, not index candidates; omitted or zero means
  100, and the maximum is 1000.
- Rejected stale or missing candidates still advance traversal. Execution refills
  the page until its limit or stream exhaustion.
- Source materialization uses batches of at most 128 documents and 16 MiB of
  source document bytes. The source budget is separate from typed response bytes;
  oversized source batches are retried in smaller groups. A single document that
  cannot fit, or a response page that exceeds its budget, fails the page rather
  than returning partial success.
- `nextCursor` continues after the last fully consumed candidate group. A non-null
  cursor does not promise another matching document; a following page can be empty.
- Cursors are opaque and bound to database, collection, normalized predicates,
  ordering, deletion visibility, route, and index definition/generation. Changing
  the page limit is allowed. Old formats, mismatched scopes, and stale generations
  fail explicitly.
- Each query owns one index read view; authoritative document reads and separate
  pages do not form a transactional snapshot. Index lag can omit recent writes.
  A document whose current sort position or posting differs from the candidate is
  skipped. Concurrent moves can cause omissions or repeats across pages.
- Logical deletion clears business data. `showDeleted` permits tombstones but
  does not restore former field values or memberships. Indexed tombstone queries
  require a suitable `includeDeleted` template. Physical cleanup is not another
  business deletion.
- Query failures discard the page rather than returning partial success. The
  candidate budget is 100,000 examined entries, branch expansion is capped at
  128, and HTTP encoded pages are capped at 16 MiB. Oversized branch plans fail
  validation; traversal and page budgets return
  `QUERY_WORK_LIMIT`. See the [API error table](api.md#query-errors).

## Examples

```json
{
  "collection": "posts",
  "filters": [
    {"field": "status", "op": "in", "value": ["published", "featured"]},
    {"field": "tags", "op": "contains", "value": "news"}
  ],
  "orderBy": [{"field": "createdAt", "direction": "desc"}],
  "limit": 20
}
```

The query requires an index capable of its access and ordering strategy; every
predicate is still checked on source documents. Complete index rules and
membership templates are described in the
[index design](../design/server/indexer/02.index.md#query-to-index-matching-rules).
