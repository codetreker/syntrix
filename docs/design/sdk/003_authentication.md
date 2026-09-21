# SDK Authentication Design

**Date:** December 22, 2025
**Status:** Authentication sessions implemented; remaining planned integration is marked below

## Context & Why
- The SDK needs a consistent auth story across HTTP CRUD/query, replication (pull/push), and realtime channels.
- We must support short-lived bearer tokens with refresh, while keeping durable secret storage under application ownership.
- Replication and realtime must not diverge in auth handling; retries and refresh should be predictable and bounded.

## Goals
- Single, pluggable auth abstraction reused by `SyntrixClient`, replication workers, and realtime subscription.
- Safe refresh handling: serialize refresh, retry once on 401/403, avoid infinite loops.
- Token injection without leaking or persisting sensitive credentials in the SDK.
- Clear hooks so host apps control storage of refresh tokens and decide logout vs. retry.

## Non-Goals
- Defining server-side auth; this is client-only wiring.
- Managing server-side sessions or application UI flows (login/consent).
- Persisting refresh tokens/API keys in the SDK; caller owns secret storage.

## Auth Surface

| Provider method | Contract |
|---|---|
| `getSessionVersion(): number` | Required synchronous provider-local session version |
| `getToken(): Promise<string \| null>` | Current access token, if any |
| `refreshToken(): Promise<string>` | Refresh within the current session; share concurrent work for that session |
| `setToken(token: string): void` | Advance version, replace access token, clear old refresh token |
| `setRefreshToken(token: string): void` | Advance version and attach refresh token to current access credentials |

Custom providers must protect credential mutations and callbacks as well as
exposing the version getter. A version cannot be reused for a later session;
normal refresh rotation retains it. The default provider keeps credentials in
memory. Its `AuthConfig` supports `token`, `refreshToken`, `refreshUrl`, `database`,
`onTokenRefresh(newToken)`, and `onAuthError(error)`.

Planned additional hooks remain `onAuthRetry` and `onRealtimeAuthError`; they are
not part of the delivered configuration. Applications own durable secret storage.

## Request Injection
- HTTP request construction synchronously captures the provider version before
  Axios schedules asynchronous interceptors; retries preserve the captured version.
- Credential waits check the request's cancellation signal before starting and
  remain abortable while waiting, so an owned request cannot hold up the account
  transition whose credentials it is waiting for.
- Check the version before and after token acquisition. Attach
  `Authorization: Bearer <token>` when available; delete an existing Authorization
  header when no token is available.
- Never log tokens in diagnostics. The `onTokenRefresh` credential callback is for
  application credential handling and must not be treated as a diagnostic event.

## Refresh & Retry Policy
- On 401/403, check the original request session before refreshing or processing
  an already retried response. Refresh at most once within that session, check
  again after refresh, and retain the original version through retry. Normal
  rotation installs tokens without the explicit-replacement setter.
- A current refresh failure invokes `onAuthError` and propagates. Missing refresh
  credentials fail without a network attempt. Obsolete failures return
  `AuthSessionChangedError` without notifying the new session through auth hooks.
- Network errors follow existing backoff; auth errors do not exponential-backoff (they need user/token action).

## Replica WebSocket 与 SSE

- 私有 replica WS 与 HTTP 共用 tokenProvider。活动 leader 的运输租约捕获原会话，
  以 `replica-data` auth 帧发送 token/database，匹配 auth_ack 后才建立源注册。
- 当前 auth 请求收到结构化 UNAUTHORIZED 时只允许一次共享 refresh；FORBIDDEN、
  绑定身份错误或 refresh 失败保留既有阻塞语义，不能以 HTTP fallback 绕过。
- 连接/认证及注册分别受 10 秒 deadline 限制。取消监听先于凭据调用安装，关闭只等待
  可取消的本次尝试，不等待 provider 共享 refresh promise；底层迟到结果仍有处理器并
  受 provider 的会话检查保护。关闭一个 WS 不取消其它 HTTP 调用共享的 refresh。
- 重连保留原 session，只在有效 leader 租约存在时运行；账号替换退休旧租约和迟到
  消息归属。诊断回调可同步关闭句柄，回调后必须再次核对 owner。
- SSE 仍在等待 token 前建立 controller 并捕获会话；认证、响应、读取和回调核对
  controller/session，旧清理不能移除新连接。公开 SSE 行为不变。
- 登录、注册和退出先失效 provider 会话，既有 replica owner 负责取消私有运输；
  客户端在等待远端认证前清除并断开缓存 SSE。独立创建的 SSE client 仍由调用方清理。
