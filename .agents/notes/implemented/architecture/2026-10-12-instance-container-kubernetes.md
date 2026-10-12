# Agent Note: Syntrix Service Container and Kubernetes Deployment

Status: implemented

## Problem

The current instance runtime has package-local build and configuration rules but
no supported container recipe or Kubernetes resources. Service deployment must
reuse existing source databases while preserving its signing identity,
derived-index catalog, and Puller lineage across process replacement. A fresh
Indexer requires explicit maintenance bootstrap under write quiescence; ordinary
container restart cannot safely imply rebuilding.

## Decision

`packages/syntrix` owns the multi-stage Dockerfile and service-only Kubernetes
manifest. The image builds only its Go module into a nonroot distroless binary.
Configuration and credentials are mounted separately, and generated signing
material is retained under the runtime PVC. The
[deployment guide](../../../../packages/syntrix/deployment/README.md) owns operator
commands; the [deployment design](../../../../docs/design/server/03.deployment_modes.md)
places packaging within the existing runtime modes.

The manifest supplies `syntrix-instance`, one standalone Syntrix Deployment and
ClusterIP Service, a configuration ConfigMap/Secret, and its runtime data claim.
MongoDB replica-set and private instance PostgreSQL connections are explicit
Secret inputs. Their existing resources and lifecycle remain operator-owned;
this artifact does not create database workloads or change Console's predefined
test-workload executor. The runtime data claim retains the signing key, Pebble
indexes/progress, and Puller event buffers. Standalone Trigger transport remains
in-memory and needs no NATS deployment.

The Deployment initially has zero replicas. Fresh derived state requires the
operator to pause every source/application writer and MongoDB TTL cleanup before
starting `--bootstrap-indexes --writes-quiesced`. After the applied-readiness
barrier, the operator confirms normal persistent startup and indexed queries,
then resumes writers. Normal arguments and one replica are retained in the local
manifest for later applications. Compatible initialized persistent state can
start normally without bootstrap; invalid state fails rather than silently
resetting. The existing [Indexer storage contract](../../../../docs/design/server/indexer/04.storage.md)
owns maintenance and replay invariants.

Recreate with one replica prevents concurrent ownership of local Pebble stores.
The runtime uses UID/GID 65532, a read-only root, writable data/temporary mounts,
stdout JSON logging, and no ServiceAccount token. Its startup probe allows the
30-minute bootstrap operation to finish. HTTP probes establish listener
availability; normal startup still requires indexed-query acceptance before
admitting application traffic.

The shared server CI gate includes a package-context image build and hardened
help-command smoke check alongside Go build and race/coverage. Shared Compose
stacks remain repository-owned infrastructure. The obsolete commented root-image
application template is removed; the supported recipe belongs to the runtime
package. The [package-layout note](2026-10-09-package-layout.md) retains ownership
of package paths and tooling.

## Alternatives

**Deploy database workloads together with the runtime.** Existing databases
already have operator-owned installation, credentials, and persistence. Creating
new database resources would introduce a separate provisioning and data-lifecycle
contract beyond deploying this service. Mounted connection configuration keeps
that ownership explicit.

**Keep bootstrap flags in every runtime start.** This turns Pod replacement into
an unrequested maintenance operation while external writers or TTL cleanup may
be active. Explicit bootstrap preserves the source-boundary contract and lets
normal persistent startup reject incomplete or invalid state.

## Consequences

Operators can build a package-only image and deploy the current runtime against
existing MongoDB and PostgreSQL. Database addresses, credentials, replica-member
reachability, and initialization permissions must be configured before startup.
Ordinary runtime replacement retains signing keys, derived stores, and capture
buffers; runtime-volume deletion remains destructive. Initial maintenance and
recovery are explicit operational actions, and `--writes-quiesced` does not itself
fence clients or cleanup workers.

The artifact requires an operator-prepared cluster with persistent storage and
runs one standalone process. It exposes ClusterIP HTTP access, leaving public
TLS ingress and distributed-service placement to their separate deployment
contracts. Existing source-database backups and lifecycle remain external to
this manifest. Console-managed full-instance provisioning and fleet operations
remain owned by the [platform-boundary proposal](../../proposed/architecture/2026-10-10-platform-console-instance-boundaries.md).
