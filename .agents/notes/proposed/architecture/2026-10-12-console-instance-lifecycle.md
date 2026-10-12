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
owns the mechanism and five ordered delivery PRs. This note owns the proposed
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
backend pools, allocation bindings, immutable desired-spec revisions, operation
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

### Rendering and lifecycle recovery

Console calls a side-effect-free public runtime renderer over the same canonical
standalone/distributed templates used by `syntrix-cli`. Typed values, supported
role topology, replica restrictions, service derivation, and validation have one
owner. Console adapts recorded instance intent into those values and uses the
existing client-go workload controller infrastructure to reconcile the rendered
resources. A second template set, raw developer YAML, or shell-driven CLI
execution would split the accepted runtime contract.

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
listener probes alone do not.

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
validates and renders the entire proposed configuration before accepting effects.

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

**Invoke the CLI through a shell or copy its templates into Console.** A shell
adds process and secret-output handling; copied templates let Console and manual
deployments disagree about roles, bootstrap, or validation. A public pure renderer
preserves one template/configuration contract and leaves all external effects in
the Console executor.

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
- Create uses the canonical renderer, completes exclusive bootstrap termination,
  starts normal components, and proves indexed queries and the configured access
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
- Each of the five ordered PRs has executable completion evidence; real-backend
  acceptance covers two instances on shared physical servers/account and complete
  cleanup after the lifecycle and failure scenarios.

## Risks

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
