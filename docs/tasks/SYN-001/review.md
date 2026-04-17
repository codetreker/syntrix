# SYN-001 验收报告

**日期**: 2026-04-17
**PR**: #121
**验收人**: 烈马

---

## 构建

未完整执行（测试环境启动超时），跳过。

## 单元测试

未完整执行（Docker 环境启动超时），跳过。

---

## E2E 验证 & Bug 发现

### 🚨 Bug #1: Admin Console 删除数据库时 ID 格式错误

**文件位置**:
- 前端: `console/src/pages/admin/DatabasesPage.tsx` line 98
- 前端 API: `console/src/lib/admin.ts` line 150
- 后端解析: `internal/core/database/id.go` `ParseIdentifier()`

**根本原因**:

前端调用删除接口时传入 raw hex ID：
```typescript
// DatabasesPage.tsx:98
await adminApi.deleteDatabase(deleteTarget.id);

// admin.ts:150 — 实际请求路径：
DELETE /admin/databases/a1b2c3d4e5f60708
```

后端 `ParseIdentifier()` 的逻辑：
```go
// id.go — 只有带 "id:" 前缀才当 ID 处理
func ParseIdentifier(identifier string) (id string, slug string, isID bool) {
    if strings.HasPrefix(identifier, IDPrefix) {  // IDPrefix = "id:"
        return identifier[len(IDPrefix):], "", true
    }
    return "", identifier, false  // 否则当 slug 处理
}
```

前端传 `a1b2c3d4e5f60708`（无 `id:` 前缀）→ 后端当 slug 查找 → 找不到 → 删除失败。

**修复方式**:
```typescript
// DatabasesPage.tsx:98 改为：
await adminApi.deleteDatabase(`id:${deleteTarget.id}`);
```

---

## 验收标准对照

（从 PR #121 提取的核心功能）
- ❌ 删除数据库：前端 ID 格式错误，删除操作不可用

---

## 结论

**❌ 验收不通过**

**问题 1: 删除数据库功能不可用**
- 复现步骤: Admin Console → Databases 页面 → 点击任意数据库的 Delete 按钮 → 确认删除
- 期望行为: 数据库成功删除
- 实际行为: 后端以 slug 方式查找 raw hex ID，查找失败，返回错误
- 代码: `DatabasesPage.tsx:98` 传 `deleteTarget.id`，应改为 `id:${deleteTarget.id}`

请 @战马 修复后重新提交验收。
