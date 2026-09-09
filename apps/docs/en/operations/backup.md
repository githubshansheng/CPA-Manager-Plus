# Backup And Restore

CPAMP keeps request history, configuration, and encrypted credentials on the host. The common mistake is backing up only `usage.sqlite` and missing WAL/SHM files, `data.key`, or secret files in the install directory.

## Required Backup Files

Back up these files as a set:

- `usage.sqlite`
- `usage.sqlite-wal`
- `usage.sqlite-shm`
- `database-control.json.enc`
- `database-control.json.enc.bak` (when present)
- `mysql-ca-*.pem` (when a custom MySQL CA certificate is configured)
- `data.key`

If your deployment directory contains custom configuration files, back them up too. With the one-click installer, also back up `secrets/` and `data/` under the install directory; after a successful import, `secrets/cpa-management-key` is normally gone, but it may remain after a failed upgrade or with `CPAMP_SKIP_EXECUTE=1` for retry. Manual env/secret deployments should back up their matching secret files.

`database-control.json.enc` contains the database routing generation, redacted connection details, encrypted MySQL password, authentication copy, and migration/failover state. Restore it only with the matching `data.key`; do not copy it by itself to another instance.

## Backup Boundary After Enabling MySQL

After MySQL becomes the business-read primary and old SQLite history is cleaned, MySQL is the only full-history copy outside the recent cache window. A CPAMP data-directory backup does not include the external MySQL database. Configure independent MySQL physical or transactionally consistent logical backups and rehearse restoration regularly.

Before backup, verify that replication backlog is `0` in System Info and record both watermarks and the routing generation. During restore, treat MySQL, the complete CPAMP data directory, and `data.key` as one recovery point; never combine a newer control file with an older MySQL backup. The MySQL-to-SQLite action rebuilds only configuration and the most recent N-day cache, so it is not a full-history backup.

## Why data.key Is Required

CPA connections saved through setup or the panel encrypt the CPA Management Key with `data.key` before saving it to SQLite.

- If only `usage.sqlite` leaks, an attacker cannot directly read the CPA Management Key.
- If both `usage.sqlite` and `data.key` leak, the CPA Management Key can be decrypted.
- If `data.key` is lost, the saved CPA Management Key cannot be recovered. You must save the CPA connection configuration again.

If a CPA connection is managed by manual environment variables or secret files, the CPA Management Key may not be written to SQLite. Back up the related secret files together with the data directory. The installer's env input is migrated into SQLite after success, so do not rely on the one-time input file alone.

## Docker Backup Example

If you use a named volume, stop the container first, then export through a temporary container:

```bash
docker stop cpa-manager-plus
docker run --rm \
  -v cpa-manager-plus-data:/data:ro \
  -v "$PWD":/backup \
  alpine \
  tar czf /backup/cpa-manager-plus-data.tgz -C /data .
docker start cpa-manager-plus
```

If you use a host directory mount:

```bash
docker stop cpa-manager-plus
cp -a /srv/cpa-manager-plus-data /srv/cpa-manager-plus-data.backup
docker start cpa-manager-plus
```

## Native Package Backup

Stop the process, then copy the data directory:

```bash
cp -a ./data ./data.backup
```

Windows PowerShell:

```powershell
Copy-Item -Recurse .\data .\data.backup
```

## Restore

1. Stop CPAMP.
2. Restore the full data directory.
3. Confirm that `usage.sqlite`, `database-control.json.enc`, and `data.key` come from the same backup.
4. If the CPA connection is env/secret-managed, also restore `secrets/` from the install directory.
5. Start CPAMP.
6. Log in and check configuration, monitoring data, and collector status.

If restore produces decryption errors, first check whether `data.key` matches the SQLite database.

## Adopt Or Switch To An Existing SQLite Database

During first-time setup, choose **Adopt existing SQLite**. After the project has been initialized, use **System Info → Database Management → Switch SQLite source**. Both workflows make Manager Server open the selected file directly on its next start; they do not copy the database. The path must be an absolute path visible from the Manager Server host or container. A path from the browser's machine is valid only when the browser and server share that filesystem.

Prepare the source in this order:

1. Gracefully stop every old CPAMP instance that still uses the database.
2. Close `sqlite3`, database browsers, backup scripts, and maintenance jobs, including long-running read-only transactions. Read-only connections normally do not exclusively lock the whole database, but they can prevent a WAL checkpoint or truncate from completing.
3. Treat `usage.sqlite`, any remaining `usage.sqlite-wal`/`usage.sqlite-shm`, and the matching `data.key` as one recovery point. Never mix files from different points in time.
4. Enter the absolute SQLite path. If the old database contains an encrypted CPA connection, also enter the matching absolute `data.key` path. If it already has an administrator credential, verify the old Admin Key.
5. Run preflight and explicitly confirm that every old instance and tool has stopped. Preflight checks file permissions, CPA Manager tables, `PRAGMA quick_check(1)`, write-lock availability, the data key, and the administrator credential.

