# 01. 项目概览

## 1.1 项目定位

CPA Manager Plus 为 CLIProxyAPI（下文简称 CPA）提供可视化管理和增强型数据服务。其能力分为两层：

- **CPA 管理面板**：配置、认证文件、模型、OAuth、插件、日志等操作最终由 CPA Management API 执行。
- **Manager Server 增强服务**：独立管理员认证、CPA 凭证保管、使用量采集、SQLite 历史数据、价格、监控、仪表盘、账号动作和 Codex 巡检。

这两个层级在前端使用相似的 URL，但认证主体和实际处理方不同，是理解项目的第一关键点。

## 1.2 技术栈

| 层级 | 技术 | 主要职责 |
| --- | --- | --- |
| Web | React 19、TypeScript 6、Vite 8 | 管理界面、状态管理、请求编排 |
| Web 状态 | Zustand | 认证、配置、配额、主题、语言、服务状态 |
| Web 网络 | Axios | Bearer Key 注入、错误归一化、版本头处理 |
| Web 路由 | React Router Hash Router | 单文件静态面板和内嵌场景兼容 |
| 图表/编辑器 | ECharts、CodeMirror、YAML | 监控图表、配置编辑与差异比较 |
| Manager Server | Go 1.24、`net/http` | HTTP 服务、代理、采集、业务服务和 worker |
| 存储 | SQLite，WAL 模式 | 使用事件、配置、价格、巡检和自动化状态 |
| 文档站 | VitePress | 用户文档站点，源码位于 `apps/docs/` |
| 构建 | npm workspaces、Docker multi-stage | 单文件前端、Go 二进制和容器镜像 |

依赖入口见 [package.json](../../package.json)、[apps/web/package.json](../../apps/web/package.json) 和 [apps/manager-server/go.mod](../../apps/manager-server/go.mod)。

## 1.3 仓库结构

```text
CPA-Manager-Plus/
├─ apps/
│  ├─ web/                 React 管理面板
│  ├─ manager-server/      Go Manager Server
│  └─ docs/                VitePress 用户文档
├─ bin/                    安装、原生包和发布脚本
├─ docs/                   仓库级设计、迁移、发布和本分析资料
├─ tests/                  仓库级 Vitest 与架构约束测试
├─ Dockerfile.manager-server
├─ docker-compose.manager.yml
└─ package.json
```

### Web 分层

| 目录 | 作用 |
| --- | --- |
| `src/app` | 应用壳、路由创建、生命周期 |
| `src/pages` | 路由适配层，原则上保持薄 |
| `src/features` | 具体业务页面和业务组件 |
| `src/components` | 通用 UI、布局、图表、Provider 编辑组件 |
| `src/entities` | 可复用领域模型与解析逻辑 |
| `src/services` | API、存储和外部能力适配 |
| `src/stores` | Zustand 状态 |
| `src/utils` | 纯函数、格式化、配额解析、兼容逻辑 |

仓库测试要求 `features` 和 `components` 不反向依赖 `pages`。新增页面时应让 `pages/XxxPage.tsx` 只转出对应 feature。

### Manager Server 分层

| 目录 | 作用 |
| --- | --- |
| `cmd/cpa-manager-plus` | 进程入口、日志、HTTP/pprof 启停 |
| `internal/app` | 依赖装配和应用上下文 |
| `internal/http` | controller、middleware、router、response |
| `internal/service` | 业务用例和外部 CPA 协作 |
| `internal/repository` | SQLite 读写和聚合查询 |
| `internal/worker` | 采集 fanout、rollup、账号动作、巡检 |
| `internal/collector` | 采集模式选择和运行状态 |
| `internal/httpqueue` | CPA HTTP 使用量队列客户端 |
| `internal/resp` | RESP subscribe/pop 客户端 |
| `internal/usage` | 事件模型、规范化、导入和脱敏 |
| `internal/security` | 管理员凭证、数据密钥和 AES-GCM |
| `internal/store` | repository 聚合和旧调用兼容门面 |

## 1.4 功能域

1. **登录与运行模式识别**：判断目标是 Manager Server 还是 CPA。
2. **CPA 配置管理**：读取和修改 CPA Management API。
3. **认证文件与 OAuth**：浏览、启停、删除和重新授权。
4. **AI Provider 配置**：Claude、Codex、Gemini、OpenAI 等配置编辑。
5. **使用分析**：事件列表、聚合统计、导入导出和模型成本。
6. **监控中心**：事件分析、响应头快照、账号历史和动作候选。
7. **配额与自动化**：从请求或 CPA 认证文件获取配额，按规则禁用/恢复账号。
8. **Codex 巡检**：批量探测账号并执行 keep、disable、enable、delete、reauth 等动作。
9. **插件系统**：管理接口和插件资源由 Manager Server 透明代理。
10. **发布与部署**：单 HTML、原生包、Docker、GitHub Pages Demo 和 VitePress 文档。

## 1.5 代码复杂度热点

以下文件同时承担较多解析、状态或编排职责，适合优先拆分，但不适合一次性重写：

| 文件 | 当前规模 | 主要问题 |
| --- | ---: | --- |
| `services/api/usageService.ts` | 约 2.1k 物理行 | 类型、归一化、Demo、URL、请求实现集中 |
| `features/usage-analytics/UsageAnalyticsPage.tsx` | 约 3.8k 物理行 | 查询状态、图表、表格、导入导出和价格交织 |
| `features/authFiles/AuthFilesPage.tsx` | 约 2.0k 物理行 | 列表、动作、OAuth、过滤和响应处理耦合 |
| `components/layout/MainLayout.tsx` | 约 1.0k 物理行 | 导航、能力检测、响应式布局和插件菜单集中 |
| `service/monitoring/service.go` | 约 2.7k 物理行 | 请求校验、查询、聚合、兼容输出集中 |
| `service/codexinspection/service.go` | 约 2.4k 物理行 | 状态机、并发探测、动作执行和日志集中 |

具体拆分方式见 [06-refactoring-blueprint.md](06-refactoring-blueprint.md)。

## 1.6 当前实现的优点

- 已形成 Go controller/service/repository 的基本边界。
- 采集器具备多协议回退和状态可观测性。
- `event_hash` 唯一索引防止重复事件破坏统计。
- SQLite 使用 WAL、FULL synchronous、busy timeout 和外键。
- 生产面板与 Demo fixtures 在构建阶段隔离，并有检查脚本。
- Manager Server 能托管单文件面板，部署面较小。
- 大量关键兼容行为已有 Go/Vitest 测试覆盖。

## 1.7 当前主要技术债

- Web 大文件包含过多领域与展示职责，改动回归面偏大。
- `usageService.ts` 同时承担 DTO、兼容解析和 HTTP 客户端职责。
- `store.Store` 继续暴露大量转发方法，模糊 repository 所有权。
- `monitoring` 和 `codexinspection` 服务体积过大，难以隔离测试。
- 安全默认值和密钥生命周期仍有明显改进空间。
- 依赖与 CI action 使用浮动版本，构建可重复性不足。
