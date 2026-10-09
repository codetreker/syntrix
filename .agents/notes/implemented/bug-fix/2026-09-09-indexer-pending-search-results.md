# Agent Note: Preserve Pending Index Search Results

Status: implemented

## Problem

An index search can encounter a persisted row and a sampled in-memory operation
for the same document. Skipping the persisted row incorrectly marked the document
as already returned, so the merge also skipped its pending replacement. Results
could omit matching documents or fill a limit with later entries instead.

## Decision

The existing [search merge](../../../../packages/syntrix/internal/indexer/persist_store/pebble.go)
marks a document as seen only when appending it to the result. Suppressing a
persisted row because an in-memory operation shadows it does not set that mark.

| Effective in-memory operation | Search result |
|---|---|
| Matching upsert | Return its sampled order key once, in sorted position |
| Delete | Suppress the persisted row without a replacement |
| Upsert outside the bounds or cursor | Suppress the persisted row without a replacement |
| Pending and flushing for the same document | Pending takes precedence |

The [Search Consistency contract](../../../../docs/design/server/indexer/04.storage.md#search-consistency)
retains the existing memory snapshots, disk iterator, bounds, cursor, and limit.
Only the premature deduplication assignment is removed.

## Alternatives

**Flush before searching.** Avoiding pending data would change search latency
and its existing ability to read admitted updates. The merge must handle both
representations without requiring persistence first.

**Wait for persistence in the regression test.** That hides the overlapping
representations responsible for the bug. The fixture instead keeps one
persisted row and its memory replacement present throughout the public Search.

## Consequences

Deterministic tests demonstrate the omission on the original code and cover
equal/earlier/later order keys, deletion, bounds, exclusive cursors, limits, and
pending/flushing precedence after the fix. The fixture uses the existing mock
store without a batcher; it keeps one persisted query row so mock map iteration
does not introduce unrelated ordering assumptions.

This repairs result selection for the captured memory operations. It adds no
stronger concurrent snapshot guarantee and changes no locks, batching, storage
format, checkpoint, or public API.
