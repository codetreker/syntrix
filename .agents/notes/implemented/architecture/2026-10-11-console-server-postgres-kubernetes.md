# Agent Note: PostgreSQL-owned Console and Kubernetes Workloads

Status: implemented

## Problem

Developers need to request and manage deployment resources without acquiring
employee platform authority or application-user privileges. A deployment request
can outlive the accepting process, and PostgreSQL/Kubernetes failures can occur
between request acceptance, an external effect, and recording its result. Account
ownership, request identity, and deletion must survive those interruptions.

The former embedded administration SPA served one instance using instance login.
It provided neither independent developer accounts nor durable provisioning.
The [platform boundary proposal](../../proposed/architecture/2026-10-10-platform-console-instance-boundaries.md)
owns the complete Management/Console/instance target, including project Identity.
This note owns the delivered Console mechanism without treating full instance
or Management deployment as implemented.

## Decision

An independent Go module at `packages/console-server` serves developers.
The old `packages/console` frontend and Gateway static route are removed.
Module configurations own their types, defaults, and validation; the root loader
composes them. Instance runtime APIs and end-user Identity remain separate.
The later [Console client decision](2026-10-12-console-client-session-and-distribution.md)
adds an independent bilingual login/shell and static image without changing this
backend's authentication endpoints, PostgreSQL authority, or API-only build.

Console has its own PostgreSQL database. It is authoritative for developer IDs
and password hashes, opaque sessions and CSRF state, resource ownership, desired
state, immutable template/target snapshots, developer-scoped idempotency keys,
operation revisions and results, Kubernetes object UIDs, and observation times.
Configuration bootstraps absent developer accounts without resetting existing
passwords or transferring their resource ownership. Sessions expire after a
bounded configured duration, defaulting to 24 hours; logout revokes them.
Authenticated mutations require the matching CSRF token, and login enforces the
configured public origin.

Deployment requests select operator-defined templates. The HTTP test template
creates a Deployment and ClusterIP Service; developers cannot submit arbitrary
Kubernetes objects or image/resource overrides. Durable intent commits before
execution. The accepted template and Kubernetes target are frozen so later
configuration changes cannot silently alter retry behavior.

A PostgreSQL session advisory lock grants one executor authority to reconcile.
A workload row transaction serializes its desired revision with a bounded
Kubernetes call and persisted receipt. No distributed transaction is promised.
If Kubernetes acts but receipt persistence fails, retry verifies object ownership
and recovers the recorded result. Losing database authority prevents new work.
Installation/workload identities and UIDs prevent foreign-object adoption or
deletion; the Deployment anchors the Service owner reference.

Accepted, pending, ready, failed, deleting, and deleted results have distinct
meanings. Readiness observes the intended Deployment generation and matching
ready Service endpoints. Deletion persists intent before cleanup and prevents stale create work
from overruling it. Deleted records and idempotency receipts remain tombstones;
an old Console create retry cannot recreate a deleted resource. Tombstones remain
under reconciliation so an earlier timed-out Kubernetes create committing late
cannot leave an owned resource permanently uncollected. Observation timestamps
allow consumers to distinguish recorded state from current availability.

Kubernetes is accessed through a separable provisioning adapter with an explicit
kubeconfig/context or explicitly selected in-cluster credentials. Development
clusters are operator-provided; this service does not manage k3d cluster setup or
shutdown. Namespace/RBAC restrict resource authority, and ClusterIP remains an
internal endpoint. The platform deployment namespace is `syntrix-platform`,
hosting Console and providing a shared location for future Management services.
Test workloads run in `syntrix-tests` through the current Console ServiceAccount
and workload role. Snapshots persist namespace and installation ID; the executor
rejects a mismatch against current configuration. Installation ID is stable for
one Console/cluster target and changes on cluster relocation. Reusing it with a
changed kubeconfig/context does not independently prove cluster identity.

