# Agent Note: Console-managed Instance Lifecycle

Status: proposed

## Problem

Console has independent developer accounts and PostgreSQL-owned Kubernetes test
workload intent, while Syntrix has standalone/distributed runtime templates.
Developers still cannot create a complete instance, allocate its private source
databases, inspect its components, or manage its configuration and lifetime
through Console. Joining these capabilities requires ownership beyond a Pod:
source credentials, signing material, persistent runtime state, broker resources,
and a published application endpoint must belong to one durable instance.

An accepted API request is not a completed instance operation. Console can fail
between recording intent, allocating a database, applying Kubernetes resources,
and receiving a result. Reconciliation must distinguish its own late effects from
foreign resources and retain enough authority to clean up after deletion. Ordinary
replacement must preserve source data and initialized runtime state; it cannot
silently rerun destructive maintenance.

## Proposal

The [instance lifecycle design](../../../../docs/design/server/console/03.instance_lifecycle.md)
owns the mechanism and five ordered delivery PRs. The
[managed runtime contract](../../../../docs/design/server/console/04.managed_runtime_contract.md)
specifies PR 1 public rendering, attachment, initialization, and readiness. This
note owns the proposed
allocation, ownership, and recovery decisions. The
[platform-boundary proposal](2026-10-10-platform-console-instance-boundaries.md)
retains employee Management, project Identity realms, and OAuth responsibilities.
The [runtime deployment decision](../../implemented/architecture/2026-10-12-instance-container-kubernetes.md)
remains the owner of delivered CLI, templates, and current runtime limitations.
The [workload-interface proposal](../feature/2026-10-12-console-workload-interface.md)
continues to own the browser interface for existing HTTP test workloads; it is
independent of the full-instance interface specified here.

### Source authority and administrative pools

Console PostgreSQL remains authoritative for developer ownership, instance IDs,
backend pools, allocation bindings, selected published runtime-package receipts,
immutable desired-spec revisions, operation
phases, observations, deletion tombstones, external-effect attempts/resource
receipts, and encrypted credentials. Kubernetes and backend servers execute recorded intent;
they do not replace Console source authority. Developer endpoints remain scoped
to the authenticated Console developer and use the existing session, Origin, and
CSRF contracts.

Trusted operator configuration/bootstrap import registers existing MongoDB,
PostgreSQL, and NATS physical servers with the administrative privileges required
for allocation. A full employee Management authentication system is not a
prerequisite for this flow. Pool configuration is an administrative capability,
never a developer-supplied connection string or runtime configuration field.
An administrative browser interface is not required for configured pools to be
usable; a later interface can operate on the same pool records without changing
instance ownership.

Console allocates one private MongoDB database and database-scoped user, plus one
private system PostgreSQL database and role, for every instance. Separate logical
databases share physical servers without sharing application database authority.
Allocation establishes access isolation, including PostgreSQL connection
privileges, before publishing credentials to runtime Pods. Administrative
credentials stay with Console. Pods receive only instance-scoped source
credentials and the permitted runtime broker credentials.

Secret material is durably encrypted before the first external effect that needs
it; encryption keys remain outside Console PostgreSQL. Binding records retain
credential generations and exact target identities so retries recover the same
credentials and allocations. API responses, observations, routine logs, and
rendering errors exclude infrastructure credentials and signing material. Initial
instance-administrator credentials have a separate explicit owner-authenticated
handoff; they never appear in ordinary lists, polling, or browser persistence.
Database backups and encryption-key custody must be operationally coordinated:
restoring encrypted records without their key cannot recover ownership actions.

### Shared broker ownership

A distributed instance may use a shared NATS account. Console gives each instance
an immutable, non-reused stream name, a non-overlapping subject namespace, and one
durable consumer; Trigger Worker replicas share that consumer. A Kubernetes name
prefix alone does not isolate JetStream resources. Instance credentials and
broker permissions are documented with their actual scope; shared credentials
are never described as instance-scoped or destroyed during instance deletion.

Console is the complete canonical owner of the stream configuration, including
storage, retention, limits, subjects, and consumer identity. The runtime must
attach to and verify a provisioned stream/consumer without issuing an upsert that
restores runtime defaults or resets a Console-managed quota. The existing CLI
deployment path retains its current broker-creation behavior until that runtime
integration is delivered. Stream names and subject prefixes are explicit runtime
configuration, not a dependence on the current global `TRIGGERS` default.

