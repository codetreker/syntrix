# Realtime Watching Mechanism Discussion

**Date:** December 15, 2025
**Topic:** Realtime Push Architecture and Change Stream Processing

## Scope & Assumptions

- Single gateway, single port; realtime endpoints: `/realtime/ws` (WebSocket) and `/realtime/sse` (SSE).
- Watch expressions: reuse the query/filter language from 003_query (CEL subset) as the watch matcher to keep semantics and safety consistent.
- Scale targets: aim for up to ~1M concurrent connections and ~10k events/s; initial phase at ~1/10 scale (~100k connections, ~1k events/s) with linear scaling headroom.
- Broadcast policy: small-scope broadcast is acceptable as a fallback; prefer directed routing first.
- Client protocol: primarily WebSocket with brief-disconnect resume; SSE is supported for server-to-client streaming with same auth/semantics.

## 1) Change Stream Processor (CSP) Internals

- Ingestion (Why: throughput and stability; How):
  - Subscribe to MongoDB change streams per collection; CSP nodes consume partitions by (collection, hash(documentKey)) to avoid single hot spots.
  - Pull in batches with rate limits (events/s, bytes/s) and server heartbeat checks.
- Resume & durability (Why: lossless recovery; How):
  - Persist resume token per partition (etcd/embedded KV/DB) with periodic checkpoints plus on-commit writes.
  - Recover per partition on restart/rebalance; if the gap exceeds retention, trigger a full rescan or ask clients to resync.
- Dedup (Why: avoid duplicate pushes; How):
  - Rely on resume token monotonicity; across partitions or replay windows use a short-TTL cache keyed by (clusterTime, txnId, documentKey).
- Bloom/index hygiene defaults (Why: predictable FP rate; How):
  - Initial Bloom target FP rate 0.5–1% per collection with bits/key ~10 and k ~7; rebuild every 24h or when FP sampling >2% to keep memory bounded and drift low.
- Ordering (Why: predictable client ordering; How):
  - Keep event order within a partition and attach a per-partition increasing seq downstream.
- Gap handling (Why: avoid silent data loss; How):
  - Track per-partition lag; if resume token gap > retention window, mark the partition as “stale” and refuse delivery until a catch-up scan or client resync occurs.
  - When stale, surface an operator alert plus a client-visible `resync-required` status so downstream can reconcile.

Example CSP loop (Go-style pseudocode):

```go
for evt := range stream {
    part := partitionKey(evt.Collection, evt.DocumentKey)
    if !shouldProcess(part) { continue }

    if seen := dedupCache.Has(part, evt.ClusterTime, evt.TxnID, evt.DocumentKey); seen {
        continue
    }
    dedupCache.Add(part, evt.ClusterTime, evt.TxnID, evt.DocumentKey)

    seq := seqGen.Next(part)
    checkpointAsync(part, evt.ResumeToken)

    candidates := matchIndex.Lookup(part, evt) // fast path + Bloom
    for _, sub := range candidates {
        if matchExact(sub.Expr, evt) {
            enqueue(sub.Gateway, Delivery{
                SubscriptionID: sub.ID,
                Seq:            seq,
                Partition:      part,
                LSN:            evt.LSN,
                Payload:        evt.Doc,
            })
        }
    }
}
```

## 2) Query Matching Pipeline

- Matcher language: reuse the expression engine from 003_query (CEL subset) supporting field filters, ranges, and auth context.
- Pipeline (Why: reduce per-event compute; How):
  1) Fast path: documentKey exact subscriptions (primary-key watches).
  2) Probabilistic filter: per-collection field hashes into Bloom/segmented Bloom; tunable parameters (initial phase uses conservative bits/k with headroom).
  3) Exact match: evaluate survivors with the expression engine.
- Subscription index (Why: reduce broadcast; How):
  - Gateway registers active subscription summaries (field filters, database, subscription id) to CSP; CSP maintains per-collection indexes with periodic refresh/expiry.
  - Bloom false positives are filtered by the Gateway (small-scope broadcast remains an acceptable fallback).
- Index hygiene (Why: keep match quality stable; How):
  - Enforce TTL on inactive summaries; rebuild Blooms periodically to limit false-positive drift; size budgets per collection to prevent single-database blowup.

Example of building a subscription index entry:

