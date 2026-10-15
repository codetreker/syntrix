# Agent Note: Deployment Tool and Runtime Contract Ownership

Status: implemented

## Problem

The instance service needs broker identity and policy to attach publishers and
consumers. Keeping those types in deployment-value configuration makes runtime
business code depend on operator tooling. Rendering manifests, embedding
Kubernetes templates and publishing protected output files are deployment
responsibilities; they are not service configuration responsibilities.

## Decision

`packages/syntrix/tools/deployment-cli/` owns the deployment command, renderer,
deployment values, private file adapter, canonical templates and operator/release
documentation. Its command is `cmd/deployment-cli`, and local and packaged
executables are named `deployment-cli`. It remains part of the existing Syntrix
Go module.

The runtime Pubsub module owns `BrokerBinding`, ownership and policy types,
operator defaults, validation and the complete JetStream specification in
`internal/core/pubsub/config`. Deployment values import that runtime contract.
Runtime packages never import deployment tooling. The service Docker context
excludes the tool tree and builds only `cmd/syntrix`; local and release builds
compile the deployment command separately.

The [managed runtime design](../../../../docs/design/server/console/04.managed_runtime_contract.md)
owns the combined deployment/runtime compatibility contract. This decision
partially supersedes the [managed-runtime decision](2026-10-13-managed-runtime-contract.md)
for renderer location and broker ownership, and the
[container/deployment decision](2026-10-12-instance-container-kubernetes.md)
for command placement and runtime Docker inputs. Their initialization,
attachment, readiness, persistence and publication rationale remains applicable.
The release archive retains `deployment/{standalone,distributed}` and places
the renamed executables under `bin/linux-{amd64,arm64}/deployment-cli`.

Future Console allocation can reuse the runtime broker specification because
its `github.com/codetreker/syntrix/console-server` module path is within the
parent module's internal import boundary. Its first consuming implementation
declares the module dependency. This move adds no Console allocator or generic
administrative API client.

## Alternatives

**Move deployment configuration unchanged into the tool.** Runtime imports would
still reach tooling to obtain broker types. Moving the broker contract to its
runtime owner removes that dependency rather than preserving it under a new path.

**Create a sibling `packages/deployment-cli` component.** A separate location was
considered before selecting package-local tooling. The existing Go module lets
the tool consume runtime-owned internal contracts and retain tests that decode
rendered values into the runtime's configuration and service options, without
adding a second dependency manifest or workspace arrangement.

## Consequences

Deployment values and runtime configuration have distinct owners. The complete
broker specification remains shared without duplicate types or compatibility
aliases. Build and CI commands use the renamed tool and keep both topology and
all three render-phase checks. The runtime import graph is checked for tool
dependencies.

The command and source paths change deliberately. Scripts and new release
archives use `deployment-cli`; previously published archives are unchanged.
The managed-runtime capability marker and broker wire values retain their
existing semantics. The tooling and runtime share a Go module but have separate
build inputs and a one-way dependency.
