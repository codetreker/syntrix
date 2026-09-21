# Replica Demo

通过两个独立的本地副本演示新版 TypeScript SDK：本地写入、动态查询、持久化和自动 HTTP replication。目录继续使用 `realtime-demo`。

## 启动

需要 Bun、pnpm **10.34.5**，以及已配置好的 Syntrix 服务。浏览器需要 IndexedDB、Web Locks 和 Web Crypto；通过 localhost 或 HTTPS 打开页面。

```bash
cd example/realtime-demo
./start-demo.sh
# 使用其它前端端口
./start-demo.sh --port 3001
```

脚本安装锁定依赖、构建 SDK、刷新 demo 的本地包依赖，再进行类型检查和分块构建。前端默认监听 `http://127.0.0.1:3000/`，按 Ctrl+C 关闭。端口被占用会报错。

| 页面配置 | 要求 |
|---|---|
| API endpoint | 已运行的服务地址；默认当前页面主机的 8080 端口 |
| Database | 已存在的数据库名称；默认 `default` |
| Collection | 用于演示的集合路径；默认 `messages` |
| Username / Password | 已有的数据库 owner 或具有该数据库 `db_admin` 权限的账号；两个面板可以使用同一账号 |

服务须允许页面来源的跨域请求，并且 Query、Puller 和 Indexer 已就绪。基础设施配置见[开发环境](../../deployment/dev/README.md)；新存储的受控初始化见[索引存储操作说明](../../docs/design/server/indexer/04.storage.md)。启动脚本只负责前端，服务的配置、账号和初始化由操作者管理。

## 操作

1. 填写服务范围和两个面板的账号，点击 **Open both replicas**。
2. 等待两个面板显示 **Complete source received**。本地打开完成与远端初始化完成是不同状态。
3. 在任一面板输入消息并点击 **Save locally**。消息先出现在本地查询结果，随后通过服务同步到另一个面板。
4. 点击 **Pause sync** 后继续写入，观察本地结果与 pending 数量；点击 **Resume sync** 后等待另一个面板收到消息。
5. 点击 **Close** 再次打开，验证浏览器中保留的数据。账号或服务范围改变前，先关闭副本。

| 状态或操作 | 含义 |
|---|---|
| Saved locally | 本地持久化已完成；不是远端提交回执 |
| Pending local changes / Protected edits | SDK 报告的待同步变化和受保护的本地编辑 |
| Idle between refreshes | 当前空闲，下一轮仍会继续拉取；不是永久一致性承诺 |
| Synchronization blocked | 需要查看错误及恢复信息 |
| Inspect | 只读查看问题数量、受保护目标和持久化阶段 |
| Local query stopped | 本地 watch 因错误终止，列表保留最后快照；关闭并重新打开以启动新查询，持续超限时仍会报错 |
| Close | 释放当前副本句柄，保留本地数据、pending 和本标签页的登录身份 |
| Sign out | 关闭副本并清除本标签页保存的 access token；保留按账号隔离的本地数据 |

两个面板分别使用 `demo-panel-1-<collection>` 和 `demo-panel-2-<collection>` 作为副本名，因此同一账号也能演示经过服务器的同步。SDK 另行隔离 endpoint、database 和账号；浏览器 origin 变化也会使用不同的本地存储。

## 离线与登录身份

- 页面已加载且至少打开过副本后，可以断网写入、关闭及重新打开本地数据。
- 本标签页的 `sessionStorage` 保存 access token、用户名和服务范围；密码和 refresh token 不写入浏览器持久化存储。运行期间 SDK 的 token 刷新会更新保存的 access token。
- 刷新页面后，可以不填写密码，使用匹配的已保存身份打开本地副本。过期 access token 仍可用于离线打开；恢复在线同步时需要重新登录。
- 离线能力针对本地副本。页面没有 Service Worker；断网时硬刷新或导航仍取决于浏览器是否能加载 HTML 和 JavaScript。带内容哈希的运行时 chunk 使用长期缓存，但浏览器可能清理缓存。测试刷新后的离线重开时，先在线打开过副本以缓存运行时，在线刷新页面，再断网打开已保存身份。
- 写请求发出后连接中断，结果可能不确定。demo 会显示阻塞状态并保留变化；明确的恢复流程见 [SDK 同步与恢复](../../docs/reference/typescript_sdk.md#synchronization-status-and-recovery)。

## SDK 数据流

```text
Panel 1 local add -> persisted replica -> HTTP Push -> server
                                                       |
Panel 2 local watch <- persisted replica <- HTTP Pull <--+
```

核心 API：

```typescript
const replica = await client.openReplica({
  name: 'demo-panel-1-messages',
  collections: { messages: client.replicate<Message>('messages') },
  sync: { pollIntervalMs: 1000 },
});
const messages = replica.collection<Message>('messages');
const stopWatch = messages.orderBy('sentAt', 'desc').limit(20).watch(render, onError);
await messages.add({ text, sender, panel: 1, sentAt: Date.now() });
```

源复制覆盖整个集合，本地查询按 `sentAt` 降序显示最新 20 条消息。`sentAt` 是客户端生成的毫秒时间，只用于演示排序，不代表服务端提交顺序。demo 将轮询间隔设为 1 秒；SDK 默认是 10 秒。界面消费当前查询快照，并以文本渲染消息。

查询继续遵守 SDK 的资源限制。正常同步控制更新不会要求重建；真实视图竞争会有界退让并保持 watch。稳定查询的扫描、内存或输出超限仍可触发 `QueryBudgetExceeded` 并停止对应 watch；查询状态与同步状态分别显示，错误不会自动丢弃本地变化。

应用仅导入 `@syntrix/client` 的公开 API。构建保留懒加载运行时的独立 chunk；打开副本时才加载该运行时。

## 开发检查

SDK 已构建后，在此目录执行：

```bash
bun install --frozen-lockfile --force
bun run build
```

`--force` 刷新 Bun 复制的本地 SDK 包；`build` 包含类型检查。SDK CI 同时检查此 demo 的构建。
