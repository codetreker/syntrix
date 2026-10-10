# Agent Note: Align Published References with Implemented Contracts

Status: proposed

## Problem

The accepted [platform architecture](../../../../docs/architecture.md) separates
employee Management, developer Console, and instance/project end-user Identity.
Current references must describe supported runtime routes and SDK methods while
keeping that target distinct from implementation. The
[SDK README](../../../../packages/sdks/client-ts/README.md) still places token
options at the constructor's top level, while
[SyntrixClient](../../../../packages/sdks/client-ts/src/clients/syntrix-client.ts)
requires a database and nested `auth` configuration. Authenticated examples also
need an explicit actor and runtime scope; a token described as `admin` cannot
stand for all three authority domains. Authored examples lack executable checks
that prevent these interfaces and permission meanings from drifting.

## Proposal

Reconcile active README, API, SDK, configuration, and console integration guidance against executable routes, exported types, and configuration parsing. Establish one owner for each public contract and link supporting guides to it. Rewrite each affected document coherently, preserving the rationale needed by design discussions while clearly marking future capabilities as planned. Historical archives retain their historical content.

Create executable documentation examples for representative authentication, database selection, CRUD, queries, conditional writes, triggers, and realtime setup. Type-check TypeScript examples against package exports and exercise HTTP examples against a bounded local fixture in the relevant validation workflow. Document response shapes, required fields, error behavior, and deployment prerequisites actually supported by the code; a proposal is not evidence of delivery.

Maintain a change checklist connecting route, schema, SDK, and configuration changes to their owning references. This work corrects documentation and adds focused validation; newly found runtime defects require their own implementation scope. Record verification limits when an example depends on services unavailable to a documentation check.

## Alternatives

**Generate every reference from code:** prevents some signature drift but cannot recover permission semantics, lifecycle guarantees, or operational prerequisites from type declarations alone. Generated snippets can support authored contracts.

**Correct examples manually without executable checks:** resolves current mismatches with less tooling but leaves the same drift undetected during later API changes. Focused checks are proposed for the public entry points most likely to mislead consumers.

## Acceptance Criteria

- Active consumer references use registered routes, valid constructor options, and exported method signatures; supported examples pass their declared validation.
- HTTP examples state actual response shapes, including typed query-page envelopes, and documented error behavior matches handlers.
- README commands identify prerequisites and supported runtime modes, including current embedded instance administration assets and the target developer Console boundary.
- Planned replication, rule publication, account, and realtime guarantees are clearly separated from implemented behavior; active links resolve and archives remain unchanged.

## Dependencies

[Console delivery](2026-09-07-console-build-delivery.md) owns build integration. [Security-rule publication](../architecture/2026-09-07-security-rule-publication-lifecycle.md), [account provisioning](../feature/2026-09-07-admin-account-provisioning.md), and [realtime resume](../feature/2026-09-07-realtime-client-resume.md) remain proposals until their implementations provide validation evidence.

## Risks

Automatically copying implementation details can institutionalize defects as promises. Contract reconciliation must distinguish accepted requirements, current limitations, and intended changes without silently changing runtime scope.
