# 09. 测试、发布与运维

## 9.1 测试矩阵

| 层级 | 命令 | 关注点 |
| --- | --- | --- |
| TypeScript | `npm run type-check` | DTO、组件、API 类型 |
| ESLint | `npm run lint` | Hooks、未使用代码和规则 |
| Web/仓库测试 | `npm run test` | 组件、解析器、架构和脚本 |
| Web 构建 | `npm run build` | 单文件生产包 |
| Demo 构建 | `npm run build:demo` | Demo 路由和 fixtures |
| Demo 隔离 | `npm run check:demo-isolation` | 生产包不含 Demo 数据 |
| Go 测试 | `npm run manager-server:test` | API、service、repository、worker |
| Go race | `go test -race ./...` | worker、状态和并发 |
| Go vet | `go vet ./...` | 常见 Go 静态问题 |
| 文档 | `npm run docs:build` | VitePress 用户文档 |
| Docker | `docker build -f Dockerfile.manager-server .` | 前端嵌入与最终镜像 |

本目录位于根 `docs/project-analysis/`，不属于 `apps/docs/` VitePress 站点构建输入，因此还需要单独进行 Markdown 链接检查。

### 9.1.1 本轮验证结果

执行日期：2026-07-13。

| 检查 | 结果 | 说明 |
| --- | --- | --- |
| `npm ci` | 通过 | 按锁文件安装 409 个 package |
| `npm run type-check` | 通过 | TypeScript 无错误 |
| `npm run lint` | 通过 | ESLint 无错误 |
| `npm run test` | 通过 | 86 个测试文件、728 个测试全部通过 |
| `npm run build` | 通过 | 生产单文件约 4.59 MB，gzip 约 1.25 MB |
| `npm run build:demo` | 通过 | Demo 单文件约 4.62 MB，输出到 `dist-demo` |
| `npm run check:demo-isolation` | 通过 | 生产 bundle 未发现 Demo fixture marker |
| `npm run docs:build` | 通过 | VitePress client/server bundle 和页面渲染完成 |
| 根分析文档链接 | 通过 | 所有相对 Markdown 链接目标存在 |
| 根分析文档格式 | 通过 | 代码围栏闭合、UTF-8 有效、无替换字符 |
| `npm audit` | 有风险 | 9 个 package：5 high、3 moderate、1 low、0 critical |
| `govulncheck ./...` | 有风险 | 本地 Go 1.26.4 存在 1 个代码可达标准库漏洞 |
| `gitleaks git --redact` | 人工核对通过 | 扫描 1349 个提交；33 条命中均为测试/Demo 合成值 |
| `go test ./...` | 当前 Windows 环境未通过 | SQLite 打开统一报 `out of memory (1)`；另有 POSIX 权限断言差异 |

Go 最小 SQLite 用例在 Go 1.24.0 和 Go 1.26.4、项目盘与纯 ASCII 临时目录组合下均可复现
同一错误，因此不能归因于单纯 `ENOSPC` 或中文路径。发布门禁应继续以 Linux CI/容器结果为
准，同时增加 Windows CI 复现并修复 DSN、驱动或平台兼容问题。密钥文件权限用例在 Windows
读取为 `0666`，需要改为 ACL 语义验证。

## 9.2 高风险回归场景

### 认证

- Manager 自有接口接受 Admin Key。
- CPA Key 不应在默认 Manager 模式被误当作 Admin Key。
- 代理发往 CPA 的是保存的 CPA Key，不是浏览器 Admin Key。
- 未配置状态只公开最小初始化信息。
- Key 轮换后旧 Key 立即失效。

### 路由

- 自有 API 不会落入 proxy。
- 未占用 management 路径能透明代理。
- plugin management 和 resource 路径正确识别。
- `/models` 仅 GET。
- `/` 重定向，`/management.html` 正常加载。

### 采集

- subscribe 成功时不重复轮询。
- subscribe 失败回退 HTTP。
- HTTP 不支持回退 RESP pop。
- 控制帧不入库。
- 解析失败进入 dead letter。
- 重复 event hash 不 fanout。

### 自动化

- 没有明确 recover time 不创建 cooldown。
- 手工禁用账号不被自动恢复。
- 文件身份变化后不恢复。
- 同一 owner 不产生重复 active cooldown。
- 重启后能继续处理到期项。

