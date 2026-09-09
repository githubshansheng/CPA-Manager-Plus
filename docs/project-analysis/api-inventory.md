# Manager Server API 清单

> 以 [router.go](../../apps/manager-server/internal/http/router/router.go) 和各 controller 为准。
> `Admin/Panel` 表示通过 Manager Server 管理员/兼容面板认证；`公开探测` 仍只应返回最小信息。

## 独立入口

| Method | Path | 处理方 | 认证 | 说明 |
| --- | --- | --- | --- | --- |
| GET | `/health` | Manager | 无 | 存活检查 |
| GET | `/status` | Manager | Admin/Panel | 数据量和 collector 状态 |
| GET | `/usage-service/info` | Manager | 无 | 登录页识别服务和 bootstrap 状态 |
| GET | `/usage-service/config` | Manager | 已配置后需认证 | 读取脱敏 Manager 配置 |
| PUT | `/usage-service/config` | Manager | Admin | 校验并保存 Manager 配置 |
| GET | `/usage-service/account-processing-policy` | Manager | 已配置后需认证 | 自动化策略状态 |
| PATCH | `/usage-service/account-processing-policy` | Manager | Admin | 更新策略并 reload runtime |
| GET | `/usage-service/quota-cooldowns` | Manager | Admin/Panel | 只读 active cooldown 提示 |
| POST | `/setup` | Manager | Admin | 首次/兼容初始化 |
| GET | `/management.html` | Manager | 无 | 外部或嵌入式单文件面板 |
| GET | `/` | Manager | 无 | 307 到 `/management.html` |

## 使用量

| Method | Path | 认证 | 说明 |
| --- | --- | --- | --- |
| GET | `/v0/management/usage` | Admin/Panel | 兼容使用量 JSON |
| GET | `/v0/management/usage/export` | Admin/Panel | NDJSON 导出，不含 `fail_body` |
| POST | `/v0/management/usage/import` | Admin/Panel | JSON/JSONL 导入，最大 64 MiB |

## 模型价格

| Method | Path | 认证 | 说明 |
| --- | --- | --- | --- |
| GET | `/v0/management/model-prices` | Admin/Panel | 价格列表 |
| PUT | `/v0/management/model-prices` | Admin/Panel | 全量替换/保存 |
| POST | `/v0/management/model-prices/sync` | Admin/Panel | 从配置来源同步 |
| GET | `/v0/management/model-prices/usage-summary` | Admin/Panel | 价格配置与使用模型摘要 |

## API Key 别名

| Method | Path | 认证 | 说明 |
| --- | --- | --- | --- |
| GET | `/v0/management/api-key-aliases` | Admin/Panel | 别名列表 |
| PUT | `/v0/management/api-key-aliases` | Admin/Panel | 保存别名，可清理孤儿 |
| DELETE | `/v0/management/api-key-aliases/{apiKeyHash}` | Admin/Panel | 删除别名 |

## 账号动作候选

| Method | Path | 认证 | 说明 |
| --- | --- | --- | --- |
| GET | `/v0/management/account-action-candidates` | Admin/Panel | 支持 `status`、`limit` |
| POST | `/v0/management/account-action-candidates/{id}/ignore` | Admin/Panel | 忽略 |
| POST | `/v0/management/account-action-candidates/{id}/resolve` | Admin/Panel | 标记已解决 |
| POST | `/v0/management/account-action-candidates/{id}/enable` | Admin/Panel | 通过 CPA 启用账号 |
| DELETE | `/v0/management/account-action-candidates/{id}/auth-file` | Admin/Panel | 删除 CPA 认证文件 |

## Codex 巡检

| Method | Path | 认证 | 说明 |
| --- | --- | --- | --- |
| POST | `/v0/management/codex-inspection/run` | Admin/Panel | 启动人工巡检 |
| GET | `/v0/management/codex-inspection/runs` | Admin/Panel | 任务列表，支持 `limit` |
| GET | `/v0/management/codex-inspection/runs/{id}` | Admin/Panel | run、results 和 logs |
| POST | `/v0/management/codex-inspection/runs/{id}/actions` | Admin/Panel | 执行选定人工动作 |

## Dashboard 与监控

| Method | Path | 认证 | 说明 |
| --- | --- | --- | --- |
| GET | `/v0/management/dashboard/summary` | Admin/Panel | 必填 `today_start_ms` |
| POST | `/v0/management/monitoring/analytics` | Admin/Panel | 时间范围、过滤和 include |
| POST | `/v0/management/monitoring/account-history` | Admin/Panel | 最多 200 个账号 |
| GET | `/v0/management/monitoring/header-snapshots` | Admin/Panel | 可选 `days`、`limit` |

## CPA 透明代理

以下请求在自有路由未命中后转发到保存的 CPA upstream：

| Method | Path | 浏览器认证 | 上游认证 | 说明 |
| --- | --- | --- | --- | --- |
| 任意 | 其余 `/v0/management/*` | Admin/Panel | 保存的 CPA Management Key | 配置、认证文件、OAuth、日志、插件等 |
| GET | `/v1/models` | Admin/Panel | 保留调用方头 | 模型列表代理 |
| GET | `/models` | Admin/Panel | 保留调用方头 | 兼容模型列表 |
| 任意 | `/v0/resource/plugins/*` | Admin/Panel | 依代理入口决定 | 插件页面和资源 |
| 任意 | 未占用的插件 management head | Admin/Panel | 保存 Key 或调用方认证 | 插件自定义管理 API |

`ProxyModelList` 不注入保存的 CPA Management Key；其他 management proxy 默认替换 Authorization。修改该差异前必须确认 CPA 和插件兼容需求。

## 路由扩展规则

新增 Manager 自有 `/v0/management/<domain>` 时：

1. 在通用 proxy 分支之前注册。
2. 将 head 加入 proxy 的内置保留列表，避免被识别为插件 management path。
3. 增加自有接口和透明代理各自的回归测试。
4. 更新本清单。
