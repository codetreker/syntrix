# Replication API Reference

HTTP 复制端点显式使用 database URL namespace；replica-data WS 从 auth.database
固定同一用途的 namespace。Pull 返回 typed 文档状态、带 opaque continuation 的
查询成员事件，或完整查询窗口。Push 接受 typed 文档并返回带 typed current 的冲突。

## Bound Database Identity

A client bound to a database incarnation sends the optional header
`X-Syntrix-Expected-Database-Identity: <database ID>` on Pull, Push, ordinary Query,
and document GET requests. The value is exactly one lowercase 16-digit hexadecimal
ID. An empty, repeated, or malformed header returns HTTP 400 `BAD_REQUEST`.

| Request | Database resolution and authorization |
|---|---|
| Any request carrying the header on these routes | Read the management store afresh; require an active database and its owner or matching `db_admin` grant before comparing identity |
| Query-source Pull without the header | Use the same authoritative resolution and full-scope authorization; return the real ID for initial binding |
| Existing requests without the header or a Pull source | Preserve their existing resolution and authorization behavior |

Status, permission, and identity use the same fresh database object. A cache hit
cannot replace this check, and management-store failure has no cache fallback.
Missing, suspended, and deleting databases retain their existing errors.
Unauthorized callers receive the permission error before identity comparison.
A different identity returns HTTP 409 `DATABASE_IDENTITY_MISMATCH` before any
document read, scan, Watch, or write. Push reports it as a request error, not a
per-document conflict. Bound ordinary Query and GET use this full-scope gate as
well; the header does not enable per-document-authorized replication reads.

The URL namespace still selects storage. This check does not rewrite a slug into
an ID address. It checks database identity when admitting the request; it does
not lock the database against concurrent deletion or slug reassignment for the
request's lifetime. Keep pending writes and uncertain earlier attempts after an
identity failure: rejecting this request says nothing about an earlier timed-out
Push.

All Gateway and Query nodes must support this contract before query-replication
protocol version 1 is enabled. Header presence alone cannot establish support
when an older node may ignore it.

## Pull Changes

**Endpoint:** `POST /replication/v1/databases/{database}/pull`

```json
{
  "collection": "users",
  "checkpoint": null,
  "limit": 100
}
```

| Field | Contract |
|---|---|
| `collection` | Required concrete collection path; no wildcard selection |
| `checkpoint` | Omit, null, or empty string to initialize; otherwise reuse the returned string verbatim |
| `limit` | Optional integer, default 100; 0 also selects default; valid range 0–1000 |

Unknown fields, duplicate keys, invalid Unicode, and extra JSON values are rejected.
The request is bounded to 1 MiB and the checkpoint to 256 KiB.

Pull currently requires the validated database owner or a matching `db_admin`
grant for its ID or validated slug. A global `admin`/`user` role or authentication
alone is insufficient. This full-scope permission profile is provisional pending
approval; per-document authorization filtering is not supported.

### Response and Typed Values

```json
{
  "documents": [
    {
      "type": "object",
      "value": {
        "id": {"type": "string", "value": "alice"},
        "collection": {"type": "string", "value": "users"},
        "name": {"type": "string", "value": "Alice"},
        "version": {"type": "int64", "value": "2"},
        "createdAt": {"type": "int64", "value": "1700000000000"},
        "updatedAt": {"type": "int64", "value": "1710000000000"}
      }
    }
  ],
  "checkpoint": "opaque-continuation",
  "caughtUp": false
}
```

Each document is one recursive typed value. Objects and arrays recursively contain
typed values; scalar tags are `null`, `bool`, `string`, `int64`, and `float64`.
An int64 is a decimal string; float64 is a finite JSON number. The TypeScript SDK
decodes every int64 to bigint, including metadata and nested business values.
Decoded documents are flattened business fields plus reserved metadata:

| Metadata | Meaning |
|---|---|
| `id`, `collection` | Required logical document identity |
| `version` | Server document version, not replication order |
| `createdAt`, `updatedAt` | Server timestamps in milliseconds |
| `deleted` | When true, remove the server document state locally |

