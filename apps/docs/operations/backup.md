# 备份与恢复

CPAMP 的请求历史、配置和加密凭证都在本机。备份时最容易犯的错，是只复制 `usage.sqlite`，漏掉 WAL/SHM、`data.key` 或安装目录里的 secret 文件。

## 必备备份文件

至少把这些文件作为一组备份：

- `usage.sqlite`
- `usage.sqlite-wal`
- `usage.sqlite-shm`
- `database-control.json.enc`
- `database-control.json.enc.bak`（存在时）
- `mysql-ca-*.pem`（配置自定义 MySQL CA 证书时）
- `data.key`

如果部署目录还有自定义配置文件，也应一起备份。使用一键安装脚本时，至少额外备份安装目录中的 `secrets/` 和 `data/`；成功导入后通常不会再有 `secrets/cpa-management-key`，但升级失败或 `CPAMP_SKIP_EXECUTE=1` 时该临时文件可能仍需保留以便重试。手动 env/secret 部署仍应备份对应 secret 文件。

`database-control.json.enc` 保存数据库路由 generation、脱敏连接信息、加密后的 MySQL 密码、认证副本和迁移/故障切换状态。它必须与同一份 `data.key` 一起恢复；不要单独复制到另一套实例。

## 启用 MySQL 后的备份边界

MySQL 切为业务主读并清理 SQLite 历史后，MySQL 是 15 天缓存窗口之外的唯一全历史副本。数据目录备份不包含外部 MySQL 数据，必须另外建立 MySQL 物理备份或一致性逻辑备份，并定期做恢复演练。

备份前确认系统信息中的同步积压为 `0`，记录源/目标水位和当前 routing generation。恢复时必须把 MySQL、整个 CPAMP 数据目录和 `data.key` 作为同一恢复点处理；禁止把较新的控制文件与较旧的 MySQL 备份混用。MySQL→SQLite 按钮只重建配置和最近 N 天缓存，不能代替 MySQL 全历史备份。

## 为什么必须备份 data.key

通过 setup 或面板保存的 CPA 连接，会把 CPA Management Key 使用 `data.key` 加密后保存到 SQLite。

- 只有 `usage.sqlite` 泄露时，攻击者不能直接读出 CPA Management Key。
- `usage.sqlite` 和 `data.key` 同时泄露时，CPA Management Key 可被解密。
- 丢失 `data.key` 时，已经保存的 CPA Management Key 无法恢复，只能重新保存 CPA 连接配置。

如果 CPA 连接由手动环境变量或 secret 文件管理，CPA Management Key 可能不写入 SQLite；请把对应的 secret 文件和数据目录作为一组备份。一键安装器的 env 输入会在成功后迁移到 SQLite，不应只备份一次性输入文件。

## Docker 备份示例

如果使用 named volume，可以先停止容器，再用临时容器导出：

```bash
docker stop cpa-manager-plus
docker run --rm \
  -v cpa-manager-plus-data:/data:ro \
  -v "$PWD":/backup \
  alpine \
  tar czf /backup/cpa-manager-plus-data.tgz -C /data .
docker start cpa-manager-plus
```

如果使用宿主机目录挂载：

```bash
docker stop cpa-manager-plus
cp -a /srv/cpa-manager-plus-data /srv/cpa-manager-plus-data.backup
docker start cpa-manager-plus
```

## 原生包备份

停止进程后复制数据目录：

```bash
cp -a ./data ./data.backup
```

Windows PowerShell：

```powershell
Copy-Item -Recurse .\data .\data.backup
```

## 恢复

1. 停止 CPAMP。
2. 恢复完整数据目录。
3. 确认 `usage.sqlite`、`database-control.json.enc` 和 `data.key` 来自同一次备份。
4. 如果使用 env/secret 管理 CPA 连接，同时恢复安装目录里的 `secrets/`。
5. 启动 CPAMP。
6. 登录后检查配置、监控数据和采集器状态。

如果恢复后出现解密失败，优先检查 `data.key` 是否和 SQLite 匹配。

## 直接接管或切换现有 SQLite

首次初始化可以选择“接管旧 SQLite”；项目已经初始化后，可以在“系统信息 → 数据库管理”中选择“切换 SQLite 数据源”。两种方式都会让 Manager Server 在下一次启动时**直接打开指定文件**，不会复制数据库。路径必须是 Manager Server 所在主机或容器内可访问的绝对路径；浏览器本机路径只有在浏览器和服务运行于同一文件系统时才有效。

接管前应按以下顺序准备：

1. 正常停止所有仍在使用该数据库的旧 CPAMP 实例。
2. 关闭 `sqlite3`、数据库浏览器、备份脚本和维护任务，包括仅执行查询的长事务。只读连接通常不会独占锁住整个数据库，但可能阻止 WAL checkpoint 或 truncate 完成。
3. 把 `usage.sqlite`、仍存在的 `usage.sqlite-wal`/`usage.sqlite-shm` 和匹配的 `data.key` 当作同一恢复点；不要从不同时间点拼接。
4. 在面板输入 SQLite 绝对路径。旧库包含加密 CPA 连接时，还要输入匹配的 `data.key` 绝对路径；旧库已有管理员凭证时，还要验证旧 Admin Key。
5. 运行预检并确认旧实例及工具均已停止。预检会验证文件权限、CPA Manager 表结构、`PRAGMA quick_check(1)`、写锁、data key 和管理员凭证。

正常启动的 Manager Server 会持有数据库旁的进程锁；因此另一个遵守同一锁协议的实例不能同时接管该 SQLite。SQLite WAL 只能改善短事务的读写并发，不是多实例协调机制。绕开进程锁或让其他程序持续写入同一数据库，仍可能产生 `database is locked`、checkpoint 长时间不能完成，甚至造成不一致的恢复点。

