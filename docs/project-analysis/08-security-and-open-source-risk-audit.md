# 08. 安全与开源泄露风险审计

## 8.1 结论摘要

当前仓库未发现已提交的真实高置信度 API Key、私钥、证书、`.env`、SQLite 或 credentials
文件。`gitleaks` 已扫描完整 Git 历史的 1349 个提交，33 条规则命中均已核对为测试常量、
Demo 索引、固定哈希、示例 Key 或故意构造的无效 JWT；另有一个使用 `abc` 占位内容的假私钥
fixture。这些命中不构成真实泄露证据。

但项目仍存在多项需要在公开部署前整改的高风险设计：

1. 管理员 Key 摘要迭代次数为 1，弱自定义 Key 的离线抗爆破能力不足。
2. 首次启动会将完整生成管理员 Key 写入日志。
3. 浏览器“安全存储”是可逆 XOR 混淆，记住密码后 Key 可由页面脚本读取。
4. 数据库可持久化失败正文、dead letter 原文和巡检错误正文，脱敏覆盖不完整。
5. 管理员可配置 CPA URL，服务端缺少明确 SSRF/私网地址限制。
6. CORS 默认 `*`，RESP 可选择跳过 TLS 校验。
7. npm 锁文件存在已知安全公告；CI actions 和基础镜像使用浮动 tag。
8. 本地 Go 1.26.4 工具链存在一个代码可达的标准库 TLS 漏洞，且发布工具链未固定到精确补丁版本。

因此，“源码中未发现真实密钥”不等于“开源部署无泄露风险”。风险主要来自运行期密钥生命周期、原始数据留存和网络边界。

## 8.2 审计范围与限制

已检查：

- 当前跟踪源码中的高置信度密钥模式；
- 敏感文件名和 `gitleaks` 完整 Git 历史；
- 管理员认证、浏览器存储和服务端加密代码；
- SQLite 敏感字段和导入导出；
- CORS、TLS、代理和外连代码；
- npm 锁文件审计结果；
- `govulncheck` 的代码可达性结果；
- GitHub Actions 和 Dockerfile。

未完成：

- 动态渗透、XSS、SSRF、DNS 重绑定和代理绕过测试；
- 真实部署的文件权限、日志平台、备份、云 IAM 和反向代理审计。
- 容器镜像、发布二进制和 SBOM 的制品级漏洞扫描；
- 法律、许可证和隐私合规审查。

系统盘最初因空间不足（`ENOSPC: no space left on device`）阻断 npm 和 Go 工具。将缓存与
临时目录重定向到其他磁盘后，三项静态扫描均已完成，未擅自清理用户磁盘。本报告仍不得被
解读为“历史绝无密钥”或“真实部署不存在泄露路径”。

## 8.3 源码与 Git 历史密钥扫描

### 当前跟踪文件

- 高置信度真实密钥规则命中：0。
- 未发现被跟踪的 `.env`、私钥、证书、SQLite、credentials 文件。
- `.gitignore` 应继续覆盖本地数据、密钥、构建产物和环境文件。

### 全历史扫描

2026-07-13 使用 `gitleaks v8.30.1` 扫描：

```text
1349 commits
约 15.06 MB
33 findings
```

命中按内容归类：

| 类型 | 示例特征 | 判断 |
| --- | --- | --- |
| 后端测试 Admin Key | `cpamp_test_key_...` 固定测试常量 | 合成测试值 |
| Demo 账号索引 | `vertex-regional-01` 等 | 业务标识，不是凭证 |
| API Key hash | `abcdef1234567890`、`1234567890abcdef` | 固定展示/过滤测试值 |
| 前端 Key fixture | `sk-1234567890abcdef`、重复数字片段、`.example` 域名 | 合成测试值 |
| JWT fixture | 无效 Base64、固定 `.sig`、测试 payload | 故意构造的无效 token |
| 私钥 fixture | PEM 包裹的 `abc` | 无法构成有效私钥 |

结论是 33 条均为规则误报，未发现需要立即轮换的真实凭证。不要因此全局关闭
`generic-api-key` 规则；应在仓库增加逐条带注释的 fingerprint allowlist，并在 CI 对新增
命中执行阻断，避免真实泄露被历史噪声掩盖。

