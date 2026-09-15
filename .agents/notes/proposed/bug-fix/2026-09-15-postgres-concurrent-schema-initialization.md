# Agent Note: Serialize PostgreSQL Schema Initialization

Status: proposed

## Problem

Concurrent service initializers can fail against an empty PostgreSQL database.
`CREATE TABLE IF NOT EXISTS` does not coordinate simultaneous creation of the
same table and its catalog type. A PostgreSQL 18 run reported
`pg_type_typname_nsp_index` for `(auth_users, 2200)` while separate integration
environments initialized the shared public schema. One environment failed before
its test cases started; running its initializer alone completed successfully.

The existing [user schema initializer](https://github.com/codetreker/syntrix/blob/f7ebae7a6c608c6b00197e01173b8c56e2c9a6ae/internal/core/storage/postgres/user_store.go#L33-L57)
executes DDL without cross-process coordination. The
[storage factory](https://github.com/codetreker/syntrix/blob/f7ebae7a6c608c6b00197e01173b8c56e2c9a6ae/internal/core/storage/factory_impl.go#L137-L197)
can initialize both authentication and database metadata schemas during startup.

## Proposal

Give schema initialization one cross-process owner per PostgreSQL schema. Choose
either coordinated startup DDL or an explicit migration owner, and apply that
choice consistently to authentication and database metadata initialization.
Initialization failures must retain the database error and prevent a partially
initialized service from reporting readiness.

Until coordination is delivered, shared test databases require serialized schema
initialization before parallel suites. This is a test-environment preparation
step, not evidence that concurrent production startup is safe.

## Alternatives

**Isolate every integration suite in its own PostgreSQL schema.** Removes the
test collision but does not protect concurrent production service startup.

**Retry or ignore duplicate catalog errors.** An initializer can observe partially
completed DDL; suppressing the error does not establish schema readiness.

## Acceptance Criteria

- Concurrent independent initializers against an empty schema complete without
  catalog collisions and produce the required tables and indexes.
- Existing initialized schemas remain safe to open repeatedly.
- Failed initialization stays observable and cannot publish readiness.
- Cold-start integration tests exercise concurrent processes and retain setup
  errors emitted before the first test case.

## Risks

Deferral leaves cold-start service replicas and shared integration environments
exposed to intermittent initialization failure. Adding coordination later must
define ownership, waiting bounds and cleanup for failed initializers; it does not
require changing replication checkpoints or document write semantics.
