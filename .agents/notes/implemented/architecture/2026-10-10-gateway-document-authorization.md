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
`authorization.NewEngine(config, queryService)` separately from Identity account
and token contracts. Gateway projects the existing user ID, username, roles,
`db_admin`, and full claims into authorization inputs. Account/password/JWT
implementation remains in `core/identity/authn` with current user/revocation
stores; transport-free contracts live in `internal/identity`. Neither owns the
Query-backed document-rule evaluator.

Each module owns its configuration type, defaults, path resolution, and
validation. `internal/gateway/config.GatewayConfig.AuthZ` composes
`internal/gateway/authorization/config.Config`; root runtime configuration
references Identity and Gateway module configurations directly. Rule settings
use YAML `gateway.authz.rules_path`, retaining the `security_rules` default and
config-directory path resolution. Identity owns authentication and admin
bootstrap settings. There is one authorization implementation and type owner.

Deployment files and local overrides use the module-owned configuration layout.
Custom rule-path values must move from `identity.authz.rules_path` to
`gateway.authz.rules_path`; the former key has no compatibility alias. Public
HTTP/rule formats and evaluation behavior remain unchanged.

The [Gateway authorization design](../../../../docs/design/server/gateway/authorization.md)
owns rule behavior and pending safeguards. This implements the document-policy
boundary within the broader [instance architecture](../../proposed/architecture/2026-10-10-platform-console-instance-boundaries.md).
The [Identity contract decision](2026-10-10-identity-contracts-and-gateway-authentication.md)
owns the later account/verifier/issuer capability separation, safe user views,
and Gateway authentication adapter. The broader proposal remains active for
repository/runtime composition, verification-key distribution, project realms,
sessions, deployment, and both OAuth roles. The
[token capability decision](2026-10-10-identity-token-capabilities.md) owns the
later concrete signer/issuer/public-verifier separation; current process
assembly still loads private keys for local issuing capabilities.

## Alternatives

**Move the whole Identity facade into a peer service.** Keeping the CEL evaluator
there would make authentication depend on Query/business-document access and
potentially add an RPC to each policy lookup. Gateway already owns request and
resource evaluation; moving only account authority later preserves that direction.

**Preserve `identity.authz` through a root Identity wrapper.** A cross-module
wrapper can preserve the old YAML shape, but places module-specific composition
and lifecycle in `internal/config` and keeps the setting under the wrong owner.
Module configuration belongs with its module. Gateway therefore composes its
authorization settings and owns their YAML path; configured rule values move with
that ownership.

## Consequences

- Identity account/token code is independent of CEL, Query, or document-rule
  types. Gateway owns the evaluator and its Query-backed helper execution.
- Existing matching, loading, publication, database-admin bypass, error behavior,
  and authorization enforcement points remain unchanged. Authenticated users do
  not receive additional database/document grants through this separation.
- Current global-account, project-admission, query-authorization, helper-scope,
  publication durability, and resource-limit gaps remain documented. This change
  does not establish those proposed guarantees.
- Public authentication APIs, JWTs, user IDs, catalog/document namespaces,
  persistence, and local process placement retain their existing contracts.
  Configuration values/defaults retain their meaning under the module-owned
  YAML layout; deployment overrides require the documented rule-path move.
  Account contracts and the Gateway authentication adapter follow the Identity
  contract decision; concrete token capabilities follow their own decision.
  Store/runtime separation, key distribution, and new Identity capabilities
  retain separate delivery gates.
