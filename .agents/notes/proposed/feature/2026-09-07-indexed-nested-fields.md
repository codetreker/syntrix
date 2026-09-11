# Agent Note: Resolve Nested Field Paths Consistently in Indexes

Status: proposed

## Problem

The original field extractor advertised paths such as `user.name` while reading
only a literal top-level key. A nested document could therefore appear indexed
while its key represented a missing value. The
[completed indexed-query decision](../../implemented/bug-fix/2026-09-07-indexed-query-filter-semantics.md)
now rejects dotted template/query fields explicitly and shares top-level metadata
resolution across source scan and live projection. This prevents ambiguous
interpretation but does not provide nested-field queries.

Nested objects remain valid stored data. A shared path contract is needed before
accepting their fields for index access, residual filtering, or ordering. Rebuild
and live projection must agree so reconstruction cannot change field meaning.

## Proposal

Specify dot-separated object paths for index template fields and query fields, with a shared resolver used by live key construction and rebuild key construction. A path walks object members one segment at a time. Define missing members, explicit null, and a non-object intermediate value consistently with storage predicate semantics. Validate malformed paths, including empty segments, when loading templates and planning queries.

Do not infer array traversal from object-path syntax; array membership remains the separately specified operator capability. Define how literal dotted property names are represented or declared inaccessible through this path syntax before accepting such templates, avoiding an ambiguous fallback from nested lookup to a top-level dotted key.

Reuse existing scalar order encoding after value resolution. Mark indexes affected by the extractor change as requiring reconstruction and invalidate their old cursors, since existing persisted keys may encode missing values. Cover equality, ranges, ordering, updates that move a nested value, and removal of a nested member. Errors should identify the template and field path without serializing source documents.

## Alternatives

**Flatten documents before storage.** This simplifies lookup but changes stored user-data shape and creates collisions between literal dotted names and nested objects. Field interpretation belongs at the indexing boundary.

**Restrict indexes to top-level fields.** This is the implemented validation
boundary while path semantics remain unresolved. It makes unsupported queries
fail explicitly but does not satisfy the nested-field requirement retained here.

## Acceptance Criteria

- The nested-object example indexes and queries `user.name` as `"Ada"` in memory and persistent modes.
- Missing, null, non-object intermediate, literal dotted names, and malformed paths each have specified and tested outcomes.
- Live ingestion and rebuild produce identical keys; nested updates and deletes remove obsolete entries.
- Cross-database fixtures remain isolated, and an affected persisted index cannot become ready without reconstruction.

## Risks

Resolving previously missing values changes ordering and index membership. Inconsistent path rules between Mongo, Indexer, and query validation would recreate deployment-dependent results. Path parsing should avoid repeated allocation on the event hot path.

## Dependencies

[Indexer recovery](../architecture/2026-09-07-indexer-recovery-lifecycle.md) owns reconstruction, and [query cursor pagination](../../implemented/feature/2026-09-07-query-cursor-pagination.md) owns invalidation of old continuation formats and generations.
