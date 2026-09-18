# Agent Note: SDK 本地 alias 存储

Status: implemented

## Problem

可靠的原生复制协议仍需要明确的本地身份、记录格式和生命周期。业务目标、源成员及
原生 assumed 混在一起，会让 metadata-only 更新破坏条件写，或让成员退出丢弃本地
pending。逐 ID 清除原生历史又可能使旧状态重新上传。直接批量读取或保留高层事件历史，
会在应用检查预算之前形成无界载荷。

## Decision

私有 lazy bundle 提供 Dexie alias 存储，采用固定记录、raw revision CAS 和干净整代回收。
[复制设计](../../../../docs/design/sdk/002_replication_client.md#private-local-alias-storage)
维护内部契约；[SDK reference](../../../../docs/reference/typescript_sdk.md#local-replication-availability)
维护公共可用性。公共 openLocal、查询/watch、自动 HTTP 下行/上行继续由
[离线复制 proposal](../../proposed/feature/2026-09-07-sdk-offline-replication.md)负责。

### 身份与所有权

| 事实 | 决策 |
|---|---|
| 物理 namespace | 规范 endpoint、JWT sub、精确配置 database、本地库名、alias 的 SHA-256；manifest 保存原 tuple 校验 |
| 离线身份 | sub 非空，oid 存在时必须相同；允许过期 token 离线打开，拒绝缺失/畸形 token，不引入默认账号 |
| 凭据变化 | 同 subject refresh 保留库；subject 改变先失效再 drain，覆盖尚未完成的 open；失败的 drain 不解除所有权义务 |
| 正常取消 | session 使用稳定的取消原因；原生运行时仅识别所属已取消 signal 的同一原因或以其为 cause 的 Axios 取消，旧读取成功后的失效不会变成关闭故障；真实存储错误仍传播 |
| HTTP 等待 | 请求构造时同步固定会话；凭据等待前和等待期间响应取消，避免旧请求等待正在 drain 自己的新凭据 |
| bundle | 生命周期能力附在同一个 token provider 的版本化 Symbol hub 上，remote entry 与独立 lazy bundle 共享所有者 |
| 数据库绑定 | 初始可 unbound 离线编辑；首次 CAS 绑定 databaseIdentity/sourceHash，之后不自动改绑 |
| 请求准入 | 捕获 subject/session/source definition/physical epoch/native instance/request ID；就绪后提供预期数据库身份头 |

JWT 解析只用于本地命名空间，不是鉴权或抵抗同源恶意脚本的隔离机制。不同 slug/ID URL
保留独立缓存；网络 guard 不发送 HTTP，也不能代替服务端权威身份检查。

### 固定事实与条件写

| 存储 | 权威事实 |
|---|---|
| d | 本地 desired、live/deleted/absent、editToken、pin 和已知 wire metadata |
| m | 至多两个 source-generation slot 与最后源观察 metadata |
| c | 源进度、generation、bootstrap 完成及 partial 状态 |
| manifest | 原身份/源定义、绑定、active physical epoch、成员代次、issue/dirty marker/recoveryIntent |
| native metadata | assumed 与复制进度，继续由[原生运行时](2026-09-18-sdk-native-replication-runtime.md)维护 |

业务数据用 recursive typed JSON 字符串持久化，int64 不转 Number。逻辑 ID 保存于 hashed
physical key 旁并在读取时验证。业务删除与 absence 都不使用原生 _deleted；非 live 清空
payload，同 ID 可以立即重建。读出的业务内容来自 d，version/time 来自 m 的最新观察，
并不代表本地编辑 revision。成员与待处理本地工作保持可见，absence 始终不可见。

```text
alias shared -> 当前 epoch -> view exclusive -> d/m + 冻结条件
  -> 新 desired + token + pin -> raw CAS -> 409 重读重算，最多 8 次
```

所有 fork/control/pin 写入遵守相同锁顺序，get 使用 view shared。metadata-only 更新也
参与联合条件写的互斥；非冲突错误直接传播。锁不跨 HTTP 等待。编辑与 pin 同次提交，
不会在成功后异步补登记 pending。source generation 表示远端成员全集，physical epoch
表示本地整理换代，两者独立。

行与 manifest feed 只发视图失效提示。跨 tab 以持久化 manifest 为真相，source generation
或 physical epoch 改变都需要重读；本层不实现查询索引或动态查询结果。

### 准入、读取与容量

| 默认界限 | 数值 |
|---|---:|
| d/m/c 实际行，含原生系统字段 | 16 MiB |
| manifest | 34 MiB |
| native metadata，含嵌套记录包络 | 17 MiB |
| 原始数据读取池 | 64 MiB |
| 独立控制读取池 | 2 × manifest 上限，即 68 MiB |
| 底层一次有索引读取 | 至多 4 行，按池额度缩小 |
| native handoff 结果 | 128 MiB |
| known logical IDs | 每 alias 100,000，可配置 |

所有最终持久化入口，包括源状态应用、恢复、seed 和控制写，都检查实际行大小。recoveryIntent
是同一目标的两份 DataRecord，不接受无界任意 JSON。读取前预留额度，作用域内仍持有
的 snapshot 继续占额；物理扫描必须由主键索引提供有序 seek。bulk write 对底层隐式读取
当前行也分块预留额度；保留的写入输入和冲突结果受 native handoff 预算约束，每次委派
下一块之前按最坏行大小准入。额度不足明确失败，已成功块保持持久化，复制 checkpoint
不能推进；重试通过原有逐行 CAS 协调部分成功。上述界限不声称限制全局 JavaScript heap、
所有原生队列或尚未实现的查询缓存。

仅使用 collection 的 raw storage，不使用 RxDocument/RxQuery。关闭未使用的高层
change-event history 并同步排空 lazy document-cache tasks，保留真实 change feed。
这些行为依赖固定 RxDB 内部机制，升级时必须重新核对。

容量账目包括 d/m/c、manifest 和 native metadata。默认依赖 browser quota 与 known-ID
限制，不增加固定 512 MiB cap。配置有限字节 cap 后，写前用有界扫描重新核对跨 tab
实际字节；容量不足明确失败并保留 pending，不驱逐待处理工作。

### 干净整代回收

```text
stop/cancel/drain -> alias exclusive -> clean 检查 -> 单个 shadow
  -> native down seed + assumed/cp -> 校验 -> manifest CAS flip
  -> 删除旧 fork + 明确配对的 native metadata
```

clean 必须满足源已激活完成，无 staged generation/partial、pending 差异、pin、issue、
dirty marker 或 recoveryIntent。仅保留当前正常成员和源控制状态。seed 复用新代正常
replication identifier，重建 assumed 与本地上行 checkpoint，保留原 source checkpoint；
任何业务 Push 都使 seed 失败，避免复制行回声上传。

maintenance capability 在已有独占锁内完成 shadow 写入，不嵌套请求 shared 锁。30 秒没有
扫描或持久化进度才触发 seed 超时，不对大型集合设置 30 秒总时限。新旧代需要同时容纳。
manifest CAS 结果不确定时先重读，无法读出选择就保留两代；成功 flip 后和重开时清理
确认非 active 的 fork 及 paired metadata。清理完成前不建立第二个 shadow。

## Alternatives

**高层 incrementalModify：** 其异常队列不适合作为必须向调用者结算的写路径。raw storage
保留 RxDB revision/lwt/hooks，再由 SDK 显式 await 和处理 CAS 冲突。

**只对 d CAS：** 条件可能来自 m 的最后观察版本；读取 m 后单独提交 d 无法防止并发
metadata-only 更新改变条件。共享的 view-write 锁保护联合判断和提交。

**逐 ID 删除 native metadata：** 可能丢失 assumed，使旧状态重新变成待上传工作。
干净整代 seed 用原生协议构造基线，代价是复制和临时双份空间。

**仅返回读取结果后检查字节：** 分配已发生，无法约束大行批量读取。固定行准入加预读
reservation 限制单次物化，独立控制池避免 manifest 与业务记录相互挤占预算。

**按 RxDB 高层默认保留事件：** raw-only 消费者不会使用该历史，仍可能长期保留大文档。
主动关闭或排空相关缓存，代价是固定版本升级时需要核对内部行为。

## Consequences

- 私有存储可以离线读写与重开；自动网络同步、源成员协调和公开本地 API 仍需集成。
- 条件写、quota 和维护会显式失败；close/drain 失败保留可见错误，不能宣称安全切换账号。
- 取消等待不会解除凭据 drain 义务；正常 session 取消可完成关闭，真实 I/O 和清理故障仍阻止新凭据安装。
- 完整字节容量核对、clean 检查和 seed 都有扫描成本；没有性能或总 heap 的额外承诺。
- 远程窗口沿用普通 Query 的索引滞后模型，可能暂时退出后重入；本地成员协调不得据此
  删除 pending，具体应用属于下游复制集成。
- 浏览器双 tab 的存储和锁验证不等于断电持久性，也不等于真实服务端端到端复制。