如果设置了 `USAGE_DB_PATH`，面板不能覆盖 SQLite 路径；如果设置了 `CPA_MANAGER_DATA_KEY_PATH`，面板也不能选择不同的 data key。先移除冲突的环境变量并正常重启，再执行接管。运行中切换只允许数据库拓扑处于稳定的纯 SQLite 状态：三个读写主路由均为 SQLite、未配置 MySQL、未启用同步、没有迁移或故障切换，并且 SQLite cleanup/rebuild 已结束。

保存选择后，当前进程继续使用原数据库。面板会显示待切换路径和“重启 Manager Server”按钮；只有用户点击后，服务才会依次停止 HTTP、后台 worker、WAL 维护和连接池，释放进程锁，再打开目标数据库。请勿用浏览器刷新代替重启。

选择记录保存在数据目录的 `.cpa-manager-plus.sqlite-source.json`。切换时，当前 `database-control.json.enc` 及其 `.bak` 会按 `.before-sqlite-switch-<timestamp>` 后缀归档，避免不同 data key 的控制状态混用。若目标在重启阶段消失、被占用、data key 不匹配或初始化失败，服务会释放目标资源、恢复旧 SQLite 和旧控制文件、自动重新启动，并在系统信息中保留失败路径、阶段及底层原因；失败目标生成的控制文件以 `.failed-target` 后缀保留供排查。

旧库已有管理员凭证时，重启后使用旧 Admin Key 登录。旧库没有管理员凭证时，确认切换会把当前凭证的 salt/hash 写入目标库，因此仍使用当前 Admin Key；明文 Admin Key 不会写入数据源选择文件。

## 不保留请求历史，只迁移 Manager 配置

如果旧 `usage.sqlite` 很大且请求历史不需要保留，可以让新实例使用空数据目录，然后通过现有 Manager 配置 API 导出和导入非敏感的 CPA 连接地址、采集器、Codex 巡检与 External Usage Service 配置。该方式不会复制 `usage_events`、rollup、巡检运行历史、模型价格、API 密钥别名或账号处理策略，也不会导出 CPA Management Key。

在旧实例仍可访问时导出：

```bash
export OLD_CPAMP_URL='http://old-host:18317'
export OLD_CPAMP_ADMIN_KEY='cpamp_...'

curl -fsS \
  -H "Authorization: Bearer ${OLD_CPAMP_ADMIN_KEY}" \
  "${OLD_CPAMP_URL}/usage-service/config" \
  | jq '{config: .config}' \
  > manager-config.json
chmod 600 manager-config.json
```

新版本的 `manager-config.json` 不包含 CPA Management Key；仍应按配置文件管理，避免把其他敏感配置提交到版本库或发送到 Issue。来自旧版本的导出文件可能包含明文密钥，必须立即按 secret 处理并在迁移后删除。

停止旧实例并准备新实例的空数据目录。先在 Manager Server 未运行时用离线命令重新提供 CPA Management Key：

```bash
cpa-manager-plus store-cpa-connection \
  --cpa-base-url 'http://cpa:8317' \
  --management-key-file '/secure/cpa-management-key' \
  --db-path './data/usage.sqlite' \
  --data-key-path './data/data.key'
```

该命令要求先停止 Manager Server；它会把密钥加密写入 SQLite，命令输出不会回显密钥。

连接记录的 authority 规则是：完整的 `manager_config_v1` 权威；它与过期或冲突的旧 `setup` 同时存在时，启动和导入会保留 manager 连接并 canonicalize setup，不需要修复。manager 只有 partial 数据而完整的旧 setup 与其已有字段兼容时，setup 会补全 manager。只有在没有完整 authority 且 partial 记录彼此冲突，或解析器无法判断持久化状态时，上面的命令才会拒绝写入并在错误信息中给出修复方式。确认要以显式提供的连接为准时，追加 `--repair-conflict` 修复：

```bash
cpa-manager-plus store-cpa-connection \
  --repair-conflict \
  --cpa-base-url 'http://cpa:8317' \
  --management-key-file '/secure/cpa-management-key' \
  --db-path './data/usage.sqlite' \
  --data-key-path './data/data.key'
```

`--repair-conflict` 只用于修复解析器无法信任的历史状态：互相冲突的 `manager_config_v1`/`setup` 记录，或与请求冲突且没有权威方的 partial 记录。它把你显式提供的连接在单个事务里同时写入 `manager_config_v1` 与旧 `setup` 镜像（密钥加密存储），并保留采集器设置与其他数据；对完整且一致的已存连接仍然要求输入完全匹配，不会静默改绑。修复完成后再正常启动，连接存储迁移会照常完成。然后再启动新实例并导入其余配置：

```bash
export NEW_CPAMP_URL='http://new-host:18317'
export NEW_CPAMP_ADMIN_KEY='cpamp_...'

curl -fsS \
  -X PUT \
  -H "Authorization: Bearer ${NEW_CPAMP_ADMIN_KEY}" \
  -H 'Content-Type: application/json' \
  --data-binary @manager-config.json \
  "${NEW_CPAMP_URL}/usage-service/config"
```

导入时会校验 CPA Management API；成功后检查采集器状态和相关开关。确认恢复完成后安全删除导出文件和临时密钥文件。

如果旧实例仍由环境变量或 secret 文件管理，API 返回的 `source` 为 `env`，连接字段不能通过 API 导入覆盖；应先用上面的离线命令把 CPA 连接写入新 SQLite，或在新实例 setup 中重新填写。管理员登录凭证也不属于 Manager 配置导出：新实例使用新生成或显式设置的 `CPA_MANAGER_ADMIN_KEY`。
