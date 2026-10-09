# Agent Note: Verifiable Database Backup and Recovery

Status: proposed

## Problem

The [control-plane discussion](../../../../docs/design/server/console/02.control_plane.md)
lists backup, restore, and point-in-time recovery as future topics. The
[authentication design](../../../../docs/design/server/core/identity/02.authentication.md)
requires user records and rule versions in backups. Current
[storage assembly](../../../../packages/syntrix/internal/core/storage/factory_impl.go) places documents
in MongoDB and users/database metadata in PostgreSQL. Static inspection finds no
coordinated backup catalog or restore operation. Existing database deletion
cleanup is implemented; it does not supply recovery artifacts.

## Proposal

Define a versioned backup manifest covering database identity, document data,
metadata, rule/trigger configuration, backend/schema versions, checksums, and
recovery checkpoints. Define separate scopes for a logical database and the
installation-wide identity store so restoring one database cannot overwrite
unrelated users. Backup artifacts must have protected access and retention;
credentials and encryption keys require a separately controlled recovery path.

Coordinate a consistent recovery boundary across PostgreSQL and MongoDB using
backend-native snapshots/log positions plus an explicit write-fencing or change-
capture protocol. Measure and document the resulting recovery-point and recovery-
time objectives in minutes. Point-in-time recovery requires retained history in
every participating backend and rejects requested times outside their common
recoverable interval.

Restore into an isolated destination, validate checksums and schema compatibility,
restore authoritative records, rebuild derived indexes, and reset consumer
checkpoints to the recovered event history. Keep outbound trigger delivery fenced
until its duplicate/replay policy is explicitly selected. Admit traffic only after
validation. Persist job progress for cancellation and restart; cleanup must target
only resources allocated by that job.

## Alternatives

**Publish independent database dump instructions.** Useful for operator maintenance,
but they cannot establish a shared recovery point or automatic integrity checks.

**Back up the whole installation only.** This reduces cross-scope choices but makes
single-database recovery overwrite or copy unrelated state. Retain installation
backups alongside explicitly scoped logical recovery.

## Acceptance Criteria

- A backup manifest verifies all required artifacts; missing/corrupt artifacts and
  incompatible schemas fail before destination traffic is enabled.
- Restore tests recover documents, metadata, and configuration at the stated point
  while preserving unrelated databases/accounts.
- Point-in-time requests outside retained common history fail with the available
  interval; supported points satisfy measured recovery objectives.
- Restart/cancellation preserves recoverable job state and releases temporary
  resources without deleting source data.
- Trigger replay and derived-index readiness are verified before cutover; job
  diagnostics expose IDs and checkpoints without data or credentials.

## Risks

Cross-backend consistency, retained history, storage cost, and replay side effects
require substantial operational validation. Deferral leaves recovery dependent
on operator-managed artifacts; stable database identity and explicit authoritative
versus derived data ownership keep a future manifest feasible.

## Dependencies

[Indexer recovery](../architecture/2026-09-07-indexer-recovery-lifecycle.md) owns
derived-index reconstruction; [trigger delivery idempotency](../architecture/2026-09-07-trigger-delivery-idempotency.md)
owns duplicate-delivery semantics.
