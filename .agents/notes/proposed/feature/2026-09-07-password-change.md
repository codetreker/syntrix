# Agent Note: Authenticated Password Change

Status: proposed

## Problem

The [authentication design](../../../../docs/design/server/core/identity/02.authentication.md)
requires old-password verification and token rotation. The current
[authentication service](../../../../packages/syntrix/internal/core/identity/authn/service.go)
implements explicit signup, signin, refresh, and logout; its interface has no
password-change operation. The [PostgreSQL user update](../../../../packages/syntrix/internal/core/storage/postgres/user_store.go)
changes roles, database administration, and disabled state, without updating
credentials. Existing token revocation targets individual token identifiers.
Static inspection therefore finds an absent account capability, not an absent
signup or login implementation.

## Proposal

Add authenticated `POST /auth/v1/password` accepting the old and new passwords.
Resolve the account from the authenticated subject, verify its old password,
apply the configured signup password policy, and replace its hash using the
existing hashing implementation. Return a fresh token pair only after the
credential update succeeds.

Introduce a persisted credential generation, updated atomically with the password
hash, and include that generation in user tokens. Validate it for access-token
admission and refresh, including concurrent signin and refresh issuance. A
successful change invalidates all older user sessions; service tokens retain
their separate lifecycle. Use conditional updates so concurrent changes cannot
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
Deployment requires an intentional user-session expiration. Deferral leaves users
unable to rotate compromised credentials themselves; keeping credential mutation
centralized avoids divergent policies later.

## Dependencies

[Administrative audit records](2026-09-07-administrative-audit-records.md) own retained
outcomes; [console account self-service](2026-09-07-console-account-self-service.md)
owns the UI.
