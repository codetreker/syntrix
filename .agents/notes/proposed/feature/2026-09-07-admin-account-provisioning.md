# Agent Note: Administrative Account Provisioning and Credential Rotation

Status: proposed

## Problem

The [console design](../../../../docs/design/server/console/01.console.md) calls for
administrators to create users and rotate passwords. The
[REST routes](../../../../packages/syntrix/internal/gateway/rest/handler.go) currently expose
`GET /admin/users` and `PATCH /admin/users/{id}`; the latter changes roles,
database administration, and disabled state through
[admin handlers](../../../../packages/syntrix/internal/gateway/rest/handler_admin.go). Public signup
already creates accounts. What is missing is an authorized administrative
creation/credential-recovery operation with separate actor and target semantics.

## Proposal

Add administrator-only user creation and password-rotation endpoints using stable
user IDs for existing accounts. Creation accepts an explicit username, initial
password, and allowed role/database-admin assignments; it returns account
metadata without a session for the created user. Rotation accepts a policy-valid
replacement password and invalidates the target's earlier sessions through the
same credential mutation mechanism as self-service change.

Keep authentication, password hashing/policy, username uniqueness, and user
storage shared with the existing service. Public signup and the existing
list/update operations retain their responsibilities. Apply admin rate limits
and enforce the existing administration boundary before reading target details.
Return predictable duplicate, missing-account, and invalid-request errors.

Persist mutation idempotency records scoped to actor, operation, and key. A retry
with the same key and request returns the completed metadata result; reuse with
different input fails. Never store raw password material in those records. Make
credential-state changes and their operation result recoverable together so an
interrupted response cannot create a second account or perform another rotation.

## Alternatives

**Use public signup followed by role updates.** This provides primitives but emits
the target user's session to the administrator and separates provisioning into
partially completed operations.

**Generate credentials in the server.** This can support managed enrollment, but
requires a secret-delivery and recovery contract. Explicit initial credentials
match the existing password authentication model without settling that workflow.

## Acceptance Criteria

- Administrators can create users and rotate target credentials; ordinary users
  cannot invoke either operation or discover account details through failures.
- Duplicate usernames, weak passwords, and invalid assignments cause no partial
  account creation; successful rotation invalidates previous sessions.
- Retries after a lost response or restart return one operation result; conflicting
  idempotency-key reuse is rejected.
- Public signup, user listing, and existing role/disabled updates continue working.
- Audit correlation records actor, target, action, and outcome, excluding passwords,
  hashes, and tokens.

## Risks

Administrative credential operations require strict access control and durable
operation records. Deferral keeps recovery dependent on manual storage changes;
sharing the credential mutation operation prevents inconsistent revocation rules.

## Dependencies

[Password change](2026-09-07-password-change.md) owns credential/session invalidation;
[administrative audit records](2026-09-07-administrative-audit-records.md) own audit storage.