Deletion removes only the recorded instance stream and durable consumer. It never
deletes the shared NATS account, server, other streams, or shared credentials.
Broker isolation does not imply a new task-durability guarantee; the chosen stream
storage and retention policy must state its actual failure behavior.

### Published runtime selection

The [release guide](../../../../packages/syntrix/deployment/RELEASE.md) owns the
implemented manual publication of tested version-matched images and deployment
archives. A main commit is not itself a release. New instance provisioning selects
the latest complete published non-draft/non-prerelease release in the fixed
`syntrix-YYMMDD.sequence` family, ordered by numeric date and sequence, from the
administrator-configured Syntrix source. The metadata's exact workflow run/attempt
must have succeeded from its main source commit. A consumed tag, an image alone,
or a draft/failed publication cannot supply that package.

PR 1 adds an integer managed-runtime contract version to `meta.json` and matching
bounded `syntrix-cli deployment-contract` output, including version/source commit
and required modes/phases. Console validates the selected release's archive,
metadata and bundled CLI before backend effects. Missing or unsupported contract
versions fail visibly; an older compatible release is not silently substituted.
The administrator controls source identity and supported contract versions;
developers cannot submit executable URLs or arbitrary images.

The durable receipt freezes repository/release/tag/asset identities, computed
archive SHA-256, version/source SHA, exact workflow attempt, index digest/platforms,
contract version and resolution timestamp. Cache and recovery verify those exact
bytes and identities. Bootstrap, acceptance, finalization and every full runtime
role use the same recorded package and versioned image tag, such as
`ghcr.io/codetreker/syntrix-server:261013.1`, with `imagePullPolicy: IfNotPresent`.
Console's Pod image references omit the digest suffix; metadata's index digest
remains release validation and diagnostic evidence. Request replay, retry, start
and restart retain that release; later publication does not change existing
instance intent. Version tags must never be repointed or overwritten; corrections
receive a new version. The publisher allocates fresh versions, while preserving
those tags remains an operational policy rather than a GHCR-enforced guarantee.
Only an explicit compatible revision in PR 4 can select a different supported
package; incompatible image/data-format migration remains separate work.

### Rendering and lifecycle recovery

The side-effect-free public runtime renderer owns the published CLI implementation
and canonical standalone/distributed templates. Console's existing provisioning
adapter downloads and verifies the selected package, chooses its Linux host CPU's
bundled CLI and invokes it directly with matching templates under a bounded
process context. Typed values, topology, replica restrictions, validation and
runtime probes therefore travel with the deployed image, even when Console and
Syntrix releases differ. Backend allocation can share the public broker
specification only within explicitly supported managed contract versions.

Controlled private extraction rejects traversal and escaping links and limits
file count, compressed/unpacked bytes and path lengths. Command arguments contain
fixed flags and private file paths, never credentials; values and output use
protected files, and diagnostics sanitize subprocess output. Console reconciles
the resulting resources through its existing client-go infrastructure. It does
not invoke a shell, copy an independently maintained template set or apply an
older compiled-in renderer to arbitrary newly downloaded templates.

Every external action records its attempt, effect identity, target, and exact
request generation before the bounded call. An unknown owner-creation outcome
must be observed before another attempt; mutating clients cannot silently resend
it. An owned object's matching effect identity or definitive server rejection
can settle the original attempt. Timeout, cancellation, and observed absence
cannot prove that a delayed owner will not appear.

No PostgreSQL transaction spans the complete multi-system allocation, bootstrap
readiness, Pod termination, or other long waits. Immutable installation, instance,
and allocation identities determine resource names; these names are
never reused. Kubernetes ownership annotations and recorded UIDs protect apply,
observation, and deletion. Database/broker allocations retain their pool binding,
unique target names, and credential generation as equivalent ownership evidence.
An unrecorded or foreign object is a conflict, not an invitation to adopt it.

Deletion is durable intent. Tombstone reconciliation continues to find and remove
owned effects that arrive after a timed-out create or an earlier cleanup pass.
Bootstrap handoff, completed stop, and destruction require every earlier
owner-introducing attempt to settle, even when current Pods are absent. Unresolved
attempts block activation and destruction while observation continues. Trusted
operator recovery requires authoritative server-side completion or an actual
execution fence; unsupported or insufficient evidence leaves the gate blocked.
Tombstone cleanup cannot substitute for this exclusivity guarantee. Source
databases and broker resources are not destroyed while owned runtime Pods can
still use them. Failed cleanup remains visible with its pending owned targets;
Console never reports complete deletion while those targets remain unsettled.
This is recoverable execution across independent systems, not an exactly-once
transaction.

