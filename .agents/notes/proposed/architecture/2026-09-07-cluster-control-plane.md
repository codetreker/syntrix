# Agent Note: Cluster Membership and Configuration Control

Status: proposed

## Problem

The [control-plane discussion](../../../../docs/design/server/console/02.control_plane.md)
is explicitly a placeholder listing node discovery, health, and dynamic
configuration. The [service manager](../../../../packages/syntrix/internal/services/manager_start.go)
starts configured services, while [service configuration](../../../../packages/syntrix/internal/services/config/config.go)
describes local/remote placement. Static inspection does not establish a cluster
membership or configuration-reconciliation service. This is planned operating
capability, not evidence that configured standalone or distributed startup fails.
Database CRUD, suspension, and deletion already have their own implementation.

## Proposal

Define a control-plane API for node identity/capabilities, expiring membership
leases, desired configuration revisions, and each node's applied revision/status.
Use the existing PostgreSQL metadata backend as the proposed durable authority;
review its availability and contention requirements before implementation.

Nodes reconcile only configuration explicitly classified as dynamically safe.
Validate a complete revision, stage resources, then apply it atomically at each
node or report a failure retaining the prior revision. Changes requiring restart
must be identified before acceptance. Serialize configuration publication with
an expected revision and report convergence independently from durable acceptance.

Lease expiry removes a node from new-work eligibility. Any component that obtains
exclusive work through the control plane requires a monotonically increasing
fencing generation that the work owner checks. Bound heartbeat/reconciliation
retries and queues; shutdown relinquishes leases and cancels workers. Preserve
static configuration as an explicitly selected deployment mode, with one authority
per managed setting. Backup operations remain separately owned.

## Alternatives

**Use deployment-platform discovery and configuration exclusively.** This fits
platform-managed installations and avoids another reconciler, but requires an
adapter to present consistent behavior across standalone and other deployments.

**Introduce a dedicated consensus store.** This provides specialized membership
primitives but adds a dependency and operating burden. Reconsider if measured
requirements exceed the existing metadata backend's guarantees.

## Acceptance Criteria

- Joining, expiring, restarting, and partitioned nodes report observable membership
  state; expired ownership cannot authorize exclusive work after reassignment.
- Conflicting publications, invalid revisions, and failed resource staging leave
  each node on a complete identifiable configuration.
- Recovery reconciles desired/applied revisions without unbounded retries;
  shutdown completes within the configured timeout.
- Standalone static configuration and existing database lifecycle operations retain
  their documented behavior.
- Diagnostics identify node, lease generation, configuration revision, and operation
  ID without connection secrets.

## Risks

Dynamic configuration and ownership fencing add substantial availability and
operational complexity. Deferral leaves topology changes dependent on deployment
operations; explicit static mode and configuration ownership keep future adoption
possible without competing authorities.

## Dependencies

[Consumer shard scaling](2026-09-07-consumer-shard-scaling.md) owns assignment and
handoff behavior; [application observability](2026-09-07-application-observability.md)
owns generic metrics and tracing.
