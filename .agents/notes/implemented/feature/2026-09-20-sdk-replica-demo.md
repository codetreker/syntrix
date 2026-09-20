# Agent Note: Browser Replica Demo

Status: implemented

## Problem

现有双面板 demo 通过 REST 写入和 WebSocket 事件展示变化，无法演示新版 SDK 的本地持久化、查询 watch、离线编辑和同步生命周期。自动注册账号也不能满足复制源要求的数据库权限。

## Decision

保留 `example/realtime-demo`，用公开的 `openReplica` API 重写其数据流。运行方法由 [demo README](../../../../example/realtime-demo/README.md) 维护。

| 责任 | 决策与原因 |
|---|---|
| 本地数据 | 每个面板使用独立副本名；同一账号的两个面板也必须经过服务器同步 |
| 查询 | 完整复制集合，本地按 `sentAt` 降序 watch 最新 20 条；消息列表代表当前结果集 |
| 写入 | 调用副本 collection 的 `add`，明确显示本地保存与远端同步的区别 |
| 同步 | 消费 SDK 状态并提供暂停、恢复和只读检查；1 秒轮询便于观察演示 |
| 账号 | 显式登录已有 owner 或 db_admin；access token 仅保存在标签页 sessionStorage，支持刷新后的离线打开 |
| 生命周期 | 关闭保留数据；退出清除保存的身份；失败关闭保留句柄用于重试，异步回调受当前客户端和视图身份约束 |
| 运行 | 启动器构建 SDK 与 demo，并在指定 loopback 端口服务前端；后端就绪和权限配置由操作者负责 |

## Alternatives

**继续使用 REST 写入与 WebSocket 事件列表。** 可以保留原演示路径，但不能展示待同步写入、离线查询或关闭后的本地持久化，无法作为副本 API 的使用示例。

**由启动器管理后端并自动注册账号。** 旧脚本依赖过时的服务参数和基础设施配置，还会终止占用端口的进程。复制源要求匹配数据库的授权，脚本不能替操作者决定账号权限或宣告数据库已停止写入以执行 bootstrap。

**持久化登录时的 refresh token。** 公开的刷新回调只提供新 access token，无法持续维护旋转后的 refresh token。demo 保存 access token，并在重新加载后需要在线续期时要求重新登录。

## Consequences

- 示例只使用公开 SDK，构建保留副本运行时的懒加载 chunk；CI 覆盖类型检查和打包。
- 页面明确呈现本地保存、初始化、pending、暂停和阻塞；不会自动重试不确定写入或丢弃变化。
- 真正的查询错误后明确标记 watch 已停止并保留最后快照；重新打开会启动新查询。正常视图竞争由 SDK 有界退让，真实资源限制仍保持，见[查询竞争修复](../bug-fix/2026-09-20-replica-watch-contention.md)。
- 本地数据离线可用不意味着应用静态资源离线可用；没有引入 Service Worker。内容哈希 chunk 使用长期缓存以支持刷新后的离线重开，浏览器仍可清理缓存。
- access token 可由同源脚本读取。标签页退出会清除保存的身份，但浏览器中的账号隔离数据保留。
- `sentAt` 使用客户端时间；其排序用于演示，不作为服务端提交顺序。
