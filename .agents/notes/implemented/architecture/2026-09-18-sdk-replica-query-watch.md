# Agent Note: SDK 副本查询与动态 watch

Status: implemented

## Problem

副本中的业务内容、源成员和同步状态分别持久化。直接依赖数据库的通用查询或只观察
业务行，会遗漏成员代切换、metadata 条件和待同步状态造成的可见性变化。仅保存 Top-N
又无法在窗口成员离开时正确补位。查询还必须在读取、解码和持续保留数据时约束资源，
不能在大批量数据已分配后才判断预算。

## Decision

私有副本运行时提供查询客户端，消费已有 alias 存储；公开 `openReplica()`、真实上行
与恢复仍由[离线复制 proposal](../../proposed/feature/2026-09-07-sdk-offline-replication.md)
负责。查询不改变存储真相源、复制 checkpoint 或 pending 的持有方式。
[私有下行协调器](2026-09-19-sdk-downstream-replication.md)已通过持久化成员和 manifest
通知驱动这些查询视图。

### 查询契约

| 方面 | 行为 |
|---|---|
| 过滤 | 顶层字段、AND、missing 不匹配任何条件、null 独立、in 标量集合、contains 直接数组成员 |
| 精确值 | bigint 与有限 number 精确比较；字符串按 UTF-8 字节序；不把 bigint 转成 Number |
| 排序 | missing、null、false、true、numeric、string；array/object 排序明确失败；默认逻辑 ID 升序，显式排序补 ID tie-break |
| 页读取 | get/getPage 默认 100、最大 1000；cursor 绑定 alias 与规范查询、携带 typed 排序位置，不承诺跨页快照 |
| watch | 无 limit 时返回全部匹配，有 limit 时返回动态窗口；每次交付完整、与内部缓存隔离的数组；不接受 startAfter |
| 删除 | showDeleted 可见业务 tombstone，absence 始终不可见；保留既有 pending/pin/保护状态可见性 |
| 配置 | 异步初始化前冻结；同义 AND/in 条件规范化，相同查询共享索引，窗口大小独立 |

服务端 metadata 从成员观察取得，修改 metadata 可以影响过滤、排序及回调内容。
cursor 仅描述逻辑位置，不绑定物理 epoch；整理存储不使有效位置凭空失效。

### 所有权与更新

| 状态 | 所有者 |
|---|---|
| 物化读取队列和资源账目 | 同一执行上下文、同一 endpoint/subject/database/name 的副本数据库 |
| matcher 与完整候选 AVL | alias 内的规范查询；不同观察窗口共享 |
| 不可变解码记录 | 按 alias、记录 revision 和视图共享、引用计数 |
| 单次读取与观察者 | 发起它的句柄；关闭一个句柄不终止其他句柄仍持有的查询 |
| 查询存储来源 | 从仍存活的同 alias 句柄选择，原来源关闭后重新绑定 |

```text
订阅行和视图变化 -> 有界初始化 -> 重读失效 ID -> 核对视图 -> 发布
普通 ID 变化    -> 移除旧贡献 -> 求值当前投影 -> 更新 AVL -> 发布
结构性视图变化  -> 建立 shadow -> 协调期间变化 -> 核对 -> 整体替换
```

AVL 保存完整匹配候选，普通单 ID 维护为 O(log M)；输出构造仍为 O(result count)。
行通知只排队有界物理键，不保留事件 payload。成员行与 assumed metadata 的变化也
触发同 ID 重算；manifest revision 属于视图身份，覆盖同 generation 内保护状态变化。

初始化前先订阅。source generation 或 physical epoch 变化时重建当前投影，覆盖旧、新
成员及未解决本地修改；重建完成前不发布混合视图。发布前核对持久化 manifest；活动
查询每 10 秒及页面恢复可见时核对一次，恢复遗漏通知，不承诺后台调度的墙钟延迟。

### 读取与持续预算

