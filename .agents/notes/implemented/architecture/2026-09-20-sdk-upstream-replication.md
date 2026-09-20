# Agent Note: SDK 上行复制、冲突与恢复

Status: implemented

## Problem

本地 desired 与原生 assumed 可以在没有最新服务端 version 的情况下继续变化。把成功
ACK 伪造成冲突，或猜测下一版本，会改变原生基准；把旧 update 改成 create，又会覆盖
真实的删除或重建。业务内容、条件版本与确认进度需要分开处理。

一次原生持久化单元可能包含多个写入 callback。远端成功、原生 metadata、up checkpoint
和 pin 结算不是同一个提交。一个 callback 失败时，另一个可能已经写入远端；单独看
失败请求不能证明整个单元可以重试。恢复还需要跨 fork 与 metadata 保存一次明确决定，
避免崩溃后重新读取不同的 current，或覆盖用户在读取期间产生的新编辑。

## Decision

私有上行适配器把 typed HTTP Push 接入已有原生协议及协调器。公开 facade 仍由
[离线复制 proposal](../../proposed/feature/2026-09-07-sdk-offline-replication.md)拥有；
[下行决策](2026-09-19-sdk-downstream-replication.md)继续拥有源成员、generation 和 pin，
[原生运行时](2026-09-18-sdk-native-replication-runtime.md)继续拥有扫描与 metadata/checkpoint。
不增加普通编辑 Outbox、accepted journal 或新的源 checkpoint。

### Success and conditional writes

| 输入或结果 | 处理 |
|---|---|
| live assumed，有 version | 根据最终 desired 发 conditional update/delete |
| live assumed，无 version | 单 ID 权威 Query，包含 tombstone、不加源 filter；只有 current live 且业务等于 assumed 才采用其 version |
| absent/tombstone assumed，live desired | create，省略 version |
| absent/tombstone assumed，deleted desired | 不发远程 mutation |
| 完整成功 | 返回真实 `[]`，让 native 将已提交 desired 写成 assumed，不虚构 version/time |
| conflict current 等于 desired | 目标已满足，按 native 成功处理 |
| update/delete conflict current live 且等于 assumed | 只替换 wire CAS version；同一 handler 最多三次尝试 |
| 业务变化、missing 或不相容 tombstone | 保存 issue 并停止普通复制，不将 stale update 转 create |

handler 固定 assumed 与 desired。相等性比较身份、existence 和 exact typed payload，
忽略 server metadata、pin 与对象字段顺序；int64(1) 和 float64(1) 不相等。已知成功索引
在同一 handler 内不再发送。较新本地编辑仍与先前 ACK 对应的 desired 区分。

缺版本 preflight 只提供 CAS 候选，不注入下行、不推进成员或 checkpoint。读取失败
不授权无条件 mutation；成功 ACK 后也不做常规逐 ID 回读。删除尚未发出又 set 时，
最终状态对 live assumed 产生 update；删除已确认后再 set 才产生 create。

### Transport and identity

| 边界 | 保证 |
|---|---|
| 数据库身份 | 每次 Push、CAS retry、preflight、recovery read 独立携带原绑定的 `X-Syntrix-Expected-Database-Identity` |
| URL 与会话 | 保留配置 namespace；捕获会话贯穿认证等待、重试、响应校验，不使用可变 client-wide identity header |
| 请求 | 每批最多 50 项，按实际 HTTP 10 MiB 与保守 protobuf 20 MiB 预算切分 |
| HTTP Push ID | 沿用 `[A-Za-z0-9_.-]{1,64}` ASCII 限制；本地存储允许更宽的逻辑 ID，上行在 dispatch 前拒绝不合要求的 ID，不改名或映射 |
| 索引 | 保留原 changeIndex，重复逻辑 ID 不合并丢索引；控制记录只按不可变 kind 过滤 |
| 响应 | Push 采用 32 MiB 客户端资源上限；过大或畸形的 postdispatch 响应仍是 unknown |
| 线级串行 | 同一 alias 上行实际 wire dispatch 并发为 1；这不代表前一 callback 的 metadata 已提交 |

身份不匹配停止新 dispatch，保留 pending、原绑定和更早的不确定结果。失败的身份检查
不能变成 ABSENT，也不能被普通 resume 或显式 retry 绕过。权威读取与后续 CAS 之间仍有
竞争，原子版本条件保护这段间隙；不提供跨管理存储和文档存储的事务。

### Native persistence phase

phase 是一次原生 persist-to-master 单元，最多四个扫描 batch、200 个业务目标，
不是可以无限持续的 active 周期。manifest marker 只保存 phase/session/physical epoch、
目标 ID/token 和可能 dispatch 的标记。确认 marker 持久化后才准许 wire；多个 callback
共享同一 marker，不能各自清理其他未结算工作。

```text
marker confirmed -> serialized wire -> native assumed/conflict metadata
  -> native up checkpoint (including no-op) -> current-token pin settlement
  -> clear this phase marker
```

whole-phase 失败先停止新 dispatch，再收束 sibling。仅当全部 callback 已退出，且整个
phase 从未发 mutation，或每个已发 mutation 均证明未执行，才能按类别自动重试。
存在已接受、按 desired 满足处理或可能提交的未可靠结算项，整个 phase 即 uncertain。
例如 A 已 create X、B 失败，不能因 B 未执行而自动重新 create X。

