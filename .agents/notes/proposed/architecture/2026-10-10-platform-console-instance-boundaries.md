# Agent Note: Platform, Console, and Instance Boundaries

Status: proposed

## Problem

The existing account, console, and database descriptions use `user`, `admin`,
`management`, and `database` for several different authority domains. The runtime
has a global user model and an embedded administration frontend. Those facts
do not define employee platform accounts, developer accounts, or application
end-user realms. Treating them as one domain would give application roles
platform meaning and confuse private system records with developer documents.

## Proposal

The [platform architecture](../../../../docs/architecture.md) owns the accepted
target model. This note remains proposed because the runtime and deployment
separation has not been implemented.

- The Management platform serves Syntrix employees and owns whole-platform
  scheduling, resource operations, monitoring, and administration.
- The Console service serves developers. Each developer creates and manages
  one or more Syntrix instances through Console.
- A Syntrix instance serves application end users and contains Syntrix services,
  MongoDB, and a private system PostgreSQL database.

Every instance owns its projects. Each project owns an isolated identity realm
and may use multiple logical Syntrix databases. Instance Identity is a peer
module to Indexer and Puller. It owns project end-user accounts, sessions, OAuth
client/provider state, Syntrix's OAuth/OIDC issuer role, and external-provider
login. The process-level Go `services.Manager` retains dependency-composition
and startup/shutdown ownership.

Gateway [document authorization](../../implemented/architecture/2026-10-10-gateway-document-authorization.md)
owns the existing CEL evaluator, rule configuration, and evaluation types
separately from account authentication. Gateway's module configuration owns
YAML `gateway.authz`, while Identity owns authentication/admin settings. Rule
behavior, enforcement, and storage retain their contracts.

[Identity contracts and Gateway authentication](../../implemented/architecture/2026-10-10-identity-contracts-and-gateway-authentication.md)
provide transport-free account/verifier/issuer capabilities, safe user views,
opaque verified actor provenance, and Gateway-owned HTTP/context adapters.
[Token capabilities](../../implemented/architecture/2026-10-10-identity-token-capabilities.md)
separate concrete user signing, system issuance, and public-material-only
verification while sharing verifier provenance with account administration and
Gateway. Current implementation remains under `identity/authn`, and
API/Trigger Worker assembly still loads private keys for local issuance.
[Repository/runtime ownership](../../implemented/architecture/2026-10-10-identity-repository-and-runtime-ownership.md)
places current account logic, configuration, credential repositories, and
administrator bootstrap inside Identity, borrowing a shared physical Backends
owner. Gateway authorization, transport-free contracts, concrete token
capabilities, and repository/runtime extraction complete the current-logic
module separation. Verification-key distribution, project realms, sessions,
independent deployment, and both OAuth roles remain unimplemented
responsibilities. This note retains proposed status for them.

Instance PostgreSQL stores projects, application users, credentials, OAuth/OIDC
state, sessions, and logical-database configuration and metadata. Syntrix
databases are the developer-facing logical document product; MongoDB holds
their business documents. System records are reached through authorized system
operations, not application document APIs.

Management and Console have their own platform/developer account and inventory
ownership. Their backend choices remain open. Neither adopts a developer
instance's PostgreSQL as global platform authority. The
[cluster-control proposal](2026-09-07-cluster-control-plane.md) owns membership,
leases, fencing, and configuration reconciliation within the Management domain.

Platform employee, developer, and project end-user credentials have distinct
authority. A project role named `admin` cannot authorize Console instance
creation or Management platform operations. Shared identity or protocol
mechanisms do not establish shared accounts or permissions.

## Alternatives

**Combine application identity and platform Management.** This would place
end-user credentials, OAuth state, database catalog, and platform operations
behind one user-facing service. It conflates employee/developer authority with
application roles and obscures which instance owns system data. The accepted
model separates these authorities.

**Use each logical database as an end-user identity realm.** This associates
account lifecycle with one data store. An application using several databases
would need separate accounts or additional federation, and deleting a database
would affect authentication scope. Project identity supports multiple databases
without making one database the account owner.

**Share application users across every application of a developer.** This would
couple unrelated application account, provider, and session lifecycles. The
accepted project boundary keeps those lifecycles independent; cross-project
account sharing would require an explicit separate contract.

## Acceptance Criteria

- Architecture, component designs, navigation, and future implementations use
  the same three user domains and service boundaries.
- A developer's multiple instances each retain their own projects, identity
  state, configuration, and business data.
- A project has one isolated end-user identity realm and can bind multiple
  Syntrix databases. Shared identity does not bypass database/document policy.
- Instance PostgreSQL is private system storage, and Syntrix database APIs
  expose authorized business-document operations.
- OAuth/OIDC issuer and external-provider login are instance Identity
  responsibilities; platform credentials and application roles remain distinct.
- Database deletion, project deletion, and instance deletion have separate
  scopes. Deleting one project database does not delete users still belonging
  to the project.
- Current embedded console, global accounts, JWT endpoints, and Mongo revocation
  are documented as current implementation facts, not the completed target.

## Risks

The current account model cannot identify the three domains through role names
alone. Implementation needs an explicit account classification, project binding,
and session transition. Metadata IDs and data namespaces cannot be rewritten
silently during extraction. Existing business references and SDK replica identity
must be accounted for by the migration design.

Identity's current repository/runtime ownership preserves initialization,
borrowed-connection lifetime, and capability behavior. Until verification-key
distribution and process
composition are designed, local API/Trigger Worker startup still loads private
keys despite the verifier object's public-only ownership. Project/session/OAuth
work must adopt those boundaries without reintroducing signing access into
verification-only consumers.

Provisioning, deletion coordination, public issuer configuration, token
revocation deadlines, platform storage backends, and service-to-service
authorization require further design. These open mechanisms do not change the
accepted actor, ownership, project, or database boundaries.

The [reference-alignment proposal](../process/2026-09-07-reference-contract-alignment.md)
owns executable API/SDK documentation consistency. It must preserve the
distinction between current routes and the accepted target model.
