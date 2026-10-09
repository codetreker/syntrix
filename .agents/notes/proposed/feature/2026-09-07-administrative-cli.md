# Agent Note: Implement Administrative CLI Commands

Status: proposed

## Problem

The [administrative entry point](../../../../packages/syntrix/cmd/syntrix-cli/main.go) only prints
`"This is the syntrix-cli command."`. The [console design](../../../../docs/design/server/console/01.console.md)
specifies rule publication, dry-run, rollback, version listing, and account
administration commands. Operators cannot perform those workflows through the
CLI. This is an unfinished client interface; it does not establish that every
underlying server capability is missing. User listing and status updates, for
example, already exist in the [authentication service](../../../../packages/syntrix/internal/core/identity/authn/service.go).

## Proposal

Implement the documented rules and auth command groups as clients of authenticated
administrative APIs. Rules commands should support push, dry-run, activation,
rollback, and version listing. Auth commands should support user listing,
creation, enable/disable, and password rotation with explicit account identifiers.
Provide target and database selection, request deadlines, cancellation, readable
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
