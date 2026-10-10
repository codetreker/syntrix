# Agent Note: Platform Cluster Membership and Configuration Control

Status: proposed

## Problem

The [runtime service manager](../../../../packages/syntrix/internal/services/manager_start.go)
starts configured services, while [service configuration](../../../../packages/syntrix/internal/services/config/config.go)
describes local/remote placement. Neither supplies platform membership or
configuration reconciliation. Operators need to distinguish available nodes,
exclusive work ownership, desired configuration, and successful convergence
without treating process startup as proof of fleet health.

The [platform control-plane design](../../../../docs/design/server/console/02.control_plane.md)
now scopes these mechanisms to the employee-facing Management platform outside
instance runtimes. The [product-boundary decision](2026-10-10-platform-console-instance-boundaries.md)
owns that separation from developer Console and application runtime identity.
Existing configured startup and logical-database lifecycle operations do not
establish a cluster control plane, and this gap does not imply that those current
operations fail.

## Proposal

Define platform node identity/capabilities, expiring membership leases, desired
configuration revisions, and each node's applied revision/status. The durable
authority must belong to platform Management. Its backend remains open pending
availability, consistency, contention, and operating-cost requirements.

The earlier proposal considered reusing the existing PostgreSQL metadata backend.
Under the accepted ownership model, that metadata store is instance-owned system
data, including project and logical-database state. Reuse of PostgreSQL tooling
may be considered for an independently owned platform store; borrowing a
developer instance's PostgreSQL is not a platform persistence contract. The
backend choice must not couple fleet recovery to the lifecycle of a developer's
instance.

Nodes reconcile only configuration explicitly classified as dynamically safe.
Validate a complete revision, stage resources, then apply it atomically at each
node or report a failure retaining the prior revision. Changes requiring restart
must be identified before acceptance. Serialize configuration publication with
an expected revision and report convergence independently from durable acceptance.

Lease expiry removes a node from new-work eligibility. Any component that obtains
exclusive work through the control plane requires a monotonically increasing
fencing generation that the work owner checks. A lease alone does not prevent a
partitioned prior owner from acting. Bound heartbeat/reconciliation retries and
queues; shutdown relinquishes leases and cancels workers.

Preserve static configuration as an explicitly selected runtime operating mode,
with one authority per managed setting. Scheduling and reconciliation retain
instance ownership; membership grants neither project enduser identity authority
nor application data access. Backup operations remain separately owned. This
proposal does not choose routes, schemas, Console account protocols, or a backend.

## Alternatives

**Use deployment-platform discovery and configuration exclusively.** This fits
platform-managed installations and avoids another reconciler, but requires an
adapter to present consistent behavior across supported runtime deployments and
to identify desired/applied revisions and stale ownership.

**Use an independently owned platform PostgreSQL store.** Existing PostgreSQL
expertise and metadata tooling may be reusable, but platform availability,
contention, lease expiry, and fencing requirements must be validated independently
of instance metadata. This option remains open; instance PostgreSQL cannot act as
its implicit authority.

**Introduce a dedicated consensus store.** This provides specialized membership
primitives but adds a dependency and operating burden. Reconsider when measured
platform availability and coordination requirements justify that cost.

## Acceptance Criteria

- Joining, expiring, restarting, and partitioned nodes report observable membership
  state; expired ownership cannot authorize exclusive work after reassignment.
- Conflicting publications, invalid revisions, and failed resource staging leave
  each node on a complete identifiable configuration.
- Recovery reconciles desired/applied revisions without unbounded retries;
  shutdown completes within the configured timeout.
- Static runtime configuration and existing logical-database lifecycle operations
  retain their documented behavior.
- Diagnostics identify instance, node, lease generation, configuration revision,
  and operation correlation without connection secrets or enduser credentials.
- The selected durable authority is platform-owned and its recovery does not
  depend on borrowing a developer instance's system-data store.

## Risks

Dynamic configuration and ownership fencing add substantial availability and
operational complexity. Deferral leaves topology changes dependent on deployment
operations; explicit static mode and configuration ownership keep future adoption
possible without competing authorities. Backend evaluation must account for fleet
failure and recovery independently of application-instance lifecycle.

## Dependencies

[Consumer shard scaling](2026-09-07-consumer-shard-scaling.md) owns assignment and
handoff behavior; [application observability](2026-09-07-application-observability.md)
owns generic metrics and tracing. The
[platform/Console/instance boundary decision](2026-10-10-platform-console-instance-boundaries.md)
owns product audience, project identity realms, and instance storage responsibility.
