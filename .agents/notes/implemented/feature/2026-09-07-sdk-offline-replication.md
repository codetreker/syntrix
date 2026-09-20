# Agent Note: SDK 离线复制公共 API

Status: implemented

## Problem

单次手动 Pull 只交付一个经过校验的页面，应用仍需自行完成本地应用、checkpoint、
账号隔离、pending 保存和恢复。远程 REST 引用也不能表达离线可用的本地查询与动态
结果。重复实现这些职责，容易混淆网络成功、本地持久化和源初始化完成。

副本还需要明确公开身份与生命周期：两个 alias 是否共享数据、源定义变化如何处理、
关闭是否删除状态，以及删除后重建能否被旧 tab 的句柄继续操作。内部存储和复制保证
必须通过同一公开契约组合，不能让应用直接拥有 RxDB 对象或拼装复制协议。

## Decision

公开 `replicate(path)`、`openReplica(options)`、`ReplicaDatabase`、
`ReplicaCollection` 及 typed 查询、状态、检查与恢复类型。REST 引用继续远程访问，
replica 引用只读写本地状态，不隐式回退网络，也不发现或复制子 collection。
[SDK reference](../../../../docs/reference/typescript_sdk.md#replica-availability)拥有签名、
可运行示例、配置、错误和浏览器要求；[复制设计](../../../../docs/design/sdk/002_replication_client.md#public-replica-database)
拥有架构与授权通知条件。

本决定完成原 proposal 中的公开组合，并沿用已确认的授权轮询范围。自动 WebSocket
通知仍以源授权一致为前提，不作为轮询复制交付的前置条件；该条件及代价保留在本 note
和复制设计，不把通知描述为已经接通。

### Composition and ownership

| 层次 | 已有决策的职责 |
|---|---|
| [原生运行时](../architecture/2026-09-18-sdk-native-replication-runtime.md) | 有界 changed-doc 扫描、metadata/checkpoint 持久化、取消与 lazy vendor bundle |
| [Alias 存储](../architecture/2026-09-18-sdk-replica-storage.md) | 身份、typed d/m/c/manifest、raw CAS、容量和干净整理 |
| [本地查询](../architecture/2026-09-18-sdk-replica-query-watch.md) | 精确过滤/排序、动态完整结果、共享资源界限和跨 tab 视图核对 |
| [下行](../architecture/2026-09-19-sdk-downstream-replication.md) | 源成员、generation 激活、pin、授权 HTTP 轮询和原生 election |
| [上行与恢复](../architecture/2026-09-20-sdk-upstream-replication.md) | Typed Push、真实 native ACK、有界 phase、不确定结果和恢复 intent |
| 公共 facade | 冻结输入、投影业务文档与状态、拥有句柄生命周期、公开显式恢复与安全移除 |

源 builder 不访问存储或网络，且只能由创建它的 client 注册。where/orderBy/limit
返回新定义；省略 source limit 表示完整匹配集合，正整数 limit 表示完整远程窗口。
open 在异步加载前冻结配置，所有请求 alias 本地就绪后返回并启动同步，不等待网络收敛。

### Identity and local behavior

| 方面 | 规则 |
|---|---|
| Namespace | 规范 endpoint/path prefix、JWT subject、精确配置 database、本地 name、alias 共同标识 |
| 离线打开 | 要求有效的本地 JWT 身份，允许 token 已过期；服务端仍独立鉴权 |
| 浏览器 | 需要 window/document、IndexedDB、Web Locks、Web Crypto；远程 API 不增加这些要求 |
| 两个 alias | 即使源相同也独立；一个 alias 的本地编辑经服务端同步后才进入另一个 |
| 重开 | 冻结源定义必须相同；少传 alias 保留历史存储但不启动它 |
| 更改源 | 新 alias，或安全 remove/recreate；reset 保留当前源定义与原数据库绑定 |
| 本地写 | set 替换、update 合并 live、delete 产生 tombstone；缺失删除幂等，同 ID 可重建 |
| Metadata | bigint 保真；version/time 来自最后源观察，不代表未上传编辑，未知值不补造 |
| Query/watch | 沿用有界本地查询，watch 发布完整本地结果；不存在隐式 REST 回退 |

网络请求保留已绑定数据库身份，不能通过 alias 重分配绕过服务端条件。账号替换立即
失效旧句柄与正在 open 的任务，再 drain 已拥有工作；pending 留在原账号 namespace。
同 subject refresh 不改本地身份。普通本地 ID 允许范围宽于现有 HTTP Push：
不符合 ASCII `[A-Za-z0-9_.-]{1,64}` 的上传在 dispatch 前拒绝，不静默映射。
浏览器明确 `navigator.onLine === false` 时，新 Push 在 dispatch 前返回可退避的
OFFLINE；重试仍需满足整个 phase 的安全条件。已经 dispatch 后才离线，不能据此
证明未执行或清除可能提交的结果。

### Status, inspection and recovery

状态组合 durable source readiness/generation/完整 round/pending/pins 与当前
leader/state/error。native idle 不是源 watermark。follower 从共享 manifest 获得就绪，
所有 tab 保留本地 CRUD/watch；查询代切换仍需要自己的完整视图重建。

pause 停止并 drain 自动同步，保留本地操作。resume 不能越过未解决 conflict/uncertain。
inspect 默认离线，按需附带单个 ID 的 desired/assumed；显式 readCurrent 才读取原绑定
数据库的权威 current。公开结果保留 live/deleted/absent 与未知观察的差别，不伪造请求
历史。公开 `id` 投影到内部逻辑 ID，editToken 允许 null，恢复要求原 physicalEpoch。
inspection 同时返回 `phase: {id, state: prepared|dispatched} | null`、recovering 和
`availableActions: {kind, issueId}[]`。phase 只反映持久 marker 的可能 dispatch 事实，
不证明远端执行；recovering 表示 intent 尚未完成。动作列表来自同一锁内快照，仅供
展示，resolve 仍重新校验。未解决的内容冲突不能借 retry-uncertain 绕过。

adopt/merge 独立重读并复核 issue/token/epoch，完成后仍暂停；retry-uncertain 要求显式
接受重复副作用，reset 要求 stateToken 与明确丢弃 pending。其他 ID 与正常复制进度的
保护继续由上行 note 定义。公共接口不增加 Outbox、accepted journal 或 public 手动 Push。

状态回调与查询回调属于各自句柄。可选 diagnostic 用 replicaId 标识本次 open，
operationId 关联操作与 watch 生命周期，携带 sessionVersion、alias、operation、phase、
timestamp，以及可用的 physicalEpoch、durationMs、count、requestId 和净化 code。
Pull 记录观察到的请求 ID 与结果计数；这些本地关联不承诺分布式服务端 tracing。
诊断不带凭据、payload、filter 值或原始 error；方法与错误回调仍保留实际失败和 cause。

### Removal and lifetime fencing

removeCollection 可以操作本次配置中的 alias，也能按同一身份删除未配置的历史 alias；
不需要重新提供旧源 builder。`collections: {}` 可打开仅用于历史移除的句柄，不启动
任何源。移除只影响本地状态，不删除远程文档。缺失历史 alias 幂等，空或尚未绑定的
离线 alias 也可移除。

调用方先取消/drain，独占 alias 锁内检查真实 desired/assumed 差异、pin、issue、dirty
phase 和 recoveryIntent。受保护工作存在即拒绝；失败后不悄悄恢复网络。确认安全后先
持久化 removed，再清理 fork 与配对 metadata。小型 terminal manifest 保留生命周期
围栏；清理失败可重试，重开先完成已确认属于旧代的清理。
close 可取消尚在排队的历史移除；若 terminal removal 已持久化，先完成所拥有的
物理清理，再向调用方返回取消原因。

重建分配新 lifecycleId 与 physical epoch。旧句柄每次访问核对 lifetime，即使遗漏通知
也不能读写新代。alias 锁名保持稳定以串行化移除/重建，election 与查询 namespace
绑定 lifetime，旧 owner 的清理不能影响新 owner。普通 compaction 保留 lifetime。

close 同步停止准入与回调，尝试全部 drain/清理并保留失败，持久数据保留。不同公共
句柄有各自会话关闭责任，关闭一个不结束同账号的其他句柄。

### Notification condition and cost

授权 HTTP 轮询提供收敛，包括窗口补位与遗漏通知恢复。现有 realtime 授权范围尚未
与 query source 对齐，公共 facade 不自动连接 WebSocket；本地 watch 不依赖该连接。

保留此条件的成本是周期性源读取和 poll/backoff 延迟。只有通知授权与所选源一致时，
才能接入已有 hint 入口作为调度优化；消息本身不能推进 checkpoint 或替代源核对。
[Realtime resume proposal](../../proposed/feature/2026-09-07-realtime-client-resume.md)
继续拥有其独立传输恢复工作，本决定不把它标为完成。

## Alternatives

**应用自行同步。** 可以只保留手动 Pull，但每个应用都需重新处理 checkpoint、可靠
ACK、账号隔离和本地查询。SDK-owned replica 统一承担这些重复义务。

**Realtime 事件作为本地真相源。** 能减少 Pull 请求，却无法用可靠源进度补齐遗漏，
也不能绕过当前授权差异。选择轮询为收敛路径，通知只在满足条件后提供提示。

**独立 SDK Outbox。** 原 proposal 曾包含这一方向；原生 changed-docs 与 metadata
已经保存待上传工作和确认基准。增加第二个队列会重复持久化与恢复职责。

**将 bigint 转成 Number。** 可以绕过 JSON 序列化异常，但丢失整数精度及 int64/float64
区别。公开本地值、持久化、HTTP 编码和冲突处理沿用 lossless typed 表示。

## Consequences

- 公开 API 组合既有存储、查询和双向 HTTP 复制，应用无需安装、修补或直接使用 RxDB。
  私有运行时按需加载并随包附许可证，远程客户端入口不加载整个副本运行时。
- 完整匹配集合、有限远程窗口与本地 watch 的边界保持独立；源结果仍可能暂时回退，
  document version 不是全局顺序，删除后同 ID 重建仍被允许。
- 资源限制会明确中止操作或查询，不截断结果或驱逐 pending。预算配置在 open 冻结，
  额度不能代替浏览器总 heap 或断电持久性保证。
- 网络未知结果可能需要人工决定，显式 retry 仍可能重复副作用；没有 exactly-once 声明。
- 自动 WS 通知继续受上面的授权条件约束。公开轮询复制已交付不意味着通知优化已交付。
- 持久 lifetime fence 增加少量保留 manifest 和切代校验成本，换取跨 tab 删除/重建隔离。
- 真实服务端、浏览器故障与 packed consumer 需要独立验证，结果不能互相替代；
  存储故障注入不构成浏览器断电持久性保证。