### 数据

- 旧 schema 自动增加列。
- rollup schema 变化会清空并重建派生表。
- 导出不含 `fail_body`。
- 导入 64 MiB 限制和 256 批次有效。
- JSONL 局部失败返回清晰统计。

## 9.3 发布构建链

### 单 HTML

```text
npm run build
 -> apps/web/dist/index.html
 -> 发布时重命名 management.html
```

### Manager Server Docker

```text
Node stage 构建 index.html
 -> 复制到 Go embed 路径
 -> Go 交叉编译静态二进制
 -> Alpine runtime
```

### 原生包

发布 workflow 调用 `bin/release/package-native.sh`，并生成平台资产。修改：

- 二进制名；
- 默认配置位置；
- 面板路径；
- 启停脚本；
- 数据目录；

都需要运行 `tests/nativeControlScripts.test.mjs`。

## 9.4 发布前建议命令

```bash
npm ci
npm run type-check
npm run lint
npm run test
npm --workspace apps/web run build:bundle
npm run check:demo-isolation
npm run build:demo
npm run docs:build
npm run manager-server:test
```

后端目录追加：

```bash
go test -race ./...
go vet ./...
govulncheck ./...
```

安全与供应链：

```bash
npm audit
gitleaks git --redact
docker build -f Dockerfile.manager-server .
```

## 9.5 部署基线

- Manager Server 置于 HTTPS 反向代理后。
- `USAGE_CORS_ORIGINS` 配置为实际面板 Origin，不使用 `*`。
- 显式提供高熵 `CPA_MANAGER_ADMIN_KEY`。
- Data Key 使用独立 secret，不与 SQLite 同卷。
- `USAGE_RESP_TLS_SKIP_VERIFY=false`。
- CPA upstream 限制为批准的地址。
- 数据目录仅服务账号可读写。
- 日志不采集完整 Key、Authorization 或原始响应正文。
- pprof 保持关闭；需要时只监听 loopback。
- 配置数据库和敏感表保留期限。

## 9.6 可观测性

至少监控：

- `/health` 可用性；
- `/status` 中 collector transport、last error、事件数和 dead letter 数；
- SQLite 文件大小、WAL 大小和磁盘空间；
- 采集最后成功时间；
- rollup checkpoint 落后量和错误；
- 自动禁用/恢复失败；
- 巡检 running 超时；
- CPA proxy 5xx/超时；
- 外部价格同步失败；
- 管理员认证失败速率。

日志中使用 request ID、run ID、event hash 或文件哈希关联，避免使用完整账号、Key 或原始 body。

## 9.7 备份与灾难恢复演练

至少每季度验证：

1. 从一致性 SQLite 备份恢复。
2. 提供正确 data key 后可解密配置。
3. Manager Server 能读取旧设置和历史事件。
4. rollup 可从事实表重建。
5. CPA Key 轮换后代理和采集恢复。
6. 自动化 worker 不会对恢复后的过期记录执行重复动作。

备份介质应加密，数据库和 data key 分开授权。恢复演练日志不得输出 secret。

## 9.8 故障排查顺序

### 面板无法登录

1. 检查 `/health`。
2. 检查 `/usage-service/info` 是否识别为 Manager。
3. 确认输入的是 Admin Key 还是 CPA Key。
4. 查看 CORS 和反向代理是否保留 Authorization。
5. 使用管理员重置命令时遵循现有重置文档。

### 请求监控为空

1. 查看 `/status` collector 状态。
2. 确认 CPA 开启 usage statistics。
3. 检查 queue retention 与 poll interval。
4. 查看当前 transport 和 fallback 错误。
5. 检查 dead letter。
6. 确认数据库可写和磁盘空间。

### CPA 管理功能失败

1. Manager 自有接口是否正常。
2. 保存的 CPA URL/Key 是否有效。
3. CPA `/v0/management/config` 是否可达。
4. 代理路径是否被新自有路由误占用。
5. 反向代理和出站策略是否阻断。

### 统计不一致

1. 检查重复 event hash 和 skipped 数。
2. 检查 rollup checkpoint。
3. 对比原始事件和 rollup 时间范围。
4. 检查模型价格和 billing model 映射。
5. 必要时备份后重建派生 rollup。
