# Agent Note: Concrete Identity Token Capabilities

Status: implemented

## Problem

Narrow account, verifier, and system-token interfaces do not separate key
ownership when one implementation constructs signing and validation together.
A verifier callback that captures the signing service retains private-key access,
and obtaining a system issuer through account construction also requires
unrelated user/revocation dependencies. Those concrete dependencies obstruct
future composition of verification-only consumers and issuing services.

## Decision

`authn.NewServices(config, users, revocations)` constructs separate account,
shared verifier, and system-token-issuer capabilities. The account service,
user-token signer, and system-token issuer are unexported implementations under
`core/identity/authn`. Accounts retain user signing and validation needed for
existing signin, signup, refresh, logout, and administrative operations. Manager
holds distinct capabilities and passes the same verifier to account
administration and Gateway.

`identity.NewPublicTokenVerifier(publicKey)` makes a detached copy of the RSA
public key and captures only public verification material. It has no signing
service or callback closure retaining private keys. Caller mutation of the key
cannot change validation. Existing RS256/JWT time checks and detached claim
snapshots remain unchanged. A newly constructed verifier still has independent
provenance; equal public keys do not make its actors valid for another account
service's administration.

The system issuer signs existing service tokens without account-management or
verification capabilities. `authn.NewSystemTokenIssuer(config)` constructs the
benchmark's narrow issuing dependency without user or revocation stores.
Configured RSA key loading/generation, access/refresh TTLs, JWT claims, refresh
revocation ordering/overlap, logout behavior, service roles, and error contracts
retain their meaning. No key rotation, JWKS, project claim, session policy, or
OAuth endpoint is introduced.

Current API and Trigger Worker process assembly still constructs local issuing
capabilities and loads the configured private key. A verification-only object is
not a private-key-free process deployment. Repository ownership, implementation
placement, verification-key distribution, and independent Identity service
composition retain separate delivery gates. Identity/Gateway module-owned
configuration and the `gateway.authz` rule setting remain unchanged.

The [Identity architecture](../../../../docs/design/server/core/identity/01.architecture.md)
and [authentication design](../../../../docs/design/server/core/identity/02.authentication.md)
own the current contracts. The
[Identity contract decision](2026-10-10-identity-contracts-and-gateway-authentication.md)
retains its account/adapter, safe-view, and verified-actor rationale; the
[Gateway authorization decision](2026-10-10-gateway-document-authorization.md)
retains CEL/configuration ownership. The broader
[platform/instance proposal](../../proposed/architecture/2026-10-10-platform-console-instance-boundaries.md)
remains active for repository/runtime separation, key distribution, project
identity, sessions, independent deployment, and both OAuth roles.

## Alternatives

**Keep one concrete authentication service behind narrow interfaces.** This keeps
call sites small, but verifier construction can retain its private signing
service and system issuance still requires account dependencies. Separate
concrete capabilities make those dependency boundaries observable.

**Keep the verifier's signing-service callback.** This preserves the generic
validation adapter but lets a supposedly verification-only value retain private
keys through a closure. A detached public-key verifier preserves existing
validation without that private ownership.

**Relocate stores and deploy remote Identity with the key split.** This would
couple capability ownership to repository initialization, transport, readiness,
and key distribution changes. Those operations need their own lifecycle and
migration contracts. The current local composition preserves behavior while
making the eventual verification-only dependency possible.

## Consequences

- Public verification holds only copied public-key material. User signing and
  system issuance remain distinct private-key capabilities.
- Account administration and Gateway share one verifier provenance. Independent
  verifiers and detached claim mutations cannot authorize account commands.
- Benchmark system-token creation no longer requires account/revocation stores.
- Existing password/account, wire/configuration, refresh/logout, REST/realtime,
  and SDK session semantics remain unchanged; no extra account-state or
  revocation checks are added to ordinary verification.
- API/Trigger Worker startup still loads private keys for local issuance. Key
  distribution and repository/runtime extraction remain necessary before a
  private-key-free deployment can be claimed.
- Project realms, PostgreSQL sessions/revocations, OAuth/OIDC issuer and external
  login, and other new user capabilities remain governed by their existing
  proposal requirements.
