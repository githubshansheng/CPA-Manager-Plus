# SQLite 在线迁移到 MySQL

[简体中文](./mysql-migration) | [English](../en/operations/mysql-migration) | [繁體中文](../zh-TW/operations/mysql-migration) | [Русский](../ru/operations/mysql-migration)

Manager Server 可以在保留 SQLite 默认行为的同时，把全量历史迁移到外部 MySQL，并在迁移后把 SQLite 作为配置库和近期缓存。该能力面向单机 Manager Server；不会部署内置 MySQL 容器，也不会自动故障切换。

## 支持范围

- 支持官方 MySQL 8.x，最低版本为 8.0.12；不支持 MariaDB 或 MySQL 9.x。
- MySQL 8.0.17+ schema 使用 `utf8mb4_0900_bin`；8.0.12–8.0.16 自动使用该版本可用的 NO PAD、区分大小写及重音的 `utf8mb4_0900_as_cs`。会话统一使用 UTC、严格 SQL 模式和 `innodb_strict_mode=ON`。
- MySQL 必须允许当前 schema 的 `SELECT`、`INSERT`、`UPDATE`、`DELETE`、`CREATE`、`ALTER`、`DROP`、`INDEX`、`REFERENCES` 和 `TRIGGER`。系统会用随机探针表实际验证，不只解析 `SHOW GRANTS`。
- 如果启用了 binary logging，普通 schema 账号创建 trigger 时返回 Error 1419，要求 DBA 设置并持久化 `log_bin_trust_function_creators=ON`，或由具备相应管理权限的 DBA 安装 trigger；不要向应用账号授予 `SUPER`。
- 外部 MySQL 默认必须验证 TLS 证书身份。关闭 TLS 会显示安全警告并要求再次确认。
- `max_allowed_packet` 至少为 4 MiB；现有 SQLite 任一权威行无法放入单个 MySQL 协议包时，迁移预检会停止。

## 数据路由

正常模式下，权威写入先在 SQLite 事务提交，并在同一事务写入 Outbox；后台把完整字段最终同步到 MySQL。业务查询在切换后优先 MySQL，只有连接/可用性故障才回退 SQLite。SQL、schema、权限、扫描或数据错误不会被静默回退掩盖。

SQLite 永久保留设置、管理员凭据、数据库连接配置、模型价格/context/service tier 和 API Key 别名。切换并完成安全清理后，其他业务数据默认只保留最近 15 天；MySQL 保留全部历史。活动、待处理或未完成的动作、冷却、巡检和配额生命周期不会因年龄被清理。

## 迁移前准备

1. 备份完整 CPAMP 数据目录，包括 SQLite/WAL/SHM、`database-control.json.enc`、其 `.bak` 和 `data.key`。
2. 为外部 MySQL 建立独立备份，并完成一次恢复演练。
3. 确认 MySQL 数据库为空，或与系统信息显示的 schema manifest 完全兼容。
4. 确认磁盘空间、MySQL 包大小、连接数和备份保留策略满足全历史容量。
5. 在低峰期操作；最终校验会短暂暂停 Collector 和后台写 Worker。

## 三阶段操作

### 1. 测试 MySQL 并启用双写

在“系统信息 → 数据库拓扑”填写地址、数据库、账号、密码、TLS 和 CA 证书，先点击“测试 MySQL”。保存后点击“启用双写”。

后端会验证版本、字符集/collation、UTC、严格模式、包大小和实际 DDL/DML 权限，创建完整 schema，复制全部配置字段，启用 SQLite Outbox，再启动实时同步。此时业务查询仍使用 SQLite。

密码不会返回浏览器或写入日志。再次保存时留空密码表示保留原密码。