Initial creation renders a temporary bootstrap Pod with `restartPolicy: Never`.
Bootstrap reaches the runtime's applied-readiness barrier while the process stays
running. Console then requests graceful termination, waits for the Pod to stop,
and only then starts normal roles against the same signing material and claims.
It must not treat the bootstrap process as a batch Job that exits on readiness or
allow bootstrap and normal owners to overlap on persistent stores. Component
readiness, a real indexed-query acceptance check, and successful access through
the configured public or internal-only route establish instance readiness;
listener probes alone do not. Module-owned preparation verifies schemas and
non-cleanup indexes; document cleanup TTL and internal cleanup/Trigger writers
remain held through normal read-path indexed-query acceptance. An explicit
acceptance-only process/render option suppresses colocated writers that distributed
replica settings cannot hold. A bounded finalizer in the same runtime image then creates or
verifies owner-defined cleanup indexes and returns an observed specification
receipt. Console persists that receipt, activates the full normal render, and
verifies all required roles before application access/write release. Held-role
process readiness does not claim full instance availability.
The command opens no state claims or listeners and changes no server-wide TTL
setting. Observed definitions establish preparation/finalization; no parallel
runtime finalization database is introduced. Partial or uncertain finalizer effects
converge against the original specification and remain subject to effect settlement
before deletion. Restart exposes missing cleanup without silently repairing it.

Create, delete, start, stop, and restart operate through persisted desired state.
Stop retains source databases, credentials, signing material, claims, and owned
message resources. Start and ordinary restart reuse initialized state without
bootstrap or reset. Delete is explicitly destructive: after stopping the runtime,
it removes owned source databases/accounts, runtime resources, claims, signing
material, and broker resources while preserving shared infrastructure. The API
and bilingual interface state these different data consequences directly.

Stopping unfinished bootstrap leaves partial claims `initialization-required`;
restart, configuration, and resize cannot treat them as initialized. Recovery
requires owned deletion and a new create, not automatic reset/rebootstrap. Once
bootstrap completion is durably recorded, stop preserves that receipt and a later
start continues handoff. This distinction prevents a data-preserving action from
silently changing the initialization contract.

### Configuration and resize

Configuration edits and resize use immutable desired-spec revisions with
compare-and-swap acceptance. A stale request fails without overwriting a newer
desired revision. An observation identifies the revision it represents, and
unfinished operations remain distinct from the last applied state. Console
validates and renders the entire proposed configuration with its selected package
before accepting effects. Ordinary config/resize retains that version; an explicit
compatible release revision records a newly validated package before rollout.

Resize covers supported role replicas, CPU/memory requests and limits, and the
managed stream limits defined by the design. Stateful single-owner roles retain
their runtime restrictions. It does not promise automatic topology conversion,
image/data-format migration, or migration between source pools. Those cases fail
validation rather than applying part of a change. Broker-limit resize converges
the complete Console-owned stream specification without changing stream/consumer
identity or allowing runtime attachment to restore previous limits.

### Credential transitions

Explicit rotation records `prepared`, `quiescing`, `backend_changed`, `verified`,
`applying`, `converged`, and `retired` stages. Replacement secrets are persisted
before backend mutation. MongoDB/PostgreSQL single-password users require settled
owner launches and stopped runtime owners before changing the same scoped user's
password. Authentication with the replacement verifies its current value; it does
not establish settlement of a delayed mutation. Only definitively rejected
requests retry that recorded password. No automatic old-password rollback is
allowed. Previously running instances resume after adoption; stopped instances
adopt the new revision and stay stopped. This preserves ownership while accepting
explicit downtime.

Shared NATS rotation covers every active and stopped binding and requires
administrator authority over server authentication. With externally prepared
overlap on the same account, validate replacement access to owned streams and
converge all bindings; every old-generation launch must settle/drain before
administrator retirement, followed by verification that the old credential is
rejected. Without overlap, quiesce all affected instances before the external
change, then import/validate the replacement and restore prior desired states.
Unsupported authority or incomplete adoption blocks completion. Ordinary config
editing never silently changes passwords; generic NATS client access is not
server-authentication administration.

Password authentication verifies the current value rather than request settlement.
Any unresolved credential mutation blocks resend, completion/retirement, and the
next credential generation until authoritative completion or execution fencing.
This prevents a delayed older password change from overwriting a newer rotation.

### Scope and future costs

