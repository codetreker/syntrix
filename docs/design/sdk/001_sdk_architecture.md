# TypeScript Client SDK Architecture

**Status:** Remote clients and public replica APIs are implemented; private WS data pages with automatic HTTP fallback carry downstream replication, while HTTP Push carries upstream writes.

**Related:** [003_authentication.md](003_authentication.md) defines the shared auth surface used by HTTP clients, replication, and realtime. Client specifics: [004_syntrix_client.md](004_syntrix_client.md), [005_trigger_client.md](005_trigger_client.md).

**Usage examples:** see [004_syntrix_client.md](004_syntrix_client.md#usage-examples) and [005_trigger_client.md](005_trigger_client.md#usage-examples).

## 1. Overview

The SDK serves application data access within a Syntrix runtime instance. Two
clients, standard application and trigger, share a fluent reference API while
keeping transport, authentication, and capabilities distinct.

The [system architecture](../../architecture.md) separates employee-facing
Management, developer-facing Console, and end-user-facing runtime instances.
The target [Identity module](../server/core/identity/01.architecture.md) is a peer
to Indexer and Puller inside each instance. It manages project-isolated end
users and both OAuth/OIDC roles using instance-local PostgreSQL system data.
Each project can use multiple logical Syntrix databases backed by MongoDB.

This SDK does not administer Console developers or Management employees.
Current authentication is username/password JSON login or injected credentials;
project selection and complete OAuth/OIDC flows are not yet SDK contracts.
Existing token/session and replica database bindings must remain explicit when
the project identity model is introduced.

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

### 2.5 Replica transport and SSE

Private replica WS sends token, database, and mode in its auth frame, retaining
source authorization and identity binding. Ordinary SSE uses the Authorization
header. Both retain session isolation. Public raw WS subscriptions have been
removed; applications consume applied replica results through local watch.

## 3. Architecture

```text
Application -> SyntrixClient -> REST document/query/manual Pull
            -> openReplica  -> Local CRUD/query/watch
                                +-> Private source -> WS typed page / HTTP fallback
                                +-> HTTP Push
            -> realtimeSSE  -> Ordinary SSE events

Trigger worker -> TriggerClient -> Trigger RPC
```

Replicas have one downstream applier. Transport selection does not change local
membership, pins, conflicts, or checkpoints. Ordinary SSE does not write into
replicas or own replica connections.

## 4. Implementation Details

### 4.1 Component responsibilities

| Component | Responsibility |
|---|---|
| Public references | Explicit REST/local replica separation; no native RxDB objects exposed |
| Authentication provider | Session ownership, credential refresh, and obsolete-request isolation |
| Replica database handle | Local aliases, queries, election, private transport, and shutdown ordering |
| Source transport | Shared private WS used by active leaders; bounded HTTP fallback when unavailable |
| Native replication and storage | Whole-page application, member activation, metadata/checkpoint, and recovery |

### 4.2 Auth

- Client configuration carries the target `database` for data requests. Current
  login/signup send only username and password; that selector does not create
  a project-bound login or restrict the issued token to one database.
- Token refresh is serialized; refresh/error hooks retain session ownership.
- Private WS refreshes current authentication at most once on `UNAUTHORIZED`;
  SSE retains header-only authentication.

## 5. Replication (Overview)

`SyntrixClient.pull` provides one authenticated manual page through the dedicated
replication HTTP route and shared session handling. It decodes typed values and
leaves local state/checkpoint transactions to the application.

The private replication runtime uses a pinned, patched RxDB protocol with bounded
durable scans, checkpoint completion hooks, and cancellation that drains owned
work. Its bundled dependencies load lazily and are absent from the remote API's
initial dependency graph. The public `openReplica` facade composes private alias storage, which
provides account-scoped Dexie persistence, lossless typed values, raw CAS CRUD,
source/physical generation records, and clean compaction. Private queries use
bounded storage projections, exact scalar semantics, shared AVL candidates and
dynamic watch with manifest reconciliation. The same source adapter carries
private downstream WS pages or HTTP fallback, preserving generation activation,
pins, periodic reconciliation, and alias leadership. Private upstream sends typed HTTP Push, retains
native successful acknowledgements, and persists bounded phase/recovery state.
Uncertain results pause automatic synchronization while local CRUD remains
available; explicit recovery is guarded by the original database identity and
current edit token. Public replica references use local state; REST references
retain direct remote behavior. Periodic and change-triggered rounds use the
current transport; HTTP handles WS unavailability. Whole-page persistence
controls switching and ACK. See
[002_replication_client.md](002_replication_client.md).

## 6. Primary Test Coverage (Planned/Implemented)

- SyntrixClient: 401/403 single refresh + retry; 404 -> null; create with/without id; query shape.
- TriggerClient: reject create without id; batch forwards writes; get returns null on empty; missing token fails fast.
- Auth layer: serialized refresh under concurrent 401s; hooks fire correctly; realtime auth failure retries once then surfaces.
- Replica transport: WS auth/registration correlation, bounded reads, HTTP fallback, whole-page ACK, source leases, and late messages; SSE retains separate header authentication.
- Manual Pull: typed page validation, request routing, cancellation, and session replacement.
- Runtime and storage: bounded scans, durable page/checkpoint ordering, identity and lifecycle fences, raw CAS CRUD, size admission, and compaction recovery.
- Private queries: exact filtering/order/cursors, window refill, generation and metadata invalidations, shared handle lifecycle, and continuous resource admission.
- Private downstream: WS/HTTP share typed validation, member projection, pins, leadership handoff, and late-response cancellation.
- Private upstream: typed request limits, conflict-driven CAS, whole-phase failure classification, durable recovery intent and paused local access.
- Public replica integration: immutable source definitions, offline open, typed references, status/recovery projection, safe alias removal, lifecycle fencing and lazy package exports.
