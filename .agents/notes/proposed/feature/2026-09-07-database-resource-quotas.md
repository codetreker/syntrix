# Agent Note: Enforce Per-Database Resource Quotas

Status: proposed

## Problem

The [database model](../../../../packages/syntrix/internal/core/database/types.go) stores
`MaxDocuments` and `MaxStorageBytes`, and metadata APIs persist those settings.
The [integration design](../../../../docs/design/server/core/database/04.integration.md)
explicitly labels resource enforcement as future work. Production references to
these fields are confined to metadata handling. By contrast,
[database creation](../../../../packages/syntrix/internal/core/database/service.go) already checks
`MaxDatabasesPerUser`, and the
[deletion worker](../../../../packages/syntrix/internal/core/database/deletion_worker.go) performs
background cleanup. The missing capability is document/storage admission within
each database, not all quota or lifecycle handling.

## Proposal

Define `max_documents` as the number of live documents and `max_storage_bytes`
as the canonical persisted document bytes, including retained tombstones.
Physical indexes, replay caches, and backend allocation overhead require separate
operational capacity reporting. Preserve zero as unlimited.

Enforce quotas at the shared storage mutation boundary used by CRUD, replication,
and internal writers. Keep authoritative usage and published limit revisions
alongside document storage so each mutation and its usage delta can commit
atomically. Administrative metadata updates publish a new limit revision through
a recoverable operation; responses distinguish pending application from active
limits. Reject growth beyond an active limit while allowing shrinking writes and
deletes. Lowering a limit below usage must preserve existing data and expose the
over-limit state.

Bootstrap counters from a consistent scan and reconcile them against stored data.
Concurrent writes must be fenced or captured during bootstrap; a database cannot
claim enforced quotas before its baseline is valid. TTL/tombstone cleanup and
database deletion update usage through the same accounting contract.

## Alternatives

**Count documents before each write.** This avoids maintained counters but permits
concurrent writers to pass the same limit and makes admission increasingly costly.

**Allow bounded overage using asynchronous estimates.** This reduces contention
but changes quotas into advisory limits. Reconsider only with an explicit overage
budget in the product contract.

## Acceptance Criteria

- Concurrent creates and size-increasing updates cannot exceed active limits;
  replacement, replication, deletes, tombstone expiry, and retries account once.
- Zero limits remain unlimited; lowering a limit preserves data and rejects only
  prohibited growth until usage falls below it.
- Restart and interrupted baseline/reconciliation cannot reset usage or report
  enforcement before a valid baseline exists.
- Separate databases retain separate counters; existing user database-count
  quota and deletion behavior remain intact.
- Rejections expose database, limit kind, revision, and measured usage without
  document contents.

## Risks

Atomic accounting adds write contention and backend transaction requirements.
Usage migration and limit publication need explicit recovery semantics. Deferral
leaves resource settings descriptive; keeping their meaning and zero-value
semantics stable prevents future enforcement from redefining existing metadata.