A normally started Manager Server holds a process lock next to its database, so another instance that follows the same lock protocol cannot adopt that SQLite file concurrently. SQLite WAL improves concurrency for short transactions; it is not a multi-instance coordination mechanism. Bypassing the process lock or allowing another program to keep writing can still cause `database is locked`, indefinitely delayed checkpoints, or an inconsistent recovery point.

When `USAGE_DB_PATH` is set, the panel cannot override the SQLite path. When `CPA_MANAGER_DATA_KEY_PATH` is set, the panel cannot select a different data key. Remove the conflicting environment override and restart normally before adoption. A running project can switch only from a stable SQLite-only topology: all three read/write primary routes use SQLite, MySQL and replication are disabled, no migration or failover is active, and SQLite cleanup/rebuild has finished.

After saving a selection, the current process keeps using its original database. The panel shows the pending path and a **Restart Manager Server** button. Only that explicit action stops HTTP, background workers, WAL maintenance, and connection pools, releases process locks, and then opens the target. Reloading the browser is not a restart.

The selection is stored as `.cpa-manager-plus.sqlite-source.json` in the data directory. During a switch, the current `database-control.json.enc` and its `.bak` are archived with a `.before-sqlite-switch-<timestamp>` suffix so control state encrypted by different data keys is never mixed. If the target disappears, becomes locked, has a mismatched data key, or fails initialization during restart, the service releases target resources, restores the previous SQLite and control files, starts the previous source automatically, and retains the failed path, startup stage, and underlying cause in System Info. Control files created for the failed target are retained with a `.failed-target` suffix for diagnosis.

If the old database already has an administrator credential, log in with its old Admin Key after restart. If it has no administrator credential, confirming the switch copies the current credential salt/hash into the target, so the current Admin Key remains valid. The plaintext Admin Key is never written to the source-selection file.

## Move Manager Configuration Without Request History

If the old `usage.sqlite` is large and request history is no longer needed, start the replacement instance with an empty data directory and use the existing Manager configuration API to move the non-sensitive CPA URL, collector, Codex inspection, and External Usage Service settings. This does not copy `usage_events`, rollups, inspection run history, model prices, API Key aliases, or account-processing policy, and it does not export the CPA Management Key.

Export while the old instance is still reachable:

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

The new `manager-config.json` does not contain the CPA Management Key; still treat the configuration file as sensitive and do not commit or attach it to an issue. An export from an older version may contain the plaintext key, so handle it as a secret and delete it after migration.

Stop the old instance and prepare an empty data directory for the replacement. While Manager Server is not running, provide the CPA Management Key with the offline command:

```bash
cpa-manager-plus store-cpa-connection \
  --cpa-base-url 'http://cpa:8317' \
  --management-key-file '/secure/cpa-management-key' \
  --db-path './data/usage.sqlite' \
  --data-key-path './data/data.key'
```

Stop Manager Server before running this command. It encrypts the key into SQLite and never echoes it.

Connection records follow these authority rules: a complete `manager_config_v1` is authoritative; if it coexists with stale or conflicting legacy `setup` data, startup and import keep the manager connection and canonicalize setup without repair. If manager data is partial and legacy setup is complete and compatible with its existing fields, setup completes manager. The command above refuses the write and explains the repair path only when no complete authority exists and partial records conflict, or when the persisted state cannot be resolved. After confirming this explicit connection is correct, append `--repair-conflict` to repair explicitly:

```bash
cpa-manager-plus store-cpa-connection \
  --repair-conflict \
  --cpa-base-url 'http://cpa:8317' \
  --management-key-file '/secure/cpa-management-key' \
  --db-path './data/usage.sqlite' \
  --data-key-path './data/data.key'
```

`--repair-conflict` exists only for persisted state the resolver cannot trust: `manager_config_v1`/`setup` rows that conflict with each other, or authority-less partial rows that conflict with the request. It writes the connection you explicitly provide into both `manager_config_v1` and the legacy `setup` mirror in a single transaction (the key stays encrypted at rest) while preserving collector settings and other data. A complete and consistent stored connection still requires exactly matching input; repair never rebinds silently. After repairing, start normally and the connection-storage migration completes as usual. Start the new instance, then import the remaining configuration:

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

The import validates the CPA Management API. After it succeeds, verify collector status and the related settings, then securely delete the exported file and temporary key file.

If the old connection is managed through environment variables or secret files, the API reports `source` as `env` and an API import cannot override the connection fields. Use the offline command above to write the CPA connection into the new SQLite database, or enter it again during setup. Administrator credentials are also outside the Manager configuration export; the new instance uses its newly generated or explicitly configured `CPA_MANAGER_ADMIN_KEY`.
