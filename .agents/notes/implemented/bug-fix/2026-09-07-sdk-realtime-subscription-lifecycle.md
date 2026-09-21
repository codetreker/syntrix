# Agent Note: SDK Realtime Subscription Lifecycle

Status: implemented

## Problem

Convenience subscriptions require a connected, authenticated transport and
independent callbacks. Without that ownership, subscriptions can remain local,
replace another subscription's callbacks, or survive teardown through reconnect
timers and late authentication results. These shared SDK defects outlive the
[retired Chat example](../simplification/2026-09-10-remove-chat-example.md).

## Decision

本决定记录此前公开 RealtimeClient 的生命周期：每个 client 拥有一条 WebSocket，
并发 connect 共用尝试，成功需要 auth_ack；便捷 subscribe 自动连接，低层 subscribe
仅登记本地订阅。这些公开入口现已由[私有 replica WS 决定](../architecture/2026-09-21-sdk-replica-websocket.md)
取代并移除。下面保留原协议的历史决策与理由，不是现行公开 API 使用说明；SSE 仍公开。

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

The earlier API's `activityTimeoutMs` (default 90,000 ms) bounds socket establishment
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

- 旧便捷订阅曾在认证后自动注册，并用 onReady 报告注册完成；现有应用改用 replica
  的本地 watch 和同步状态，不能继续调用已移除的公开 WS API。
- 旧连接由 client 显式关闭的理由保留于本决定。当前私有 WS 由活动 leader 租约管理，
  最后一个租约释放时关闭，不沿用旧 API 的最后一次 unsubscribe 保留连接规则。
- No server protocol, durable checkpoint, or replay continuity guarantee changes.
  [Realtime resume](../../proposed/feature/2026-09-07-realtime-client-resume.md) and
  [offline replication](../feature/2026-09-07-sdk-offline-replication.md)
  retain those responsibilities.
- WebSocket teardown and provider ownership have separate responsibilities. The
  [authentication session decision](2026-09-10-sdk-authentication-session-race.md)
  prevents obsolete credential mutations and retries across accounts. Transport
  guards still prevent stopped connections from reviving; outstanding HTTP calls
  need not be canceled for provider ownership checks to apply.

[SDK reference](../../../../docs/reference/typescript_sdk.md#4-realtime)维护当前 SSE 可用性；
[replica 运输](../architecture/2026-09-21-sdk-replica-websocket.md)维护新的 WS 数据生命周期。
