# Agent Note: Implement Administrative CLI Commands

Status: proposed

## Problem

The [deployment tool](../../../../packages/syntrix/tools/deployment-cli/cmd/deployment-cli/main.go)
renders Kubernetes manifests and reports its deployment compatibility contract.
It does not provide an authenticated administrative API client. The
[console design](../../../../docs/design/server/console/01.console.md) describes
rule publication, dry-run, rollback, version listing and account administration,
but operators cannot perform those workflows through a supported CLI. This is
an unfinished client interface; it does not establish that every underlying
server capability is missing. User listing and status updates, for example,
already exist in the [authentication service](../../../../packages/syntrix/internal/identity/authn/service.go).

## Proposal

Implement an administrative client with the documented rules and auth command
groups, separate from deployment rendering. The
[deployment-tool decision](../../implemented/architecture/2026-10-13-deployment-cli-ownership.md)
owns the existing tool's boundary. The [platform architecture](../../../../docs/architecture.md) requires explicit employee Management, developer Console, and instance/project Identity authority. A command must select the authority and resource scope it targets; an application role named `admin` does not authorize employee or developer operations. Rules commands should support push, dry-run, activation,
rollback, and version listing. Auth commands should support user listing,
creation, enable/disable, and password rotation with explicit account identifiers.
Provide instance, project, target, and database selection, request deadlines, cancellation, readable
output, and structured output with documented exit statuses. Validate local
arguments and rule-file syntax early while leaving authoritative authorization
and publication validation to the server.

Use explicit credentials supplied through a documented secret-input mechanism;
passwords and tokens must not appear in command-line arguments, output, or logs.
State-changing commands should show the target identity and return the server's
operation or version identifier. An interrupted or timed-out mutation must report
an uncertain outcome when the server may have applied it; do not silently retry
non-idempotent operations. Expose server errors with their correlation identifier
and preserve authentication and permission failures as failures.

## Alternatives

**Document direct API calls only.** This can exercise existing endpoints, but
repeats credential handling, request construction, and result interpretation in
operator scripts. It remains useful for API debugging.

**Access storage directly from the CLI.** This avoids missing endpoints but
duplicates authorization and persistence logic and cannot safely coordinate
multi-node rule publication. Administrative changes should have one server owner.

## Acceptance Criteria

- Each documented command maps to the intended API and reports a server-issued
  identifier or structured result; unsupported server capabilities fail explicitly.
- Cross-database and non-admin fixtures are denied by server authorization, with
  nonzero CLI exit statuses and no success output.
- Invalid arguments, cancellation, transport failure, and uncertain mutation
  outcomes are distinguishable without exposing secret inputs.
- Rule dry-run leaves the active version unchanged; activation, rollback, and
  account mutations are verified against their server contracts.

## Risks

The CLI can amplify a target-selection mistake. Require unambiguous database,
account, and rule-version parameters and avoid broad implicit mutation defaults.
Server API evolution must keep command behavior and reference examples aligned.

## Dependencies

[Rule publication](../architecture/2026-09-07-security-rule-publication-lifecycle.md)
owns rule persistence and version semantics;
[administrative provisioning](2026-09-07-admin-account-provisioning.md) owns
account creation and rotation; [audit records](2026-09-07-administrative-audit-records.md)
owns server-side audit persistence. This proposal owns the command interface.
