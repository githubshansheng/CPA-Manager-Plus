# 07. 开发指南

## 7.1 环境要求

- Node.js：CI 使用 Node 24。
- npm：使用根目录 `package-lock.json`。
- Go：1.24.x。
- 可选：Docker/Compose，用于完整部署验证。
- Windows、macOS、Linux 均有原生控制脚本测试。

首次安装：

```bash
npm ci
```

Go 依赖：

```bash
cd apps/manager-server
go mod download
```

本轮在 Windows 上使用 Go 1.24.0 和 1.26.4 时，`modernc.org/sqlite` 的最小打开用例均返回
`SQL logic error: out of memory (1)`；这不是业务测试断言失败，也无法仅用切换临时目录解决。
在 Windows 兼容问题关闭前，后端发布门禁应使用 Linux CI 或容器，并保留 Windows CI 用于
复现。`data.key` 的 `0600` 测试同样属于 POSIX 语义，Windows 应改为验证 NTFS ACL。

## 7.2 常用命令

| 命令 | 用途 |
| --- | --- |
| `npm run dev` | 启动 Web Vite 开发服务器 |
| `npm run dev:demo` | 启动 Demo 模式 |
| `npm run build` | TypeScript 检查并构建生产单文件 |
| `npm run build:demo` | 构建 Demo |
| `npm run type-check` | TypeScript 类型检查 |
| `npm run lint` | ESLint |
| `npm run test` | Web 与仓库级 Vitest |
| `npm run manager-server:test` | Go 全量测试 |
| `npm run check:demo-isolation` | 检查生产包不含 Demo fixtures |
| `npm run docs:dev` | 启动 VitePress 用户文档 |
| `npm run docs:build` | 构建 VitePress 用户文档 |

后端高风险改动还应在 `apps/manager-server` 执行：

```bash
go test -race ./...
go vet ./...
```

## 7.3 本地运行模式

### 只运行前端并直连 CPA

```bash
npm run dev
```

在登录页填写 CPA 地址和 CPA Management Key。

适合：

- CPA 配置页；
- 认证文件；
- Provider；
- 插件；
- 直连兼容性。

不适合验证 Manager Server 历史数据和自动化。

### 运行 Manager Server

Manager Server 默认监听 `0.0.0.0:18317`，常用最小环境变量：

```text
CPA_UPSTREAM_URL=http://127.0.0.1:8317
CPA_MANAGEMENT_KEY=<CPA management key>
CPA_MANAGER_ADMIN_KEY=<manager admin key>
USAGE_DATA_DIR=./data
```

构建并运行：

```bash
npm run build
cd apps/manager-server
go run ./cmd/cpa-manager-plus
```

若直接 `go run`，嵌入的是仓库中已有 `internal/httpapi/web/management.html`。验证最新前端时，需要按 Docker 构建链把 `apps/web/dist/index.html` 复制成该文件，或通过 `PANEL_PATH` 指向构建结果。

### Docker

```bash
docker compose -f docker-compose.manager.yml up --build
```

使用 Docker secret 或外部 secret 管理系统提供 CPA Key、Admin Key 和 Data Key，不要写入镜像或提交到仓库。

## 7.4 如何定位一个前端功能

推荐顺序：

1. 从 [appRoutes.tsx](../../apps/web/src/app/appRoutes.tsx) 或 [MainRoutes.tsx](../../apps/web/src/router/MainRoutes.tsx) 找路由。
2. 跟到 `src/pages` 的薄适配文件。
3. 进入对应 `src/features/<domain>`。
4. 查找使用的 store/hook。
5. 跟到 `src/services/api`。
6. 根据 URL 对照 [api-inventory.md](api-inventory.md) 判断后端是 CPA、自有接口还是代理。

示例：

```text
UsageAnalyticsPage
 -> features/usage-analytics/UsageAnalyticsPage.tsx
 -> services/api/usageService.ts
 -> GET /v0/management/usage
 -> manager-server usage controller
 -> usage service/repository
```

