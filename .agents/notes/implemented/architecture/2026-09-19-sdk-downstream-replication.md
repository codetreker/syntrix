# Agent Note: SDK 下行复制、源成员与 pin

Status: implemented

## Problem

副本存储与原生复制运行时仍需接入服务端查询源。成员退出与业务删除语义不同，原生
协议保留本地 pending 时也必须推进成员信息。窗口、源重建和异常恢复还需明确整页进度、
就绪条件与 pin 释放边界，避免部分结果替换完整成员集或旧回调丢失后来编辑。

## Decision

私有下行协调器连接已有 HTTP 源、原生复制、alias 存储和查询视图。
[私有上行与恢复](2026-09-20-sdk-upstream-replication.md)已接入同一协调器；公开
`openReplica()` 继续由[离线复制 proposal](../../proposed/feature/2026-09-07-sdk-offline-replication.md)
维护。未配置内部上行适配器时禁用上行扫描，不以虚假成功确认本地 pending。

### 源与身份

| 源 | 请求与完成条件 |
|---|---|
| 完整匹配集合 | 每次最多 100 个事件；保留服务端 cursor；只有 caughtUp 与 bootstrapComplete 同时成立才完成当前轮次 |
| 有限窗口 | 获取最多 1000 个文档的完整 replace；不发送顶层 checkpoint/limit；核对 requestId 与 effectiveOrder |
| 通用校验 | 16 MiB 文本预算先于 JSON/typed 解码；校验身份、source hash、generation、类型、计数和阶段 |
| 数据库绑定 | 首次授权响应固定 databaseIdentity/sourceHash；后续续页、刷新及空 cursor 重建都带 expected identity header |
| 读取准入 | 允许尚未绑定或尚未 ready 的源读取；上行写入仍使用原有更严格的准入 |

URL 保留配置的 database namespace。绑定不匹配停止网络工作，保留旧绑定和 pending。
服务端 generation 与本地成员 generation 分开记录；同一非空 cursor 链必须保持服务端
generation 一致。窗口与新的空 cursor 初始化允许产生新 generation。

### 投影与持久化

| 输入 | 业务记录 d | 成员记录 m |
|---|---|---|
| upsert | 原生应用正常内容；保留竞争的本地 pending | 当前目标 generation 标记 present，记录实际 metadata |
| leave | 不制造业务删除 | 标记非成员；保留已观察的业务存在性和 metadata |
| Syntrix delete | 生成无业务 payload 的 tombstone，服从原生 pending 规则 | 标记非成员，保留已有 metadata，不虚构版本 |
| 未知 leave/delete | 没有 d/m 时不创建永久记录 | 仅通过 c 推进源进度 |
| 源物理清理 | 不作为业务删除下发 | 后续完整成员重建移除缺失成员 |

同页重复 ID 按事件顺序归约，不按最大版本合并。m/c 独立于 d，业务 pending 被跳过
不会阻止成员退出；控制记录冲突必须失败并重放。下载覆盖 clean d 时从实际 previous
保留 editToken/pin，并继续使用原 revision CAS，不能覆盖后来编辑。

get/query/watch 共用 pending 判定：下载来源 hash 与当前 revision 同时匹配的 d 不因
assumed 缺失或滞后而成为本地 pending。因此 staged 新成员在激活前保持隐藏，metadata
写入失败和重开也不泄漏成员。后续真实编辑推进 revision，恢复正常 pending 判定；
active 成员、pin 与恢复保护仍独立保留可见性。

```text
一个源响应 -> 完整校验与只读投影预检 -> 绑定/准备目标 generation
           -> 有界 d/m/c 批次 -> 原生 fork -> assumed -> checkpoint
           -> 完成页或继续本地分块 -> 完成轮次后激活 manifest
```

每次 native delivery 最多 201 行且编码不超过 16 MiB，始终包含 c 进度。投影预检包括
当前 pin、metadata 及控制记录，不能只检查 upsert payload。预检逐组进行，不保留第二份
完整规范窗口。实际写入仍执行原有最终行大小及容量准入。

源 cursor 仅在整个源页最后一块前进；内部 delivery 标识区分轮次、页和块，不替换服务端
checkpoint 机制。部分 matching-set 交付恢复时从上一完整 cursor 重放；窗口丢失内存状态
后重新获取完整结果。旧 active generation 保留，最多两个成员 slot，只有必要记录、
metadata、checkpoint 都完成后才 CAS 激活新 generation。确认丢失时重读 manifest。

窗口的成员替换完整激活，但部分业务字段可能先落盘，不承诺跨文档事务快照。源完成
不等于每个 tab 的查询索引已完成重建；持久化 manifest 与既有失效通知供查询层收敛。

### Pin 与结算