| 默认界限 | 数值 |
|---|---:|
| 数据库共享物化池 | 64 MiB；底层读取最多 4 行，manifest 也计入 |
| payload/cache，含解码前预留 | 128 MiB |
| 规范配置、排序键及相关保留键 | 64 MiB |
| 聚合 AVL 节点 | 1,000,000 |
| 初始化/重建候选扫描 | 100,000 条、128 MiB 编码数据 |
| 失效键集合 | 100,000 个、16 MiB |
| 一次输出 | 16 MiB typed 编码 |
| cursor | 16 KiB |
| 视图重建尝试 | 最多 8 次，共享该次工作的扫描预算 |

物理主键 seek 先验证索引计划；扫描只返回键和大小描述，再逐条进行有界 d/m/assumed
投影。业务 payload 在资源预留后才解码；查询读取不提前解码 manifest 的恢复 payload。
解码预留至少为编码大小的 8 倍加固定开销，采用确定性的保守计费，不声称等于 JS heap。
排序键在 UTF-8 分配前预留；输出在复制给应用前检查 typed 编码额度。

更新期间，新旧对象同时存在时分别计费。活动和 shadow 索引共享同一上限，不能为
重建临时放宽预算。窗口外匹配候选也受持续计费；释放最后引用后归还额度。在记录和
输出项之间检查 8ms CPU 工作额度并让出执行权，逻辑分组不扩大底层物化批次。

初始化、持续更新或重建超限，以 `QueryBudgetExceeded` 结束受影响的规范查询并释放
其资源；其他查询和复制继续。查询错误不修改业务记录、pending、成员或 checkpoint。
应用保留已交付历史结果的内存不属于 SDK 持有量。

## Alternatives

**直接使用 Mango/RxQuery 处理业务语义。** 数值族、UTF-8、missing 和逻辑 ID 的契约需要
统一处理；高层结果缓存还会在显式预算之外保留 payload。存储查询只负责有界物理读取。

**只保存当前窗口。** 成员退出时无法从未知候选中正确补位。完整候选 AVL 提供增量
排序，代价是候选与 payload 的持续资源成本，超限必须明确失败。

**只观察业务行，或只核对 generation。** 成员、assumed 和保护状态变化也会改变可见性。
行与 manifest 共同驱动求值，定期权威核对恢复漏通知。

**让切代 shadow 使用独立预算。** 连续切代可放大缓存与索引峰值。新旧视图共享额度，
空间不足时终止查询，不发布残缺或混合结果。

## Consequences

- 公开 facade、真实上行与恢复仍需后续集成；此处交付私有查询能力及存储读取边界。
- 大结果、复杂排序或持续增长可以触发明确查询失败；limit 不豁免窗口外候选成本。
- 完整初始化和切代需要有界扫描，读取次数随候选数增长；不沿用较大批次原型的性能结论。
- 同一数据库的查询句柄使用一致的预算配置，不以新句柄扩大既有资源上限。
- 浏览器调度和真实存储延迟影响收敛速度；逻辑资源额度不构成浏览器总 heap 或断电保证。

### 浏览器测量

Chromium 140、真实 IndexedDB/Web Locks，业务内容为单个 score 字段，观察 limit(10)
排序窗口；时间不含准备数据。下表是一次环境中的观察值，不是性能保证。

| 操作 | 1,000 条 | 10,000 条 |
|---|---:|---:|
| 首次初始化 | 3.724s | 39.419s |
| 单条更新重排 | 46.9ms | 212.6ms |
| generation 切换重建 | 3.927s | 41.846s |
| 初始化有序 seek 次数 | 251 | 2,501 |

两种规模的单条更新均为 4 次物理 query 和 9 次 ID 读取，没有重新扫描完整候选集。
所有物理读取 limit 不超过 4、skip 为 0；取消后节点、缓存和 payload 账目归零。
初始化和结构性重建有明显扫描成本，不能将原型大批次读取的延迟当作当前实现性能。

[复制设计](../../../../docs/design/sdk/002_replication_client.md)说明与同步流程的关系；
[SDK reference](../../../../docs/reference/typescript_sdk.md#replica-availability)维护公开可用性。
