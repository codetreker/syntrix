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
| WebSocket | Capture a version per connection attempt, check token waits and authentication ACKs, and retain that version through automatic reconnect |
| Explicit WebSocket connect | End an obsolete attempt and allow a new attempt under the current session |
| SSE | Capture a controller and version before waiting for a token; check authentication, response, and read/callback ownership; old cleanup cannot clear a newer controller |
| SyntrixClient login, signup, logout | Begin provider invalidation, clear both cached realtime references, dispose the old WebSocket and disconnect the old SSE, then await authentication completion |

Realtime references are cleared before cleanup callbacks can reenter. Independently
constructed realtime clients retain explicit owner cleanup. Connection identity
checks remain necessary alongside session checks; the
[realtime lifecycle decision](2026-09-07-sdk-realtime-subscription-lifecycle.md)
owns subscription and transport lifetime.

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
  [local-storage decision](../architecture/2026-09-18-sdk-local-storage.md) owns
  credential-drain and native cancellation behavior for attached offline storage.
- No global provider-to-transport registry is introduced. Owners of independently
  constructed transports must close them explicitly; session checks guard their
  later asynchronous work and prevent automatic reconnect across sessions.
- Adding immediate server invalidation later requires server session identifiers,
  revocation storage and checks, and a broader logout contract. Existing token
  formats remain untouched, leaving that cost explicit.

The [SDK authentication design](../../../../docs/design/sdk/003_authentication.md)
and [SDK reference](../../../../docs/reference/typescript_sdk.md#authentication-sessions)
own the mechanism and consumer contract.