| 事件 | 条件与结果 |
|---|---|
| 真实本地编辑 | 保持既有新 token 与 await-settlement pin |
| 上行持久化完成 | 原生 up checkpoint 已覆盖当前 d 的 lwt/key，d 与 assumed 业务相等，且没有未决 marker/recovery，才转为 await-source |
| 新编辑 L2 | L1 的 frontier 或 token 不能结算 L2 |
| 完整 fresh round | 只清理早于该 round 结算、仍为同 token 且无 pending/未决状态的 await-source pin |
| 旧 round 完成 | 不能释放后来结算的 pin |
| A→B→A | 原生 no-op checkpoint 同样触发结算，无需 masterWrite |
| 重开/恢复 | 有界扫描 durable up frontier 与遗留 pin，恢复 checkpoint 成功而 pin 记账未完成的情况 |

原生上行钩子等待 metadata/checkpoint 与队列完成，不使用 processed/ACK 事件充当持久化
收据。纯 pin 记账不生成业务 token，也不触发业务刷新循环。真实上行适配器复用此边界，
并拥有其 dirty phase、冲突及不确定结果处理；只有当前实例持有的活跃 phase 可以参与
正常源应用与 pin 结算，遗留 marker 和 follower 不获得该许可。

### 所有权、调度与失败

| 方面 | 规则 |
|---|---|
| 多实例 | 同 alias 使用 RxDB 原生 election；coordinator 独立持有 channel/elector，关闭后可在同 alias 重新创建 |
| follower | 从持久化 manifest 获得 readiness，不等待自身从未运行的 native 实例 |
| 生命周期 | coordinator 属于 alias，native 实例单独登记；维护只替换 native，关闭等待网络、队列和资源退出后释放 election |
| 轮询/hint | 默认 10s 授权轮询，hint 默认 200ms 合并；每 alias 一个轮次，变化只保留 dirty 位 |
| 读取退避 | 默认 1s 指数退避至 30s，带 jitter；429 尊重 Retry-After，hint 不绕过退避 |
| 历史过期 | RESYNC_REQUIRED 重建源，保留绑定、pending 和旧完整成员 |
| 阻塞 | 身份、权限、协议、容量、持久化错误及未决恢复状态明确阻塞，不静默重绑或清除工作 |
| 清理错误 | 保留原始 I/O 错误与后续清理错误，尝试所有清理；正常取消与已收束的源拒绝不伪装成存储故障 |

源端临时 5xx、网络失败和限流走读取退避；持久化失败不按网络错误重试。已有 WS
订阅的授权范围尚未与查询源对齐，因此此处提供私有 hint 入口，正确性依赖授权轮询，
不自动连接范围不符的通知。

空闲边界根据已有存储建议触发整理，使用默认 30s 维护退避。整理成功、not-clean 或
已验证回退完成的容量/超时失败后都重新捕获 scope/native；回退验证要求 active epoch
不变、维护状态已清理且只剩原物理代。未知持久化结果保持阻塞，不能盲目恢复。

同句柄或其他句柄发起维护时，协调器保留 leadership，识别旧 native owner 的取消并
等待退出，再从所选持久化代重建 adapter/runtime。捕获 scope 等待同句柄维护结束；
捕获与取得 native 之间的竞争仅在重新捕获证明同会话、同定义的实例已更换时重试。
真实持久化或 drain 错误仍阻塞，alias/session 关闭不能触发重启。

## Alternatives

**只复制 d。** 原生可因 pending 跳过业务覆盖，这会同时遗漏 leave。独立 m 保存成员
事实，pin 保留尚不能依据源结果隐藏的本地编辑。

**在 ACK 时释放 pin。** ACK 不代表原生 metadata/checkpoint 已持久化，也不代表新的
源轮次已决定成员；需要 durable frontier 和后续 fresh round 两个边界。

**直接复用数据库缓存的 elector。** coordinator 关闭会终止它，而同数据库后续创建
仍可能拿到已终止对象。每个 coordinator 用独立引用复用原生 election 算法。

**把 coordinator 注册为 native 资源。** 维护会递归关闭自身并丢失 leadership。两层
所有权分开，维护只等待和替换实际复制实例。

## Consequences

- 下行复制、成员、pin、查询和私有上行/恢复可组合使用；公开 facade 仍需后续集成。
- 一页响应和一个交付批次限制 backlog 内存；完整窗口仍需保留其有界响应。
- 投影预检与 pin 恢复扫描增加本地读取成本，不给出未测量的吞吐或收敛时延承诺。
- 仅收到网络成功、短页或空页不能认定就绪；只有持久化与源完成事实共同成立才激活。
- 浏览器多 tab 和 HTTP fixture 验证不替代最终 Go 服务到 SDK 的完整端到端验收。

[复制设计](../../../../docs/design/sdk/002_replication_client.md)维护架构衔接，
[SDK reference](../../../../docs/reference/typescript_sdk.md#replica-availability)维护公开可用性。
