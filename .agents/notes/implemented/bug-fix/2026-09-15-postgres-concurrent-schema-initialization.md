# Agent Note: Serialize PostgreSQL Schema Initialization

Status: implemented

## Problem

Concurrent service initializers can fail against an empty PostgreSQL database.
`CREATE TABLE IF NOT EXISTS` does not coordinate simultaneous creation of the
same table and its catalog type. A PostgreSQL 18 run reported
`pg_type_typname_nsp_index` for `(auth_users, 2200)` while separate integration
environments initialized the shared public schema. One environment failed before
its test cases started; running its initializer alone completed successfully.

The original [user schema initializer](https://github.com/codetreker/syntrix/blob/f7ebae7a6c608c6b00197e01173b8c56e2c9a6ae/internal/core/storage/postgres/user_store.go#L33-L57)
executed DDL without cross-process coordination. The
[storage factory](https://github.com/codetreker/syntrix/blob/f7ebae7a6c608c6b00197e01173b8c56e2c9a6ae/internal/core/storage/factory_impl.go#L137-L197)
can initialize both authentication and database metadata schemas during startup.

## Decision

Retain automatic initialization and serialize its DDL with one transaction-level
advisory lock per physical PostgreSQL database. The fixed key
`0x53594e5452495801` is shared across initializers and releases; it does not depend
on DSN text or `search_path`, which can differ while resolving to the same tables.

| Entry point | Atomic DDL unit |
|---|---|
| Authentication `EnsureSchema(ctx, db)` | User table and indexes |
| Database metadata `EnsureSchema(ctx, db)` | Database table and indexes |
| User store `EnsureIndexes(ctx)` | User indexes |

The shared `schema.Ensure` helper uses this sequence on one transaction and
connection:

```text
BeginTx(ctx, READ COMMITTED)
    -> SELECT pg_advisory_xact_lock(key)
    -> execute existing DDL in a separate call
    -> Commit
```

Explicit `READ COMMITTED` prevents connection defaults from selecting a stale
transaction snapshot. Lock acquisition and DDL are separate commands so that DDL
started after a lock wait observes the preceding initializer's commit.
PostgreSQL releases the lock when the transaction commits or rolls back.

The Factory propagates its caller's context through `PingContext`, authentication
initialization, and database metadata initialization. The standard CLI startup
shares its existing 10-second initialization deadline across these operations;
the helper adds no separate default timeout. Each entry point commits its own DDL
unit, and the Factory succeeds only after all required initialization succeeds.
Errors retain their cause, including cancellation and rollback errors.

The [PostgreSQL storage design](../../../../docs/design/server/core/storage/06.user-store-postgres.md#7-schema-initialization)
owns the startup contract. PostgreSQL documents
[transaction-level advisory lock release](https://www.postgresql.org/docs/18/explicit-locking.html#ADVISORY-LOCKS)
and [READ COMMITTED command snapshots](https://www.postgresql.org/docs/18/transaction-iso.html#XACT-READ-COMMITTED).

## Alternatives

**Isolate every integration suite in its own PostgreSQL schema.** Removes the
test collision but does not protect concurrent production service startup.

**Retry or ignore duplicate catalog errors.** An initializer can observe partially
completed DDL; suppressing the error does not establish schema readiness.

**Require a separate migration owner.** Centralizes DDL ownership, but introduces
a deployment prerequisite for the existing automatic initialization path. The
shared transaction lock coordinates the current entry points without a migration
framework or configuration change.

## Consequences

- Cold-start and repeated initialization use the same coordination protocol;
  participating instances do not require a serialized CI preparation step.
- Initialization in different schemas of one physical database also serializes.
  This costs startup concurrency while avoiding ambiguous DSN or `search_path`
  lock identities; ordinary CRUD does not acquire the advisory lock.
- Waiting consumes the caller's initialization budget. Cancellation and DDL
  failure remain observable, roll back that entry point, and release its lock.
- Earlier successful entry points can remain committed if a later one fails.
  The failed Factory does not report successful initialization; a subsequent
  attempt can safely repeat the committed setup.
- Table and index definitions, CRUD behavior, and schema resolution are
  unchanged. There are no new dependencies, configuration options, or data
  migrations.
- Advisory locking is cooperative. Older binaries and external DDL that do not
  acquire this key remain outside the protocol; mixed-version cold starts still
  require coordinated initialization.
