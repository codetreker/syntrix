# Agent Note: Developer Console Account Self-Service

Status: proposed

## Problem

Developers need to inspect and maintain their own account profile while managing
owned Syntrix instances. The [system architecture](../../../../docs/architecture.md)
and [boundary decision](../architecture/2026-10-10-platform-console-instance-boundaries.md)
separate those developer accounts from platform employees and application
end users. The current embedded instance administration frontend has no account
page and derives identity from current runtime token claims. Its unused profile
API declaration does not provide a developer account backend.

The [Console design](../../../../docs/design/server/console/01.console.md) owns the
separate developer service. Profile self-service requires an authoritative
account contract in that service; application documents and instance JWT claims
cannot serve as the developer profile store.

## Proposal

Deliver a developer account page backed by authenticated current-account read
and update operations owned by Console. Derive the developer account from a
validated Console principal; expose only bounded non-secret profile fields and
an explicit editable-field allow list. Platform employee roles, instance
ownership/delegation grants, account enablement, hashes, and credential material
are outside profile writes.

Specify field validation, update concurrency, and persistence semantics with the
backend contract. Refresh displayed state from successful responses. Failed or
conflicting writes retain draft fields and expose actionable errors. Token
claims identify a session rather than a mutable profile database. Any persisted
schema transition requires an explicit migration.

Developer credential change belongs to the developer account authority and must
specify verification and session invalidation. Application end-user password
change belongs to project-scoped instance Identity and the separate
[password-change proposal](2026-09-07-password-change.md). Shared mechanisms must
not allow one account domain to mutate another. Provide a final-style interactive
mockup for load/edit/save/validation/conflict/expired-session states before UI
implementation.

## Alternatives

**Application collection profile documents.** This reuses document CRUD but
couples a developer account to an instance/project schema and application rules.
It cannot own a developer's profile across multiple instances.

**Read-only token claims.** This needs no profile persistence but cannot deliver
editable fields or reliably reflect server-side changes.

## Acceptance Criteria

- A signed-in developer can reload/edit allowed Console profile fields and a new
  Console session observes successful persistence.
- Cross-account writes, roles, resource grants, and credential-field injection
  are rejected by the Console backend.
- Concurrent edits follow the defined conflict contract, and errors preserve
  drafts without treating browser state as authority.
- Switching developer accounts clears prior profile state; credential changes
  apply their documented session outcome without logging secrets.
- Employee and application tokens do not authorize developer profile operations.

## Dependencies

The [Console design](../../../../docs/design/server/console/01.console.md) owns
account/service boundaries. [Admin provisioning](2026-09-07-admin-account-provisioning.md)
and [audit records](2026-09-07-administrative-audit-records.md) retain their explicitly
scoped authority; they do not make developer self-service an application admin
operation. The instance password proposal owns application credentials only.

## Risks

Permissive profile payloads can expose privileged grants. Explicit field ownership,
principal-derived account selection, and server authorization are required.
The current embedded frontend and runtime-global user model do not supply the
separate Console account authority; its persistence backend remains a separate
design decision.