- 公开 `realtime()`、`subscribe()` 及 WS 构造器/协议类型已移除；SSE 与手动 Pull 保留。
  运输所有权和 fallback 的完整决定见[SDK WS 复制](../../../.agents/notes/implemented/architecture/2026-09-21-sdk-replica-websocket.md)。

## Replication (pull/push)
- Pull/Push use the same auth layer; retries on 401/403 follow the refresh-once rule.
- Do not advance checkpoints on auth failures; resume after refresh with the last persisted checkpoint.

## Trigger Handler
- `TriggerHandler` continues to require `preIssuedToken` from the payload; no auto-refresh. Fail fast if missing/invalid.

## Multi-database / Audience
- Prefer token-scoped database. If a database header is ever needed, expose an explicit option (not implicit) to avoid drift between token and header.

## Authentication Session Ownership

Asynchronous operations record the session that started them so obsolete results
cannot restore credentials or retry business requests under another account.

| Operation | Ownership and result |
|---|---|
| Begin login/signup | Advance version, clear both credentials synchronously; the last operation started owns its result |
| Successful current login/signup | Install the pair and advance again; requests admitted while login was pending cannot retry under the new identity |
| Failed current login/signup | Remain logged out; propagate the failure |
| Obsolete authentication result | Return `AuthSessionChangedError`; do not mutate current credentials or emit obsolete hooks |
| Logout | Capture old refresh token, advance version and clear locally, then call existing remote logout with the captured token |
| Remote logout completion | Return success or failure without changing current local credentials |
| Complete credential injection | Call `setToken()` followed by `setRefreshToken()` |

Refresh-token-only initial configuration belongs to one session: refresh may
obtain its access token without advancing the version. Refresh operations coalesce
by session and clear only their own operation record. Check ownership before
credential mutation and before and after hooks; a hook may synchronously log out.
Obsolete errors do not invoke `onAuthError` for the new session.

`AuthSessionChangedError` extends `Error`, has code `AUTH_SESSION_CHANGED`, and has
no HTTP status. Consumers preserve it rather than converting it to a server 401.

HTTP ownership starts synchronously when the Axios request is constructed, before
its asynchronous authentication work. SDK operations that capture an earlier
session retain that binding. Cancellation releases token and refresh waits without
discarding the provider's credential-drain barrier. Already dispatched requests
may finish using old credentials; they cannot automatically retry using a new
session. Successful old responses are not generally filtered, and remote effects
are not rolled back. Normal refresh preserves the version and each request remains
limited to one authentication retry.

Local integer comparisons and operation-identity checks add no server lookup or
token-format change. Existing logout revokes the submitted refresh token; access
and derived refresh tokens retain existing server expiration and revocation rules.
The [authentication session decision](../../../.agents/notes/implemented/bug-fix/2026-09-10-sdk-authentication-session-race.md)
records alternatives and costs; the [SDK reference](../../reference/typescript_sdk.md#authentication-sessions)
owns public usage and errors.

## Planned Observability
- Additional diagnostic hooks should expose sanitized metadata (no tokens):
  - `onAuthError({ endpoint, status })`
  - `onTokenRefreshed()`
  - `onAuthRetry({ endpoint })`
  - `onRealtimeAuthError({ reason })`

## Testing Plan
- Control refresh/login/logout completion order; verify obsolete results cannot
  mutate credentials, publish stale hooks, or retry under another account.
- Exercise callback reentry, same-session refresh sharing, and operation cleanup.
- Check WebSocket ACK/reconnect and SSE authentication/read/controller ownership.
- 401 on CRUD: trigger refresh -> retry succeeds.
- 401 on CRUD without refresh: propagate error, no retry.
- Refresh failure: single retry attempt, then error, hook fired.
- Concurrency: multiple parallel requests hit 401 -> only one refresh occurs; all retry once with new token.
- Replica WebSocket：当前 auth 的 UNAUTHORIZED 只 refresh 一次；终止错误结束尝试，
  新连接认证后为活动 alias 建立新注册，凭据取消不等待共享 refresh。
- Replication pull/push: 401 triggers refresh-once, checkpoint unchanged on failure, resumes on success.

## Integration Points
- `001_sdk_architecture.md`: Auth abstraction shared by `SyntrixClient` and `TriggerClient` (where applicable), with clear separation of trigger pre-issued tokens.
- `002_replication_client.md`: Replication workers use the same auth layer; realtime trigger also relies on it; no checkpoint advance on auth errors.
