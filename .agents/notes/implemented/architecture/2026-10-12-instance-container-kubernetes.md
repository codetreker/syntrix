# Agent Note: Runtime Container and Kubernetes Generation

Status: implemented

## Problem

The instance runtime has package-local build and configuration rules, but service
placement needs repeatable Kubernetes resources for standalone and distributed
operation. Deployments must reuse existing source databases while preserving
signing authority, derived-index catalogs, and Puller lineage across process
replacement. A fresh Indexer requires explicit maintenance bootstrap under write
quiescence; ordinary container restart cannot safely imply rebuilding. Workload
names, replicas, and resources must remain configurable without making service
addresses or singleton state ownership inconsistent.

Application request budgets must not deny process-liveness probes. Initial
dependency failure must also remain visible to the process owner so a normal
container can restart instead of retaining an inactive Indexer. Template
diagnostics can include source expressions and runtime values; their display
must preserve useful failure categories without exposing credentials.

## Decision

`packages/syntrix` owns the multi-stage runtime Dockerfile. The
[deployment-tool decision](2026-10-13-deployment-cli-ownership.md) places
standalone/distributed Kubernetes templates and rendering under
`tools/deployment-cli` and replaces the original `syntrix-cli` entrypoint with
`cmd/deployment-cli`. The tool supplies
`render-kubernetes --template FILE --config FILE --output FILE`. A module-owned
configuration package loads one strict values document and validates names,
connections, exact roles, replica limits, and CPU/memory requests against limits.
The renderer derives service addresses and names from that configuration and
returns validated YAML without cluster/database effects; the private CLI adapter
publishes it atomically with mode 0600. The
[managed-runtime decision](2026-10-13-managed-runtime-contract.md) owns the
rendering compatibility contract and partially supersedes this note for initialization,
broker attachment and readiness. The [deployment guide](../../../../packages/syntrix/tools/deployment-cli/deployment/README.md)
owns values and commands; the [deployment design](../../../../docs/design/server/03.deployment_modes.md)
places these artifacts within runtime modes.

Template parsing, execution, and manifest validation finish before output
emission. Their failures leave stdout empty and the prior private output file
unchanged. Template diagnostic strings omit source contents and runtime values;
the raw underlying cause remains available through `Unwrap` for programmatic
classification.

The image contains the nonroot distroless instance binary, keeping runtime
configuration, credentials, and signing keys outside its build context. Its
builder cross-compiles Linux amd64/arm64 using the target platform. The
[manual-release decision](../process/2026-10-13-manual-syntrix-release.md) owns
version allocation, public image publication, and version-matched CLI/deployment
archives. Both
templates accept existing MongoDB replica-set and private PostgreSQL connections;
distributed Trigger transport additionally requires existing NATS JetStream.
Database installation, source persistence, backups, and cluster lifecycle remain
operator-owned. Templates create runtime resources rather than changing Console's
predefined test-workload executor.

Standalone runs local services in one process and retains its signing key, Pebble
indexes/progress, and Puller buffers in one claim. Distributed mode creates API,
Query, Puller, Indexer, Streamer, Trigger Evaluator, and Trigger Worker Deployments
with separate Puller and Indexer claims. API, Trigger Worker, and bootstrap share
one operator-managed immutable PKCS#1 RSA key Secret. Standalone optionally uses
that Secret; otherwise its generated key persists in the data claim. These are
placements of current local Identity capabilities, not a new Identity service.

Normal rendering honors configured replicas. Standalone, Puller, Indexer, and
Trigger Evaluator are limited to zero or one; other distributed roles accept
Kubernetes signed 32-bit replica counts. Stateful and evaluator Deployments use
Recreate to prevent overlap during replacement. Processes run as UID/GID 65532
with a read-only root, writable data/temporary mounts, stdout JSON logs, and no
ServiceAccount token.

Explicit bootstrap requires both `--bootstrap` and `--writes-quiesced`. It stops
normal Deployments in the rendered state and adds one temporary standalone Pod
with `restartPolicy: Never`. Distributed bootstrap mounts the same Puller and
Indexer claims at their normal paths and uses the shared key/source/templates.
Before applying it, the operator pauses every writer and MongoDB TTL cleanup and
waits for existing normal Pods to stop. `ReadWriteOnce` alone does not prohibit
simultaneous same-node access to Pebble stores. After bootstrap's applied-readiness
barrier, the operator gracefully deletes the maintenance Pod, waits for deletion,
and starts held read-path acceptance before finalization and full activation. This preserves exclusive store ownership and keeps
maintenance flags out of ordinary restart. Compatible initialized state starts
normally; invalid state fails rather than silently resetting. The existing
[Indexer storage contract](../../../../docs/design/server/indexer/04.storage.md)
owns recovery and replay invariants.