The five PRs sequence the complete accepted lifecycle, configuration, resize,
bilingual interface, final-style interactive mockups, and real-backend acceptance.
Sequencing does not remove any of those outcomes.

Employee Management, project realms, and OAuth remain separate target work in the
platform-boundary proposal. Adding them later requires new authority and identity
contracts, but stable developer IDs and separate pool/instance ownership avoid
redefining application users as platform administrators. Administrative pool
browser pages can be added over the same trusted pool import model; doing so adds
operator UI and permissions rather than changing the allocation schema.

Topology changes, source-pool migration, image/data-format migration, and backups
remain separate operations. Deferring them leaves explicit unsupported requests
and may require new transfer, quiescence, or compatibility mechanisms later.
Immutable bindings and revisioned specifications preserve the evidence those
operations need; they do not establish a migration or backup guarantee. The
[backup/recovery proposal](../feature/2026-09-07-database-backup-recovery.md)
retains that broader recovery work. Delete has one explicit destructive meaning
and stop retains data; additional deletion retention modes would add policy and
cleanup states rather than changing the meaning of an accepted operation.

## Alternatives

**Runtime administrative HTTP finalization.** A new privileged endpoint would
require another authorization and exposure contract for one finite initialization
step. The same-image command can reuse configuration and owner initializers
without adding a serving capability. Console owns query acceptance and command
ordering; the finalizer verifies definitions and returns its own observed receipt.

**Enabling all TTL during bootstrap.** Document TTL can delete source tombstones
before the normal indexed query is accepted, violating the existing maintenance
fence. Preparation therefore excludes cleanup, and finalization follows acceptance.
Per-instance orchestration cannot toggle a shared server's global TTL monitor.

**Deploy dedicated physical database servers per instance.** This provides a
separate infrastructure lifetime but adds database installation, storage,
patching, backup, and capacity management to every instance. Existing physical
servers with per-instance databases and scoped accounts deliver private source
ownership without requiring that infrastructure contract. Dedicated placement
can be reconsidered when a workload requires physical isolation or independently
managed failure domains; pool binding must preserve that option.

**Require an exclusive NATS server or account for every instance.** This matches
the current runtime's fixed stream and consumer names, but it makes account
provisioning a prerequisite and excludes permitted shared-account deployments.
Explicit non-overlapping subjects and immutable per-instance stream/consumer
identities supply the required resource ownership. Dedicated accounts remain a
valid pool configuration when stronger broker permission isolation is needed.

**Deploy a floating latest image.** A mutable tag can change between bootstrap,
acceptance, restart and later retries, without a matching CLI/template change.
The selected package and stable version tag follow the no-overwrite publication
policy, keeping ordinary replacement on the recorded release. Choosing latest
happens once for a new instance or an explicit compatible revision, not every Pod
replacement. `IfNotPresent` then reuses the selected version on nodes that already
have it, without relying on a floating `:latest` tag.

**Append the registry index digest to Console Pod references.** The publisher's
archive examples support digest-qualified operator deployment. Console uses the
human-readable version tag and `IfNotPresent` under the strict no-overwrite
publication policy. The digest still validates the selected release and supports
diagnostics; this choice accepts the operational requirement that corrections
always publish a new version.

**Render downloaded templates with Console's embedded renderer.** Sharing a pure
renderer initially avoids process execution, but a newly published runtime can
change its model fields, validation and lifecycle semantics independently of
Console. Downloaded template syntax cannot prove that an older compiled-in
renderer implements them. The public renderer remains the matched CLI's owner;
a compact supported contract marker gates Console adapters and allocation.

**Invoke the CLI through a shell or copy its templates into Console.** A shell
adds expansion and credential-handling risks, while copied templates let Console
and runtime disagree about roles, bootstrap or validation. Direct bounded
execution of the verified package CLI with its exact templates retains one
release contract. This accepts private extraction/cache and process lifecycle
handling in the existing executor without adding a general script runner.

**Introduce a new controller or CRD and complete Management first.** A Kubernetes
API can own custom resource observations, but it does not remove Console's need
for developer authority, database allocations, encrypted secrets, and durable
deletion history. The existing PostgreSQL/client-go lifecycle already supplies
that foundation. A new fleet control plane can be reconsidered for independent
placement and scheduling requirements; it must preserve recorded instance
ownership rather than requiring this flow to depend on employee authentication.

## Acceptance Criteria

