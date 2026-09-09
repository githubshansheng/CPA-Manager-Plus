# Online SQLite-to-MySQL Migration

[简体中文](../../operations/mysql-migration) | [English](./mysql-migration) | [繁體中文](../../zh-TW/operations/mysql-migration) | [Русский](../../ru/operations/mysql-migration)

Manager Server can retain its default SQLite behavior while migrating full history to an external MySQL database. After cutover, SQLite remains the configuration store and recent-data cache. This feature targets a single Manager Server process; it does not deploy a bundled MySQL container or perform automatic failover.

## Supported Target

- Official MySQL 8.x is supported starting at 8.0.12; MariaDB and MySQL 9.x are not supported.
- MySQL 8.0.17+ schemas use `utf8mb4_0900_bin`. Versions 8.0.12–8.0.16 automatically use the available NO PAD, case- and accent-sensitive `utf8mb4_0900_as_cs`. Sessions use UTC, strict SQL mode, and `innodb_strict_mode=ON`.
- The account needs `SELECT`, `INSERT`, `UPDATE`, `DELETE`, `CREATE`, `ALTER`, `DROP`, `INDEX`, `REFERENCES`, and `TRIGGER` on the selected schema. CPAMP verifies these with a random probe table instead of trusting `SHOW GRANTS` parsing alone.
- When binary logging is enabled and a regular schema account receives Error 1419 while creating a trigger, require a DBA to set and persist `log_bin_trust_function_creators=ON`, or have a DBA with the required administrative privilege install the trigger; do not grant `SUPER` to the application account.
- External MySQL uses certificate identity verification by default. Disabling TLS displays a security warning and requires a second confirmation.
- `max_allowed_packet` must be at least 4 MiB. Preflight stops if any authoritative SQLite row cannot fit in one MySQL protocol packet.

## Data Routing

In normal mode, each authoritative write commits to SQLite and appends a complete Outbox group in the same transaction. A worker eventually applies it to MySQL. After read cutover, business queries prefer MySQL and fall back to SQLite only for classified connection or availability failures. SQL, schema, permission, scan, and data errors are never hidden by fallback.

SQLite permanently retains settings, administrator credentials, database connection settings, model prices and their context/service tiers, and API Key aliases. After cutover and guarded cleanup, other business data defaults to a 15-day cache while MySQL retains full history. Active, pending, or incomplete actions, cooldowns, inspections, and quota lifecycle records are not aged out.

## Before You Start

1. Back up the complete CPAMP data directory, including SQLite/WAL/SHM, `database-control.json.enc`, its `.bak`, and `data.key`.
2. Configure an independent MySQL backup and complete a restore rehearsal.
3. Ensure the target database is empty or exactly compatible with the schema manifest shown by CPAMP.
4. Confirm capacity, `max_allowed_packet`, connection limits, and backup retention for the full history.
5. Work during a quiet period. Final validation briefly pauses the Collector and background write workers.

## Three Migration Stages

### 1. Test MySQL And Enable Dual Write

Open **System → Database Topology**, enter the address, database, account, password, TLS mode, and CA certificate, then select **Test MySQL**. Save the connection and select **Enable dual write**.

The backend validates version, character set/collation, UTC, strict modes, packet size, and real DDL/DML access. It creates the complete schema, copies every configuration field, enables the SQLite Outbox, and starts live synchronization. Business reads still use SQLite at this point.

The password is never returned to the browser or written to logs. Leaving it empty on a later save preserves the existing password.

If an upgrade leaves the MySQL schema stale, stop replication and migration and make sure MySQL is serving neither reads nor writes, then select **Reinitialize MySQL schema**. The UI presents two consecutive dangerous confirmations and names the current database; the backend also checks the target, database name, routing generation, and idempotency key. Once confirmed, CPAMP deletes every table, view, trigger, and row in the configured database and recreates the latest schema manifest. It does not drop the database itself, touch SQLite, copy data, enable replication, or change routing. Complete a MySQL backup before using it.

### 2. Migrate History

Select **Migrate history**. CPAMP copies every authoritative table in foreign-key order at a stable primary-key/rowid watermark, preserving explicit IDs, NULL, raw JSON, failure bodies, client/IP data, tokens, service tiers, prices, inspections, actions, cooldowns, and quota lifecycle fields.

The default batch is 1,000 rows and is bounded by a 4 MiB read budget; an oversized row receives its own batch. Durable checkpoints support pause, resume, retry, and process restart. Historical batches cannot overwrite newer rows already delivered by the Outbox. MySQL then rebuilds its aggregates, projections, indexes, and search data from authoritative rows.

**Migration task history** in System Info lists persisted tasks, their current phase and status, per-table rows/bytes/requests, and the complete execution timeline. Background failures are copied verbatim into an append-only audit record; resuming a task clears only its current alert and never removes an earlier failure reason. Administrators can query the same records through `GET /v0/management/databases/migrations?limit=20`.

### 3. Validate And Cut Over Business Reads

Wait for backlog `0`, then select **Validate**. CPAMP briefly acquires the global write fence, catches MySQL up to the final Outbox watermark, and compares schema, row counts, primary-key ranges, foreign keys, field-normalized SHA-256, tokens, success/failure counts, and cost calculated with the frozen price-book snapshot.

A validation token and **Cut over business reads** become available only after every check and the derived/search conformance contract pass. Cutover requires the current migration ID, token, target, and routing generation; stale values return `409`. SQLite remains the default write-front and recent cache after cutover.

## Cache Cleanup

Scheduled cleanup is disabled by default (the checkbox is initially clear), so startup and upgrades never delete SQLite data implicitly. After an administrator explicitly enables it, SQLite history still cannot be cleaned before MySQL read cutover. Once cutover, validation, and synchronization watermarks are safe, preview the cleanup and then start the bounded cleanup task. It pauses automatically while MySQL is unavailable, Outbox is pending, migration is incomplete, or validation is stale. Existing installations retain their previously saved setting.

Manual write-primary failover, SQLite cleanup, and SQLite cache rebuild are dangerous operations. In addition to the current routing generation and idempotency key, each request must echo the target backend, migration ID, and validation token currently shown by the UI. The backend checks them again against the encrypted control file and durable migration state; missing or stale confirmation never executes.

Online deletion makes SQLite pages reusable; it does not immediately shrink the physical `usage.sqlite` file. System Info reports physical size, live pages, and reusable space separately. Do not run an external `VACUUM` or replace the database file while Manager Server is active.

## Failure And Recovery

- **MySQL interruption:** SQLite continues accepting writes and Outbox accumulates. Fallback responses include `X-CPAMP-Data-Source`, completeness, and cache-coverage headers, and the UI keeps a visible partial-data warning.
- **SQLite cannot write:** CPAMP enters read-only/pending-switch state and alerts. It never promotes MySQL automatically; an administrator must verify target, epoch, and watermarks before confirming failover.
- **Rebuild SQLite from MySQL:** this restores all configuration plus only the latest N-day business cache. It builds and validates a temporary SQLite database before an atomic replacement under a short write fence. It is not a full-history migration back to SQLite.
- **SQLite recovery after MySQL writes:** wait for reverse Outbox catch-up and validation before switching back. Writes from an old fencing epoch are rejected.

If backlog exists with no progress for 60 seconds, inspect connectivity, disk, locks, and permissions. CPAMP marks the worker `stalled` after 120 seconds. Never start a second Manager Server process to “speed up” migration.
