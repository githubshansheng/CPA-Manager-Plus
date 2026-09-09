# 风险台账

评分说明：

- **高**：可能导致管理凭证泄露、内部网络访问、自动化误动作或重要供应链风险，应优先进入近期版本。
- **中**：需要部署条件或组合攻击，但会扩大影响面。
- **低**：主要影响可维护性、可重复性或纵深防御。

## 安全与泄露风险

| ID | 等级 | 风险 | 证据 | 建议负责人 | 处置建议 | 状态 |
| --- | --- | --- | --- | --- | --- | --- |
| SEC-01 | 高 | Admin Key 摘要迭代为 1，弱口令可被高速离线猜测 | `internal/security/security.go` | Backend/Security | 版本化迁移 Argon2id/scrypt/高迭代 PBKDF2 | 待整改 |
| SEC-02 | 高 | 首次生成的完整 Admin Key 写入日志 | `cmd/cpa-manager-plus/main.go` | Backend/Operations | 改为一次性 secret 文件或显式提供，不进普通日志 | 待整改 |
| SEC-03 | 高 | 浏览器 Key 仅 XOR+Base64 混淆并可持久化到 localStorage | `utils/encryption.ts`、`useAuthStore.ts` | Web/Security | 默认内存会话；评估 HttpOnly session；强化 CSP | 待整改 |
| SEC-04 | 中 | 默认 data key 与 SQLite 同数据卷 | `config.go`、`security.go` | Operations | 生产使用外部 secret/KMS，分开授权和备份 | 待整改 |
| SEC-05 | 高 | `fail_body`、dead letter payload、inspection detail 可保存未完整脱敏正文 | SQLite schema、usage/inspection 代码 | Backend/Security | 统一 redactor、长度限制、保留期、默认摘要 | 待整改 |
| SEC-06 | 高 | CPA/Provider URL 缺少统一 SSRF 和私网限制 | proxy、managerconfig、Provider clients | Backend/Security | URL allowlist、协议/IP/DNS/重定向校验 | 待整改 |
| SEC-07 | 中 | CORS 默认 `*` | `config.go`、`middleware/cors.go` | Backend/Operations | 生产默认同源，部署显式 allowlist | 待整改 |
| SEC-08 | 高 | 可配置跳过 RESP TLS 验证 | RESP/config | Backend/Operations | 支持自定义 CA，开启时告警和显式风险确认 | 待整改 |
| SEC-09 | 中 | 历史测试 fixture 产生较多密钥扫描噪声，可能掩盖新增真实命中 | gitleaks 扫描 1349 commits，33 条均为合成值 | Security | 建立带注释的 fingerprint allowlist，CI 只允许已核对项 | 本轮已验证，持续控制 |
| SEC-10 | 高 | 使用本地 Go 1.26.4 构建时存在代码可达的 `GO-2026-5856` TLS 漏洞 | govulncheck 调用链 | Backend/Platform | Go 1.26 升级到 1.26.5+；固定发布工具链并重扫制品 | 待整改 |
| SEC-11 | 中 | Windows 下 `data.key` 的 POSIX `0600` 断言不成立，NTFS ACL 未显式验证 | security test 得到 mode `0666` | Backend/Operations | 显式设置服务账号 ACL，增加 Windows ACL 集成测试 | 待整改 |

## 依赖与供应链

| ID | 等级 | 风险 | 证据 | 建议负责人 | 处置建议 | 状态 |
| --- | --- | --- | --- | --- | --- | --- |
| SUP-01 | 高 | npm 锁文件审计含 5 high、3 moderate、1 low | 2026-07-13 `npm audit` | Web/Platform | 优先升级 Axios、Router、Vite 链并回归 | 待整改 |
| SUP-02 | 中 | GitHub Actions 使用浮动 major tag | `.github/workflows` | Platform | 固定 Action commit SHA，自动化更新 | 待整改 |
| SUP-03 | 中 | Docker 基础镜像使用浮动 tag | `Dockerfile.manager-server` | Platform | 固定 digest，定期刷新和扫描 | 待整改 |
| SUP-04 | 中 | 未生成 SBOM、provenance 和签名 | release workflow | Platform | 增加 SPDX/CycloneDX、SLSA provenance、cosign | 待整改 |

