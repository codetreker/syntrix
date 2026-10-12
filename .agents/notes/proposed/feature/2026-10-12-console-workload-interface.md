# Agent Note: Console Workload Interface

Status: proposed

## Problem

Console developers can create, inspect, and delete predefined Kubernetes test
workloads through the [backend API](../../../../docs/reference/console_api.md),
but the [browser shell](../../implemented/architecture/2026-10-12-console-client-session-and-distribution.md)
currently supplies login and Overview only. It has no workload list, deployment
form, operation status, or deletion interface. Developers must use API tooling
to manage these existing resources.

## Proposal

Add browser resource pages for the existing API rather than expanding the
deployment model. List/detail queries remain scoped to the authenticated
developer, and account changes cancel private requests and clear account-owned
page state. Do not substitute instance-user credentials or client-side menu
visibility for Console backend ownership.

Create selects a server-listed template and sends an Idempotency-Key. Preserve
the same request identity after uncertain acceptance; never create a fresh key
merely because a response was lost. Distinguish accepted intent from provisioning,
ready, failure, deletion intent, and observed deletion. Delete uses the existing
durable lifecycle. Failed or unknown mutations require authoritative resource
inspection before the interface presents a confirmed outcome.

Display observation timestamps and stale state rather than claiming that an old
ready receipt is live availability. Polling has bounded cadence, cancellation,
and one owner per selected resource; late responses cannot replace another
resource/account's state. Use the current API's limits and sanitized errors.
Final-style interactive mockups must represent the existing lifecycle and failure
states before implementing those pages.

## Alternatives

**Keep API tooling as the only resource interface.** This requires no browser
pages but leaves the developer resource workflow outside the delivered Console.
It remains the supported way to use workloads until this proposal is delivered.

## Acceptance Criteria

- A developer lists, creates, inspects, and deletes only their workloads through
  the existing backend API.
- Response loss retains create identity and never turns an uncertain result into
  a second resource or confirmed success.
- Lifecycle status and observation time distinguish accepted intent from actual
  resource observations.
- Account/resource changes and teardown cancel polling/private requests and
  prevent stale responses from exposing a prior selection.
- The existing session and CSRF authority remains unchanged; unsupported
  full-instance/project operations are not presented as available.

## Risks

Adding these pages requires auth-scoped data state, idempotency handling,
observation feedback, and polling cleanup. The current shell separates session
authority from page data so they can be added without replacing login/restoration.
The [platform proposal](../architecture/2026-10-10-platform-console-instance-boundaries.md)
continues to own full instance/project administration; this note owns only the
missing browser interface for the implemented test-workload API.