```go
type SubscriptionSummary struct {
    ID        string
    Database    string
    Gateway   string
    Expr      cel.Ast // parsed from watch expression
    BloomKeys []uint64
}

func buildSummary(sub SubscribeRequest) SubscriptionSummary {
    keys := bloomKeysFromExpr(sub.Expr, bloomConfig)
    return SubscriptionSummary{
        ID:        sub.ID,
        Database:    sub.Database,
        Gateway:   sub.Gateway,
        Expr:      compileToCEL(sub.Expr),
        BloomKeys: keys,
    }
}
```

## 3) Routing & Distribution

- Topology (Why: horizontal scale; How):
  - CSP and Gateway clusters both use a consistent-hash ring; events route by (collection + docKey hash) to the responsible Gateway by default.
  - If the routing key is uncertain (e.g., range queries), CSP can broadcast to a small candidate set of Gateways; Gateways then filter locally.
- Membership (Why: elasticity and migration; How):
  - Control plane maintains heartbeats and leases; ring changes trigger smooth partition migration (drain plus resume token handoff).
- Rebalance semantics (Why: avoid gaps/dupes during movement; How):
  - Donor drains the partition queue, checkpoints resume token, transfers the last committed token to the recipient, and enters a brief “handoff broadcast” mode (up to 1s) so no events are lost; recipient starts from the token and exits broadcast once caught up.
- Backpressure (Why: prevent amplification; How):
  - Gateways enforce queue watermarks and rate limits; on overflow they fall back to conservative broadcast or request client reconnect/slowdown.
- Backpressure thresholds (Why: predictable degradation; How):
  - Default per-Gateway watermark at 70% soft, 90% hard. Soft triggers shed optional load (low-priority subscriptions); hard triggers switch the affected partition to “broadcast + client resync required” until drained below 60%.
- Degradation (Why: preserve correctness under stress; How):
  - If Gateway or CSP lag exceeds threshold, switch that partition to “broadcast + client resync required” mode until queues drain; emit alerts and shed optional load (non-critical subs) before dropping any events.
  - Broadcast trigger (Why: bound fan-out choices; How): if match index lookup latency p99 > 50ms or FP rate sampling >5% for a partition, temporarily expand candidate Gateways to a fixed fan-out (e.g., 3) and reevaluate after 1m.

## 4) Client Protocol (WebSocket / SSE)

- Endpoints: `/realtime/ws` (bidi WebSocket), `/realtime/sse` (server-to-client SSE).
- Connection: authenticate then establish stream; client declares database/project context.
- Subscription management: client submits watch expressions; server returns subscription ids and current matcher version.
- Delivery & sequencing: each event includes (subscription ids, seq, partition id, lsn). Gateway preserves per-partition order.
- Brief-disconnect resume: client keeps last seq/lsn; on reconnect (WS) it sends a resume token; server replays from a sliding window (in-memory/short TTL). Outside the window, client performs resync. SSE clients reconnect with last known cursor if supported.
- Ack: optional client ack (WS) supports flow control and window trimming on the Gateway.
- Sliding window (Why: bounded memory with useful coverage; How):
  - Default window: time-based 3 minutes or size-based last 10k events per partition, whichever is smaller; cap memory at ~32 MB per partition group with LRU eviction; clients exceeding the window get a `resync-required` status.
- Message schema (Why: interoperability and evolvability; How):
  - Include protocol `version`, `database`, `subIds`, `seq`, `partition`, `lsn`, `payload`, optional `checksum`; reserve an `extensions` map for forward-compatible fields.

Client brief-disconnect resume (protocol sketch):

```json

// Client -> Gateway (reconnect)
{ "type": "resume", "token": "<resume-token>", "lastSeq": 12345 }

// Gateway -> Client
{ "type": "resume-ack", "status": "ok", "fromSeq": 12346 }

// Gateway replays from sliding window [12346, latest], else asks for full resync
```

### 4.1. Realtime Protocol (WebSocket/SSE)

Endpoints (single gateway, single port):

- WebSocket: `ws://host/realtime/ws`
- SSE: `http://host/realtime/sse`

### 4.2 Message Structure

All messages follow a standard JSON envelope:

```json
{
  "id": "request-id",
  "type": "message-type",
  "payload": { ... }
}
```

### 4.3 Authentication

**Client -> Server:**

```json
{
  "id": "1",
  "type": "auth",
  "payload": {
    "token": "jwt-token-here"
  }
}
```