原生 metadata/checkpoint 失败终止实例，保留 marker。重开看到未完成 marker 保守阻塞
自动双向应用；允许安全误报。普通 pause/resume 不代表重复不确定写入的授权。
begin/complete/failed 钩子位于原生串行单元内部，complete 等待包含 no-op 的
metadata/checkpoint 与 pin 结算。固定 RxDB 补丁及完整性清单覆盖 17 个源码、运行时
和声明文件，随私有 lazy bundle 交付。

### Explicit recovery

协调器提供私有 pause/resume/inspect/resolve。pause 取消并 drain 网络与原生实例，
保留 election、本地 CRUD 和 watch。resolve 要求 elected owner，完成后仍保持暂停。
没有解决的 issue/marker 阻止 resume；已持久化 intent 在 leader 应用源数据之前恢复。

inspect 默认离线，只返回有界 issue/target 元数据；指定 logicalId 才附带该单个 ID 的
desired/assumed 快照，沿用既有行预算。显式 `readCurrent: true` 要求 logicalId、已配置
transport 和数据库绑定，才返回 `current: {source: 'authoritative-read', document}`。
current 省略表示未知，document 为 null 表示确认不存在，tombstone 与不存在分开。
HTTP 在 alias 锁外执行，保留原绑定身份与会话；返回后复核 issue、editToken、physical
epoch、绑定和 desired/assumed 快照，失效结果返回 stale。该观察供选择恢复决定，
adopt/merge 的 resolve 仍独立重读 current；它不是历史请求收据，不持久化 payload
journal，也不增加逐 ACK 回读。

权威检查在每个存储句柄的 128 MiB 预算中预留两份本地行、manifest 引用和 16 MiB
响应，再发请求；并发检查超额时明确失败，成功、取消或 stale 都释放该调用的预留。

| 决策 | 前提与结果 |
|---|---|
| adopt-server | 权威 current 成为 desired 与 assumed；missing 明确写为 ABSENT |
| merge-local | current 成为 assumed，授权业务内容成为新 desired，分配新 token/pin |
| retry-uncertain | 显式确认可能重复副作用，才清理当前 phase 的不确定保护并允许正常原生重试 |
| reset-alias | 显式允许丢弃 pending，并核对涵盖所有编辑的 inspection token；更换物理状态，保留原数据库绑定 |

adopt/merge 在锁外读取原绑定数据库的权威 current，随后取得独占 alias 能力，复核
issueId、editToken、physical epoch 与绑定。新编辑使旧决定返回 stale。每 alias 只有
一个有界 recoveryIntent，保存原 current（含 ABSENT）、desired、受保护 token 和
预分配结果 token。两份允许的数据记录与固定包络单独核算 manifest 预算，不套用单份
普通 native 记录大小上限；intent 确认持久化前不修改 fork/meta。

```text
persist intent -> CAS desired fork -> CAS native assumed=current
  -> verify both records -> complete issue/phase target -> clear intent
```

两份存储没有跨集合事务。fork 后/meta 前，或 meta 后/清 intent 前崩溃，都按同一个
intent 补齐与核对，不能用重新读取的 current 替换已授权目标。该 ID 在 intent 未完成时
拒绝普通编辑；其他 ID 在有界持久化锁之外仍可本地编辑。adopt/merge 不改成员记录或普通 up/down
checkpoint、不伪 ACK 其他 ID；剩余 pending 从原 native checkpoint 扫描。
只有全部关联目标解决后才能解除整个 phase 的保护。

## Alternatives

**成功后逐 ID 回读或 synthetic conflict。** 增加每次 ACK 的读取，且改变原生 assumed
写入语义。真实 `[]` 保存提交时 desired；只有缺版本或真实冲突才获取新 CAS 基准。

**按 callback 清 marker。** 无法覆盖 sibling 已成功而本 callback 失败的组合，也遗漏
native metadata/checkpoint 失败窗口。marker 必须覆盖整个有界原生持久化单元。

**用整个 active 周期作 phase。** 连续编辑可使关联目标无限增加，无法保持 manifest
有界。四个扫描 batch 的原生单元提供明确的结算与失败边界。

**自动重试 unknown create。** 创建可能已经成功后又被其他写入删除；自动重试会复活
同名 ID。只有显式 retry 授权可以接受该效果。

**不用 intent，直接依次修改 fork 和 assumed。** 两次写入没有共同事务，崩溃后无法
区分应继续哪次授权决定。单个有界 intent 提供幂等重放，不复制正常编辑历史。

**另建 Outbox 或 accepted journal。** 会重复 native changed-docs 与 metadata 的投递
职责。marker 只保留未结算单元的保护信息，普通 pending 仍由既有存储判断。

## Consequences

- 私有 HTTP 上行与显式恢复已接通；公开 replica facade、匹配源授权的通知接入和完整
  browser-to-server 端到端验收仍由原 proposal 持有，不提供 public 手动 Push。
- 暂停同步不冻结全部本地编辑；恢复 intent 的目标锁与 token 校验防止旧决策覆盖新编辑。
- marker、preflight、恢复检查与 pin 结算增加持久化和读取成本。资源限额约束对应编码
  与记录集合，不承诺整个 JavaScript heap 或浏览器断电持久性。
- unknown 结果允许保守暂停；显式 retry 仍可能重复副作用。相同版本 ABA、触发器重复
  副作用和 exactly-once 不在此决定内。
- 原生、HTTP fixture 与故障注入分别验证协议和持久化边界，不代替真实服务端完整复制
  或公开 API 验收。验证结果由实际运行记录提供，不从实现存在推断成功。

[复制设计](../../../../docs/design/sdk/002_replication_client.md#private-upstream-and-recovery)
维护完整机制，[SDK reference](../../../../docs/reference/typescript_sdk.md#replica-availability)
维护公开可用性。