## 数据与自动化

| ID | 等级 | 风险 | 证据 | 建议负责人 | 处置建议 | 状态 |
| --- | --- | --- | --- | --- | --- | --- |
| DAT-01 | 中 | dead letter 和原始正文无明确保留期限 | schema/service | Backend/Operations | 增加 retention worker 和配置 | 待整改 |
| DAT-02 | 中 | `settings` 为无 schema JSON，迁移可观测性有限 | settings repository | Backend | DTO 版本化和显式 schema migrations | 待整改 |
| DAT-03 | 中 | SQLite WAL 备份若只复制主文件可能不一致 | SQLite WAL 配置 | Operations | 在线备份或停机 checkpoint，定期恢复演练 | 待整改 |
| AUT-01 | 高 | 自动禁用依赖外部 CPA 状态，竞态可能影响人工操作 | quota worker | Backend | 保留 owner/identity/pre-state 校验，增加并发测试 | 已有控制，持续验证 |
| AUT-02 | 中 | fanout consumer 若阻塞或重复执行会影响 rollup/动作 | usage fanout | Backend | consumer 契约、重试和幂等显式化 | 待重构 |
| AUT-03 | 中 | 巡检状态和动作字段较多，状态转换隐式 | codexinspection service/schema | Backend | 建立 DecisionEngine 和状态机 | 待重构 |

## 可维护性与兼容

| ID | 等级 | 风险 | 证据 | 建议负责人 | 处置建议 | 状态 |
| --- | --- | --- | --- | --- | --- | --- |
| ARC-01 | 中 | 前端 usage service/page 超大，修改回归面大 | `usageService.ts`、UsageAnalytics | Web | 按领域 client、hook、view 渐进拆分 | 待重构 |
| ARC-02 | 中 | monitoring 和 inspection service 超大 | Go service 文件 | Backend | 按 use case/orchestrator/policy 拆分 | 待重构 |
| ARC-03 | 低 | `store.Store` 转发接口模糊 repository 所有权 | `internal/store/store.go` | Backend | 新 service 依赖最小 repository interface | 待重构 |
| ARC-04 | 高 | 新自有 management 路由可能被通用 CPA proxy 抢先 | `router.go` | Backend | 路由注册表和顺序测试 | 已有测试，持续验证 |
| ARC-05 | 高 | 删除采集回退、旧字段或单 HTML 会破坏部署兼容 | collector/import/build | Full stack | 作为架构契约纳入 PR 清单 | 持续控制 |
| ARC-06 | 中 | 当前 Windows 环境无法打开测试 SQLite，后端全量测试失去平台覆盖 | Go 1.24/1.26 最小用例均报 `out of memory (1)` | Backend/Platform | 增加 Windows CI，修复 DSN/驱动或明确支持边界 | 已复现，待定位 |

## 建议整改批次

### P0：发布前

- SEC-02 首次 Key 日志。
- SEC-05 原始正文脱敏与长度。
- SEC-06 CPA URL 出站策略。
- SEC-10 Go 工具链升级、固定与制品重扫。
- SEC-11 Windows data key ACL。
- SUP-01 直接运行依赖高风险公告。

### P1：近期版本

- SEC-01 管理员摘要迁移。
- SEC-03 浏览器会话策略。
- SEC-04 Data Key 外置。
- SEC-07 CORS 默认值。
- SEC-08 TLS 自定义 CA。
- SEC-09 密钥扫描 allowlist 与 CI 门禁。
- SUP-02/SUP-03 供应链固定。
- ARC-06 Windows SQLite 测试兼容。

### P2：重构周期

- DAT-02 显式迁移框架。
- AUT-02/AUT-03 状态机和 fanout 契约。
- ARC-01/ARC-02/ARC-03 领域拆分。
- SBOM、provenance、签名和保留期治理。

## 关闭风险所需证据

风险不得只因“代码已修改”关闭，至少需要：

- 对应单元/集成/安全回归测试；
- 旧配置、旧数据库或旧会话迁移验证；
- 部署文档和默认值更新；
- 依赖审计或扫描报告；
- 对高风险项给出回滚方案；
- 在发布构建产物上复查，而不只检查源码。
