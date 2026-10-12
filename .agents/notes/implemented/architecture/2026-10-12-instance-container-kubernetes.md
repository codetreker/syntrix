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

## Decision

`packages/syntrix` owns the multi-stage Dockerfile and standalone/distributed
Kubernetes templates. The existing `syntrix-cli` entrypoint supplies
`render-kubernetes --template FILE --config FILE --output FILE`. A module-owned
configuration package loads one strict values document and validates names,
connections, exact roles, replica limits, and CPU/memory requests against limits.
The renderer derives service addresses and names from that configuration and
writes validated YAML atomically with mode 0600. It has no cluster/database side
effects. The [deployment guide](../../../../packages/syntrix/deployment/README.md)
owns values and commands; the [deployment design](../../../../docs/design/server/03.deployment_modes.md)
places these artifacts within runtime modes.

The image contains the nonroot distroless instance binary, keeping runtime
configuration, credentials, and signing keys outside its build context. Both
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
and applies a normal render. This preserves exclusive store ownership and keeps
maintenance flags out of ordinary restart. Compatible initialized state starts
normally; invalid state fails rather than silently resetting. The existing
[Indexer storage contract](../../../../docs/design/server/indexer/04.storage.md)
owns recovery and replay invariants.

API/bootstrap HTTP probes and other roles' TCP probes report listener availability,
not dependency/replay/consumer readiness. Real normal-runtime indexed-query
acceptance is required before resuming writers. API rule caches remain local to
each process. Distributed Trigger composition currently uses a MEMORY JetStream
stream and the durable consumer `consumer`; pending tasks do not survive NATS
restart merely because NATS has a volume. Standalone pubsub is also in-memory.
The templates preserve these runtime limits without claiming new coordination
or durability guarantees.

The server CI gate includes Go build, race/coverage, container build/hardened help
smoke, and normal/bootstrap rendering of both templates using the built CLI.
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

Operators select instance-private MongoDB and PostgreSQL databases. Distributed
connections must use an instance-dedicated NATS server or account with JetStream:
generated configurations retain the runtime-default `TRIGGERS` stream/subject
prefix and durable consumer `consumer`. Namespace and resource-prefix values
rename Kubernetes resources without creating broker or source-data tenancy
isolation.

Ordinary replacement preserves stateful claims and shared signing material.
Deleting runtime volumes or the signing-key Secret is destructive. Initial
maintenance and recovery remain explicit operational actions; the quiescence
flag does not itself fence clients or cleanup workers. Distributed storage must
allow one bootstrap Pod to mount both claims before separated owners restart.

The templates expose ClusterIP application/internal service access. Public TLS
ingress, source-database backups/lifecycle, sharding, rule distribution, and trigger
durability remain outside the delivered deployment guarantees. Configurable
replicas do not establish those mechanisms. Console-managed full-instance
provisioning and fleet operations remain owned by the
[platform-boundary proposal](../../proposed/architecture/2026-10-10-platform-console-instance-boundaries.md).
