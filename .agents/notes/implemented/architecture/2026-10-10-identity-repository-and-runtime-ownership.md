# Agent Note: Identity Repository and Runtime Ownership

Status: implemented

## Problem

Account implementations and configuration still lived under shared core
components, while the document storage factory constructed credential users,
revocations, and catalog stores and owned their physical connections. Moving
only consumer contracts leaves module initialization, credential records, and
shutdown coupled to document storage. Catalog default bootstrap also depended
on a user store rather than a narrow account-owner query.

## Decision

`internal/identity` owns current account logic under `authn`, configuration under
`config`, credential repository contracts/adapters under `repository`, and local
composition under `runtime`. PostgreSQL users, Mongo revocations, and existing
revocation routing retain their storage and operation semantics. Public account
views remain separate from credential repository records.

`runtime.NewModule` constructs repositories from borrowed backend handles and
creates the account/verifier/system-issuer capabilities. The module owns
configured administrator bootstrap and resolves account owners into ID and
normalized username. Catalog bootstrap receives that resolver without exposing
credential records or creating an Identity dependency on catalog business types.
The catalog default bootstrap checks for an existing `default` row before
resolving an account; an existing catalog row needs no credential repository or
user lookup. Missing configured accounts retain the current skip behavior and
other errors propagate. `Module.Accounts()`, `Verifier()`, and
`SystemTokenIssuer()` expose the narrow capabilities; `EnsureAdmin(ctx)` owns
bootstrap and `ResolveOwner(ctx, username)` returns ID and normalized username.

`core/storage.Backends` owns named Mongo providers and the PostgreSQL pool
selected by the existing user topology. Identity repositories and the
document/catalog factory borrow those handles. Their schema/index construction uses the caller's startup context;
Identity repositories expose no physical Close operation, and the factory does
not close shared pools. Manager stops consumers before closing the Backends
owner once. Physical Mongo providers and
the PostgreSQL schema-coordination helper remain shared infrastructure.
User/revocation topology types, defaults, and defaulting belong to
`identity/config`, preserving their `storage.topology` YAML placement. Shared
physical infrastructure still depends
on configuration types; it does not own account/repository business logic.
Backends performs no schema DDL; module constructors retain their existing
schema/index calls rather than adding an index-readiness guarantee.

Startup failure runs the same stop-before-close cleanup path and retains
initialization and cleanup errors. If a consumer stop fails or cleanup times
out, Backends remains open for a later cleanup attempt after consumers finish.
Repeated physical Close attempts retain the owner's once-only result; a failed
shutdown does not claim that the connections were released.

Existing Identity and storage YAML, user/catalog IDs, password/key/JWT behavior,
refresh overlap and atomic revocation, logout, grants, data namespaces, and
current admin/system policy remain unchanged. The `user.strategy` field does
not become a split-user router; SQL still addresses literal `auth_users` despite
the retained table-name constructor parameter. Administrator bootstrap and
signup-role usernames remain distinct fields. Concurrent account creation still
relies on the PostgreSQL unique index and can return a raw competing-insert
error; this ownership extraction adds no transactional signup guarantee.

This completes module ownership of existing Identity logic after
[Gateway authorization](2026-10-10-gateway-document-authorization.md),
[account and HTTP contracts](2026-10-10-identity-contracts-and-gateway-authentication.md),
and [token capabilities](2026-10-10-identity-token-capabilities.md). The
[Identity architecture](../../../../docs/design/server/core/identity/01.architecture.md)
and [storage assembly](../../../../docs/design/server/core/storage/01.architecture.md)
own current behavior. The broader
[platform/instance proposal](../../proposed/architecture/2026-10-10-platform-console-instance-boundaries.md)
remains active for project realms, PostgreSQL sessions/revocations, key
distribution, independent service deployment, and both OAuth roles.

## Alternatives

**Leave credential adapters in the document factory.** This avoids changing
construction, but the Identity module still depends on a factory that owns
unrelated document/catalog assembly and credential records. Repository ownership
must follow the account module.

**Let each module open and close its own connections.** This gives local resource
ownership but duplicates pools and can disconnect another borrower during
cleanup. One physical Backends owner separates connection lifetime from record
and policy ownership.

**Pass the credential repository into catalog bootstrap.** This keeps the old
lookup path but lets catalog code access credential storage and resolve an
account unnecessarily when the default row already exists. A lazy owner resolver
preserves bootstrap outcomes while keeping account queries within Identity.

**Implement project/session/OAuth migration with extraction.** These changes need
new schemas, principal semantics, retained session/revocation rules, and protocol
activation. Mixing them with ownership would obscure existing-behavior parity.
The module and borrowed-backend boundaries support those future changes without
claiming their guarantees now.

## Consequences

- Account logic, configuration, credential records, repository routing, and
  bootstrap are owned by Identity; document/catalog assembly exposes no user or
  revocation stores.
- Consumers receive narrow account/verifier/issuer capabilities. Catalog owner
  resolution is lazy and cannot inspect password material.
- Shared connections remain available until all consumers stop; cleanup closes
  the physical owner once instead of closing from borrowed repository adapters.
- Current storage/configuration/account/token semantics and known limitations
  remain observable. No data migration, project default, new foreign key,
  distributed key protocol, or new session behavior is introduced.
- API/Trigger Worker local composition still constructs issuing capabilities and
  loads private keys. Key distribution and independent deployment remain required
  before a private-key-free Gateway process can be claimed.
- New project, session, user-lifecycle, and OAuth/OIDC capabilities remain in the
  broader proposal with their own compatibility and activation gates.
