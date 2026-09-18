# Agent Note: SDK 原生复制运行时

Status: implemented

## Problem

离线复制需要在本地文档、复制元数据和 checkpoint 全部完成持久化后确认进度。
存储变慢时持续读取下游页，或把每次本地写入都放入内存队列，会让未完成工作随
输入增长。忽略 metadata 写入返回的错误、遗漏 checkpoint promise，又会让恢复
位置超过可靠数据，或让失败脱离复制实例的生命周期。

依赖安装时的补丁只影响开发工作区。若发布产物仍由消费者安装未修补的依赖，
开发环境中的可靠性保证无法随 SDK 交付。

## Decision

使用固定 RxDB `17.5.0` 的低层原生复制协议，SDK 拥有输入调度、适配器及生命周期。
fork 和 metadata 存储由调用方提供并负责最终关闭；协议本身保留持久化进度和
冲突处理职责。[副本 alias 存储](2026-09-18-sdk-replica-storage.md)提供这些存储的
身份、记录、准入与整代回收；本 note 继续拥有原生协议可靠性。公共本地数据库接口仍由
[离线复制提案](../../proposed/feature/2026-09-07-sdk-offline-replication.md)拥有。

源 checkpoint 以 `{ source: checkpoint }` 保存到原生下游 metadata。RxDB 会浅合并
checkpoint；稳定的外层键让整个源值替换前值，阶段变化时移除的字段也随之消失。
适配器读取及完成 hook 均接收解包后的源值，连续分页与新实例恢复保持相同语义。

### 原生可靠性补丁

| 位置 | 必须完成的动作 |
|---|---|
| 下游页 | 等待当前页文档、metadata、checkpoint 持久化，再读取下一页 |
| 双向 metadata | 检查批量写入返回的错误，包括冲突 metadata；失败立即停止 |
| checkpoint | 等待所有写入路径，包括空页、无变化、上行早返回 |
| fork 插入 | wrapper 保留输入 metadata 的下载来源；刷新 lwt 并保留 revision 生成和 hooks，文档先于 assumed 落盘时仍可安全恢复 |
| fatal | 先取消实例，再发布一次错误诊断；启动读取和异步队列的 rejection 也进入此路径 |
| 恢复 | 用已有可靠 metadata 创建新实例；失败实例的 promise 队列不复用 |

