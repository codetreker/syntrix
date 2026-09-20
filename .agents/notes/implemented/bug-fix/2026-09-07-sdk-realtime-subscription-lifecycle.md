# Agent Note: SDK Realtime Subscription Lifecycle

Status: implemented

## Problem

Convenience subscriptions require a connected, authenticated transport and
independent callbacks. Without that ownership, subscriptions can remain local,
replace another subscription's callbacks, or survive teardown through reconnect
timers and late authentication results. These shared SDK defects outlive the
[retired Chat example](../simplification/2026-09-10-remove-chat-example.md).

## Decision

Each `RealtimeClient` owns one WebSocket and its connection attempt. Concurrent
`connect()` calls share the attempt; success requires `auth_ack`. Convenience
`SyntrixClient.subscribe()` starts or reuses the connection and returns its handle
synchronously. Low-level `RealtimeClient.subscribe()` registers locally and leaves
connection initiation explicit.

| Operation | Ownership and result |
|---|---|
| Subscribe | Store options and callbacks by `subId`; send after authentication |
| Event or snapshot | Dispatch to the matching active subscription and the global observer, including before its registration ACK |
| Subscription error | Correlate the request ID; notify that subscription and the global error observer |
| `snapshot_failed` or `snapshot_limit` after registration ACK | Notify the existing error observers while preserving an active subscription and subsequent live events |
| Connection or authentication failure | Notify active subscriptions and the global error observer once for the failed attempt |
| `subscribe_ack` | Invoke subscription `onReady` once per connection registration; ignore duplicate ACKs |
| Unsubscribe | Remove that subscription's callbacks and registration state; preserve the shared connection |
| `disconnect()` | Close the socket, cancel timers, reject connection waiters, and retain logical subscriptions for explicit reconnect |
| `dispose()` | Stop transport work and clear subscriptions and observers permanently; reject reuse |
| Login, signup, logout | Invalidate the local authentication session, clear cached realtime references, and dispose the owned WebSocket and disconnect SSE before awaiting remote authentication |

`onReady` means registration succeeded. It does not establish historical delivery
or snapshot completion; applications can schedule reconciliation from it after
initial registration and reconnect. A failed registration remains locally active
until unsubscribe and can register again on a later connection. The two snapshot
error codes are nonterminal only after registration acknowledgment; they cannot
revive a previously failed or removed subscription. Global `.on(...)`
retains one observer per event; convenience subscriptions do not replace it.
Synchronous callback exceptions are reported separately from protocol parsing and do not
prevent dispatch to other eligible callbacks.

The existing `activityTimeoutMs` (default 90,000 ms) bounds socket establishment
and authentication independently of incoming heartbeats, and bounds inactivity
after authentication. Only `unauthorized` correlated with the current auth request
permits one token refresh. Invalid auth, a missing token, refresh failure, and a
second rejection fail the attempt. Authentication success resets reconnect
attempts. Disconnect and disposal invalidate old socket handlers, timers, and late
token results so they cannot revive transport work.

## Alternatives

**One socket per subscription.** This separates callbacks but multiplies
authentication, heartbeats, and reconnection load as subscriptions grow.

**One application callback that resynchronizes everything.** This schedules work
for unrelated collections and gives individual subscribers no cleanup ownership.

**Disconnect after the last unsubscribe.** This requires distinguishing implicit
and explicit connection users. Client-owned lifetime supports both APIs without
closing a manually managed connection when a subscription ends.

## Consequences

- Convenience subscriptions connect automatically; `connect()` completes after
  authentication, and `onReady` reports each subscription's registration.
- Subscription routing and teardown remain independent on a shared connection.
  The last unsubscribe leaves the connection open until explicit teardown.
- No server protocol, durable checkpoint, or replay continuity guarantee changes.
  [Realtime resume](../../proposed/feature/2026-09-07-realtime-client-resume.md) and
  [offline replication](../feature/2026-09-07-sdk-offline-replication.md)
  retain those responsibilities.
- WebSocket teardown and provider ownership have separate responsibilities. The
  [authentication session decision](2026-09-10-sdk-authentication-session-race.md)
  prevents obsolete credential mutations and retries across accounts. Transport
  guards still prevent stopped connections from reviving; outstanding HTTP calls
  need not be canceled for provider ownership checks to apply.

The [SDK reference](../../../../docs/reference/typescript_sdk.md#4-realtime-ws--sse)
owns the public usage and lifecycle contract.