Exact GET/HEAD `/health` and `/ready` requests bypass application budgets.
HTTP readiness and standard gRPC role probes now report actual enabled-role
readiness; separate liveness probes retain process availability. The
managed-runtime decision owns admission freshness, role aggregation and held-role
semantics. These probes do not replace authenticated indexed-query acceptance.

Manager returns synchronous startup and terminal asynchronous errors and rejects
startup before bootstrap readiness. The CLI owner cancels work and invokes
shutdown before exiting nonzero. Normal Kubernetes containers restart and
revalidate persistent state when dependencies become available. Invalid state
still fails closed without implicit bootstrap/reset. Manager does not introduce
a separate dependency retry loop; active Indexer transport recovery retains its
existing bounded policy. Owner cancellation is normal shutdown only when the
returned startup error also represents cancellation. The network server checks
its canceled owner under the startup mutex before initializing listeners, so late
background startup cannot reopen sockets after owned cleanup.

Real normal-runtime indexed-query acceptance is required before resuming writers.
API rule caches remain local to each process. Distributed Trigger configuration
now declares a complete broker policy, including storage and retention. The
managed-runtime decision replaces runtime upsert with managed attach/verify and
operator create-missing/refetch/verify. Standalone pubsub remains in-memory;
replica configuration does not add fleet coordination.

The server CI gate includes Go build, race/coverage, container build/hardened help
smoke, and bootstrap/acceptance/full rendering of both templates using the built CLI.
Shared Compose stacks remain repository-owned infrastructure; their obsolete
commented root-image application template is removed. The
[package-layout note](2026-10-09-package-layout.md) retains package/tooling ownership.

## Alternatives

**Manually maintain fully instantiated manifests.** Operators would need to keep
names, per-role addresses, replicas, and resource edits aligned across multiple
copies. Typed values and derived addressing provide reusable templates while
keeping the rendered Kubernetes artifact inspectable before application.

**Deploy database workloads together with runtime services.** Existing databases
already have operator-owned installation, credentials, and persistence. New
database resources would add a provisioning/data-lifecycle contract beyond
service placement. Connection configuration keeps that ownership explicit.

**Keep bootstrap flags in restartable normal workloads.** Pod replacement could
repeat maintenance while source writers or TTL cleanup are active. A temporary
non-restarting Pod preserves the source-boundary contract and normal persistent
startup's rejection of incomplete or invalid state.

**Generate signing keys independently in distributed issuing Pods.** API replicas
and Trigger Worker would use different token authorities after startup or
replacement. An existing shared immutable key keeps issuance and verification
consistent without implementing a remote Identity service.

## Consequences

Operators can generate either runtime placement from one mode-specific values
file and an explicit template. The tool enforces supported quantity forms,
request/limit ordering, exact roles, and singleton replica counts before producing
resources, but it does not validate live connectivity, Secret existence, storage
placement, or runtime readiness. Generated manifests contain credentials and
remain operator-controlled artifacts outside Git and routine logs.

Failed initial Puller/Indexer startup remains observable as a nonzero process
exit and ordinary container restart. Component readiness cannot replace
the indexed-query acceptance step, and invalid persisted state remains an explicit
maintenance failure. Template rendering failures preserve output integrity.
Displayed diagnostics identify the failure stage while omitting template
expressions and runtime values; programmatic callers can classify the retained
raw cause through the error chain.

Operators select instance-private MongoDB and PostgreSQL databases. Distributed
connections use JetStream with an explicit broker binding. Distinct stream/subject
and durable names permit shared-account routing separation; common credentials
retain account-wide authority. Kubernetes namespace and prefix values rename
resources without assigning source-database authority.

Ordinary replacement preserves stateful claims and shared signing material.
Deleting runtime volumes or the signing-key Secret is destructive. Initial
maintenance and recovery remain explicit operational actions; the quiescence
flag does not itself fence clients or cleanup workers. Distributed storage must
allow one bootstrap Pod to mount both claims before separated owners restart.

The templates expose ClusterIP application/internal service access. Public TLS
ingress, source-database backups/lifecycle, sharding, rule distribution, and trigger
durability remain outside the delivered deployment guarantees. Configurable
replicas do not establish those mechanisms. Console-managed full-instance
provisioning is planned in the
[Console instance-lifecycle proposal](../../proposed/architecture/2026-10-12-console-instance-lifecycle.md),
which owns future backend allocation, durable lifecycle authority, matched-package
selection/execution and product acceptance. The runtime prerequisite supports
shared broker namespaces but does not create Console instance records or allocate
backend accounts. Employee fleet operations and project Identity retain the
[platform-boundary proposal](../../proposed/architecture/2026-10-10-platform-console-instance-boundaries.md).
