# Agent Note: Query Replication Sources and Bound Database Identity

Status: implemented

## Problem

集合级 Pull 无法表达查询成员退出，也不能给客户端一个持久保留的初始化完成事实。
有限排序结果还需要在成员退出或排名变化后补位，单条变更不能独立确定完整窗口。
仅凭本地数据库绑定和 Pull cursor，还不能阻止旧 pending 在下一次 Pull 之前向同 slug
的新数据库发送 Push。数据库缓存可能延长这一错误窗口。

## Decision

在现有 committed scan + Watch 上提供 matching-set 查询源；沿用 Store 的进度、
逻辑删除和 caught-up 证明。有限窗口通过一次普通 Query 提供完整 replace。精确接口由[复制参考文档](../../../../docs/reference/replication.md)
维护，架构由[复制设计](../../../../docs/design/server/gateway/replication.md)维护。

| 机制 | 约束与原因 |
|---|---|
| 源投影 | 匹配的正常文档产生 upsert；Watch 中不匹配的正常文档产生 ID-only leave；逻辑删除产生 ID-only delete |
| 查询身份 | sourceHash 绑定协议版本、真实数据库 ID、collection、规范化 typed filters、有效排序和结果 limit |
| 进度 | version-4 opaque cursor 绑定源、generation、scan/replay/live 阶段和既有 Store checkpoint；跨服务实例续传不依赖内存 |
| 完成事实 | Watch 首次证明 caughtUp 后进入 live；bootstrapComplete 在同一 generation 内持续为 true；空页和短页不能证明完成 |
| 有限窗口 | source.limit 为 1–1000，使用规范化 filters/order 执行一次普通 Query；恰好 N 条或短页且无 continuation 才返回 complete=true 的 replace |
| 窗口身份 | 每次成功产生新 generation，原样返回 requestId；没有事件进度字段，客户端以 session/request 拒绝旧响应 |
| 工作预算 | 每个扫描候选和 Watch frame 都计入现有工作/字节预算；过滤掉的工作不免费，cursor 不越过未交付事件 |
| 数据库绑定 | Pull、Push、普通 Query 和文档 GET 接受可选预期 ID 请求头；查询源首次绑定也使用权威数据库对象 |
| 授权与检查顺序 | 从管理存储取得同一新对象，先核对状态及 owner/dbAdmin 权限，再比较预期 ID；拒绝发生在数据访问之前 |
| namespace | 保留 URL namespace；数据库 ID 用于绑定和前置条件，不改变数据存储地址 |

source.orderBy 参与身份规范化，但 matching-set 日志继续按源顺序传输。
扫描不匹配候选只前移进度；增量 leave 无需保存每客户端的旧成员表。这个选择可能暴露
从未匹配的 ID，因此需要完整数据库范围授权，不能将 filter 当作文档权限。

同一 Watch 的 current-state enrichment 可能产生重建、历史删除、再次重建的暂时回退。
客户端必须按源顺序收敛，不能按最大 version 丢弃状态；删除后同 ID 重建仍被允许。
历史过期显式要求重同步，新 generation 的完整成员激活和 pending 保留由客户端负责。

窗口默认使用显式逻辑 ID 升序，其它排序补足 ID tie-breaker；执行顺序与 sourceHash
绑定的顺序相同，沿用普通 Query 的索引需求。短页仍带 continuation 时返回
REPLICATION_WINDOW_INCOMPLETE，不拼接独立页面。整个 replace 包括元数据和文档同时满足
16 MiB JSON / 20 MiB protobuf 预算；缺索引、索引不可用、工作超限和编码错误不产生部分替换。

窗口接受普通 Query 的一致性模型。索引滞后可能让一个仍然匹配的旧成员暂时退出，投影
更新后重入，并非仅延迟新成员出现。complete 证明一次 Query 已满足窗口大小或穷尽条件，
不证明索引与源具有严格共同水位，也不宣称跨文档事务快照。后续完整刷新负责补位和挤出；
实时消息只提示刷新，SDK 仍需要轮询以恢复遗漏通知。

数据库请求头检查绕过缓存，管理存储失败不降级。身份不符返回请求级
DATABASE_IDENTITY_MISMATCH，Push 不转换为逐条 conflict；带绑定头的普通 Query/GET
也使用复制的完整范围授权。检查提供请求接纳时的身份判断，不为请求持有数据库生命周期锁，
也不保证与检查之后的删除/slug 重用线性化。当前拒绝不能证明较早超时写入从未提交。

## Alternatives

**独立 ReplicationSource 或持久化同步副本：** 会重复扫描、源生命周期、进度和保留期契约。
既有 Watch 与 committed scan 已具备所需源能力，公共查询投影无需新增 Store API。

**客户端仅比较本地绑定：** 在下一次 Pull 发现身份变化之前，旧 pending 仍可能发往被
重新分配的 slug。请求必须在服务端访问数据之前核对权威身份；缓存解析不能满足这一点。

**把绑定 ID 改写成数据库 URL：** 当前文档存储使用 URL namespace；仅复制路径改成 ID
地址会选择不同数据空间。绑定前置条件保留既有地址语义。

**每客户端服务端成员表：** 可以只向已知成员下发 leave，但会引入持久化会话及其恢复、
清理责任。当前完整范围授权允许无状态 ID-only leave；客户端负责自己的成员集合。

**拼接普通 Query 分页：** 独立页面不共享固定源快照，组合后不能证明一个完整排序窗口。
窗口仅接受一次 Query 的恰 N 条或穷尽结果；有 continuation 的短页显式失败。

**新增严格源水位或窗口持久副本：** 可以建立更强的索引新鲜度约束，但需要协调索引、源进度
和窗口状态。窗口复用已选择的普通 Query 模型，并明确接受索引滞后导致的暂时退出/重入。

## Consequences

- 服务端交付完整匹配集合、有限结果窗口和跨路由身份检查；公共 SDK API、HTTP 自动同步、客户端成员持久化
  仍由[离线复制 proposal](../../proposed/feature/2026-09-07-sdk-offline-replication.md)负责。
- source.limit 表示结果窗口大小，窗口禁止顶层传输 limit 和 checkpoint，包括显式空值/零值。
  窗口刷新与持久成员替换由 SDK 集成负责；服务端错误不能触发本地空集合替换。
- 新协议启用前 Gateway 与 Query 必须协调升级；旧节点可能忽略身份头，不能仅靠发送头声明绑定安全。
- 稀疏查询仍支付全源扫描、过滤和 leave ID 成本；预算约束 adapter 可见工作，不是所有数据库内部工作。
- matching-set 重同步建立新的 generation；窗口每次成功也产生新 generation。
  两种模式均不提供跨文档事务快照，也不能自动完成客户端本地激活。
- 有限窗口增加周期性完整 Query 成本，并要求匹配有效排序的索引；索引滞后可能造成暂时误退出。
- 权威管理解析增加每次绑定数据请求的管理存储读取，换取跨缓存节点一致的接纳检查。
- 既有[Pull checkpoint 决策](../bug-fix/2026-09-07-replication-pull-cursor-progress.md)
  继续维护底层源顺序、保留期及数据库生命周期限制。
