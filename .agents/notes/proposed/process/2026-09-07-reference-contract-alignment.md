# Agent Note: Align Published References with Implemented Contracts

Status: proposed

## Problem

Active references describe incompatible API generations. [The REST reference](../../../../docs/reference/api.md) documents `GET /api/v1/{path...}`, while [gateway registration](../../../../packages/syntrix/internal/gateway/rest/handler.go) uses database-scoped document routes. [The SDK reference](../../../../docs/reference/typescript_sdk.md) calls `client.login('username', 'password', 'my-database')`, but [SyntrixClient](../../../../packages/sdks/client-ts/src/clients/syntrix-client.ts) accepts two login arguments. [The SDK README](../../../../packages/sdks/client-ts/README.md) supplies token options at the constructor's top level, while the implementation requires a database and nested `auth` configuration. These static mismatches make documented entry points unreliable.

## Proposal

Reconcile active README, API, SDK, configuration, and console integration guidance against executable routes, exported types, and configuration parsing. Establish one owner for each public contract and link supporting guides to it. Rewrite each affected document coherently, preserving the rationale needed by design discussions while clearly marking future capabilities as planned. Historical archives retain their historical content.

Create executable documentation examples for representative authentication, database selection, CRUD, queries, conditional writes, triggers, and realtime setup. Type-check TypeScript examples against package exports and exercise HTTP examples against a bounded local fixture in the relevant validation workflow. Document response shapes, required fields, error behavior, and deployment prerequisites actually supported by the code; a proposal is not evidence of delivery.

Maintain a change checklist connecting route, schema, SDK, and configuration changes to their owning references. This work corrects documentation and adds focused validation; newly found runtime defects require their own implementation scope. Record verification limits when an example depends on services unavailable to a documentation check.

## Alternatives

**Generate every reference from code:** prevents some signature drift but cannot recover permission semantics, lifecycle guarantees, or operational prerequisites from type declarations alone. Generated snippets can support authored contracts.

**Correct examples manually without executable checks:** resolves current mismatches with less tooling but leaves the same drift undetected during later API changes. Focused checks are proposed for the public entry points most likely to mislead consumers.

## Acceptance Criteria

- Active consumer references use registered routes, valid constructor options, and exported method signatures; supported examples pass their declared validation.
- HTTP examples state actual response shapes, including bare query arrays, and documented error behavior matches handlers.
- README commands identify prerequisites and supported runtime modes, including console assets.
- Planned replication, rule publication, account, and realtime guarantees are clearly separated from implemented behavior; active links resolve and archives remain unchanged.

## Dependencies

[Console delivery](2026-09-07-console-build-delivery.md) owns build integration. [Security-rule publication](../architecture/2026-09-07-security-rule-publication-lifecycle.md), [account provisioning](../feature/2026-09-07-admin-account-provisioning.md), and [realtime resume](../feature/2026-09-07-realtime-client-resume.md) remain proposals until their implementations provide validation evidence.

## Risks

Automatically copying implementation details can institutionalize defects as promises. Contract reconciliation must distinguish accepted requirements, current limitations, and intended changes without silently changing runtime scope.