Schema upgrades may be destructive; no legacy migration or compatibility is
delivered. Startup does not delete existing data. The [Console design](../../../../docs/design/server/console/01.console.md)
and [API reference](../../../../docs/reference/console_api.md) own supported
interfaces and lifecycle details.

The module-owned [go-cov policy](../../../../packages/console-server/.go-cov.toml)
enforces total 90%, package 85%, and function 80% coverage, race detection,
and critical uncovered-block checks. `make test`, `make coverage`, CI, and
pre-push share the explicit enforcing entrypoint and require a PostgreSQL test
database. Command code is included; no coverage exclusions are configured.
The concurrent idempotency fixture holds a PostgreSQL SHARE table lock while
all callers finish the absent-request lookup and wait for INSERT's ROW EXCLUSIVE
lock. Observing every waiting INSERT before releasing the barrier guarantees
one accepted request and conflict replay for the remaining callers. This makes
branch coverage depend on the required database interleaving, not scheduler
luck. Fixture cleanup releases the transaction and joins callers before dropping
its private schema.

## Alternatives

**Keep the embedded instance frontend as developer Console.** Its account and
resource scope were one runtime, so retaining it would preserve ambiguous
developer/end-user authority and require replacing its authentication and
instance selection throughout. The approved change drops that package and
establishes an independent backend.

**Persist deployment intent in Kubernetes ConfigMaps.** This was considered for
restart recovery, but Console source data must be managed in PostgreSQL.
Kubernetes is the resource executor and observed state, not the developer account,
ownership, session, or request database.

**Build employee Management before Console deployment execution.** Management
remains the fleet execution target, but the approved sequence needs a developer
backend that can demonstrate actual Kubernetes creation now. Keeping the direct
adapter separate preserves Console's ownership and acceptance model when
Management supplies execution later.

**Migrate legacy accounts and document namespaces.** The approved upgrade policy
allows destructive changes and requires no old-data migration. No implicit
mapping converts instance accounts into developers or projects.

## Consequences

Requests, ownership, and recovery do not depend on browser state or a live
accepting process. Database failure prevents acceptance instead of fabricating
success. Single-executor and per-row serialization favor clear lifecycle ordering
over parallel deployment throughput; bounded calls prevent indefinite database
lock retention.

PostgreSQL and Kubernetes interruptions remain recoverable through recorded
intent and verified object identities. They are not atomic effects across both
systems. Stale observations and cleanup failures stay visible; successful
acceptance is not a workload availability promise. Persistent tombstones consume
storage until a retention policy can preserve the idempotency guarantee.

The HTTP test template validates provisioning mechanics. Full Syntrix instance
bundles remain in the [platform boundary proposal](../../proposed/architecture/2026-10-10-platform-console-instance-boundaries.md):
they add persistent volumes, dependency readiness, secret distribution, endpoint
publication, and explicit whole-instance cleanup/backup policy. Freezing templates
and targets preserves the operation model while those capabilities are added.
The current service rejects unsupported templates and cannot claim to deploy
MongoDB/PostgreSQL/Syntrix as an instance.

Management-specific service identities and permission policy remain part of
Management delivery. Current deployment uses only the access needed to complete
Console's request lifecycle. Adding Management will require its deployment and
permission configuration; the separate provisioning adapter preserves that
choice without changing developer accounts or accepted workload records.

OAuth login, self-service profiles, resource-administration UI, project realms,
full instance administration, and employee Management remain substantive
follow-on work in the existing Console/boundary proposals. The independent
client now provides login, session restoration, logout, and Overview; it does
not expose workload administration. Stable developer IDs preserve ownership when account
flows are expanded; separate provisioning modules avoid moving developer source
data into Management. UI work requires final-style interactive mockups and must
use server-validated authority. No instance application token grants Console
deployment rights.

The old embedded build-delivery and collection-discovery proposals are rejected
because their consumer was removed. Remaining rule, diagnostics, realtime,
trigger, and self-service requirements retain their active owners.