## 8.4 管理员 Key 摘要强度

证据：[security.go](../../apps/manager-server/internal/security/security.go)

```go
const adminHashIterations = 1
```

迭代数为 1 时实现使用带随机 salt 的 HMAC-SHA256；只有迭代数大于 1 才调用 PBKDF2。

### 风险

- 随机 salt 防止预计算彩虹表，但不增加单次猜测成本。
- 自动生成的 32 字符 Key 熵较高，风险相对可控。
- 用户通过环境变量设置短 Key、口令或重复使用密码时，数据库泄露者可高速离线猜测。

### 建议

使用版本化凭证格式：

```json
{
  "version": 2,
  "algorithm": "argon2id",
  "parameters": {},
  "salt": "...",
  "hash": "..."
}
```

迁移策略：

1. 验证旧 v1 成功后立刻用 Argon2id、scrypt 或高迭代 PBKDF2 重算。
2. 新管理员 Key 只生成 v2。
3. 设置最小长度和弱口令检查。
4. 保留常量时间比较。

## 8.5 首次启动日志泄露

证据：[main.go](../../apps/manager-server/cmd/cpa-manager-plus/main.go)

首次自动生成管理员 Key 时打印：

```text
CPA Manager Plus admin key generated: <完整 Key>
```

### 风险

- Docker logs、Kubernetes、systemd journal、CI 和集中日志平台可能长期保留。
- 日志读取权限往往大于 secret 读取权限。
- 轮换管理员 Key 后旧日志仍存在。

### 建议

优先要求部署显式提供 `CPA_MANAGER_ADMIN_KEY`。自动生成时可：

- 写入权限为 `0600` 的一次性 secret 文件；
- 只在交互终端显示一次且不进入普通日志 sink；
- 输出指纹和取回路径，不输出完整 Key；
- 首次登录后强制轮换。

## 8.6 浏览器凭证持久化

证据：

- [utils/encryption.ts](../../apps/web/src/utils/encryption.ts)
- [services/storage/secureStorage.ts](../../apps/web/src/services/storage/secureStorage.ts)
- [stores/useAuthStore.ts](../../apps/web/src/stores/useAuthStore.ts)

实现使用：

```text
固定 salt + window.location.host + navigator.userAgent
 -> 重复 XOR
 -> Base64
```

### 结论

这是可逆混淆，不是加密，也不是浏览器安全边界。代码已经通过命名和注释承认这一点，但历史 alias 仍叫 `secureStorage`。

“记住密码”开启时，`managementKey` 被保存到 localStorage。任何同源 XSS、恶意浏览器扩展、DevTools 操作者或本机用户都可以恢复。

### 建议

- 默认不记住 Key，只保存在内存或 sessionStorage。
- 明确提示“此设备将保存管理凭证”。
- Manager 内嵌模式可改为 HttpOnly、Secure、SameSite session cookie，并增加 CSRF 防护。
- 配置严格 CSP，减少第三方脚本。
- 彻底删除 `secureStorage` 误导 alias，统一称 `obfuscatedStorage`。
- 不要尝试用前端内置密钥“加密”localStorage，那仍无法防页面脚本。

## 8.7 服务端 CPA Key 加密与数据密钥

证据：[security.go](../../apps/manager-server/internal/security/security.go)

- CPA Management Key 使用 HKDF 派生 key 后 AES-GCM 加密。
- 默认数据密钥路径为数据目录下 `data.key`。
- POSIX 文件权限参数使用 `0600`。

### 正面结论

单独泄露 SQLite 时，攻击者不能直接读取受保护 Key。

### 剩余风险

默认 Docker 数据卷同时包含：

```text
/data/usage.sqlite
/data/data.key
```

整个卷、宿主机或容器权限泄露时，攻击者同时获得密文和解密材料。

本轮 Windows 验证中，`TestLoadOrCreateDataKeyCreatesStableRestrictedFile` 读取到的模式为
`0666`，而测试预期 `0600`。Windows 的 Go `FileMode` 不等价于 NTFS ACL，这个结果不能直接
证明文件对所有用户可读，但也说明当前代码和测试没有建立可验证的 Windows ACL 安全边界。

### 建议

