# Agent Note: Replication Create 不覆盖正常文档

Status: implemented

## Problem

Push 的显式 `create` 在目标为正常文档时会进入更新路径。省略 version 或
version 相等的创建请求可以覆盖已有内容；version 不同则返回版本冲突。
创建意图因此取决于客户端是否携带版本，无法保护同 ID 的并发创建。

删除后允许创建同 ID 文档是既有需求。这里的 ID 是同一 database、同一
collection 路径下的逻辑文档名；tombstone 用来传递删除语义，不占用创建资格。

## Decision

在现有权威预读之后、通用版本比较之前，显式 `create` 遇到正常文档立即返回
`already_exists`，不执行写入。相同内容、相同版本也不构成创建成功。

| 目标状态 | 显式 create |
|---|---|
| 不存在 | 通过现有 Store.Create 创建 |
| tombstone | 通过现有 Store.Create 立即同 ID 重建 |
| 正常文档 | `already_exists`，保留原有内容及元数据 |

- 完整请求仍先校验；create 接受合法的非负 int64 version，但忽略其条件意义。
- update/delete 的可选 version、显式零、缺失目标处理保持原有规则。
- 初始读取和冲突重读仍使用权威写源并包含 tombstone。
- 原子插入及仅替换仍处于删除状态的目标继续由 Store.Create 保证。预读不加锁；
  并发创建先成功后，本次写入不能覆盖新出现的正常文档。
- 创建写入失败后保留现有一次权威重读。`missing`、`tombstoned` 或
  `already_exists` 描述随后观察到的状态；前两者不是禁止重新创建的规则。
  不自动重试创建，读取错误和非冲突写入错误继续传播。

本决策替代[条件写修复](2026-09-07-replication-push-version-checks.md)保留的
正常目标 create 转 update 行为；该决策仍拥有原子条件写和结构化冲突契约。
[insert-only 提案](../../rejected/feature/2026-09-07-replication-push-insert-only.md)
要求 tombstone 阻止创建并重新解释 version zero，与同 ID 重建需求冲突，因此拒绝。

## Alternatives

**对每个 create 无条件调用 Store.Create。** 这会让已经通过权威预读发现的冲突
仍发起写入，并在写入冲突后重读；若期间发生删除，还会改变本次尝试的结果。
现有预读可直接返回观察到的冲突，缺失和 tombstone 的读写竞争继续由原子创建保护。

**新增 absent/tombstone 创建 option，或用 version 区分两种创建。** 这会引入
观察 tombstone 版本、匹配删除状态后才可重建的额外策略。逻辑删除允许直接同 ID
重建，不需要这些条件或新的存储、传输接口。

**保持 create 转 update。** 这保留既有覆盖行为，但不能保证正常文档免于创建
请求覆盖；客户端显式选择 update 时仍可使用其现有更新语义。

## Consequences

- 只有显式 create 遇到正常目标的结果改变；HTTP/gRPC 格式、Store 接口和
  SDK 不变。依赖 create 更新正常文档的调用方需要使用 update。
- 保留 tombstone 与已经物理清理的目标均可创建，不需要观察删除版本或等待清理。
- 批次保持顺序执行、非事务性和冲突后继续处理；重复 ID 通过 changeIndex 区分。
- 成功响应丢失后，重试 create 可能返回 already_exists；相同内容不证明是同一次
  创建，不提供 exactly-once 或文档生命周期身份。

[Replication reference](../../../../docs/reference/replication.md#version-preconditions)
定义完整调用契约；[Gateway design](../../../../docs/design/server/gateway/replication.md#push)
描述执行顺序及存储职责。