固定版本的源码、ESM 和 CJS 产物同步修补，避免入口选择改变失败行为。
上游基线为 npm gitHead `d88180e334512bf0097373ad62e9fbe6811010aa`；
[下游循环](https://github.com/pubkey/rxdb/blob/d88180e334512bf0097373ad62e9fbe6811010aa/src/replication-protocol/downstream.ts)、
[上游循环](https://github.com/pubkey/rxdb/blob/d88180e334512bf0097373ad62e9fbe6811010aa/src/replication-protocol/upstream.ts)、
[checkpoint 写入](https://github.com/pubkey/rxdb/blob/d88180e334512bf0097373ad62e9fbe6811010aa/src/replication-protocol/checkpoint.ts)
及 [storage wrapper](https://github.com/pubkey/rxdb/blob/d88180e334512bf0097373ad62e9fbe6811010aa/src/rx-storage-helper.ts)
是补丁复核的固定源码依据。

下载来源的标识和 revision 高度必须同时匹配当前 fork 行。部分 fork 插入或 assumed
写入失败后，新实例重放下载数据，不产生业务上传；真实本地编辑会推进 revision，
不能被旧来源标记吞掉。回归使用实际 collection wrapper 和持久化后重开的存储。

### 输入边界与初始化

```text
真实 storage change feed --> dirty 标志
                                |
                      原生双向处理空闲
                                |
                             RESYNC
                                |
                      有界持久化记录扫描
                                |
                       串行远程写入适配器
```

- 原生协议看到的 fork live feed 为空；真实 feed 仍供上层使用。装饰器显式组合
  storage 接口，不提供可绕过边界的 underlying storage 解包入口。
- dirty 只是唤醒提示。进程恢复仍扫描持久化记录和原生 checkpoint，不依赖内存队列。
- 每个新实例都等待一次新的源完成标记、原生持久化和完成 hook，随后开放上行。
  恢复得到的旧 checkpoint 本身不证明当前源已完成初始化。
- 未 ready 时 changed-docs 返回空记录和原 checkpoint；不能在远程写入 handler
  中等待下游 ready，否则原生双向等待可能死锁。
- 推进进度的源空页必须提供可识别的控制记录，让进度进入原生持久化；终止空页
  若与已持久化 checkpoint 相同则无需控制记录。源的短页不等于完成。
  控制记录冲突使实例失败，普通业务记录保留原生冲突处理。

### 扫描与生命周期

| 默认界限 | 值 |
|---|---:|
| 输出条数 | 50 |
| 目标编码 JSON 字节 | 8 MiB |
| 单行编码上限 | 16 MiB |
| 一次底层读取条数 | 4 |
| 实际远程写入适配器并发 | 1 |

合法大单行独立成页。块中只有部分记录能容纳时，从原块起点缩小 limit 重读并
重新计算大小；禁止截断返回记录后保留整块 checkpoint。超限行使本次读取失败，
不返回部分成功进度。这些是扫描 payload 界限，不是总 JS heap 或查询缓存承诺。
所有持久化入口的最终行大小校验由本地 alias 存储落实；HTTP 编码预算仍由网络适配器落实。
源适配器返回的文档数组另受默认 16 MiB 编码 JSON 上限约束；返回前的读取与
分配需要适配器自己设界，返回后的验证无法约束已经发生的分配。

停止先关闭新任务准入、取消 handler 信号及订阅，再等待已拥有的 storage 调用、
handler、完成 hook、调度器和原生队列。回调必须响应取消或最终结束，才能完成
drain；已经开始的底层存储操作不能通过取消信号回滚。
实例绑定覆盖 session、alias 和本代 native 生命周期的 owner signal，只将该已取消
signal 的同一原因或以其为 cause 的 Axios 取消识别为正常取消。alias 关闭与维护换代
先取消旧实例，再撤销其 scope；维护 seed 的所有权关联 alias 生命周期。正常关闭完成 drain
后成功返回；真实存储、
handler 或清理失败在 drain 后继续抛出，不能按异常类名统一忽略。

### 发布

| 交付对象 | 约束 |
|---|---|
| 构建依赖 | 固定版本、锁文件、补丁及校验和一同提交 |
| 本地运行时 | 将 patched RxDB、Dexie、RxJS 打入私有 lazy bundle |
| 远程客户端 | 不在导入时加载本地运行时 |
| 包边界 | 公共 exports 和声明不暴露 RxDB 对象；随包附第三方许可 |
| 构建验证 | 拒绝版本漂移、补丁缺失及残留 vendor external import |
| 消费者验证 | 隔离安装 packed tarball，仅使用产物运行故障回归，无工作区解析回退 |

## Alternatives

**直接使用未修补的原生协议。** 保留上游默认实现，但不能保证逐页背压、metadata
失败后的安全进度和 checkpoint rejection 的完整传播。

**只包装 storage，或改用高层 collection replication。** 高层仍经过相同原生循环；
storage 包装不能让已被原生忽略的 checkpoint promise 重新获得 await。

**只提交 pnpm patch。** 能修复工作区安装，但下游应用不会自动应用依赖包自身的
pnpm 补丁配置。将修补结果打入 SDK 后，消费者不必使用相同包管理器。

**新增 SDK Outbox。** 会形成第二套持久化投递与确认机制。原生 changed-docs 扫描
和 metadata 已承担这部分职责；SDK 保留协议转换及持久化完成边界。

## Consequences

- RxDB 升级需要重审补丁，重新执行原生故障回归和隔离消费者验证；依赖版本不能
  脱离补丁单独升级。
- 初始源完成前延迟首次上传，本地编辑仍可由上层持久化。源成员、generation 激活
  及 HTTP checkpoint 的含义由后续适配器实现，当前运行时不推断这些业务状态。
- 协议、memory 或 fake IndexedDB 验证不能替代完整浏览器端到端、跨 tab 所有权
  或断电持久性验证。当前没有公开 `openReplica`，也没有接通自动 HTTP Push。
- [SDK 复制设计](../../../../docs/design/sdk/002_replication_client.md)拥有运行时职责；
  [SDK reference](../../../../docs/reference/typescript_sdk.md#replica-availability)
  标明当前公共能力。