## 7.5 如何定位一个后端接口

1. 从 [router.go](../../apps/manager-server/internal/http/router/router.go) 判断匹配顺序。
2. 查看 `internal/http/controller/<domain>/handler.go`。
3. 跟到 `internal/service/<domain>`。
4. 找到使用的 repository 或 CPA adapter。
5. 若由 worker 触发，再查 `internal/worker`。
6. 对照同目录 `*_test.go` 确认兼容行为。

特别注意：没有命中自有路由的 `/v0/management/*` 会进入 CPA proxy，不能仅凭 URL 前缀判断处理方。

## 7.6 新增 Manager Server 自有接口

建议流程：

1. 明确它是否真的属于 Manager Server，而不是 CPA。
2. 在独立领域 controller 定义 method 和输入。
3. 使用 `AuthorizePanel` 或更严格的 `AuthorizeAdmin`。
4. service 接受领域 DTO，不直接接受 `http.Request`。
5. repository 使用参数化 SQL。
6. 在 `rootHandler` 的通用 proxy 之前注册。
7. 增加未认证、错误 method、正常和边界测试。
8. 在 Web API 领域模块增加类型和客户端。
9. 更新接口清单和用户文档。

### 认证选择

- 只读面板接口：通常 `AuthorizePanel`。
- 写配置、重置、初始化等敏感操作：`AuthorizeAdmin` 或 `VerifyHeader`。
- 首次配置前需要公开的信息：返回最小字段，不能包含 secret。

## 7.7 修改 CPA 代理

代理修改需要检查：

- 是否保留 method、path、query 和 body；
- 是否正确替换 Authorization；
- 是否错误转发 Admin Key；
- 是否允许任意非 management 路径；
- 插件路径是否需要重写 `management_origin`；
- 目标地址是否经过安全策略；
- 上游错误是否泄露内部信息。

代理兼容测试位于：

- [server_compat_test.go](../../apps/manager-server/internal/httpapi/server_compat_test.go)
- [service/proxy/service_test.go](../../apps/manager-server/internal/service/proxy/service_test.go)

## 7.8 修改使用事件解析

任何新字段或兼容格式都应：

1. 在 `usage.Event` 增加明确字段。
2. 在 `NormalizeRaw` 兼容旧字段。
3. 判断是否需要 SQLite 列。
4. 修改 insert/select/export/import。
5. 更新脱敏逻辑。
6. 检查 rollup 和监控是否需要该字段。
7. 增加 raw fixture 测试。
8. 验证重复 `event_hash` 不 fanout。

若无法解析，不要静默丢弃；应进入 dead letter，并确保 payload 有长度和敏感性控制。

## 7.9 修改 SQLite

- 使用参数化 SQL。
- schema 扩展先加列，再切换读写。
- 大回填不要在单个启动事务中无限执行。
- 派生表优先清空后从 `usage_events` 重建。
- 对唯一索引变化增加迁移测试。
- 增加旧 schema fixture 或 `pragma table_info` 测试。
- 评估备份、降级和 data key 兼容。

## 7.10 前端编码约束

仓库约定：

- Prettier：2 空格、单引号、分号、100 列。
- React 组件和页面用 PascalCase。
- hooks/stores 使用 `useXxx`。
- 优先 `@/` alias。
- feature/component 不依赖 page。
- 页面展示文本进入 i18n，不在组件硬编码多语言副本。
- Demo API 分支必须受 `__DEMO_SITE__` 和 `isDemoMode()` 保护。

不要对整个仓库执行无关格式化，以免造成大面积差异。

## 7.11 完成定义

一个功能至少满足：

- 正常和错误路径有测试；
- 类型检查、lint 和相关测试通过；
- 生产与 Demo 行为明确；
- Manager/CPA 双模式不被误伤；
- 数据迁移和回滚已说明；
- 未新增明文 secret 或原始响应正文；
- 接口、运维或用户行为变化已更新文档；
- 单文件构建和 Demo 隔离仍通过。
