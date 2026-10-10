# Agent Note: Project End-User Provisioning and Credential Rotation

Status: proposed

## Problem

Applications need authorized provisioning and recovery of end-user accounts,
with separate actor and target semantics. Under the
[platform/instance boundaries](../architecture/2026-10-10-platform-console-instance-boundaries.md),
these accounts belong to a project's Identity realm inside one Syntrix instance;
Console developer accounts and Management employee accounts are separate.

The current [REST routes](../../../../packages/syntrix/internal/gateway/rest/handler.go) expose
`GET /admin/users` and `PATCH /admin/users/{id}`; the latter changes roles,
database administration, and disabled state through
[admin handlers](../../../../packages/syntrix/internal/gateway/rest/handler_admin.go). Public signup
already creates accounts. Existing list/update now requires an opaque verified
identity from the account service's own verifier and the current exact admin/system
policy. Account responses use safe noncredential views; REST retains its blank
credential fields. These operations still use the instance-wide user and role
model without project isolation. An authorized administrative creation/credential
recovery operation and its project-scoped authority are absent.

## Proposal

Add project-authorized end-user creation and password rotation through the
instance Identity module, using stable local user IDs. Creation accepts an
explicit username, initial password, and allowed application-role assignments;
it returns account metadata without a session for the created user. Database
permissions remain part of the project's data-access policy and must not be
interpreted as Console or Management authority. The final route, credential,
and permission representation depend on the project Identity API design.

Rotation accepts a policy-valid replacement password and invalidates the target's
earlier sessions through the same credential mutation mechanism as self-service
change. Users, credential state, and durable operation records belong to the
instance's PostgreSQL system data.

Keep authentication, password hashing/policy, project-scoped username uniqueness,
and user storage shared with the instance Identity module. Public signup and the existing
list/update capabilities retain their responsibilities while receiving explicit
project authority. Apply administrative rate limits and enforce instance/project
scope before reading target details. A role string named `admin` alone cannot
authorize another project or platform account domain.
Return predictable duplicate, missing-account, and invalid-request errors.

Persist mutation idempotency records scoped to instance/project, actor, operation,
and key. A retry with the same key and request returns the completed metadata
result; reuse with
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

- Authorized project administrators can create project end users and rotate
  target credentials. Ordinary users and actors from another project or account
  domain cannot invoke either operation or discover account details through failures.
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
