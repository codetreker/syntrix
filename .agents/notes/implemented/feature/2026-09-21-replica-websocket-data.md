# Agent Note: WebSocket 复制数据通道

Status: implemented

## Problem

复制客户端需要及时发现变化，同时保持 typed 值、查询成员、完整窗口和可靠源进度。
普通 realtime 文档事件不包含这些完整契约，通知也不能证明索引已追上或源已完成。
将大复制页直接放入普通事件队列，会把单页预算放大成大量并存页面。

注册、读取、发送和客户端应用各有独立生命周期。socket 仍有心跳，不表示 backend
注册有效；取消也不表示 Query 或阻塞写入已经退出。缺少实际 owner 与资源归还边界，
旧代结果可能进入新请求，容量账目也会提前释放。

## Decision

在现有 `/realtime/ws` 上增加显式 `mode=replica-data`，复用 Query.Pull、既有源
规范化、typed page codec、数据库授权及错误分类。普通 WS/SSE 继续使用原协议。
本次交付服务端能力；公开 TypeScript replica SDK 仍使用 HTTP，自动 WS 数据路径
由后续 SDK 接入负责。

[复制参考](../../../../docs/reference/replication.md#replica-websocket-data)拥有 wire
和配置细节；[Gateway 设计](../../../../docs/design/server/gateway/realtime_watching.md#45-replica-websocket-data)
与 [Streamer 契约](../../../../docs/design/server/streamer/02.overall-design.md#31-stream-注册契约)
分别拥有客户端运输和 backend 生命周期。

### Data and identity

| 机制 | 决策与原因 |
|---|---|
| 有限读取 | 一个 replica_read 对应一次现有 Query.Pull；数据页本身通过 WS 交付，没有浏览器 HTTP 中转 |
| 源语义 | Matching set 保留 events/源 cursor/水位；window 保留完整 replace，错误不产生部分窗口 |
| Typed 值 | 直接使用既有编码，不经过普通 realtime 的 map 扁平化；保留 int64 与 float64 区别 |
| 变化订阅 | Streamer 覆盖整个 concrete collection，不带源 filter/order/limit，避免漏掉 leave 与窗口边界变化 |
| 轮次 | 原始变化只置 dirty，200ms 合并；客户端发起新 read 才生成新页，旧页不能成为新轮次完成证明 |
| 周期核对 | 通知可能遗漏，窗口索引可能滞后；客户端仍需定时读取，不把 changed 当作源进度 |

```text
collection change -> dirty hint -> replica_changed
                                    |
client round -> replica_read -> authoritative gate -> Query.Pull
                                    |
                 typed replica_page -> local whole-page commit -> ACK
```

模式在 Upgrade 前选择，并在 auth 中核对。现有 Origin 规则继续执行，URL 不接受
token。认证使用 JWT subject 作为 principal，权威数据库对象提供状态和 owner；
owner/匹配 db_admin 判断先于 expected ID 比较。auth、注册、read 与 changed flush
共享该判断；数据库 namespace 保持配置值，不改写成解析 ID。

初次未绑定注册固定当时权威 ID。subscribe ACK 只证明当前代 backend 注册已确认，
客户端必须从通过共同 scope 校验的第一份 typed 数据页建立持久绑定。后续请求受
该固定 ID 和规范化 source hash 约束。expectedDatabaseIdentity / expectedSourceHash
位于运输 payload，严格 Pull request JSON 不新增字段。

订阅 attempt 的外层 id 就是 subId，连接内不可复用。read/page/ACK 的外层 id 等于
运输 requestId；只有 window 的内层 request 才带同值 requestId，matching-set 内层
禁止该字段。认证代、实际连接对象、Stream 对象及 backend generation 都参与所有权
检查，不能只靠字符串关联接受迟到完成。

每次 read 和 changed flush 权威检查身份及权限，排队/写出前检查 owner 与 token
有效期。重新认证退休旧订阅，到期关闭连接。不新增会话撤销存储，也不承诺管理存储
身份检查与随后文档读取/发送组成跨存储事务。

### ACK and resources

ACK 是运输流控，不是新的 checkpoint 或服务端持久收据。客户端应在整页全部本地
分块、文档/assumed/checkpoint 及必要 manifest/pin 完成后 ACK，再按同一 socket
顺序发送下一次 read。服务端只核对 ACK 关联，不能证明客户端磁盘已提交。
最近一次 ACK 重复可幂等；未知或不属于当前页的 ACK 是协议错误。

| 约束 | 当前边界 |
|---|---|
| 单订阅 | 一个未 ACK 页 |
| 单连接 | 最多四个常驻页 credit；独立四帧数据队列与 32 帧控制队列 |
| 单页 | 16 MiB typed JSON 加最多 1 KiB WS 包络，沿用 20 MiB protobuf 预算 |
| 输入 | Pull request 1 MiB、cursor 256 KiB，read 额外包络最多 1 KiB；不沿用普通 WS 的 64 KiB 数据入口 |
| 工作 | 默认八个 read worker；授权并发八个、每秒 100、突发 100，有限 FIFO 等待 |
| 总量 | 默认 1,024 个 replica 连接、4,096 个订阅、256 个 pending 工作、256 MiB 页预留；源定义字节另行计费 |

这些额度由 `gateway.realtime.replica` 配置，默认配置与运行时同步。新限制属于
replica-data，不宣称普通 WS/SSE 获得相同的页确认或预算协议。Wire 编码字节和任务数
并不等于总 RSS；Query 的扫描、候选、编码和时间预算继续独立执行。

取消先退休 owner 并取消上下文，真正归还资源仍依赖实际工作结束：

- read permit 在 Query、校验、编码 worker 退出后归还。
- 编码帧在写完或丢弃时清除；页字节预留及连接 credit 同时要求 ACK/退休、worker
  退出、buffer 释放，不能因断线或早到 ACK 提前重用。
- 注册源额度等退休且注册 worker 退出；backend cleanup permit 等 Unsubscribe
  实际退出，10 秒清理超时可退休 Stream，但不能假装 worker 已结束。
- 连接额度保留至该连接拥有的 pump/worker 退出。账本不随 Stream 换代重置。

auth/注册为 10 秒、read 为 30 秒、写帧为 10 秒、已发送页 ACK 等待为 60 秒。
socket 关闭中断阻塞写入；分区中的只读 Query 仍可能运行到取消或 deadline。
ACK 丢失不撤销客户端已提交的页，也不改变并行 Push 的不确定结果。

源执行/授权额度不足返回 `REPLICATION_SOURCE_BUSY` 与秒级 retryAfter；不能立即
切 HTTP 绕过这次源退避。运输队列、连接页 credit 或注册保留容量不足返回运输类
错误，允许消费者重新选择既有 HTTP 路径。身份、权限、源预算和 RESYNC_REQUIRED
保留明确的源语义，不伪装成普通断线。

### Stream lifecycle

`Subscribe(ctx,...)` 返回当前实际 ACK 的 registration ID/generation。ctx 控制注册
过程；确认后由 owner 显式释放。Status 原子给出状态和下一次变化信号，避免观察空隙。

| 变化 | 结果 |
|---|---|
| 同一 Stream 对象内重连 | 普通注册恢复原 ID，等待恢复 ACK 后才 Connected |
| backend 不再 Connected 或 generation 改变 | 退休相关 replica owner/连接，取消 Query、未发送页和注册 |
| 实际 Stream 终止或替换 | 关闭依赖旧对象的普通 WS、SSE 与 replica 连接，清映射并有界建立新对象 |
| 普通事件共享队列满载 | 关闭溢出事件匹配的普通连接，使消费者重连并恢复状态；按原 Stream owner 定位，避免影响新 owner 复用的订阅 ID；replica 提示继续独立处理 |
| 取消后晚 ACK | 不能完成新 owner；有界清理孤立注册，无法清理则退休旧 Stream |

generation 只围定注册所有权，不是 Store 数据位置。此扩展不增加通知持久化、普通
协议 replay、主动 Puller 切换或跨服务订阅存储。日志关联 connection/subId/requestId
和固定分类，排除 token、文档、filter 与 opaque checkpoint 内容。

### Validation

服务端 CI 将普通构建与 race/coverage 放入独立的五分钟 job。冷缓存时两种编译
不能复用完整产物；串行执行会共同消耗测试预算。既有 `Syntrix Server (Go)`
检查汇总两个结果，只有全部成功才通过，失败、取消和跳过均阻止通过。
保留既有 race、函数/包/总覆盖率及 critical 未覆盖代码块检查；本地验证使用
`CI=true make coverage` 执行同样的门槛。

## Alternatives

**普通 WS 文档事件直接成为副本数据。** 缺少完整 typed、源成员/窗口和续传语义，
还可能经过普通 JSON 转换；需要重建一套数据应用与恢复契约。

**WS 仅提示，实际页面继续 HTTP 拉取。** 可复用现有 SDK hint，但没有提供所需的 WS
主数据通道。本模式保留提示，同时让有限 read 的真实页面走同一连接。

**服务器连续主动推页。** 会在客户端本地应用前积压页面，也可能把旧页当成新
settlement 轮次的完成证明。请求/整页 ACK 保留明确的轮次与背压边界。

**另建 source-stream RPC 或 checkpoint。** 会重复 Query/Store 已有规范化、历史、
窗口和错误责任。直接调用既有 Query 服务同时覆盖进程内和远程 gRPC 部署。

**普通事件队列满时阻塞共享接收线程。** 会同时延迟后续 replica 变化提示。共享
队列保持有界，无法接纳事件时显式断开受影响的普通连接，避免静默遗漏更新。

## Consequences

- 服务端可以通过 WS 交付与 HTTP 同契约的 typed 源页，每次读取仍支付真实 Query
  和权威授权成本，不是免查询的 raw-event fast path。
- 公开 SDK 暂未切换运输。服务端通过此模式补齐源授权与生命周期前提，不意味着
  普通 realtime 订阅已经具备同样保证，也不意味着 SDK 已自动连接通知。
- 大页使用独立 credit 和字节账目；慢连接可能耗尽容量并收到显式错误，不积压无限工作。
- 实际 Stream 退休会影响依赖它的普通客户端；明确断开使客户端重建，避免假健康状态。
- 周期核对、索引一致性、源历史保留和本地整页持久化仍由各自既有契约决定。