Decoded bigint values cannot be blindly passed to `JSON.stringify` or sent back to SDK
document `set`/`update`, whose current JSON serialization rejects them before HTTP
transmission. Number conversion can lose precision. Local storage needs a lossless
representation. HTTP Push accepts the same document typed-value representation
inside its change envelope. The SDK's [private upstream adapter](../design/sdk/002_replication_client.md#private-upstream-and-recovery)
supplies the encoder, native acknowledgement and recovery path through the public
[replica database API](typescript_sdk.md#replica-availability). The complete Pull response is not a Push request,
and ordinary CRUD still uses its existing JSON format.

A logical-delete event may return only `id`, `collection`, and `deleted: true`;
version and timestamps are then absent. Do not require or manufacture them.
Stored tombstones clear former business fields. Physical cleanup does not emit
another deletion; see [deletion semantics](../design/server/core/storage/03.stores.md#document-deletion-and-physical-cleanup).

### Continuation and Local Application

1. Initialize with a null checkpoint. The server scans committed documents and
   then replays overlapping changes from the original scan boundary.
2. Apply returned documents and deletions in order. Duplicate and later states
   are allowed; pages do not form a fixed snapshot. Never discard a record only
   because its document version is lower than a previously seen version.
3. Commit the whole page and its checkpoint in one local transaction. A failed
   transaction keeps the previous checkpoint.
4. Continue with the returned checkpoint, including after an empty page with
   `caughtUp: false`. True means a source watermark was processed, not that no
   future write exists.
5. Keep state and checkpoint isolated by account, database URL namespace, and
   collection. The cursor also binds the resolved database identity. Changing
   between ID and slug does not preserve the same continuation.
6. On `RESYNC_REQUIRED`, rebuild the server mirror with a null checkpoint while
   preserving unsent local edits for reconciliation.

Timestamp checkpoints, including numeric strings and JSON numbers, return an
explicit resynchronization error. Other malformed or scope-mismatched cursors
fail validation. Resuming on another service instance is supported against the
same retained source; source replacement or history expiry requires recovery.

### Budgets

| Budget | Limit |
|---|---:|
| Returned documents | 1000 |
| Source bytes | 16 MiB |
| Encoded JSON response | 16 MiB including checkpoint and envelope |
| Encoded protobuf response | 20 MiB |
| Incremental source frames | 10,000 |
| Incremental soft processing interval | 5 seconds |
| Hard processing timeout, including response encoding | 30 seconds |
| HTTP socket write deadline | Processing deadline plus 10 seconds |

Source-byte accounting covers records visible to the adapter, not all database
work. A successful limited page retains the last fully accepted prefix; the next
request rereads any unreturned record. A single record exceeding the supported
budgets fails explicitly. Cancellation and source/encoding/cleanup errors fail
the request; retain the last saved checkpoint.

The soft interval permits a successful stop only after the source checkpoint
advances or a caught-up watermark is proved. Opening a Watch slowly or receiving
an empty frame with the same checkpoint does not count as progress. Exhausting
the frame or source-byte budget without progress returns retryable
`REPLICATION_UNAVAILABLE`; reaching the hard deadline returns `DEADLINE_EXCEEDED`.
The HTTP write deadline leaves time to transmit the encoded result or error after
processing ends. Other routes retain the configured server write timeout.

## Query-Source Pull

The same Pull endpoint accepts a `source` to synchronize an entire matching set
or a bounded result window. Omitting `source` preserves the collection Pull
request and response above. The
SDK's existing public manual Pull method still exposes only collection Pull.

```json
{
  "collection": "users",
  "source": {
    "version": 1,
    "filters": [
      {"field": "active", "op": "==", "value": {"type": "bool", "value": true}}
    ]
  },
  "checkpoint": null,
  "limit": 100
}
```

| Field | Contract |
|---|---|
| `source.version` | Required integer `1` |
| `source.filters` | Required array, possibly empty; AND conjunction with the existing [filter semantics](filters.md#fields-and-operators); operands require recursive typed nodes |
| `source.orderBy` | Optional array of `{field, direction}` with `asc` or `desc`; orders a result window and participates in source identity; matching-set events retain source order |
| Top-level `limit` | Matching-set transfer event limit, default 100, range 0–1000; zero selects the default; forbidden for windows |
| `checkpoint` | For matching sets, omit, null, or empty string to initialize; otherwise reuse the returned opaque string; forbidden for windows |
| `source.limit` | Result-window size, integer 1–1000; omission selects the entire matching set |
| `requestId` | Required nonempty string for a window and echoed exactly; forbidden for matching sets |

Unknown or duplicate structural fields, null `source`, unsupported versions,
invalid Unicode, malformed typed nodes, and invalid field combinations return
HTTP 400 `INVALID_REPLICATION_SOURCE`. Window requests reject the presence of
top-level `limit` or `checkpoint`, including zero, null, and empty values where
otherwise valid for matching sets. HTTP and gRPC preserve this presence rule.

### Events and Source Identity

```json
{
  "protocolVersion": 1,
  "mode": "events",
  "databaseIdentity": "0123456789abcdef",
  "sourceHash": "server-computed-source-hash",
  "events": [
    {"type": "leave", "id": "alice"},
    {"type": "delete", "id": "bob"}
  ],
  "checkpoint": "opaque-query-continuation",
  "generationId": "server-generated-generation",
  "phase": "replay",
  "caughtUp": false,
  "bootstrapComplete": false
}
```

All response envelope fields are required, including an empty `events` array and
false boolean values. `databaseIdentity` comes from the authoritative database
check. `sourceHash` binds protocol version, actual database identity, collection,
normalized typed filters, effective ordering, and result limit. Filter order and
other equivalent normalized predicates produce the same identity. Effective
ordering appends logical ID ascending when not explicitly ordered; absent ordering
is ID ascending for this identity. Clients retain the hash rather than deriving it.

| Source state | Event | Consumer meaning |
|---|---|---|
| Live document matching every predicate | `upsert` with `document` in the existing recursive typed object format | Apply the current state and include its ID in this source |
| Live document failing a predicate during replay/live | `leave` with `id` | Remove this source's membership, even if that ID was never observed locally |
| Logical deletion | `delete` with `id` | Apply deletion without inventing a version, timestamps, or payload |
| Filtered scan candidate or progress-only frame | No event | Persist the returned checkpoint even when events are empty |

Delete events currently emit no `observedMetadata`. A leave is not a deletion of
the stored document or of its membership in another source. Full-scope owner or
`db_admin` authorization is required because stateless leaves may reveal IDs that
never matched. Query filters do not confer permissions.

### Generations and Recovery

```text
new generation -> scan -> replay -> live
                     original C0 ----^
```

| Phase | Completion contract |
|---|---|
| `scan` | Scan committed candidates in logical ID order; `caughtUp` and `bootstrapComplete` are false |
| `replay` | Replay from the scan's original C0; neither a short nor empty page proves completion |
| `live` | Enter only after Watch proves caught-up progress; `bootstrapComplete` remains true for the generation, while `caughtUp` describes the current page |

The opaque version-4 cursor binds source hash, generation, phase, database scope,
and existing Store progress. It can resume on another service instance. Changing
the source or database scope fails validation; old collection-mode cursors cannot
be used for query mode. Expired source history returns `RESYNC_REQUIRED`; an
explicit new initialization creates a new generation. Consumers activate rebuilt
membership only after the completed generation and its checkpoint are durable,
retaining unsent local edits throughout recovery.

The moving scan overlaps replay and is not a fixed historical snapshot. Current
state enrichment may yield a recreated document, then a historical deletion, then
the recreated document again. Apply source order and permit temporary regression;
maximum document version cannot establish delete/recreate order.

All existing Pull budgets apply to matching-set mode, including every scanned candidate,
filtered-out work, source bytes, Watch frames, and encoded envelope. The transfer
limit counts events; rejected scan candidates still consume the bounded source
page. An empty events page may therefore advance without completing bootstrap.
A cursor never advances beyond an event that did not fit the response. These
adapter-visible budgets do not promise a bound on all internal database work.

### Complete Result Windows

```json
{
  "collection": "users",
  "source": {
    "version": 1,
    "filters": [],
    "orderBy": [{"field": "score", "direction": "desc"}],
    "limit": 2
  },
  "requestId": "refresh-7"
}
```

A window runs one ordinary Query with the normalized filters, effective ordering,
and result limit. It starts without a Query continuation. Absent ordering means
explicit logical ID ascending; explicit ordering appends ID ascending unless ID
is already ordered. Execution uses that same ordering as `sourceHash`. The normal
index requirements apply, including to default ID ordering.

```json
{
  "protocolVersion": 1,
  "mode": "replace",
  "databaseIdentity": "0123456789abcdef",
  "sourceHash": "server-computed-source-hash",
  "requestId": "refresh-7",
  "generationId": "new-window-generation",
  "complete": true,
  "effectiveOrder": [
    {"field": "score", "direction": "desc"},
    {"field": "id", "direction": "asc"}
  ],
  "documents": []
}
```

All nine fields are required. Documents use the existing recursive typed object
format; an empty array is a complete empty result. Every successful request gets
a new generation. The response has no events, checkpoint, phase, caughtUp, or
bootstrapComplete fields. The client uses request/session identity to reject old
responses; generation IDs are not sortable source positions.

| Single Query result for requested N | Replication result |
|---|---|
| Exactly N documents, with or without continuation | Complete replacement |
| Fewer than N documents and no continuation | Complete exhausted replacement |
| Fewer than N documents with continuation | HTTP 503 `REPLICATION_WINDOW_INCOMPLETE`; no replacement |
| Query, encoding, or budget failure | Error; no partial replacement |

Independent Query pages are never concatenated into a purported snapshot. A
window must fit both the full 16 MiB JSON envelope and 20 MiB protobuf envelope,
including request ID, generation, ordering, and typed document overhead. Query
work limits still apply. Envelope limits fail explicitly rather than truncating
members; retain the prior active window on any failure.

Windows use ordinary Query consistency. Index lag can temporarily omit an existing
member that still matches, followed by reentry after indexing catches up; it does
not merely delay new members. A complete response certifies the single Query's
bounded result, not a strict source snapshot or index freshness fence. A later
complete refresh handles rank displacement and replacement members. Membership
exit is not a stored-document deletion.

The authoritative database identity and owner/`db_admin` gate run before window
Query execution, including first binding. Identity failure must not be interpreted
as an empty replacement. 公开 SDK 已通过 HTTP 实现窗口 adapter、成员替换和定时刷新；
新增 WS 服务端数据通道尚未自动接入 SDK。通知不能替代周期性核对。

The [query-source decision](../../.agents/notes/implemented/feature/2026-09-18-query-replication-source.md)
records source projection, identity checking, and their guarantees.

## Replica WebSocket Data

**端点：** `/realtime/ws?mode=replica-data`。这是已实现的服务端数据通道；
公开 TypeScript replica SDK 当前仍通过 HTTP 同步，尚未自动使用该模式。

| 模式 | 当前用途 |
|---|---|
| 普通 `/realtime/ws` 与 `/realtime/sse` | 保留普通 realtime 订阅与事件协议 |
| `mode=replica-data` | 注册查询源，以有限 read 请求取得真实 typed Query/Pull 页；不接收普通事件协议消息 |

Upgrade 前校验模式与现有 Origin 规则；token 不得放在 URL。连接随后以 auth 帧
提供 token、原配置 database namespace 和相同模式标记。数据库必须有效，调用者
须为 owner 或具有匹配 ID/slug 的 `db_admin` grant。权限判断先于绑定 ID 比较，
管理存储失败不降级为缓存结果。

### Envelope and correlation

沿用 `{id, type, payload}` JSON 文本帧。客户端请求的外层 `id` 非空、最多
128 UTF-8 字节且不能含控制字符；结构字段重复、未知字段和无效 Unicode 被拒绝。

| 方向 / type | payload 与关联 |
|---|---|
| 客户端 `auth` | `{token, database, mode: "replica-data"}` |
| 服务端 `auth_ack` | `{mode: "replica-data"}`；外层 id 对应 auth |
| 客户端 `subscribe` | `{collection, source, expectedDatabaseIdentity?}`；外层 id 就是本次 subId |
| 服务端 `subscribe_ack` | `{subId, databaseIdentity}`；外层 id 与 subId 一致 |
| 服务端 `replica_changed` | `{subId}`；外层 id 为 subId，仅提示开启一轮源读取 |
| 客户端 `replica_read` | `{subId, requestId, request, expectedDatabaseIdentity?, expectedSourceHash?}`；外层 id 等于 requestId |
| 服务端 `replica_page` | `{subId, requestId, page}`；外层 id 等于 requestId，page 是既有完整 typed 响应 |
| 客户端 `replica_ack` | `{subId, requestId}`；外层 id 等于 requestId |
| 客户端 `unsubscribe` | `{subId}`；外层 id 等于 subId，可取消尚未完成的注册 |
| 服务端 `unsubscribe_ack` | `{subId}`；外层 id 等于 subId |
| 服务端 `error` | `{subId?, requestId?, code, message, retryAfter?}`；保留相应关联，retryAfter 单位为秒 |

subId 标识连接内的一次注册尝试，取消或失败后不能复用。连接、认证代、实际 Stream
对象及其 generation 共同约束服务端 owner；迟到结果不能完成另一 owner 的请求。
重新认证会退休该连接已有的源注册，token 到期会关闭连接。

`subscribe.source` 使用上面的查询源结构。注册后 collection 与规范化 source hash
保持不变；等价定义按同一规范化规则比较。整个 concrete collection 的 Streamer
注册不携带源 filter/order/limit，避免遗漏离开集合及窗口边界变化。

```json
{
  "id": "source-attempt-1",
  "type": "subscribe",
  "payload": {
    "collection": "users",
    "source": {"version": 1, "filters": []},
    "expectedDatabaseIdentity": "0123456789abcdef"
  }
}
```

尚未绑定的客户端可以省略 expected ID。服务端固定注册时权威解析的 ID，用它约束
后续读取；`subscribe_ack` 只确认该代注册，不建立客户端持久绑定。客户端须校验
第一份数据页的 databaseIdentity/sourceHash 后才保存绑定。已绑定客户端在注册和
读取中保留原 expected ID；身份变化不是空页，也不能清理 pending 或不确定写入。

### Read and data pages

`request` 严格复用 HTTP Pull 请求 JSON。**expectedDatabaseIdentity 与
expectedSourceHash 位于运输 payload，不能塞进 request 对象。** 后者没有这些字段，
共享 decoder 会拒绝它们。以下示例使用此前已验证数据页保存的绑定；冷启动首读可以
省略 expected 字段，不能从 subscribe ACK 建立绑定。matching-set request 不包含 requestId：

```json
{
  "id": "read-1",
  "type": "replica_read",
  "payload": {
    "subId": "source-attempt-1",
    "requestId": "read-1",
    "expectedDatabaseIdentity": "0123456789abcdef",
    "request": {
      "collection": "users",
      "source": {"version": 1, "filters": []},
      "checkpoint": null,
      "limit": 100
    }
  }
}
```

| 源模式 | request 与 page |
|---|---|
| Matching set | request 的 requestId 禁止出现；运输 requestId 仍必需。返回既有 events/checkpoint/generationId/phase/caughtUp/bootstrapComplete |
| 有限窗口 | request.requestId 必须与运输和外层 id 一致；禁止 request.checkpoint 和顶层传输 limit。返回完整 replace、requestId、generationId、effectiveOrder、complete 和 typed documents |

每次 read 只调用一次既有 Query.Pull，支持进程内与远程 gRPC Query；不通过浏览器
HTTP 中转，也不缓存旧页冒充新轮次。响应 scope、完整 JSON 页和 typed 值编码沿用
HTTP 契约，int64 不经过普通 realtime 的 JSON 数值扁平化。

空页、changed、heartbeat、订阅 ACK 和页面 ACK 都不能制造 caughtUp。
Matching-set 的同 ID 多次事件保持源顺序；窗口只交付一个完整结果，错误不产生部分
replacement。窗口仍有既有索引一致性限制，需要后续刷新补位。源 checkpoint 继续
属于 Query/Store，连接 generation 和 ACK 都不是数据位置。

```text
注册 / changed / 客户端定时核对
             |
       replica_read
             |
    授权 -> Query.Pull -> typed replica_page
             |
    客户端完成整页本地应用 -> replica_ack
             |
    未追上则下一页；完成后等待下一轮
```

Gateway 每 200ms 合并 collection 变化提示；持续变化不滑动已安排的发送时点。
没有 read 就不会无限推页。客户端仍须周期性核对，不能只依赖通知；WS 客户端接入
后，这些请求可以继续走 WS，但本次服务端交付没有切换公开 SDK 的 HTTP 路径。

### ACK and owner retirement

同一订阅最多一个未 ACK 页。ACK 只归还运输额度，服务端不持久化它，也不验证
客户端磁盘提交。客户端应在整页所有分块、文档/assumed/checkpoint 和必要的
manifest/pin 处理完成后发送 ACK，再按同一连接的顺序发送下一次 read。

重复最近一次成功 ACK 幂等；未知请求、尚未发送的页或另一个订阅的 ACK 返回协议
错误。ACK 丢失不撤销已完成的本地提交；重连沿用客户端最后持久化的源 cursor。

取消、unsubscribe、重新认证或 backend 失效先退休 owner，再取消 Query 和未发结果。
排队及实际写出前再次核对 owner、认证代和 token 有效期。socket Close 中断阻塞写入；
网络分区中的只读 Query 可能持续到取消被观察或原有 deadline，不允许提前重用其额度。

| 资源 | 实际归还边界 |
|---|---|
| 源读取 worker | Query、校验、编码及 worker 本身退出后 |
| 编码页 buffer | 帧写完或明确丢弃时清除；仅退休 owner 不代表 buffer 已释放 |
| 最大帧字节预留、连接页 credit | ACK 或退休已成立，且 worker 已退出、编码 buffer 已释放，三者都满足后 |
| 订阅/source 字节 | 订阅退休且注册 worker 已退出后 |
| backend 清理 worker | Unsubscribe 实际返回后；超时可退休 Stream，但 permit 保留至 worker 退出 |
| 连接额度 | 连接所有拥有的 pump/worker 退出后 |

账本属于 Gateway realtime server，不随 backend generation 替换而清零。
ACK 最长等待 60 秒；超时退休订阅并返回运输不可用。auth/注册期限为 10 秒，
Query read 为 30 秒，单帧写 deadline 为 10 秒；这些超时不撤销客户端已提交的数据。

### Errors and capacity

源错误复用 HTTP Pull 分类，包括 `RESYNC_REQUIRED`、身份/权限错误、索引错误、
`QUERY_WORK_LIMIT` 和 `REPLICATION_BUDGET_EXCEEDED`。错误不携带部分成功页。

| 分类 | 处理 |
|---|---|
| `REPLICATION_SOURCE_BUSY` + retryAfter | 源读取、授权或有界控制工作额度不足；保持源退避，不立即改 HTTP 绕过本次拒绝 |
| `REPLICATION_TRANSPORT_BUSY` | 连接页额度、订阅/保留定义或 wire 字节容量不足；运输不可用，可选择既有 HTTP 路径 |
| `REPLICATION_TRANSPORT_UNAVAILABLE` | 注册/Stream owner 失效、ACK 超时或运输故障；重新建立运输归属 |
| `REPLICATION_PROTOCOL_ERROR` | 消息、关联或 ACK 状态错误，修正客户端协议 |
| `UNAUTHORIZED`、`FORBIDDEN`、`DATABASE_IDENTITY_MISMATCH` | 停止受影响的认证/绑定，不将其伪装成可绕过的普通连接失败 |

连接额度不足在 Upgrade 前返回 HTTP 503 和 `Retry-After: 1`。
以下配置位于 `gateway.realtime.replica`，约束 replica-data 模式；普通 WS/SSE
保留原协议。普通事件的共享分发队列上限为 16 条；满载时关闭本次事件匹配的普通
连接，消费者需重新订阅并恢复状态。其他普通连接和 replica 提示独立处理。
值为 0 使用默认值，负值启动失败。

| 配置字段 | 默认值 / 单位 |
|---|---|
| `connections` | 1,024 个 replica-data 连接 |
| `subscriptions` | Gateway 合计 4,096 个订阅；也限制每连接保留的注册尝试 ID 数，耗尽时关闭连接 |
| `subscriptions_per_connection` | 256 个活动/待注册源 |
| `pending_registrations` | 256 个控制/注册/清理工作额度；授权等待队列也以此为上限 |
| `source_bytes` / `source_bytes_per_connection` | 64 MiB / 4 MiB 注册 payload 的编码字节 |
| `read_concurrency` | 同时执行 8 个源读取 worker |
| `page_bytes` | 256 MiB 最大数据帧字节预留 |
| `page_credits_per_connection` | 4；有效上限不超过 4 |
| `auth_concurrency` | 8 个并发授权检查 |
| `auth_rate` / `auth_burst` | 每秒 100 次 / 突发 100 次 |

授权使用有界 FIFO 与单个补充 timer。auth、subscribe、read 和 changed flush 共享
这组并发/速率额度。配置是运行容量，不扩大 Query 原有候选、扫描或执行预算。

| 帧/队列 | 固定边界 |
|---|---|
| 原始 Pull request / cursor | 1 MiB / 256 KiB |
| replica_read 的额外包络 | 1 KiB；总入站帧最多 1 MiB + 1 KiB |
| auth 帧 | 64 KiB |
| ACK / unsubscribe payload | 1 KiB |
| 完整 typed 页 / protobuf 页 | 16 MiB / 20 MiB |
| WS 数据帧 | 16 MiB + 1 KiB |
| 出站控制帧 / 控制队列 | 1 KiB / 32 帧 |
| 独立数据队列 | 4 帧，仍受连接 credit 和 Gateway 字节预留约束 |

这些数值约束编码运输数据及任务数量，不代表总进程或浏览器 RSS。日志使用
connection、subId、requestId、身份及固定错误分类，不记录 token、文档、filter
或 opaque checkpoint 内容。完整决策见
[WebSocket 复制数据决定](../../.agents/notes/implemented/feature/2026-09-21-replica-websocket-data.md)。

## Push Changes

**Endpoint:** `POST /replication/v1/databases/{database}/push`

The request contains one concrete `collection` and a nonempty `changes` array.
Each change requires `action` (`create`, `update`, or `delete`) and a `document`
encoded as one recursive typed object. Its decoded fields are flattened and must
include a nonempty logical `id`. Missing or unknown actions are invalid.
Ordinary untyped documents, typed null, arrays, and scalar document roots are
rejected; there is no legacy decoding fallback.

```json
{
  "collection": "rooms/room-1/messages",
  "changes": [
    {
      "action": "create",
      "document": {
        "type": "object",
        "value": {
          "id": { "type": "string", "value": "msg-2" },
          "text": { "type": "string", "value": "Offline message" },
          "version": { "type": "int64", "value": "1" }
        }
      }
    },
    {
      "action": "delete",
      "document": {
        "type": "object",
        "value": {
          "id": { "type": "string", "value": "msg-3" },
          "version": { "type": "int64", "value": "5" }
        }
      }
    }
  ]
}
```

A successful HTTP 200 response contains only conflicts; an empty array means
all changes succeeded, including idempotent deletes. `changeIndex` is the
zero-based position in the request, so repeated IDs remain distinguishable.

```json
{
  "conflicts": [
    {
      "changeIndex": 1,
      "id": "msg-3",
      "reason": "missing",
      "current": null
    }
  ]
}
```

When present, `current` is a recursive typed object using the same value codec
as the request document and Pull documents. For example:

```json
{
  "type": "object",
  "value": {
    "id": { "type": "string", "value": "msg-3" },
    "collection": { "type": "string", "value": "rooms/room-1/messages" },
    "text": { "type": "string", "value": "Server copy" },
    "version": { "type": "int64", "value": "6" },
    "createdAt": { "type": "int64", "value": "1700000000000" },
    "updatedAt": { "type": "int64", "value": "1710000001000" }
  }
}
```

A retained tombstone includes typed `deleted: true` and its real metadata, with
former business fields cleared. Decoded `current.id` and `current.deleted` reflect
validated document identity and stored deletion state; business data cannot
override them. An absent target uses raw JSON null for `current`, not a typed-null
object. The outer `changeIndex`, `id`, and `reason` fields keep their existing shape.

### Version Preconditions

`document.version` is optional and case-sensitive. Storage assigns the resulting
document version; the supplied value is never copied into stored metadata.

| Typed `version` field | Behavior |
|---|---|
| Field omitted | No version precondition |
| `{"type":"int64","value":"0"}` through `{"type":"int64","value":"9223372036854775807"}` | Preserve exact value and presence |
| Null, string, bool, float64, negative/out-of-range int64, or noncanonical int64 string | HTTP 400 before any change reaches the Engine |

Int64 strings use canonical decimal notation: no leading plus, leading zeros,
negative zero, fraction, or exponent. A float64 value of `1` is not a valid version.
Nested business values retain their declared numeric type.

| Request | Target | Result |
|---|---|---|
| Versioned update/delete, including zero | Live, matching version | Atomic conditional mutation |
| Versioned update/delete | Missing, tombstoned, or different version | Conflict; target is not recreated |
| Unversioned update | Live | Unconditional update |
| Unversioned update | Missing or tombstoned | Create/recreate |
| Unversioned delete | Live | Delete |
| Unversioned delete | Missing or tombstoned | Idempotent success |
| Create | Missing or tombstoned | Create/recreate; supplied valid version is ignored |
| Create | Live | `already_exists`; leave the document unchanged regardless of supplied version |

Explicit zero remains an equality precondition for update/delete. Create accepts
any otherwise valid version but does not use it as a precondition; the live-target
check takes precedence over version comparison. Even equal content and version
return `already_exists`. A retained tombstone communicates deletion and does not
reserve the logical ID: creation can immediately reuse that ID in the same
database and collection, without observing its deletion version or awaiting
physical cleanup. See the
[create-conflict decision](../../.agents/notes/implemented/bug-fix/2026-09-18-replication-push-create-conflict.md).

### Push Size Limits

| Boundary | Limit |
|---|---|
| HTTP request body, including all typed-value tags and the outer envelope | 10 MiB |
| Encoded protobuf request, including typed data and envelope | 20 MiB |
| Encoded protobuf conflict response, including typed data and envelope | 20 MiB |

The protobuf budgets also apply to local Query execution. Production gRPC
receive limits admit messages within these budgets. The HTTP body limit counts
the encoded typed JSON, not only business data. The HTTP and protobuf budgets
apply independently; fitting the body limit does not waive the protobuf check. Such a request returns HTTP 400 before any storage operation.
An oversized conflict response returns HTTP 422 `REPLICATION_BUDGET_EXCEEDED`
without truncation; earlier changes in the batch may already have committed.

### Conflict Results and Batch Execution

| Reason | Meaning |
|---|---|
| `missing` | Target is absent; `current` is null |
| `tombstoned` | Target is a retained tombstone |
| `version_mismatch` | Live target has a different version |
| `already_exists` | Create observed a live target, or a create/recreate attempt lost to one |
| `precondition_failed` | The write failed its condition, but the later read cannot identify a more specific cause |

Push uses the database's write source for initial and conflict reads and includes
retained tombstones. A conflict's `current` is the state observed when read; after
a failed write it may already differ from the state that caused the failure.
A failed create may therefore report `missing` or `tombstoned`; this records the
later observation and does not prohibit creation in that state. Push does not
automatically retry the failed creation. It is not a guarantee that retrying will
succeed. Failed conflict reads return an error, without a fabricated document or
an incomplete success response.

Query validates the entire request before the first storage operation. Valid
changes execute in order, continuing after individual conflicts. The batch is
nontransactional: a later runtime error may leave earlier writes committed.
A lost response is ambiguous, and retrying does not provide exactly-once effects.

The conflict object replaces the document-only response. The internal gRPC
contract requires explicit action and optional int64 version presence, with no
legacy negative sentinel or unspecified-action fallback. Push document data also
uses recursive typed values internally over gRPC; old untyped gRPC data is invalid.
HTTP uses the same typed-value codec for documents, with no ordinary-JSON
fallback. Upgrade Push request and response consumers together. The
[HTTP typed-value decision](../../.agents/notes/implemented/bug-fix/2026-09-18-http-push-typed-values.md)
owns this encoding change. See the
[conditional-write decision](../../.agents/notes/implemented/bug-fix/2026-09-07-replication-push-version-checks.md)
for rationale and the [HTTP decoder decision](../../.agents/notes/implemented/bug-fix/2026-09-07-http-push-version-preconditions.md)
for exact version extraction.

## Errors

| HTTP status / code | Meaning and recovery |
|---|---|
| 400 `BAD_REQUEST` | Invalid request, cursor, scope, or database identity header; correct the request |
| 400 `INVALID_REPLICATION_SOURCE` | Invalid source structure, version, or field combination |
| 400 `NO_MATCHING_INDEX` | No eligible complete index plan for the window Query |
| 401 / 403 | Authentication or full-scope access denied |
| 409 `DATABASE_IDENTITY_MISMATCH` | Bound database identity no longer matches; stop this binding and preserve pending or uncertain writes |
| 409 `RESYNC_REQUIRED` | Old timestamp cursor, source replacement, expired history, or unavailable required payload; rebuild from null |
| 413 `REQUEST_TOO_LARGE` | Request body or checkpoint exceeds its size limit |
| 422 `REPLICATION_BUDGET_EXCEEDED` | Collection/matching-set Pull or Push cannot satisfy replication work/size limits |
| 422 `QUERY_WORK_LIMIT` | Window Query or its complete replace envelope exceeded work/size limits; no replacement |
| 499 | Request canceled; keep the last saved checkpoint |
| 501 `REPLICATION_UNSUPPORTED` | Selected source lacks required capabilities |
| 503 `INDEX_UNAVAILABLE` | Window index is not ready or is rebuilding |
| 503 `REPLICATION_WINDOW_INCOMPLETE` | Window Query returned fewer than N documents with continuation; retain the active window and retry the complete request |
| 503 `REPLICATION_UNAVAILABLE` | Transient source failure or work budget exhausted without checkpoint progress; retry the saved checkpoint |
| 504 `DEADLINE_EXCEEDED` | Request timeout; retry the saved checkpoint |
| 500 `INTERNAL_ERROR` | Invalid source output or other server failure; no progress returned |

Push continues to return version conflicts in a successful 200 response with a
`conflicts` array. Invalid supplied versions and other malformed Push parameters
return 400; the Pull recovery code is not a Push conflict format.
