# Agent Note: Console Realtime Inspection

Status: proposed

## Problem

The [console design](../../../../docs/design/server/console/01.console.md) specifies active subscriptions, a live event viewer, connection diagnostics, and a subscription test tool. The current [router](../../../../console/src/router/index.tsx) has no realtime inspector page. SDK 的[公开 WS 入口已移除](../../../../docs/reference/typescript_sdk.md#4-realtime)，私有 replica 数据运输不提供普通订阅检查器。Operators cannot inspect the intended subscription behavior through the console. This is a planned feature gap, not evidence that existing realtime delivery fails.

## Proposal

Add a database-scoped inspector with a subscription form, active subscription list, bounded event buffer, and connection state timeline. Show query, subscription identifier, transport, snapshot status, reconnect state, and resume outcome. The test tool must use the same authorization and filtering rules as ordinary clients; subscribing must never imply broader read access.

Separate subscriptions created in the current browser session from an administrator's server-side active subscription inventory. The latter requires an authenticated, paginated diagnostic API and explicit visibility permissions; label its scope and observation time. Do not represent browser-local state as cluster-wide state.

Provide pause-display, clear, unsubscribe, and disconnect controls. Pausing display must state whether events continue to be received; buffer overflow must show dropped display entries. Unmount, account change, and database change release owned subscriptions and requests. Error details should preserve correlation identifiers and categories without recording bearer tokens or full sensitive document payloads in diagnostic logs. Authorized document data may be shown only through the normal data visibility contract.

## Alternatives

**Browser developer tools:** can inspect one connection without product work, but do not explain subscription ownership, resume outcomes, or server inventory.

**Server inventory only:** helps operators count connections but cannot exercise the same client subscription flow. The combined view is proposed with clearly separated scopes.

## Acceptance Criteria

- An interactive mockup demonstrates connected, reconnecting, denied, empty, snapshot, overflow, and disconnected states using the intended visual style.
- Two simultaneous subscriptions display only their associated events, and switching databases removes previous subscriptions and visible data.
- Filters and snapshot behavior match the selected transport; unsupported operations are identified before submission.
- The event buffer and inventory requests remain bounded, and cancellation leaves no owned sockets, timers, or requests.

## Dependencies

[Realtime resume](2026-09-07-realtime-client-resume.md), [filtered snapshots](../bug-fix/2026-09-07-realtime-filtered-snapshots.md), and [SSE parity](2026-09-07-sse-subscription-filter-parity.md) own protocol behavior. [Observability](../architecture/2026-09-07-application-observability.md) owns shared diagnostics and server inventory instrumentation.

## Risks

Cluster inventory can expose tenant metadata, and high event volume can exhaust browser memory. Authorization, limited data fields, bounded buffers, and explicit observation scope are required for the inspector to remain trustworthy.
