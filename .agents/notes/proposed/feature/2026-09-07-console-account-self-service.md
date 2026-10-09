# Agent Note: Console Account Self-Service

Status: proposed

## Problem

The [console design](../../../../docs/design/server/console/01.console.md) requires users to view and update their own non-secret profile data. The current [router](../../../../packages/console/src/router/index.tsx) has no account page, and the [auth store](../../../../packages/console/src/stores/auth.ts) derives displayed identity from token claims. The [API wrapper](../../../../packages/console/src/lib/api.ts) declares `/auth/v1/me`, but the [gateway route registration](../../../../packages/syntrix/internal/gateway/rest/handler.go) does not provide that route or a profile update operation. Profile self-service is therefore an incomplete planned feature, not merely a hidden page.

## Proposal

Deliver an account page backed by authenticated current-account read and update operations. The server derives the account from the validated principal, returns a bounded set of non-secret profile fields, and allows only explicitly editable fields. User roles, database administration grants, account enablement, password hashes, and other credential material must remain outside profile writes.

Define profile field validation, update concurrency, and persistence semantics with the backend contract. Refresh the displayed profile from the successful response; failed or conflicting updates retain the entered values and show the server's actionable error. Token claims remain session identity, not a mutable profile database. If persistence requires a schema change, provide an explicit migration for deployed data.

Expose password change through the separately defined account operation, including required current-password verification and the server's session invalidation outcome. Keep user-owned profile changes separate from administrator provisioning and credential rotation. Provide an interactive mockup covering load, edit, save, invalid, conflict, and expired-session states before UI implementation.

## Alternatives

**Profile documents in an application collection:** reuses CRUD and application rules but makes account profile behavior depend on each database's schema and permission rules. A current-account API provides one ownership model for a shared console.

**Read-only token claims:** needs no profile persistence but cannot satisfy profile updates or reliably show changed server-side fields.

## Acceptance Criteria

- A signed-in user can reload and update allowed profile fields, and a new session observes the saved values.
- Attempts to modify another account, roles, grants, or credential fields are rejected by the server.
- Concurrent changes produce the documented conflict behavior; failed updates are visible without erasing the draft.
- Account switching clears prior profile state, and password changes follow the documented session outcome without logging credentials.

## Dependencies

[Password change](2026-09-07-password-change.md) owns credential semantics. [Admin provisioning](2026-09-07-admin-account-provisioning.md) owns administrator account creation and rotation; [audit records](2026-09-07-administrative-audit-records.md) owns the applicable audit mechanism.

## Risks

A permissive profile payload could accidentally expose privileged fields. An explicit field contract and principal-derived ownership are required; local browser state must never authorize a mutation.
