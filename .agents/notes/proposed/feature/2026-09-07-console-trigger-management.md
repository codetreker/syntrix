# Agent Note: Console Trigger Management

Status: proposed

## Problem

The current embedded instance administration [Triggers page](../../../../packages/console/src/pages/TriggersPage.tsx) contains only `Trigger rules management coming soon...`. The [console design](../../../../docs/design/server/console/01.console.md) calls for configuration inspection, execution history, manual tests, and error logs. The current [gateway routes](../../../../packages/syntrix/internal/gateway/rest/handler.go) expose trigger document operations, not a complete trigger administration API. This is an incomplete planned capability, established through static inspection.

## Proposal

In the [target developer Console](../../../../docs/architecture.md), require selection of an owned instance, project, and logical database before trigger administration. Authorize the developer operation separately from project end-user tokens and employee Management authority. Deliver a database-scoped trigger administration workflow covering rule inspection and editing, validation feedback, activation state, paginated execution history, delivery errors, and explicit manual tests. Establish authenticated backend operations for those actions together with the UI; document which actions require database administration rights and enforce them server-side.

Keep trigger configuration distinct from authorization rules. Use stable rule versions and conditional updates so concurrent editors receive a conflict rather than overwrite changes. An activation response must identify the persisted configuration and its effective state; history entries must identify the rule version and delivery operation. A manual test must distinguish validation from actual webhook execution, show the selected target and side effects, and require an explicit execution action. It must use the normal evaluator/delivery controls and produce a correlated result.

The UI displays secret references and resolution status only. Loading, empty history, invalid configuration, permission denial, stale edits, timeout, and partial service failure must have distinct states. Provide an interactive mockup using the intended visual style before implementing the interface.

## Alternatives

**File-based configuration with external logs:** retains the existing operations model but leaves the console's declared management workflow absent and requires operators to correlate versions and deliveries manually.

**Editing raw configuration only:** offers broad field coverage quickly, but provides no execution history or trustworthy activation status. A raw editor can complement validated forms once both use the same backend contract.

## Acceptance Criteria

- An authorized developer or explicitly delegated instance administrator can inspect, change, validate, activate, and test a rule and find its correlated execution result.
- Conflicting updates preserve the active rule; invalid configurations and unavailable services produce explicit errors.
- Database boundaries are enforced for configuration, history, and manual tests; application end users cannot obtain administration rights from an application role name alone.
- History is paginated, polling stops on navigation, and secrets never appear in responses or browser diagnostics.

## Dependencies

[Secret resolution](2026-09-07-trigger-secret-resolution.md), [execution controls](2026-09-07-trigger-rule-execution-controls.md), and [observability](../architecture/2026-09-07-application-observability.md) supply runtime controls and diagnostics. [Security-rule publication](../architecture/2026-09-07-security-rule-publication-lifecycle.md) owns authorization-rule storage, history, rollback, and dry-run; this proposal does not duplicate those operations.

## Risks

Manual tests can create real downstream effects. Trigger administration also needs a persisted configuration contract and bounded history retention, which increase backend scope beyond rendering the page.
