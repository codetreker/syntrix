# Agent Note: Verifiable Instance and Database Recovery

Status: proposed

## Problem

The [system architecture](../../../../docs/architecture.md),
[boundary decision](../architecture/2026-10-10-platform-console-instance-boundaries.md),
and [platform operations design](../../../../docs/design/server/console/02.control_plane.md)
require recoverable developer resources with distinct instance, project, and
logical-database scopes. Independent backend dumps do not establish a common
recovery boundary or protect unrelated account/data scopes.

Current [storage assembly](../../../../packages/syntrix/internal/core/storage/factory_impl.go)
places documents and revocation in MongoDB and users/database metadata in
PostgreSQL. It has no coordinated backup catalog or restore operation. Database
deletion cleanup supplies no recovery artifacts. The accepted target makes
instance PostgreSQL private system storage for projects, application identities,
OAuth/client/provider/session state, and database metadata/configuration;
MongoDB holds developer business documents.

## Proposal

Define a versioned manifest identifying the instance and relevant project/logical
Syntrix databases, document data, authoritative system metadata, rule/trigger
configuration, backend/schema versions, checksums, and recovery checkpoints.
Specify separate full-instance, project, and logical-database scopes. Restoring
one database must preserve its project's identity realm and sibling databases;
restoring a project must preserve unrelated projects in the instance.

Employee Management coordinates authorized platform recovery operations;
developer Console exposes authorized recovery of owned/delegated resources.
Instance services execute local recovery within the explicit destination scope.
Employee/developer account stores and platform inventory are not part of an
instance's private PostgreSQL backup authority. Application tokens cannot request
platform resource recovery. Protect artifacts and retention; credentials and
encryption keys require separately controlled recovery access.

Coordinate PostgreSQL/MongoDB snapshots and log positions through explicit
write-fencing or change capture. Current Mongo revocation records must be included
where required by the current security contract; target Identity/session records
are PostgreSQL system data. Record recovery-point and recovery-time objectives
in minutes. PITR requires retained history in every authoritative backend and
rejects times outside their shared recoverable interval.

Restore to an isolated destination, verify checksums/schema compatibility, restore
authoritative records, rebuild derived indexes, and reset consumers to the
recovered event history. Project issuer/client/provider/session restoration must
preserve realm authority and explicitly decide credential/session validity.
Fence outbound triggers until duplicate/replay behavior is selected. Admit
traffic after validation. Durable job progress supports cancellation/restart;
cleanup targets only resources allocated by the job.

## Alternatives

**Independent backend dump instructions.** Useful for operator maintenance but
unable to prove a shared recovery point or automatic artifact integrity.

**Whole-instance recovery only.** This reduces scope choices but makes a single
database recovery copy or overwrite unrelated project/account state. Retain
whole-instance backups alongside explicitly scoped project/database recovery.

## Acceptance Criteria

- Manifests verify required artifacts and fail missing/corrupt/incompatible input
  before destination traffic is admitted.
- Recovery tests restore documents and authoritative configuration while
  preserving unrelated instances, projects, databases, and identity realms.
- PITR rejects unavailable times with the valid interval and supported points
  satisfy measured objectives in minutes.
- Restart/cancellation preserves recoverable progress and releases temporary
  resources without deleting source data.
- Index readiness, consumer positions, trigger replay, and Identity/session
  authority are verified before cutover.
- Diagnostics expose job/resource IDs and checkpoints without documents or
  credentials; Console/Management authorize their respective actors.

## Risks

Cross-backend consistency, retained history, storage cost, issuer recovery, and
replayed side effects require operational validation. Deferral leaves recovery
operator-managed. Stable instance/project/database identity and explicit
authoritative-versus-derived ownership keep manifests feasible without treating
logical database deletion as account deletion.

## Dependencies

[Indexer recovery](../architecture/2026-09-07-indexer-recovery-lifecycle.md) owns
derived reconstruction; [trigger idempotency](../architecture/2026-09-07-trigger-delivery-idempotency.md)
owns delivery replay. Instance Identity owns project realm/issuer/session policy,
and the platform boundary note owns employee/developer recovery authority.
