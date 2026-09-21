# TypeScript Client SDK Architecture

**Date:** December 27, 2025
**Status:** 远程客户端与公开 replica API 已实现；副本下行使用私有 WS 数据页并自动 HTTP fallback，上行沿用 HTTP Push。

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

### 2.5 复制运输与 SSE

私有 replica WS 以 auth 帧传递 token、database 和模式，沿用复制源授权与身份绑定。
普通 SSE 继续使用 Authorization header；两者保留同一会话隔离规则。公开原始 WS
订阅入口已移除，应用通过本地 watch 消费副本结果。

## 3. Architecture

```text
应用 -> SyntrixClient -> REST document/query/manual Pull
     -> openReplica  -> 本地 CRUD/query/watch
                         +-> 私有 source transport -> WS typed page / HTTP fallback
                         +-> 既有 HTTP Push
     -> realtimeSSE  -> 普通 SSE 事件

Trigger worker -> TriggerClient -> Trigger RPC
```

副本只有一套下行应用器；运输选择不改变本地成员、pin、冲突或 checkpoint。
普通 SSE 不自动写入副本，也不取得 replica 连接的所有权。

## 4. Implementation Details

### 4.1 组件责任

| 组件 | 责任 |
|---|---|
| 公开引用 | REST 与本地副本显式分离，应用不接触原生 RxDB 对象 |
| 认证 provider | 会话归属、凭据刷新与旧请求隔离 |
| Replica database 句柄 | 本地别名、查询、election、私有运输及关闭顺序 |
| 源运输 | 一个句柄共享私有 WS，活动 leader 使用；不可用时有限 HTTP fallback |
| 原生复制与存储 | 唯一的整页应用、成员激活、metadata/checkpoint 和恢复 |

### 4.2 Auth

- AuthConfig carries `database`; login accepts `database` and derives `/auth/v1/login`.
- Token refresh serialized; hooks for refresh/error callbacks.
- 私有 WS 对当前 auth 的 UNAUTHORIZED 最多 refresh 一次；SSE 保持 header-only auth。

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
dynamic watch with manifest reconciliation. 私有下行通过同一 source adapter 使用 WS 数据页
或 HTTP fallback，保留 generation 激活、pin、周期核对和 alias leader。Private upstream sends typed HTTP Push, retains
native successful acknowledgements, and persists bounded phase/recovery state.
Uncertain results pause automatic synchronization while local CRUD remains
available; explicit recovery is guarded by the original database identity and
current edit token. Public replica references use local state; REST references
retain direct remote behavior. 正常核对与变化触发轮次都走当前运输；WS 不可用才走 HTTP，
整页持久化边界控制切换与 ACK。See
[002_replication_client.md](002_replication_client.md).

## 6. Primary Test Coverage (Planned/Implemented)

- SyntrixClient: 401/403 single refresh + retry; 404 -> null; create with/without id; query shape.
- TriggerClient: reject create without id; batch forwards writes; get returns null on empty; missing token fails fast.
- Auth layer: serialized refresh under concurrent 401s; hooks fire correctly; realtime auth failure retries once then surfaces.
- Replica 运输：WS auth/注册关联、有限 read、HTTP fallback、整页 ACK、source 租约与迟到消息；SSE 保持独立 header 认证。
- Manual Pull: typed page validation, request routing, cancellation, and session replacement.
- Runtime and storage: bounded scans, durable page/checkpoint ordering, identity and lifecycle fences, raw CAS CRUD, size admission, and compaction recovery.
- Private queries: exact filtering/order/cursors, window refill, generation and metadata invalidations, shared handle lifecycle, and continuous resource admission.
- 私有下行：WS/HTTP 共用 typed 校验、成员投影、pin、leader 接续及迟到响应取消。
- Private upstream: typed request limits, conflict-driven CAS, whole-phase failure classification, durable recovery intent and paused local access.
- Public replica integration: immutable source definitions, offline open, typed references, status/recovery projection, safe alias removal, lifecycle fencing and lazy package exports.

More error corners and perf cases will be added as features land.