- 生产环境通过 Docker/Kubernetes secret 或 KMS 提供 `CPA_MANAGER_DATA_KEY`。
- 数据密钥与数据库分开备份和授权。
- Windows 原生部署显式创建仅服务账号和管理员可读的 ACL，并增加 ACL 集成测试。
- 增加 key ID、轮换和重加密流程。
- 备份文档明确“数据库 + key”既是恢复必需品，也是完整敏感资产。

## 8.8 数据库原始正文泄露

高风险字段：

| 表/字段 | 内容 | 当前控制 |
| --- | --- | --- |
| `usage_events.fail_body` | 上游失败正文 | 导出不包含，但数据库保留 |
| `usage_events.raw_json` | 事件原始 JSON | 保存前递归脱敏 |
| `dead_letter_events.payload` | 解析失败原文 | 未统一脱敏 |
| `codex_inspection_results.error_detail` | 探测响应详情 | 可能含正文 |
| `codex_inspection_logs.detail_json` | 结构化日志详情 | 字段名规则脱敏有限 |

现有 `sanitizeDetail`/递归规则主要识别名称包含 token、secret、authorization、key 的字段。普通字段名 `body`、`message`、`detail` 可能仍包含凭证、请求内容、邮件或账号标识。

### 建议

1. 所有持久化正文先经过统一 `SecretRedactor`。
2. 限制每个字段最大字节数。
3. 默认只保存摘要和哈希，原文按显式诊断开关启用。
4. 增加 7/30/90 天可配置保留期限。
5. 导入、日志和 UI 展示再次脱敏。
6. 增加包含 Bearer、API Key、Cookie、私钥和嵌套 body 的测试。

## 8.9 SSRF 与出站网络

Manager Server 会连接管理员提供的 CPA URL，也会访问价格、版本和 Provider 端点。

### 风险

- 未见对 `file:`、非 HTTP(S) 协议的统一拒绝策略。
- 未见对 localhost、链路本地地址、云元数据地址和私网 CIDR 的统一限制。
- DNS 初次解析为公网、随后重绑定私网的场景未处理。
- 反向代理可能跟随用户配置目标访问内部管理服务。

管理员本身是高权限主体，但 SSRF 仍会把 Manager Server 的网络位置和云身份变成攻击能力，尤其在多管理员、凭证泄露或浏览器 XSS 场景。

### 建议

- 默认仅允许 `http`/`https`。
- 提供可配置 allowlist。
- 解析并拒绝 loopback、link-local、multicast、unspecified、云元数据和受限私网。
- 每次连接校验最终解析 IP，限制重定向。
- 对确需内网 CPA 的部署提供显式 `ALLOW_PRIVATE_UPSTREAM=true`，而不是默认开放。
- 记录目标 host 和决策，不记录凭证。

## 8.10 CORS 与 TLS

### CORS

默认 `USAGE_CORS_ORIGINS` 为 `*`。Bearer Key 不会被浏览器自动附带，因此 wildcard 不会单独形成传统 cookie CSRF，但会扩大任意网页诱导用户粘贴/使用 Key、读取响应或结合 XSS 的攻击面。

建议生产默认同源，外部面板部署时显式配置 Origin allowlist。

### RESP TLS

`USAGE_RESP_TLS_SKIP_VERIFY` 可启用 `InsecureSkipVerify`。这会允许中间人伪造队列数据，进一步触发错误统计和账号自动化。

建议：

- 默认保持 false；
- UI 以高风险配置展示；
- 支持自定义 CA，而不是用跳过验证解决私有证书；
- 开启时写结构化安全告警。

## 8.11 pprof

pprof 仅在显式配置 `CPA_MANAGER_PPROF_ADDR` 时启动，且代码强制 host 为 loopback，这是良好控制。运维仍应避免通过反向代理或端口转发将其公开，因为 profile 可能暴露路径、内存内容和运行行为。

## 8.12 npm 依赖风险

2026-07-13 将 npm 缓存迁移到项目盘后完成锁文件审计：

```text
9 packages: 5 high / 3 moderate / 1 low / 0 critical
```

涉及锁定版本：

