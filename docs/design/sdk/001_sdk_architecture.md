# TypeScript Client SDK Architecture

**Date:** December 27, 2025
**Status:** Remote clients, a private replication runtime, and private local alias storage are implemented; the public local database API remains planned.

**Related:** [003_authentication.md](003_authentication.md) defines the shared auth surface used by HTTP clients, replication, and realtime. Client specifics: [004_syntrix_client.md](004_syntrix_client.md), [005_trigger_client.md](005_trigger_client.md).

**Usage examples:** see [004_syntrix_client.md](004_syntrix_client.md#usage-examples) and [005_trigger_client.md](005_trigger_client.md#usage-examples).

## 1. Overview

The Syntrix TypeScript SDK follows a "Semantic Separation, Shared Abstraction" philosophy: two distinct clients (external vs. trigger) share a fluent reference API while isolating transport, auth, and capabilities.

## 2. Core Design Principles

### 2.1 Semantic Separation

- **SyntrixClient (Standard)** — Target: external applications (Web, Mobile, Backend); Auth: user/long-lived tokens, database-aware; Transport: REST `/api/v1/...`; Semantics: HTTP-style (404 -> null).
- **TriggerClient (Trigger)** — Target: trigger workers (serverless/container); Auth: ephemeral `preIssuedToken` scoped to the trigger event; Transport: Trigger RPC `/api/v1/trigger/...`; Capabilities: privileged atomic `batch()`.

### 2.2 Interface-Based Polymorphism

```typescript
/** @internal */
export interface StorageClient {
  get<T>(path: string): Promise<T | null>;
  create<T>(collection: string, data: any, id?: string): Promise<T>;
  update<T>(path: string, data: any): Promise<T>;
  replace<T>(path: string, data: any): Promise<T>;
  delete(path: string): Promise<void>;
  query<T>(query: Query): Promise<T[]>;
}
```

Both clients implement `StorageClient`, enabling the Reference API to stay transport-agnostic.

### 2.3 DX-First Fluent API

- CollectionReference: `client.collection('users')`
- DocumentReference: `client.doc('users/alice')`
- QueryBuilder: `client.collection('posts').where('status', '==', 'published').orderBy('date')`

### 2.4 Internal Encapsulation

Internal details live under `src/internal` and are marked `/** @internal */`, keeping the public surface minimal.

### 2.5 Realtime Auth & Transports (WS + SSE)

- Why: Realtime must honor the same database-aware auth as REST; browsers may need SSE in constrained environments.
- How: WS sends Bearer on HTTP upgrade and a frame-level `auth`; on auth errors, a single token refresh is attempted then surfaced. SSE uses Authorization header only, rejects query tokens, and applies the same database scoping.

## 3. Architecture

```mermaid
graph TD
    UserApp[External App] --> SyntrixClient
    TriggerWorker[Trigger Worker] --> TriggerHandler --> TriggerClient

    subgraph Public API
        SyntrixClient
        TriggerClient
        Ref[Reference API (doc, collection)]
    end

    subgraph Internal Implementation
        SyntrixClient -- implements --> StorageClient
        TriggerClient -- implements --> StorageClient
        Ref -- depends on --> StorageClient
    end

    SyntrixClient -- HTTP REST --> Server[/api/v1/...]
    TriggerClient -- Trigger RPC --> Server[/api/v1/trigger/...]
    SyntrixClient -- Realtime WS/SSE --> Server[/realtime/ws|sse]
```

## 4. Implementation Details

### 4.1 Directory Structure

```text
pkg/syntrix-client-ts/src/
├── api/                # Reference layer
├── clients/            # SyntrixClient, TriggerClient
├── internal/           # StorageClient contract & helpers
├── replication/        # Realtime + replication helpers (WS/SSE)
├── trigger/            # TriggerHandler helper
├── types.ts            # Shared types
└── index.ts            # Public exports
```

### 4.2 Auth

- AuthConfig carries `database`; login accepts `database` and derives `/auth/v1/login`.
- Token refresh serialized; hooks for refresh/error callbacks.
- Realtime WS retries auth once after refresh; SSE relies on header-only auth.

## 5. Replication (Overview)

`SyntrixClient.pull` provides one authenticated manual page through the dedicated
replication HTTP route and shared session handling. It decodes typed values and
leaves local state/checkpoint transactions to the application.

The private local runtime uses a pinned, patched RxDB protocol with bounded
durable scans, checkpoint completion hooks, and cancellation that drains owned
work. Its bundled dependencies load lazily and are absent from the remote API's
initial dependency graph. This foundation does not expose a local database API
or connect automatic synchronization to the HTTP routes. Private alias storage
provides account-scoped Dexie persistence, lossless typed values, raw CAS CRUD,
source/physical generation records, and clean compaction. Its invalidation feed
and request guards are integration points; query-source application, local
query/watch, and automatic Push remain planned. See
[002_replication_client.md](002_replication_client.md).

## 6. Primary Test Coverage (Planned/Implemented)

- SyntrixClient: 401/403 single refresh + retry; 404 -> null; create with/without id; query shape.
- TriggerClient: reject create without id; batch forwards writes; get returns null on empty; missing token fails fast.
- Auth layer: serialized refresh under concurrent 401s; hooks fire correctly; realtime auth failure retries once then surfaces.
- Realtime: WS auth ack gates resubscribe; SSE delivers events/snapshots with header auth; inactivity triggers reconnect.
- Manual Pull: typed page validation, request routing, cancellation, and session replacement.
- Private runtime and storage: bounded scans, durable page/checkpoint ordering, identity and lifecycle fences, raw CAS CRUD, size admission, and compaction recovery. Public local replication and browser-to-server end-to-end coverage remain planned.

More error corners and perf cases will be added as features land.
