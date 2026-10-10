# Agent Note: Durable Authentication and Administrative Audit Records

Status: proposed

## Problem

Authentication and administrative mutations inside an instance need retained,
queryable outcomes. The target
[Identity architecture](../../../../docs/design/server/core/identity/01.architecture.md)
places end-user accounts, sessions, projects, and database configuration in the
instance's PostgreSQL system data. These records are separate from developer
account/instance operations in Console and employee platform operations in
Management; each authority domain needs its own audit ownership.

Static inspection of
[authentication](../../../../packages/syntrix/internal/core/identity/authn/service.go) and
[admin handlers](../../../../packages/syntrix/internal/gateway/rest/handler_admin.go) finds operation
execution and diagnostic logging, without a durable audit store or query API.
Logs alone do not establish which administrative operation completed after a
restart or an interrupted response.

## Proposal

Add an append-only audit event store in the instance-local PostgreSQL system backend.
Cover signup, signin success/failure, logout/revocation, password change, user
administration, rule publication/rollback, and project/database administration.
Each event records instance/project scope, event/operation IDs, time, actor,
action, target/database, outcome, and bounded client metadata. Use redacted
change summaries; exclude passwords,
tokens, hashes, full rule text, and document payloads.

For mutations in PostgreSQL, commit the audit result in the same transaction as
the change. For operations spanning another store, durably record intent before
execution and append completion or unresolved outcome afterward; reconciliation
must inspect the operation result before retrying side effects. A response must
not claim audited completion until its result record is durable. Bound pending
work and reject admission when durable audit storage cannot accept it. Preserve
operation IDs when the caller receives an uncertain outcome.

Expose project/instance-authorized cursor pagination filtered by actor, action,
target, and time. Enforce the actor's authority domain and scope before querying;
a matching `admin` role string cannot grant cross-project, Console, or Management
access. Console and Management audit APIs remain separate responsibilities.
Enforce a configured retention period of at least 30 days with a
bounded, cancellable retention worker. Generic telemetry remains owned by
[application observability](../architecture/2026-09-07-application-observability.md).

## Alternatives

**Use structured application logs as the record.** This reduces schema work but
does not couple successful mutations to durable, queryable outcomes.

**Require an external audit collector.** This offers independent retention but
introduces another availability and delivery boundary. Export can be added after
the local durable event contract is established.

## Acceptance Criteria

- Every covered successful mutation has a durable correlated outcome; denied and
  failed attempts record their result without sensitive values.
- Restart and injected write/delivery failures preserve pending or unresolved
  operations without fabricating completion or repeating non-idempotent changes.
- Unauthorized actors cannot query audit records, including actors from another
  project or account domain. Pagination has no omissions or duplicate traversal
  under concurrent insertion.
- Retention preserves the configured minimum and shutdown cancels cleanup within
  its timeout.

## Risks

Audit admission increases operational coupling and failed-login volume requires
rate limits and bounded metadata. Deferral retains gaps in administrative history;
stable operation IDs keep future audit correlation possible without interpreting
free-form logs.
