# 10. 生产部署安全与数据库就绪门禁

## 10.1 目标

生产部署成功必须同时表示：

1. Manager Server 进程存活。
2. 管理员凭证可验证。
3. 服务未进入数据库恢复模式。
4. 普通业务数据接口能够读取当前路由的数据源。
5. 数据库主写、主读、epoch 和复制方向没有因升级被隐式切换。

`GET /health` 只表示 HTTP 进程存活。数据库恢复服务也会返回 `200`，因此不得单独把
`/health` 作为生产发布成功条件。

## 10.2 本次故障模型

SQLite schema 升级前，[sqlite.go](../../apps/manager-server/internal/outboxcontext/sqlite.go) 的
`SuspendForSchemaMigration` 会在事务中删除 journal triggers/contracts，并持久化
`journal_suspended=1`。升级完成后，`ResumeAfterSchemaMigration` 必须重装完整 journal fence。

MySQL 已成为写主库时，SQLite 仍是受保护的配置和近期缓存副本。此时正确的不变量是：

| 字段或合同 | 期望值 |
| --- | --- |
| `database_routing_state.write_primary` | `mysql` |
| `database_routing_state.epoch` | 正整数且保持不变 |
| SQLite journal contracts/triggers | 完整安装 |
| SQLite authority `enabled` / `active` | `0` / `0` |
| SQLite `journal_suspended` | `0` |
| 直接写 SQLite 权威业务表 | 被 fence 拒绝 |
| MySQL 到 SQLite replica apply | 仍可通过受控上下文执行 |

旧实现只允许 `write_primary=sqlite` 恢复 journal，导致合法的 MySQL-primary 状态在重启时
被误判为不可恢复。`store.Open` 返回错误后，主程序会启动受限数据库恢复服务；该服务保留
`/health`、`/status`、面板和数据库管理接口，但不开放仪表盘及普通业务接口。因此页面看似
登录成功，实际只显示“系统信息”。

## 10.3 代码级防线

`ResumeAfterSchemaMigration` 现在同时接受合法的 SQLite-primary 和 MySQL-primary 路由：

- SQLite-primary：重装合同后按当前 epoch 恢复 SQLite authority。
- MySQL-primary：重装相同的 SQLite fence，清除暂停标记，但保持 SQLite authority 禁用。
- 未知 primary、空路由或非正 epoch：继续失败关闭，不猜测主库。

回归测试 `TestSQLiteRestartRestoresDisabledJournalAfterMySQLCutover` 使用真实 SQLite 文件模拟：

1. SQLite epoch 1 初始化 journal。
2. 切换为 MySQL epoch 2。
3. 关闭并通过 `sqliterepo.Open` 重新打开。
4. 审计完整 journal contract。
5. 验证 authority 仍禁用且 `journal_suspended=0`。
6. 验证绕过复制上下文直接写 SQLite 仍失败。

Windows 源码构建脚本在生成候选二进制前运行以下门禁：

```powershell
go test ./internal/outboxcontext ./internal/repository/sqlite
```

测试失败时构建立即终止，正在运行的旧服务不会被停止，候选二进制也不会激活。

## 10.4 部署就绪门禁

Windows `cpa-manager-plus.bat rebuild`、Docker 安装器和 native 安装器都必须按顺序验证：

| 检查 | 成功条件 | 失败含义 |
| --- | --- | --- |
| `GET /health` | HTTP 200 | 进程或 HTTP listener 未就绪 |
| 带 Bearer 管理员凭证的 `GET /status` | HTTP 200 且 `recoveryMode` 不为 `true` | 凭证错误、状态接口异常或数据库恢复模式 |
| 带相同凭证的 `GET /usage-service/config` | HTTP 200 | 正常业务路由或 SQLite 配置读取不可用 |

管理员密钥只在进程内构造 Authorization header，不写入日志，也不得出现在部署输出、截图或
故障工单中。

对 `rebuild`，候选版本未通过任一门禁时：

1. 停止候选进程。
2. 不删除 `.previous.exe`。
3. 恢复旧二进制并尝试重新启动。
4. 命令以非零状态退出，不能显示部署成功。

官方 Docker/native 升级同样把恢复模式和业务数据端点不可用视为验证失败，使已有升级回滚
流程生效。发布系统不能只依据容器 healthy、PID 存在或端口可连接来提交发布。

## 10.5 上线前操作清单

1. 确认没有进行中的迁移、回切、缓存重建或 SQLite 清理任务。
2. 确认复制积压为 `0`，记录 routing generation、epoch、源水位和目标水位。
3. 分别确认 SQLite 数据目录和 MySQL 有可恢复的一致性备份。
4. SQLite 备份必须包含主文件、WAL、SHM、journal、`data.key`、数据库控制文件及其备份。
5. 验证备份目录不在发布脚本会清理的临时目录内，并检查可用磁盘空间。
6. 在候选构建阶段运行数据库启动契约测试；失败时不得停止旧服务。
7. 仅通过受管脚本执行重建，禁止直接双击新二进制形成第二实例。

`G:\usage` 等生产下载原件应作为只读来源。验证或启动前先完整复制到独立运行目录，SQLite
主文件、WAL 和 SHM 必须保持同一检查点，不能只复制 `usage.sqlite`。

## 10.6 上线后验收

脚本门禁通过后仍应记录以下非敏感证据：

```text
/health                    200
/status                    200, recoveryMode != true
/usage-service/config      200
writePrimary               升级前后相同
businessReadPrimary        升级前后相同
systemReadPrimary          升级前后相同
epoch                      升级前后相同
replication backlog        0 或持续下降
```

随后使用只读页面或接口抽样验证：

1. 仪表盘能加载时间范围内的历史数据。
2. 用量事件、账号、配置和模型价格至少各检查一个只读查询。
3. 数据源响应头和系统拓扑与预期路由一致。
4. 日志中没有 `restricted database recovery mode`、journal audit、解密或 schema 错误。
5. 后台复制水位持续推进且没有新的 dead letter。

## 10.7 失败处置

出现以下任一情况，发布必须判定失败：

- `recoveryMode=true`；
- `/usage-service/config` 非 200；
- 路由 primary 或 epoch 未经批准发生变化；
- journal audit 不完整；
- MySQL 无法读取而 SQLite 缓存覆盖不足；
- 新版本启动后复制水位停止推进。

失败时先保留日志和非敏感状态证据，再使用发布前备份和旧二进制执行回滚。不得通过手工修改
`database_routing_state`、删除 journal marker、清空控制文件或启动第二实例来绕过门禁。涉及
主库切换、MySQL schema 重建、SQLite 缓存重建或数据恢复时，必须另行审核并明确确认。
