# Agent Note: SDK Authentication Session Race

Status: implemented

## Problem

An asynchronous authentication operation can outlive the session that started it.
Without ownership checks, a late refresh restores logged-out credentials, an old
logout clears a newer login, or an HTTP authentication retry uses another account's
credentials. Transport cancellation alone cannot prevent provider mutations and
application callbacks that happen before a promise settles.

## Decision

The token provider exposes a synchronous `getSessionVersion(): number`. Each
explicit credential replacement invalidates operations that captured an earlier
version. Normal token rotation retains the session version. Custom providers must
implement this contract; `getToken()` and `refreshToken()` keep their signatures.

| Operation | Credential and version behavior |
|---|---|
| Begin login or signup | Advance the version and clear both credentials synchronously; the last operation started owns the result |
| Complete current login or signup | Install the credential pair and advance again to invalidate requests admitted during the unauthenticated interval |
| Fail current login or signup | Remain logged out; return the original error |
| Complete an obsolete authentication operation | Return `AuthSessionChangedError` without changing credentials or publishing obsolete authentication callbacks |
| Logout | Capture the old refresh token, advance the version and clear locally, then call the existing remote logout with the captured token |
| Remote logout completion | Return success or the remote error; do not mutate current local credentials |
| `setToken(access)` | Advance the version, set access token, and clear the old refresh token |
| `setRefreshToken(refresh)` | Advance the version and attach the refresh token to current access credentials |
| Refresh | Share one operation within the same version; preserve the version when rotating tokens |

Callers injecting a complete pair call `setToken()` followed by `setRefreshToken()`.
A refresh-token-only initial configuration remains one session when refresh obtains
its access token. Refresh completion checks ownership before mutation and before
and after application hooks. Obsolete failures do not invoke `onAuthError` for the
new session. A refresh operation only clears its own shared-operation record, so
an old `finally` cannot release a newer session's refresh.

`AuthSessionChangedError` is a local `Error` with code `AUTH_SESSION_CHANGED` and
no HTTP status. Authentication consumers preserve it rather than converting it to
a server authentication failure.

| Consumer | Ownership checks |
|---|---|
| HTTP | Stamp the version synchronously during Axios request construction, retain it through retry, and check around abortable credential waits and before processing an old 401/403, refreshing, or retrying |
| HTTP without a token | Remove any existing Authorization header |
| 私有 replica WebSocket | 按活动 leader 租约捕获会话，检查 token/ACK/页关联；重连保留原会话，关闭可取消本次凭据等待 |
| SSE | Capture a controller and version before waiting for a token; check authentication, response, and read/callback ownership; old cleanup cannot clear a newer controller |
| SyntrixClient login, signup, logout | 先失效 provider 会话及已有 replica owner，再清除并断开缓存 SSE，随后等待认证结果 |

SSE 引用在清理回调重入前清除，独立创建的 SSE client 仍由调用方显式清理。连接身份
与会话检查同时保留；[私有 replica WS 决定](../architecture/2026-09-21-sdk-replica-websocket.md)
拥有当前数据运输生命周期，原[公开 WS 决定](2026-09-07-sdk-realtime-subscription-lifecycle.md)
保留其历史理由。

Explicitly disconnecting an established SSE connection in the same session emits
`onDisconnect` once after detaching ownership and before aborting its fetch. This
lets callbacks reconnect without obsolete cleanup affecting the replacement.
Pending and obsolete-session connections remain silent during explicit teardown.

## Alternatives

**Ignore late results only in WebSocket callbacks.** Credential mutation and
refresh callbacks have already happened in the provider, and HTTP retries can
still cross accounts. Provider and request ownership must be enforced together.

**Cancel outstanding authentication requests.** Cancellation can race with an
already completed response. Version checks at mutation and retry points establish
the guarantee even when network cancellation is unavailable.

**Add server session-wide revocation.** This would invalidate derived tokens at
the server, requiring server-side session identity and revocation enforcement.
The SDK race is resolved locally without a session table or extra server lookup.
Server-wide immediate invalidation is a separate guarantee, not supplied by these
client checks.

## Consequences

- Session changes require local integer comparisons and operation identity checks;
  token formats, server endpoints, and server revocation behavior are unchanged.
- Starting a replacement login ends the old local session even when login fails.
  Logout errors now distinguish local exit from failed remote revocation.
- Existing server logout still revokes the submitted refresh token. Already issued
  access or derived refresh tokens follow existing server expiration and revocation
  rules; local invalidation does not revoke them all.
- HTTP ownership begins synchronously during request construction, before async
  interceptors; SDK operations may bind an earlier session. Credential waits are
  abortable so owned requests can drain during account replacement. An already
  dispatched request may still finish with its old token; it cannot automatically
  retry under a new session. Successful old responses are not generally filtered
  and remote effects are not rolled back. The
  [replica-storage decision](../architecture/2026-09-18-sdk-replica-storage.md) owns
  credential-drain and native cancellation behavior for attached offline storage.
- 私有 WS 依附已有 replica owner，没有新增全局 provider-to-transport 注册表。
  独立创建的 SSE transport 仍需显式关闭；会话检查保护其后续异步工作。
- Adding immediate server invalidation later requires server session identifiers,
  revocation storage and checks, and a broader logout contract. Existing token
  formats remain untouched, leaving that cost explicit.

The [SDK authentication design](../../../../docs/design/sdk/003_authentication.md)
and [SDK reference](../../../../docs/reference/typescript_sdk.md#authentication-sessions)
own the mechanism and consumer contract.
