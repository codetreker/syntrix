# Agent Note: Managed Runtime Deployment Contract

Status: implemented

## Problem

Existing standalone and distributed runtime placement did not provide enough
authority or evidence for a controller to safely operate instances on shared
backends. Broker startup could rewrite stream settings, initialization activated
cleanup before normal-query acceptance, and a live listener did not establish
that ingestion, replay or delivery were functioning. A matched image and template
archive also lacked a capability identifier that a controller could validate
before allocating resources.

The runtime must expose bounded, observable operations without acquiring Console
source authority. Source databases, credentials, Kubernetes effects and product
readiness belong to a durable instance lifecycle; process readiness and owner
schema definitions belong to runtime modules.

## Decision

The [managed runtime design](../../../../docs/design/server/console/04.managed_runtime_contract.md)
owns the supported runtime contract. This decision partially supersedes the
[container/deployment decision](2026-10-12-instance-container-kubernetes.md) for
renderer visibility, broker attachment, initialization and probes. That decision
continues to own placement, persistent claims, signing keys and operator bootstrap.
The [Console lifecycle proposal](../../proposed/architecture/2026-10-12-console-instance-lifecycle.md)
retains allocation, release selection, durable operations, reconciliation and UI.

The original renderer placement shared broker definitions through deployment
configuration. The [deployment-tool decision](2026-10-13-deployment-cli-ownership.md)
partially supersedes that ownership: `tools/deployment-cli/deployment` renders
embedded canonical templates or explicit trusted source in memory, its `config`
subpackage owns strict deployment values, and its private
`internal/deploymentcli` owns atomic file publication. The runtime's
`internal/core/pubsub/config` owns broker definitions, defaults, validation and
the complete specification; the tool consumes those contracts. Keeping rendering
and file effects apart allows operator Go callers to render without filesystem,
backend or Kubernetes access, while the runtime has no tool dependency.

Managed NATS startup attaches to exact preallocated stream/consumer objects and
verifies ownership, revision, routing and supported policy. It performs no broker
mutation. Operator startup creates missing objects using the same specification,
then refetches and verifies them; conflicts never trigger adoption or upsert.
Fresh checks and active consumer state are required for worker readiness. A
shared account separates subject routing but retains account-wide authority.

Storage/catalog and Identity own PostgreSQL schemas and MongoDB index definitions.
The initialization coordinator borrows physical backend connections and enumerates
routed/split placements; it does not duplicate definitions or own connection
closure. Preparation creates non-cleanup definitions. Inspection reads actual
definitions. Finalization checks an expected versioned specification digest and
creates/verifies owner-defined cleanup indexes. PostgreSQL definitions use the
resolved target schema and stable credential-free configured endpoint identity;
additional search-path entries cannot provide a different owner's tables. MongoDB
identity retains configured authority/replica-set identity and the normalized SRV
service name without adopting resolved SRV member hosts or credentials. Different
SRV services on one hostname remain distinct placements; omitted and explicit
default `mongodb` services identify the same endpoint. The bounded same-image CLI
emits one receipt after connections close without constructing Manager, opening Pebble
claims, listeners, or authentication services. Query acceptance is an explicit
caller attestation, not evidence inferred by the finalizer.

Bootstrap and read-path acceptance hold internal DeletionWorker and Trigger
writers. Bootstrap establishes its hold automatically; explicit acceptance is a
separate normal phase and requires the write-quiescence attestation. External
writers and physical cleanup stay paused through the normal
indexed-query check, cleanup finalization and full-role activation. Fresh managed
bootstrap rejects an active document tombstone TTL; ordinary managed restart
requires finalized definitions and never recreates missing cleanup implicitly.
No per-instance operation disables a shared MongoDB server's TTL monitor.

Ordinary Puller subscriptions expose ordered admission controls with transport and
admission generations, progress-neutral refresh and independent freshness expiry.
Streamer and Evaluator observe admission only after preceding business events are
processed. Upstream gRPC cancellation remains retryable while the ordinary
subscriber's caller context is active, preserving fresh admission after planned
Puller restart. Caller cancellation still ends the subscription, and permanent
rejection remains terminal. An empty-source admission does not create progress.
Overflow preserves the original admission floors; blocked channels clear readiness
through freshness
expiry without unbounded buffering or event loss.

