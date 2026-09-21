# Agent Note: SDK 私有 WebSocket 复制与 HTTP fallback

Status: implemented

## Problem

副本需要在远端变化后及时读取真实源页，同时在连接失败时继续收敛。只把 WS 当作通知，
或把普通文档事件直接写入本地，分别遗漏主数据通道和既有成员/窗口/typed/checkpoint
契约。运输切换还可能在旧页未提交时接纳另一页，提前释放资源或误认新鲜轮次。

公开原始 WS 的手动连接与订阅生命周期，也无法直接表达 alias leader、native epoch、
整页应用和恢复的共同所有权。应用不应为了使用本地副本再管理第二套下行应用器。

## Decision

在已有 source.read 之下选择私有 WS 数据页或 HTTP fallback，两者复用严格源请求与
响应 codec，并进入同一个 downstream/native 存储路径。上行仍使用 HTTP Push；
业务、成员、pin、冲突、generation 与持久 checkpoint 格式保持不变。

[服务端决定](../feature/2026-09-21-replica-websocket-data.md)拥有 replica-data wire
及服务端容量；[下行决定](2026-09-19-sdk-downstream-replication.md)拥有本地投影和
持久化。本决定拥有运输选择、租约与整页回执。[复制设计](../../../../docs/design/sdk/002_replication_client.md)
维护组件契约，[SDK reference](../../../../docs/reference/typescript_sdk.md#replica-availability)
维护公开行为与配置。

### Ownership and finite reads

| 所有者 | 契约与原因 |
|---|---|
| Replica database 句柄 | 至多一条惰性私有 WS；独立句柄不共享全局连接池 |
| 活动 alias leader | 取得源租约，绑定 session/native physical epoch/instance；follower 不创建源数据订阅 |
| Alias | 独立 source、绑定、subId、cursor 和 pending page；同 collection 只共享物理 socket |
| Native owner | 失效租约后 drain 原生队列和应用器，再释放未完成页；维护后新 owner 取得新 subId |
| 最后一个活动租约 | 关闭 socket、尝试和重连 timer，不保留无 owner 的后台连接 |

```text
source.read -> WS read/page -- 不可用 --> HTTP Pull
                         |
              一个 downstream/native 应用器
                         |
        最后分块 doc/meta/checkpoint + manifest/pin
                         |
          committed -> WS ACK / 释放共用页额度
```

read 是一次有限请求，不无限等待下一条变化。保留空进度页和轮次完成，使原生上下行
能够回到 idle，继续本地 Push 与 settlement。注册完成/changed 调度现有 hint；
默认每 10 秒核对也使用当前运输，WS 正常时不另发浏览器 HTTP Pull。

请求开始后才产生该轮响应，不提前缓存业务页。settlement 前生成的页仍属于原请求，
不能换一个 round 标签释放新 pin。窗口始终完整重取，matching set 使用既有持久 cursor；
ACK、subId 和运输代不写入 checkpoint。

### Whole-page receipt and switching

句柄持有 WS/HTTP 共用的四页池，同 alias 最多一页。每 alias 最多一个额度等待项，
按可准入顺序分配并响应取消；45 秒等待超时返回可退避的 REPLICA_PAGE_CAPACITY，
不提前释放旧页。fallback 不产生第二套页额度。页面被 decoder 接纳并交给
downstream 后，socket 丢失不会归还这份额度。

最后本地分块的文档、assumed、native checkpoint、必要 manifest 激活和 pin 处理全部
完成后才调用 committed。WS 回执同步排入 ACK，不等待 token、网络确认或新连接；
HTTP 回执只释放额度。ACK 丢失或发送失败不能反向拒绝已经完成的本地提交。
本地应用失败先走既有失败/恢复；应用 owner 的清理结束后才释放未完成页。

| 切换点 | 结果 |
|---|---|
| WS read 未返回页 | 退休请求/订阅归属，使用相同已提交状态改走 HTTP |
| WS 页已接纳、尚在本地分块 | 完成当前页，下一次 read 才切换；不会并发应用 HTTP |
| HTTP 期间 WS 恢复 | 后台只连接/认证；当前 read 退出且整页完成后注册并切回 |
| ACK 丢失 | 保留本地提交，下一次使用已持久化进度；不重复从旧接收位置覆盖 |
| 维护、pause/remove/close、账号切换 | 先撤销租约与迟到结果归属，再取消未接纳工作并 drain 应用所有权 |

首次 page 可以来自任一运输。共同 decoder 校验数据库身份、source hash、generation、
请求关联和 typed 数据；只有合法响应建立初始绑定。注册 ACK 不是绑定。
已绑定后所有 WS 注册/read/重连与 HTTP fallback 保留原 expectedDatabaseIdentity，
namespace 不改成解析 ID。

### Deadlines, backoff and cancellation

| 边界 | 选择 |
|---|---|
| 连接/认证 | 总计 10 秒，包含本次凭据等待 |
| 源注册 | 10 秒 |
| WS read / HTTP fallback read | 各 45 秒，容纳服务端 30 秒 Query 和授权准入余量 |
| WS 重连 | 指数基数从 1 秒增至 30 秒后封顶，再乘 0.5–1 jitter；有效租约存在时持续尝试 |
| 周期核对 / hint | 10 秒 / 200ms 默认值不变，不新增公共运输 option |

只有明确运输类错误、本地 WS deadline 或协议/关联损坏允许撤销 WS 后立即选择独立
校验的 HTTP 源。Query/Store 的 REPLICATION_UNAVAILABLE、DEADLINE_EXCEEDED、索引
不可用仍保留源错误与退避；权限、绑定和本地持久化错误不被 fallback 绕过。
REPLICATION_SOURCE_BUSY/429 的 retryAfter 与 retryAt 跨连接、原生租约和运输保留，
changed、后台重连及 fallback 不提前读取源。

终止连接错误保持阻塞，显式 resume 才重新允许认证。恢复入口在旧 native 应用排空、
存储恢复校验通过后执行，并保留源退避与会话 fence；恢复权限不需要重开整个副本，
普通租约替换也不能变成自动绕过权限拒绝的重试。

凭据取消监听先于 provider 调用安装。关闭只等待可取消尝试，不等待共享 refresh
底层 promise；迟到结果仍有处理器和会话 fence，另一个 HTTP 调用可继续共享刷新。
重连 probe 只连接/认证，不抢跑第二套数据源。当前 auth 的 UNAUTHORIZED 仅 refresh
一次；不能靠循环刷新隐藏权限拒绝。

消息归属以实际 socket attempt、subId 和 requestId 检查。旧代/取消注册的晚消息不
激活新 owner，同请求重复页不重复应用，当前代未知请求页明确失效。只保留当前请求
和必要的最近回执，不能累积无界历史 ID。诊断回调允许关闭句柄，之后再次核对 owner。

本地订阅退休同时在所属连接发送原 subId 的 unsubscribe。订阅级 SOURCE_BUSY 可以
保留服务端注册，不能只删除本地记录，否则其它 alias 维持共享连接时会累积订阅与
预算占用。先保留原始源错误及退避信息，再发送取消；发送失败则关闭连接，触发剩余
注册清理，不能把源拒绝降级为可立即改走 HTTP 的运输错误。请求级限流仍保留注册供退避后读取。

### Public surface and diagnostics

移除公开 realtime()/subscribe()、WS 构造器、配置和 raw 协议类型，不保留兼容包装。
应用通过 openReplica、本地 watch 和 replica.sync.subscribe 使用副本。
realtimeSSE() 与 SSE 根导出保留，client.pull() 仍是手动 HTTP 单页接口。

原公开 WS 的回调/显式连接历史理由留在
[旧生命周期决定](../bug-fix/2026-09-07-sdk-realtime-subscription-lifecycle.md)；新的私有连接
按 leader 租约结束，不沿用最后一次普通 unsubscribe 后仍保留 socket 的规则。
未接入原生运行时的旧 coordinator/outbox/checkpoint/pull/push stub 同时移除，不引入
另一种持久化投递机制。

运输位于 lazy runtime 内，REST-only 导入不建立 WS 或加载 RxDB。demo 使用默认
周期核对，不另设 1 秒 polling 或手动管理连接。

onDiagnostic 使用 operation=transport 和 connect/subscribed/read/accepted/committed/
fallback/recovered/closed，关联 alias、requestId、subId、transportEpoch 及实际 receipt
的 ws/http mode。accepted 不等于提交；committed 是本地整页回执，不证明服务器收到
ACK，更不是上行确认。诊断不含 token、文档、filter 或完整 cursor。

## Alternatives

**WS 只发提示，数据仍由 HTTP 拉取。** 可减少通知延迟，但没有交付 WS 主数据路径。
所选路径将请求及真实 typed 页放在同一 WS，HTTP 只承担不可用时的 fallback。

**向原生 live feed 直接灌数据。** 需要重建整页分块、fresh round、pin 与取消归属，
且可能让下行长期 active。有限 source.read 保留已有原生调度和唯一应用器。

**为两种运输分别设页池。** 切换时旧 WS 页仍在本地应用，新 HTTP 可另占一套额度。
共用句柄页池让断线不扩大同时保留页面数量。

**ACK 时等待连接或共享 refresh。** 会把运输恢复变成本地提交的新依赖，甚至与凭据
drain 相互等待。ACK 只使用已经存在的归属，失败独立处理。

**保留公开 raw WS 兼容层。** 应用会继续管理与副本不同的连接、订阅和状态边界。
移除该入口明确唯一的 replica 生命周期；SSE 和手动 Pull 的独立用途保留。

## Consequences

- 正常在线复制下行通过 WS 交付真实页；不可用时保留 HTTP 收敛路径，现有本地数据
  与源 checkpoint 不需要格式迁移。
- 公开 WS API 是破坏式移除，使用者需要转到本地 watch/同步状态；普通 SSE 不替代
  replica 的持久化语义。
- 周期性 Query 成本仍存在，通知和重连不承诺固定延迟或索引即时一致。
- ACK 故障与已提交本地数据分离；上行仍保留已有 CAS/uncertain 恢复，不声明 exactly-once。
- Wire 页池、存储/查询预算与服务端容量分别约束各自资源，不把四页描述为浏览器总 RSS。
