# CPA Manager Plus 项目分析与重构资料

> 审计基线：提交 `79d681c5771b536d2517a36cdcafb04f3930402e`
> 审计日期：2026-07-13
> 范围：源码、配置、构建、测试、依赖锁文件与完整 Git 历史规则扫描

本目录面向后续开发、重构、安全整改和交接，重点回答以下问题：

1. 项目由哪些运行时组成，启动后如何协作。
2. 前端请求如何在 CPA、Manager Server 和 Demo 之间分流。
3. Manager Server 如何认证、代理、采集、持久化并派生统计数据。
4. 哪些代码是核心兼容边界，重构时不能直接打破。
5. 当前开源发布是否存在密钥、敏感数据、供应链或部署边界风险。

## 阅读顺序

| 文档 | 适用场景 |
| --- | --- |
| [01-project-overview.md](01-project-overview.md) | 快速了解项目定位、技术栈、目录和能力边界 |
| [02-architecture-and-runtime.md](02-architecture-and-runtime.md) | 理解三种前端运行形态、Manager Server 启动与代理架构 |
| [03-api-call-chain.md](03-api-call-chain.md) | 追踪登录、配置、CPA 代理、监控和自动化接口链路 |
| [04-core-modules-and-data-flow.md](04-core-modules-and-data-flow.md) | 理解采集器、事件规范化、幂等写入、fanout 和业务模块 |
| [05-data-model-and-storage.md](05-data-model-and-storage.md) | 查看 SQLite 表、敏感字段、迁移和备份要求 |
| [06-refactoring-blueprint.md](06-refactoring-blueprint.md) | 按阶段执行渐进式重构 |
| [07-development-guide.md](07-development-guide.md) | 搭建环境、定位代码、开发功能和自测 |
| [08-security-and-open-source-risk-audit.md](08-security-and-open-source-risk-audit.md) | 查看开源泄露与安全审计结论 |
| [09-testing-release-and-operations.md](09-testing-release-and-operations.md) | 测试矩阵、构建发布、部署和运维检查 |
| [10-production-deployment-safety.md](10-production-deployment-safety.md) | 数据库升级不变量、上线门禁、验收和回滚手册 |
| [api-inventory.md](api-inventory.md) | 查询 Manager Server 自有与代理接口清单 |
| [risk-register.md](risk-register.md) | 按优先级跟踪整改项 |

## 一句话架构结论

CPA Manager Plus 是一个可单独直连 CPA、也可由 Go Manager Server 托管的 React 管理面板。Manager Server 同时承担静态面板托管、管理员认证、CPA Management API 反向代理、使用量队列采集、SQLite 持久化、监控聚合和账号自动化，因此它不是简单的“前端 BFF”，而是带数据处理能力的控制平面。

## 必须保留的兼容契约

- 前端支持直连 CPA、Manager Server 内嵌面板和 Demo fixtures 三种运行形态。
- Manager Server 自有接口使用管理员 Key；转发到 CPA 时使用服务端保存的 CPA Management Key。
- Manager Server 先匹配自有 `/v0/management/*`，再代理剩余同前缀接口。
- 使用量采集保留 `RESP subscribe -> HTTP queue -> RESP pop` 的自动回退。
- `usage_events.event_hash` 继续作为幂等键，fanout 只处理真正新增的事件。
- 保留旧配置、旧导入字段和旧 URL 的兼容读取，迁移必须可回滚。
- 生产构建仍需生成单文件 `management.html`，Demo fixtures 不得进入生产包。
- 前端 `features`、`components` 不得依赖 `pages`，现有架构测试会检查该约束。

## 审计边界

本资料是静态代码、配置和依赖审计，不等价于：

- 对真实部署环境的渗透测试；
- 对容器、主机、反向代理和云权限的完整审计；
- 法律、许可证或隐私合规意见；
- 使用专业工具完成的完整密钥历史扫描。

本轮在将缓存和临时目录重定向到项目盘后完成了 `npm audit`、`govulncheck` 和
`gitleaks` 全历史扫描。`gitleaks` 的 33 条命中均已核对为测试或 Demo 合成数据，未发现
真实高置信度密钥；npm 与 Go 扫描发现了需要整改的依赖和工具链风险。动态渗透、真实部署
权限和容器制品扫描仍不在本次范围内，详细结果与限制见安全审计文档。

## 关键源码入口

- 前端入口：[apps/web/src/main.tsx](../../apps/web/src/main.tsx)
- 前端路由：[apps/web/src/app/appRoutes.tsx](../../apps/web/src/app/appRoutes.tsx)
- Axios 客户端：[apps/web/src/services/api/client.ts](../../apps/web/src/services/api/client.ts)
- Manager Server 入口：[apps/manager-server/cmd/cpa-manager-plus/main.go](../../apps/manager-server/cmd/cpa-manager-plus/main.go)
- 应用装配：[apps/manager-server/internal/app/app.go](../../apps/manager-server/internal/app/app.go)
- HTTP 路由：[apps/manager-server/internal/http/router/router.go](../../apps/manager-server/internal/http/router/router.go)
- SQLite 迁移：[apps/manager-server/internal/repository/sqlite/migrate.go](../../apps/manager-server/internal/repository/sqlite/migrate.go)
- 使用量规范化：[apps/manager-server/internal/usage/event.go](../../apps/manager-server/internal/usage/event.go)
