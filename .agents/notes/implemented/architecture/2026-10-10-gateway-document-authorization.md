# Agent Note: Gateway Document Authorization Ownership

Status: implemented

## Problem

Account authentication and business-document authorization shared the Identity
facade, configuration, and types. The CEL evaluator depended on Query to read
business documents, although account and token operations did not require those
reads. That ownership coupled authentication to data policy and would carry Query
into a separately deployed Identity service.

## Decision

Gateway owns the existing CEL evaluator, rule configuration, and evaluation types
under `internal/gateway/authorization`. Manager constructs
`authorization.NewEngine(config, queryService)` separately from Identity AuthN.
Gateway projects the existing user ID, username, roles, `db_admin`, and full claims
into authorization inputs. Identity retains account/password/JWT implementation
and its current user/revocation stores; it has no Query or rule dependency.

Root runtime configuration composes Identity account settings and Gateway
authorization settings. It retains `identity.authz.rules_path`, the `security_rules`
default, config-directory path resolution, and existing validation behavior.
Deployment files and public HTTP/rule formats do not change. There is one
authorization implementation and type owner; the previous Identity AuthZ facade,
type aliases, and package no longer exist.

The [Gateway authorization design](../../../../docs/design/server/gateway/authorization.md)
owns rule behavior and pending safeguards. This implements the document-policy
boundary within the broader [instance architecture](../../proposed/architecture/2026-10-10-platform-console-instance-boundaries.md).
That proposal remains active for the remaining Identity extraction, project
realms, sessions, deployment, and both OAuth roles.

## Alternatives

**Move the whole Identity facade into a peer service.** Keeping the CEL evaluator
there would make authentication depend on Query/business-document access and
potentially add an RPC to each policy lookup. Gateway already owns request and
resource evaluation; moving only account authority later preserves that direction.

**Change the rule configuration YAML path during extraction.** Renaming
`identity.authz` would require a deployment configuration transition alongside the
ownership refactor. Root composition preserves that external setting while giving
the rule configuration its own Go owner. A future configuration change needs its
own migration contract.

## Consequences

- Identity account/token code no longer depends on CEL, Query, or document-rule
  types. Gateway owns the evaluator and its Query-backed helper execution.
- Existing matching, loading, publication, database-admin bypass, error behavior,
  and authorization enforcement points remain unchanged. Authenticated users do
  not receive additional database/document grants through this separation.
- Current global-account, project-admission, query-authorization, helper-scope,
  publication durability, and resource-limit gaps remain documented. This change
  does not establish those proposed guarantees.
- Public authentication APIs, JWTs, user IDs, catalog/document namespaces,
  persistence, local process placement, and configuration inputs retain their
  existing contracts. Subsequent account/transport/token/store extraction and
  new Identity capabilities retain separate delivery gates.