如果升级后 MySQL 表结构已过期，可在停用同步和迁移、并确保 MySQL 未承担读写后，点击“重新初始化 MySQL 表结构”。页面会连续进行两次危险确认，并显示当前数据库名；后端还会核对目标、数据库名、routing generation 和幂等键。确认后，系统会删除当前已配置数据库中的全部表、视图、触发器和数据，再按最新 schema manifest 重建。它不会删除数据库本身、不会影响 SQLite，也不会自动复制数据、启用同步或切换路由。执行前必须完成 MySQL 备份。

### 2. 迁移历史

点击“迁移历史数据”。系统以外键依赖顺序和主键/rowid 水位复制所有权威表，保留显式 ID、NULL、原始 JSON、失败 body、客户端/IP、token、service tier、价格、巡检、动作、冷却和配额生命周期等完整字段。

默认批次为 1,000 行，并受 4 MiB 读取字节上限约束；超大单行会独占一个批次。任务 checkpoint 持久化，可暂停、恢复和重试，进程重启后继续。历史批次不会覆盖 Outbox 已写入的较新行。复制完成后，MySQL 从权威数据独立重建汇总、projection、索引和搜索数据。

系统信息中的“迁移任务记录”会列出持久化任务、当前阶段与状态、逐表行数/字节/请求数以及完整执行时间线。后台错误会原样写入追加式审计记录；恢复任务只清除当前告警，不会删除此前失败原因。管理员也可通过 `GET /v0/management/databases/migrations?limit=20` 查询相同记录。

### 3. 校验并切换业务主读

等待同步积压为 `0`，然后点击“校验”。系统会短暂获取全局写栅栏，追平最终 Outbox 水位，并比较每张权威表的 schema、行数、主键范围、外键、逐字段 SHA-256、token、成功/失败数量和冻结价格表口径的费用。

只有所有检查和派生/搜索一致性合同都通过，才会签发 validation token 并启用“切换业务主读”。切换需要当前 migration ID、validation token、目标和 routing generation；任一过期值返回 `409`。切换后 SQLite 仍是默认写入前置层和近期缓存。

## 缓存清理

定时清理默认关闭（页面中的复选框默认不勾选），不会因升级或启动自动删除 SQLite 数据。管理员明确启用后，MySQL 切换前仍不能清理 SQLite 历史；切换、校验和同步水位均有效后，可先“预览清理”，再启动有界清理。清理在 MySQL 不可用、Outbox 积压、迁移未完成或校验过期时自动暂停。已有安装会保留原来已保存的开关状态。

手动切换写主库、启动 SQLite 清理和重建 SQLite 缓存都属于危险操作。请求除当前 routing generation 和幂等键外，还必须回传页面当前显示的目标后端、migration ID 与 validation token；后端会再次与加密控制文件及持久化迁移状态核对，缺失或过期的确认不会执行。

在线删除只把 SQLite 页面变成可复用空间，`usage.sqlite` 的物理文件不会立即缩小。系统信息会分别显示物理大小、有效页和可复用空间。不要在服务运行时用外部 `VACUUM` 或替换数据库文件。

## 故障与恢复

- MySQL 中断：SQLite 继续接收写入，Outbox 积压；回退查询会带 `X-CPAMP-Data-Source`、完整性和缓存覆盖 header，页面持续显示“部分数据”。
- SQLite 不可写：系统进入只读/待切换状态并告警。不会自动把 MySQL 提升为写主库；管理员必须核对目标、epoch 和水位后手动确认。
- 从 MySQL 重建 SQLite：只恢复全部配置和最近 N 天业务缓存，在临时 SQLite 中重建派生数据并校验后，才在短写栅栏内原子替换。它不会把全历史迁回 SQLite。
- SQLite 恢复：若曾让 MySQL 接管写入，必须等待反向 Outbox 补齐并校验后再切回，旧 epoch 写入会被拒绝。

若系统信息显示有积压且 60 秒无进度，应立即检查 MySQL 连通性、磁盘、锁和权限；120 秒无进度会标记 `stalled`。不要通过启动第二个 Manager Server 来“加速”迁移。
