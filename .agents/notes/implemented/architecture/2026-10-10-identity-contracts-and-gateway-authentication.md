# Agent Note: Identity Contracts and Gateway Authentication

Status: implemented

## Problem

Account operations, token verification, service-token issuance, and HTTP
middleware shared one authentication interface. Gateway and realtime callers
therefore depended on capabilities they did not use, while Trigger Delivery
received account administration alongside token issuance. User listing exposed
password-bearing repository records and relied on the HTTP handler to redact
them. Administrative policy lived at the HTTP entrypoint, leaving direct service
calls without an authenticated actor contract.

## Decision

`internal/identity` owns transport-free account, token-verification, and
system-token-issuance interfaces, request/token/claim types, safe account views,
and opaque verified identities. `AccountService` provides signup, signin,
refresh, logout, and actor-authorized user listing/update. `TokenVerifier`
provides `VerifyToken`; `SystemTokenIssuer` provides `GenerateSystemToken`.
The account/signing implementations remain in `identity/authn`.
`authn.NewServices` constructs the account service, shared public-key verifier,
and separate system-token issuer. The
[token capability decision](2026-10-10-identity-token-capabilities.md) owns that
later concrete separation; this decision owns the transport-free contracts and
Gateway adapter. The [repository/runtime decision](2026-10-10-identity-repository-and-runtime-ownership.md)
owns current account/config/repository placement and borrowed-backend composition;
independent service deployment remains separate work.

A `VerifiedIdentity` privately binds verified claims to the verifier that
issued it. Administrative operations accept that identity, require the account
service's own verifier provenance, and apply the existing exact `admin` or
`system` role policy to its private claims. Nil/zero actors and identities from
another verifier cannot administer accounts. `Claims()` returns a detached
snapshot; changing its roles, database grants, or registered claim values cannot
elevate the private identity. This is an in-process authority capability, not an RPC
serialization or project identity contract.

Public `User` values contain eleven noncredential fields: ID, username,
creation/update times, disabled state, roles, database-admin assignments,
profile, last login time, login attempts, and lockout time. Internal repositories
retain password material. REST preserves existing user JSON by adding fixed
empty `password_hash` and `password_algo` fields to the safe view. Pagination,
nil/empty result behavior, and replacement semantics for role/database-admin/
disabled updates retain their contracts.

`gateway/authentication` owns required/optional Bearer HTTP middleware and
projection into existing context keys. REST holds account and verifier
capabilities; realtime receives a verifier; Trigger Delivery receives only a
system-token issuer. Existing HTTP error mapping, claims, realtime admission and
owner checks, and SDK session fences retain their behavior. No current-user,
revocation, or additional time/disabled check is added to account-administration
provenance validation.

The [Identity architecture](../../../../docs/design/server/core/identity/01.architecture.md)
and [authentication design](../../../../docs/design/server/core/identity/02.authentication.md)
own these contracts. The earlier
[Gateway document authorization decision](2026-10-10-gateway-document-authorization.md)
continues to own CEL/configuration separation. The
[platform and instance proposal](../../proposed/architecture/2026-10-10-platform-console-instance-boundaries.md)
remains active for verification-key distribution, project identity, sessions,
independent deployment, and both OAuth roles.

## Alternatives

**Keep one authentication interface including HTTP middleware.** This preserves
existing signatures but carries HTTP into account contracts and gives realtime
and Trigger Delivery unrelated account operations. Separate capabilities state
the authority each consumer needs.

**Return repository users and redact them in Gateway.** This preserves the old
storage alias, but every new service/RPC consumer must independently prevent
credential disclosure. A safe account view makes the service response contract
independent of repository credentials; REST retains its old blank JSON fields.

**Accept caller-supplied roles for user administration.** A role-bearing struct
can be constructed or changed outside verification. Opaque verifier provenance
and private claim snapshots preserve the current administrative policy without
trusting those mutations. A future remote client must establish an authenticated
actor contract rather than serializing this local capability as authorization.

## Consequences

- Account contracts no longer depend on HTTP adapters or storage User types.
  Gateway owns Bearer/context handling and document-policy projection.
- Direct administrative calls require verified actor provenance and the current
  exact admin/system policy. Detached public claim changes do not grant authority.
- User listing returns noncredential views while existing REST consumers retain
  their current field names, empty credential fields, pagination, and updates.
- Signup, password hashing/lockout, bootstrap, JWT wire/key behavior, refresh
  revocation, logout, database namespaces/grants, and SDK/realtime session
  semantics remain unchanged.
- Current usernames and grants are instance-wide. Project realms, complete
  OAuth/OIDC, account-wide sessions/revocation, password change, and user deletion
  are not delivered by these contracts.
- The token capability decision separates concrete signing and public-key
  verification. Current composition still loads the configured private key for
  local issuing capabilities and uses PostgreSQL users/Mongo revocations.
  Identity runtime owns borrowed repository composition. Verification-key
  distribution and independent service deployment retain separate delivery gates.