- Configured administrative pools allocate isolated MongoDB and PostgreSQL
  databases/accounts; runtime Pods never receive pool administration credentials.
- Distributed instances coexist on a shared NATS account with distinct streams
  and non-overlapping subjects; all replicas of one worker role share its durable
  consumer, and runtime attachment preserves Console's complete stream policy.
- New creation selects the latest complete successfully published release by
  numeric date/sequence, verifies its asset/metadata/matched CLI contract before
  backend effects, and records the source and image index digest as evidence.
  Pods use the version tag and `IfNotPresent`, without a digest suffix or `:latest`.
  An unsupported latest package fails visibly; retries/restarts retain the selected
  version. Release corrections allocate a new version without tag overwrite.
- Create uses the selected package's canonical CLI/templates, completes exclusive
  bootstrap termination, starts normal components, and proves indexed queries and the configured access
  scope before reporting ready. An internal-only environment does not claim a
  published public endpoint.
- Create/delete interruption, Console restart, concurrent mutations, stale
  revisions, response loss, late effects, and foreign-object collisions have
  observable outcomes without orphaning owned resources or deleting foreign ones.
- Unknown owner-introducing attempts block handoff, stop completion, and
  destruction until authoritative settlement; absent Pods alone cannot release
  the gate. Interrupted bootstrap remains initialization-required, while a
  completed bootstrap receipt survives stop and later start.
- Stop/start/restart preserve initialized data; deletion destroys only the exact
  instance-owned databases/accounts, message resources, signing material, claims,
  and Kubernetes objects, leaving shared servers/accounts/credentials intact.
- Desired/applied revisions, component observations, configuration/resize
  failures, and explicit credential rotation are visible through the API and
  bilingual UI, whose interactive mockups use final components and styles.
- Broker-limit resize survives runtime restart. Credential rotation recovers from
  effect-before-receipt failure, preserves stopped state, and cannot retire shared
  credentials while old-generation launches or bindings remain unresolved.
- Release probes reject draft/failed publications, wrong source/metadata/CLI,
  archive traversal/size attacks, digest/hash/platform mismatch and changed assets;
  newer publication cannot change an in-flight or ordinary restarted instance.
- Each of the five ordered PRs has executable completion evidence; real-backend
  acceptance covers two instances on shared physical servers/account and complete
  cleanup after the lifecycle and failure scenarios.

## Risks

Published package consumption adds a network/extraction/process boundary to
Console. Fixed administrative source identity, immutable receipts, bounded
extraction/execution and protected values are required before backend effects.
The archive hash detects changed downloaded bytes but does not replace trusted
source identity or introduce a release-signing guarantee. Removed remote assets
block package-dependent recovery only when the exact verified cached package
cannot be recovered. Stop/delete and ownership observation still use durable
resource receipts, subject to existing unsettled-owner gates. Package cache
custody and exact image retention are operational requirements. Version-tag
stability relies on the no-overwrite publication policy, not registry-enforced
immutability. Repointing a tag can leave `IfNotPresent` nodes running different
cached binaries; corrections must receive a new version, and observed registry
mismatches are rejected. A latest release with an unsupported managed contract
deliberately blocks new deployment until explicit Console support is delivered.

Administrative pools concentrate allocation authority in Console. Private,
encrypted credential custody, bounded privileged operations, and exact target
binding are required before provisioning begins; adding them after a successful
happy path would leave unrecoverable effects and unsafe cleanup.

Shared physical servers retain shared availability and resource contention.
Per-instance databases and users isolate source authority, while a shared NATS
account alone does not grant separate authentication authority. Stream limits,
subject disjointness, documented credential scope, and exact deletion protect the
owned resources without claiming independent physical failure domains.

Persistent operations and tombstones add storage and reconciliation work. Their
retention is necessary while remote effects can still arrive or cleanup remains
pending. Encryption-key loss, unavailable administrative access, or changed pool
targets can block recovery, so configuration cannot silently repoint an existing
binding to a different server.

An uncertain owner-creation request can block handoff and deletion until trusted
server completion or fencing evidence exists. This is the cost of preserving
exclusive store ownership across delayed effects. Single-password rotation adds
downtime; shared credential changes add cross-instance convergence and explicit
administrator retirement rather than a local password-edit shortcut.

Bootstrap exclusivity and indexed acceptance can lengthen provisioning. Storage
placement must allow the bootstrap Pod to mount all required claims before normal
roles resume. Ordinary health probes cannot substitute for those checks, and
configuration/resize must preserve single-owner runtime restrictions.
