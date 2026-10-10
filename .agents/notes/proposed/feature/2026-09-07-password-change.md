# Agent Note: Authenticated Password Change

Status: proposed

## Problem

The [authentication design](../../../../docs/design/server/core/identity/02.authentication.md)
requires old-password verification and token rotation. The current
[authentication implementation](../../../../packages/syntrix/internal/identity/authn/service.go)
implements explicit signup, signin, refresh, and logout. The public account
contract in `internal/identity` has no password-change operation. Transport-free
contracts and verified administrative actor provenance do not add that capability.
The [PostgreSQL user update](../../../../packages/syntrix/internal/identity/repository/postgres/user_store.go)
changes roles, database administration, and disabled state, without updating
credentials. Existing token revocation targets individual token identifiers.
Static inspection therefore finds an absent account capability, not an absent
signup or login implementation. The target
[instance Identity architecture](../../../../docs/design/server/core/identity/01.architecture.md)
scopes these accounts to a project inside one instance and stores all credential
and session state in that instance's PostgreSQL. Developer accounts in Console
and employee accounts in Management have independent account capabilities.

## Proposal

Add authenticated password change accepting the old and new passwords. The
previously proposed `POST /auth/v1/password` route is not implemented; the final
route and project-selection contract follow the instance Identity API design.
Resolve the account from the authenticated instance/project identity and local
subject, verify its old password, apply the configured signup password policy,
and replace its hash using the
existing hashing implementation. Return a fresh token pair only after the
credential update succeeds.

Introduce a credential generation persisted in the instance's PostgreSQL,
updated atomically with the password hash, and include it in user tokens.
Validate it for access-token
admission and refresh, including concurrent signin and refresh issuance. A
successful change invalidates all older sessions of that project account;
accounts in other projects and service tokens retain their separate lifecycles.
The invalidation guarantee is part of this capability proposal; its propagation
mechanism and timing require the broader session/revocation design. Use
conditional updates so concurrent changes cannot
silently overwrite credentials verified against an earlier hash.

The schema/token transition must explicitly expire existing user sessions and
migrate accounts to the new generation representation. Do not accept tokens
missing the required generation through a compatibility branch. Authentication
failures return a stable error; storage failures preserve their cause and do not
claim a completed rotation.

## Alternatives

**Revoke only the submitted refresh token.** This fits the current revocation API
but leaves other sessions active after a credential change.

**Persist every active session and revoke them individually.** This supports richer
session management but requires a session inventory beyond the password-change
requirement. A credential generation provides the needed account-wide boundary.

## Acceptance Criteria

- Correct old credentials and a policy-compliant new password change the hash;
  invalid old credentials or weak new passwords leave it unchanged.
- After success, old login credentials and all older user access/refresh tokens
  fail on every node, including tokens racing with the change.
- Restart preserves the new credential state; unrelated accounts and service
  tokens remain usable.
- Concurrent changes and storage failures have observable, bounded outcomes;
  no response includes hashes or logs passwords/tokens.

## Risks

Generation validation adds storage-read or coherently invalidated cache cost.
Deployment requires an intentional user-session expiration within the migrated
identity realm. Deferral leaves users unable to rotate compromised credentials
themselves; keeping credential mutation
centralized avoids divergent policies later.

## Dependencies

[Administrative audit records](2026-09-07-administrative-audit-records.md) own retained
instance/project outcomes. Application-facing account UI and the target Identity
protocol need separate design; the developer-facing Console does not own end-user
password self-service.