Manager owns bounded role verification, terminal async failures, cancellation and
drain. Permanent NATS connection closure cancels blocked consumer delivery before
waiting for the consume context and the active delivery callback to settle.
The output channel closes under the callback gate; late callbacks observe
cancellation before sending because the SDK closure signal can resolve before
application callback settlement. Worker terminal failures are checked
before dependency readiness, so an unavailable broker/storage backend cannot
prevent cancellation from releasing dispatch backpressure. `/ready` and standard
gRPC role health report enabled-role readiness;
`/health` and `syntrix.liveness` report liveness. Held roles remain NOT_SERVING and
are excluded only from the active acceptance aggregate. Remote dependencies check
their required role to avoid aggregate cycles. Terminal failure reaches the CLI
as a nonzero exit after owned shutdown.

One validated origin policy is used by REST, classic WebSocket, replica WebSocket
and SSE. Diagnostics preserve local error chains while omitting credentials,
rendered Secrets and signing material. Terminal task failures and readiness
transitions emit subsystem, role, phase and category; readiness also records its
boolean observation. Available broker ownership supplies installation, instance,
allocation and specification revision. Safe startup error attributes preserve this
context without formatting raw dependency causes. Extended telemetry and scoped
administrative reports remain separate proposals.

Release metadata and the bundled `deployment-cli deployment-contract` declare
`managed_runtime_contract: 1`. Packaging verifies bounded CLI output, exact build
identity, both modes and all render phases. Release builds inject `main.version`
and `main.gitCommit`; canonical templates are embedded in the CLI and staged from
the same checkout. The runtime Docker context excludes deployment tooling and
builds only the service; local and release builds compile the tool separately.
The final distroless image contains only the runtime. Manual publication and daily
version allocation remain governed by the
[release decision](../process/2026-10-13-manual-syntrix-release.md).

## Alternatives

**Let runtime startup provision or repair managed broker objects.** This makes a
Pod restart an allocation or policy mutation and lets an older runtime overwrite
the controller's accepted limits. Read-only managed attachment keeps allocation
authority with Console and makes conflicting revisions observable.

**Enable all cleanup during bootstrap.** Tombstone removal can invalidate the
source/replay boundary before normal queries prove the derived state usable.
Separate preparation and attested finalization preserve that boundary across
bootstrap handoff and avoid disabling a shared server's global TTL monitor.

**Use listener probes as instance readiness.** A listener can survive failed
ingestion, expired admission or a terminated broker consumer. Module-owned role
evidence clears readiness on those failures; product acceptance remains separate.

**Compile a fixed runtime renderer into Console.** New release templates can
require a different model or phase semantics. A checked matching CLI/template
archive keeps runtime interpretation with its release. Console's actual bounded
download, extraction, cache and execution remain future lifecycle work.

**Deploy a floating latest image.** Bootstrap, acceptance and ordinary restart
could execute different source. The Console design records a version tag before
effects and retains it with `IfNotPresent`; metadata digests remain verification
evidence. The no-overwrite rule is publication policy, not enforced GHCR tag
immutability, and corrections must allocate a new version.

## Consequences

Operator deployments and future managed consumers use one renderer, broker
specification and observable initialization contract. Distributed values must
explicitly declare broker policy and authority; incompatible existing objects
fail visibly. This configuration upgrade does not add a legacy decoder or data
migration.

Finalization spans multiple backend definitions and is not atomic. Partial success
or response loss cannot claim completion; inspection and a repeated matching
finalizer converge the same specification. Console must durably settle earlier
effects before deletion or replacement and must preserve exclusive derived-store
ownership during handoff.

Readiness adds bounded dependency checks and admission refresh traffic. A stalled
check or dispatch clears stale success, so readiness may temporarily fail while
dependencies recover. An enabled process aggregate does not certify Console
accepted revision, bootstrap completion, successful cleanup finalization, or
public access.

Public renderer output contains credentials and belongs in protected storage.
Explicit template source is trusted operator input. Shared NATS credentials do
not provide per-instance broker authorization. Administrative allocation,
complete-instance APIs, release download/cache, lifecycle reconciliation,
configuration updates and bilingual instance pages remain owned by the active
Console lifecycle proposal.