**Server -> Client:**

```json
{
  "id": "1",
  "type": "auth_ack",
  "payload": {
    "status": "ok"
  }
}
```

### 4.4 Live Query (Subscription)

**Client -> Server (Subscribe):**

```json
{
  "id": "sub-1",
  "type": "subscribe",
  "payload": {
    "query": {
      "collection": "rooms/room-1/messages",
      "filters": [{"field": "timestamp", "op": ">", "value": 1678888888000}]
    }
  }
}
```

**Server -> Client (Event):**

```json
{
  "type": "event",
  "payload": {
    "subId": "sub-1",
    "delta": {
      "op": "insert",
      "doc": { "path": "rooms/room-1/messages/m3", "data": {...}, "version": 1 }
    }
  }
}
```

### 4.5 Replica WebSocket Data

当前服务端在 `/realtime/ws?mode=replica-data` 提供独立复制数据模式；普通 WS/SSE
保留既有协议。这里不使用上面的 seq/滑动窗口草图，也不实现另一套源 checkpoint。
精确消息与容量由[复制参考](../../../reference/replication.md#replica-websocket-data)维护。

```text
Streamer collection 变化 -> dirty / 200ms 合并 -> replica_changed
                                                    |
客户端新轮次 -> replica_read -> Gateway 授权 -> Query.Pull
                                                    |
               原有 typed 源页 <- replica_page <-----+
                       |
          完成整页本地应用 -> replica_ack -> 下一页 / idle
```

| 边界 | 契约与原因 |
|---|---|
| 数据来源 | Gateway 直接调用现有 Query.Pull，本地与 gRPC Query 共用源、typed 编码、窗口与预算；不把普通事件 map 当复制数据 |
| 变化注册 | 覆盖完整 concrete collection，忽略源 filter/order/limit，使 leave、删除和窗口边界变化都能唤醒读取 |
| 轮次 | 客户端每次 read 只取一页；没有 read 不持续推页，不缓存旧页充当新的源完成证明 |
| 身份 | 复用 HTTP 的权威数据库与 owner/db_admin 判断；Origin、token、原 namespace、固定数据库 ID 和认证代都参与准入 |
| 初始绑定 | subscribe ACK 仅确认当前代注册；客户端绑定必须来自经过共同 scope 校验的数据页 |
| 关联 | subscribe 外层 id 即 subId；read/page/ACK 使用同一外层 requestId。expected identity/hash 位于运输 payload，原 Pull request JSON 保持原样 |
| 完整性 | caughtUp 仍来自源水位；窗口只发送完整 replace；ACK、heartbeat 和空数组不补造这些结论 |
| 流控 | 单订阅一个未 ACK 页；页确认不持久化，也不是 checkpoint。客户端完成本地整页持久化后才 ACK |
| 资源 | Query/编码 worker、帧 buffer、未确认 credit 各按实际结束点归还；旧 Stream 退休不清零仍被占用的额度 |
| 错误 | 源容量/授权退避与运输不可用分开，不能通过立即切 HTTP 绕过 REPLICATION_SOURCE_BUSY |

Gateway 的连接、注册、源字节、读取、页面字节及授权工作使用有界账本；配置归属
`gateway.realtime.replica`，不扩大普通 WS/SSE 的交付保证。每次 read 和 changed
发送前重新权威检查数据库；排队及写出前拒绝旧 owner、已取消订阅和过期认证。

同一 Stream 对象内部重连保留普通订阅恢复；replica owner 在断开或 generation 变化时
失效并重新建立。实际 Stream 对象终止或替换时，关闭依赖旧对象的普通 WS、SSE 和
replica 连接，清除映射，再有界重建 backend；心跳不代表源注册仍有效。

变化提示不能覆盖索引滞后与通知遗漏，客户端仍需周期性源核对。当前公开 SDK 仍使用
HTTP replica adapter；本次只交付服务端通道。选择及代价见
[WebSocket 复制数据决定](../../../../.agents/notes/implemented/feature/2026-09-21-replica-websocket-data.md)。

### 4.6 Unsubscription

**Client -> Server:**

```json
{
  "id": "req-2",
  "type": "unsubscribe",
  "payload": {
    "id": "sub-1" // The ID of the subscription or stream to cancel
  }
}
```

**Server -> Client (Unsubscribe Ack):**

```json
{
  "id": "req-2",
  "type": "unsubscribe_ack",
  "payload": {
    "status": "ok"
  }
}
```

### 4.7 SSE Notes

- SSE shares the same auth and subscription semantics as WebSocket but is server-to-client only.
- Events are sent as text/event-stream; payload envelopes mirror the WebSocket `event` messages.
- Heartbeats via SSE comments; clients should reconnect with the last known resume token/seq if supported.

## 5) Reliability & Observability

- Reliability: end-to-end at-least-once from CSP to Gateway to clients; dedupe via seq + subscription id; heartbeat/keepalive to detect dead links.
- Observability:
  - Metrics: per-partition lag, events/s, match hit rate, Bloom false-positive rate, broadcast ratio, queue watermarks, reconnect/resume success.
  - Audit: subscription create/destroy, routing changes, broadcast reasons, resume failures.
  - Sampled logs: slow matchers (expression latency), large subscription sets, repeated replays.
- SLO hooks (Why: actionable signals; How):
  - Define target p99 delivery latency per partition and max acceptable Bloom FP rate; alerts tie directly to degradation policy in section 3.

## 6) Scalability Plan

- Compute: CSP and Gateway both scale horizontally; partitions >= node count for rebalancing; Bloom/index parameters adjust with growth.
- Data: subscription indexes and Blooms can be sharded by collection to avoid single-node memory blowup.
- Migration: when nodes change, resume tokens and subscription indexes move with partitions for seamless recovery.

## 7) Shadow Validation / Replay (Note)

- Reminder: matcher/routing changes should support shadow verification or offline replay of recorded change streams to validate correctness without impacting production traffic (details to be added later).
- Shadow plan (Why: validate safely; How):
  - Capture sampled change streams (sanitized) to object storage; replay against a shadow CSP+Gateway deployment running new matcher/routing code; compare match sets and latency, block rollout if divergence exceeds threshold.

## 8) Usage Examples

### 8.1 Realtime Subscription (WebSocket)

**Client sends:**

```json
{
  "type": "subscribe",
  "id": "sub-room-123",
  "query": {
    "collection": "rooms/room-123/messages",
    "filters": [{"field": "timestamp", "op": ">", "value": 1678888888000}]
  }
}
```

**Server responds (Snapshot):**

```json
{
  "id": "sub-room-123",
  "type": "snapshot",
  "payload": {
    "subId": "sub-room-123",
    "documents": [ ... ]
  }
}
```

For `sendSnapshot: true`, the Gateway drains opaque replication Pull pages through
`caughtUp`, applying each upsert and deletion by document ID. A page can end on
bytes or source work before reaching its document-count limit; sending that page
alone would leave an incomplete snapshot. Only the completed retained state is
sent in the existing snapshot envelope.

| Snapshot constraint | Behavior |
|---|---|
| Retained live documents | At most 1000; replayed updates replace the same ID and deletions remove it |
| Serialized snapshot payload | At most 16 MiB including `subId` and the document-array envelope |
| Collection deadline | 10 seconds across all Pull pages and snapshot encoding |
| Connection queue deadline | A separate 10 seconds for the snapshot or error |
| Connection closure | Wakes pending snapshot enqueue and discards late snapshot or error responses |
| Collection or encoding failure | Correlated `error` message with code `snapshot_failed`; no successful prefix |
| Document or byte limit exceeded | Correlated `error` message with code `snapshot_limit`; use paginated Pull for larger state |
| Snapshot error after subscription ACK | The subscription remains active; SDK error observers are notified and subsequent live events continue |

Snapshot enqueue and outbound channel closure share per-client synchronization.
Closure signals waiting senders before acquiring the closing lock, so a full
queue cannot make deregistration or Hub shutdown wait out the delivery deadline.
Queue timeout or connection closure can prevent delivery; it never converts an
incomplete result into a snapshot. Subscription acknowledgment does not imply
snapshot completion. This collection-wide state does not apply subscription
filters or establish an atomic handoff to live events; those guarantees remain in
the [filtered snapshot proposal](../../../../.agents/notes/proposed/bug-fix/2026-09-07-realtime-filtered-snapshots.md).

**Server pushes (Delta):**

```json
{
  "type": "event",
  "subId": "sub-room-123",
  "delta": {
    "op": "insert",
    "doc": { "id": "msg-999", "text": "New message!", ... }
  }
}
```