| 包 | 锁定版本 | 备注 |
| --- | --- | --- |
| `axios` | 1.15.2 | 直接运行依赖，优先升级 |
| `react-router` | 7.12.0 | 部分公告与 RSC/SSR 路径相关 |
| `react-router-dom` | 7.12.0 | 与 router 同步升级 |
| `vite` | 8.0.10 | 主要影响开发/构建服务器 |
| `vitepress` | 1.6.4 | 文档构建 |
| VitePress 内 `vite` | 5.4.21 | 间接依赖 |
| `esbuild` | 0.28.1 / 0.21.5 | 开发/构建链 |
| `form-data` | 4.0.5 | Axios 间接依赖 |
| `js-yaml` | 4.1.1 | 构建/文档链 |
| `@babel/core` | 7.28.5 | 构建链 |

审计结果已在本轮复现。`axios`、`react-router-dom` 和 `vite` 是直接依赖；当前依赖图中
`vite`、`vitepress` 和一个 `esbuild` 公告没有自动可用的修复方案，升级时需要调整版本范围
并重新构建，而不是只执行 `npm audit fix`。

优先级：

1. 升级 Axios 并跑 Web 全量测试。
2. 同步升级 React Router 相关包。
3. 升级 Vite/VitePress/esbuild，限制开发服务器只监听可信接口。
4. 在 CI 增加 `npm audit` 或更稳定的 SCA 阻断策略。

## 8.13 Go 依赖

2026-07-13 使用 `govulncheck v1.6.0` 和本地 Go 1.26.4 扫描 60 个根 package、9 个模块：

| 漏洞 | 可达性 | 影响 | 修复 |
| --- | --- | --- | --- |
| `GO-2026-5856` | 代码可达 | `crypto/tls` Encrypted Client Hello 隐私泄露 | Go 1.26.5 |
| `GO-2026-4970` | 已导入，未发现符号调用 | `os` 中 symlink + trailing slash 根目录逃逸 | Go 1.26.5 |

`GO-2026-5856` 的示例调用链覆盖 HTTP server、CPA auth file client、RESP TLS client 和反向
代理。扫描没有发现所依赖第三方 Go 模块中的代码可达漏洞。

仓库 CI 和 Dockerfile 当前声明 Go `1.24.x`/`golang:1.24-alpine`，而上述结果针对本地
Go 1.26.4 标准库，不能直接证明现有发布镜像包含同一漏洞。真正的问题是工具链只固定到
minor 分支或浮动 tag，无法从源码提交确定最终标准库补丁级别。

处置建议：

1. 任何使用 Go 1.26 构建的环境立即升级到 1.26.5 或更高修复版本。
2. CI、Docker 和本地发布脚本固定精确 Go patch 与镜像 digest。
3. 对实际发布二进制或其精确构建工具链再次运行 `govulncheck`。
4. 同时检查 `go list -m -u all`，按可达调用路径和回归风险升级。

## 8.14 CI 与供应链

GitHub Actions 使用 `actions/checkout@v4`、`setup-node@v4`、`build-push-action@v6` 等浮动 tag；Docker 使用：

```text
node:22-alpine
golang:1.24-alpine
alpine:3.21
```

风险：

- 上游 tag 可移动；
- 同一提交在不同日期构建出不同内容；
- Action 供应链被替换时影响 CI 权限；
- 基础镜像补丁状态不可追溯。

建议：

- Actions 固定完整 commit SHA，并由 Renovate/Dependabot 更新。
- 基础镜像固定 digest，定期自动刷新。
- 生成 SBOM 和 provenance。
- 发布资产生成校验和并签名。
- npm 使用 `npm ci`，Go 保留 `go.sum`，这些现有做法应继续保留。

## 8.15 开源发布前清单

- 使用 gitleaks/trufflehog 扫描完整历史。
- 执行 npm、Go、容器三层依赖审计。
- 将已核对的测试 fixture 加入最小化 allowlist，对新增命中保持阻断。
- 检查 release artifact 中是否包含 Demo fixtures、source map、测试数据或密钥。
- 确认 `.gitignore` 覆盖 data、key、sqlite、env、logs。
- 轮换任何曾进入日志、Issue、截图或 CI 输出的 Key。
- 对默认 CORS、Admin Key、Data Key 和 TLS 给出安全部署示例。
- 设置原始数据保留期和删除流程。
- 固定 Actions 和镜像 digest。
- 对许可证和第三方资产另行做法律合规审查。
