// Code generated from the canonical fresh SQLite schema; DO NOT EDIT.

package schema

const manifestJSON = `{
  "version": 1,
  "tables": [
    {
      "name": "account_action_candidates",
      "class": "authoritative",
      "columns": [
        {
          "name": "id",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 1,
          "autoIncrement": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "action_type",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "status",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "provider",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_file_name",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_index",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "account_snapshot",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "account_id_snapshot",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_label",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "reason_code",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "reason",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auto_disable_eligible",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "TINYINT"
        },
        {
          "name": "auto_disabled_at_ms",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "evidence_json",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "last_error",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "first_seen_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "last_seen_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "hit_count",
          "kind": "integer",
          "nullable": false,
          "default": "1",
          "mysqlType": "BIGINT"
        },
        {
          "name": "created_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ],
      "indexes": [
        {
          "name": "idx_account_action_candidates_status_seen",
          "columns": [
            "status",
            "last_seen_at_ms"
          ],
          "sourceDdl": "CREATE INDEX idx_account_action_candidates_status_seen on account_action_candidates(status, last_seen_at_ms)"
        },
        {
          "name": "idx_account_action_candidates_pending_identity_action",
          "unique": true,
          "sourceDdl": "CREATE UNIQUE INDEX idx_account_action_candidates_pending_identity_action\n\t\ton account_action_candidates(\n\t\t\tauth_file_name,\n\t\t\taction_type,\n\t\t\tcoalesce(trim(reason_code), ''),\n\t\t\tcoalesce(trim(auth_index), ''),\n\t\t\tcase when coalesce(trim(auth_index), '') \u003c\u003e '' then '' else coalesce(trim(account_id_snapshot), '') end,\n\t\t\tcase when coalesce(trim(auth_index), '') \u003c\u003e '' then ''\n\t\t\t\telse case coalesce(lower(replace(trim(provider), '_', '-')), '')\n\t\t\t\t\twhen 'x-ai' then 'xai'\n\t\t\t\t\twhen 'grok' then 'xai'\n\t\t\t\t\telse coalesce(lower(replace(trim(provider), '_', '-')), '')\n\t\t\t\tend\n\t\t\tend,\n\t\t\tcase when coalesce(trim(auth_index), '') \u003c\u003e '' or coalesce(trim(account_id_snapshot), '') \u003c\u003e '' then ''\n\t\t\t\telse coalesce(trim(account_snapshot), '')\n\t\t\tend\n\t\t) where status = 'pending'"
        }
      ],
      "sourceDdl": "CREATE TABLE account_action_candidates (\n\t\t\tid integer primary key autoincrement,\n\t\t\taction_type text not null,\n\t\t\tstatus text not null,\n\t\t\tprovider text,\n\t\t\tauth_file_name text not null,\n\t\t\tauth_index text,\n\t\t\taccount_snapshot text,\n\t\t\taccount_id_snapshot text,\n\t\t\tauth_label text,\n\t\t\treason_code text,\n\t\t\treason text,\n\t\t\tauto_disable_eligible integer not null default 0,\n\t\t\tauto_disabled_at_ms integer,\n\t\t\tevidence_json text,\n\t\t\tlast_error text,\n\t\t\tfirst_seen_at_ms integer not null,\n\t\t\tlast_seen_at_ms integer not null,\n\t\t\thit_count integer not null default 1,\n\t\t\tcreated_at_ms integer not null,\n\t\t\tupdated_at_ms integer not null\n\t\t)"
    },
    {
      "name": "account_quota_cycles",
      "class": "authoritative",
      "columns": [
        {
          "name": "id",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 1,
          "autoIncrement": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "activation_id",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "provider_cycle_key",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "state",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "scheduled_start_ms",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "scheduled_end_ms",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "actual_start_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "actual_end_ms",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "duration_seconds",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "boundary_accuracy",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "end_reason",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "first_observation_id",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "last_observation_id",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "parent_cycle_id",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "created_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ],
      "foreignKeys": [
        {
          "name": "fk_account_quota_cycles_0",
          "column": "parent_cycle_id",
          "refTable": "account_quota_cycles",
          "refColumn": "id",
          "onUpdate": "NO ACTION",
          "onDelete": "NO ACTION"
        },
        {
          "name": "fk_account_quota_cycles_1",
          "column": "last_observation_id",
          "refTable": "account_quota_observations",
          "refColumn": "id",
          "onUpdate": "NO ACTION",
          "onDelete": "NO ACTION"
        },
        {
          "name": "fk_account_quota_cycles_2",
          "column": "first_observation_id",
          "refTable": "account_quota_observations",
          "refColumn": "id",
          "onUpdate": "NO ACTION",
          "onDelete": "NO ACTION"
        },
        {
          "name": "fk_account_quota_cycles_3",
          "column": "activation_id",
          "refTable": "account_quota_window_activations",
          "refColumn": "id",
          "onUpdate": "NO ACTION",
          "onDelete": "NO ACTION"
        }
      ],
      "indexes": [
        {
          "name": "idx_quota_cycles_history",
          "columns": [
            "activation_id",
            "actual_start_ms"
          ],
          "sourceDdl": "CREATE INDEX idx_quota_cycles_history on account_quota_cycles(activation_id, actual_start_ms desc)"
        },
        {
          "name": "idx_quota_cycles_active",
          "columns": [
            "activation_id"
          ],
          "unique": true,
          "sourceDdl": "CREATE UNIQUE INDEX idx_quota_cycles_active on account_quota_cycles(activation_id) where actual_end_ms is null"
        },
        {
          "name": "sqlite_autoindex_account_quota_cycles_1",
          "columns": [
            "activation_id",
            "provider_cycle_key"
          ],
          "unique": true
        }
      ],
      "sourceDdl": "CREATE TABLE account_quota_cycles (\n\t\t\tid integer primary key autoincrement,\n\t\t\tactivation_id integer not null,\n\t\t\tprovider_cycle_key text not null,\n\t\t\tstate text not null,\n\t\t\tscheduled_start_ms integer,\n\t\t\tscheduled_end_ms integer,\n\t\t\tactual_start_ms integer not null,\n\t\t\tactual_end_ms integer,\n\t\t\tduration_seconds integer,\n\t\t\tboundary_accuracy text not null,\n\t\t\tend_reason text,\n\t\t\tfirst_observation_id integer,\n\t\t\tlast_observation_id integer,\n\t\t\tparent_cycle_id integer,\n\t\t\tcreated_at_ms integer not null,\n\t\t\tupdated_at_ms integer not null,\n\t\t\tunique(activation_id, provider_cycle_key),\n\t\t\tforeign key(activation_id) references account_quota_window_activations(id),\n\t\t\tforeign key(first_observation_id) references account_quota_observations(id),\n\t\t\tforeign key(last_observation_id) references account_quota_observations(id),\n\t\t\tforeign key(parent_cycle_id) references account_quota_cycles(id)\n\t\t)"
    },
    {
      "name": "account_quota_observations",
      "class": "authoritative",
      "columns": [
        {
          "name": "id",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 1,
          "autoIncrement": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "observation_hash",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "account_key",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "provider",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "source",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "source_observation_id",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "inventory_scope_key",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "inventory_mode",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "observed_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "window_count",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "lifecycle_applied",
          "kind": "integer",
          "nullable": false,
          "default": "1",
          "mysqlType": "TINYINT"
        },
        {
          "name": "created_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ],
      "indexes": [
        {
          "name": "idx_quota_observations_lifecycle_watermark",
          "columns": [
            "account_key",
            "provider",
            "inventory_scope_key",
            "lifecycle_applied",
            "observed_at_ms"
          ],
          "sourceDdl": "CREATE INDEX idx_quota_observations_lifecycle_watermark on account_quota_observations(account_key, provider, inventory_scope_key, lifecycle_applied, observed_at_ms desc)"
        },
        {
          "name": "idx_quota_observations_inventory",
          "columns": [
            "account_key",
            "provider",
            "inventory_scope_key",
            "observed_at_ms"
          ],
          "sourceDdl": "CREATE INDEX idx_quota_observations_inventory on account_quota_observations(account_key, provider, inventory_scope_key, observed_at_ms desc)"
        },
        {
          "name": "idx_quota_observations_account_time",
          "columns": [
            "account_key",
            "provider",
            "observed_at_ms"
          ],
          "sourceDdl": "CREATE INDEX idx_quota_observations_account_time on account_quota_observations(account_key, provider, observed_at_ms desc)"
        },
        {
          "name": "sqlite_autoindex_account_quota_observations_1",
          "columns": [
            "observation_hash"
          ],
          "unique": true
        }
      ],
      "sourceDdl": "CREATE TABLE account_quota_observations (\n\t\t\tid integer primary key autoincrement,\n\t\t\tobservation_hash text not null unique,\n\t\t\taccount_key text not null,\n\t\t\tprovider text not null,\n\t\t\tsource text not null,\n\t\t\tsource_observation_id text,\n\t\t\tinventory_scope_key text not null,\n\t\t\tinventory_mode text not null,\n\t\t\tobserved_at_ms integer not null,\n\t\t\twindow_count integer not null default 0,\n\t\t\tlifecycle_applied integer not null default 1,\n\t\t\tcreated_at_ms integer not null\n\t\t)"
    },
    {
      "name": "account_quota_snapshots",
      "class": "authoritative",
      "columns": [
        {
          "name": "id",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 1,
          "autoIncrement": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "observation_id",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "logical_window_id",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "activation_id",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "cycle_id",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "account_key",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "provider",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "provider_window_id",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "window_kind",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "window_mode",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "model_scope_kind",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "model_scope_key",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "model_ids_json",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "scope_fingerprint",
          "kind": "text",
          "nullable": false,
          "default": "''",
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "content_hash",
          "kind": "text",
          "nullable": false,
          "default": "''",
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "source",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "source_observation_id",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "observed_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "boundary_accuracy",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "cycle_start_ms",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "cycle_end_ms",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "duration_seconds",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "used_percent",
          "kind": "real",
          "nullable": true,
          "mysqlType": "DOUBLE"
        },
        {
          "name": "remaining_percent",
          "kind": "real",
          "nullable": true,
          "mysqlType": "DOUBLE"
        },
        {
          "name": "used_value",
          "kind": "real",
          "nullable": true,
          "mysqlType": "DOUBLE"
        },
        {
          "name": "limit_value",
          "kind": "real",
          "nullable": true,
          "mysqlType": "DOUBLE"
        },
        {
          "name": "quota_unit",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "reset_credits_available",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "TINYINT"
        },
        {
          "name": "reset_credits_json",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "plan_type",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "created_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ],
      "indexes": [
        {
          "name": "idx_quota_snapshots_legacy_migration",
          "sourceDdl": "CREATE INDEX idx_quota_snapshots_legacy_migration on account_quota_snapshots(\n\t\taccount_key, provider, observed_at_ms,\n\t\tcase lower(trim(source))\n\t\t\twhen 'response_body' then 1\n\t\t\twhen 'api_query' then 2\n\t\t\twhen 'inspection' then 3\n\t\t\telse 0\n\t\tend,\n\t\tcoalesce(source_observation_id, ''), id\n\t) where observation_id is null"
        },
        {
          "name": "idx_quota_snapshots_cycle_evidence",
          "columns": [
            "cycle_id",
            "observed_at_ms",
            "id"
          ],
          "sourceDdl": "CREATE INDEX idx_quota_snapshots_cycle_evidence on account_quota_snapshots(cycle_id, observed_at_ms, id)"
        },
        {
          "name": "idx_quota_snapshots_window_cycle",
          "columns": [
            "logical_window_id",
            "cycle_id",
            "observed_at_ms"
          ],
          "sourceDdl": "CREATE INDEX idx_quota_snapshots_window_cycle on account_quota_snapshots(logical_window_id, cycle_id, observed_at_ms desc)"
        },
        {
          "name": "idx_quota_snapshots_observation",
          "columns": [
            "observation_id"
          ],
          "sourceDdl": "CREATE INDEX idx_quota_snapshots_observation on account_quota_snapshots(observation_id)"
        },
        {
          "name": "idx_quota_snapshots_latest",
          "columns": [
            "account_key",
            "provider",
            "provider_window_id",
            "model_scope_kind",
            "model_scope_key",
            "observed_at_ms"
          ],
          "sourceDdl": "CREATE INDEX idx_quota_snapshots_latest on account_quota_snapshots(account_key, provider, provider_window_id, model_scope_kind, model_scope_key, observed_at_ms desc)"
        }
      ],
      "sourceDdl": "CREATE TABLE account_quota_snapshots (\n\t\t\tid integer primary key autoincrement,\n\t\t\tobservation_id integer,\n\t\t\tlogical_window_id integer,\n\t\t\tactivation_id integer,\n\t\t\tcycle_id integer,\n\t\t\taccount_key text not null,\n\t\t\tprovider text not null,\n\t\t\tprovider_window_id text not null,\n\t\t\twindow_kind text not null,\n\t\t\twindow_mode text not null,\n\t\t\tmodel_scope_kind text not null,\n\t\t\tmodel_scope_key text,\n\t\t\tmodel_ids_json text,\n\t\t\tscope_fingerprint text not null default '',\n\t\t\tcontent_hash text not null default '',\n\t\t\tsource text not null,\n\t\t\tsource_observation_id text,\n\t\t\tobserved_at_ms integer not null,\n\t\t\tboundary_accuracy text not null,\n\t\t\tcycle_start_ms integer,\n\t\t\tcycle_end_ms integer,\n\t\t\tduration_seconds integer,\n\t\t\tused_percent real,\n\t\t\tremaining_percent real,\n\t\t\tused_value real,\n\t\t\tlimit_value real,\n\t\t\tquota_unit text,\n\t\t\treset_credits_available integer,\n\t\t\treset_credits_json text,\n\t\t\tplan_type text,\n\t\t\tcreated_at_ms integer not null\n\t\t)"
    },
    {
      "name": "account_quota_window_activations",
      "class": "authoritative",
      "columns": [
        {
          "name": "id",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 1,
          "autoIncrement": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "window_id",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "generation",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "status",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "activated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "deactivated_at_ms",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "activation_accuracy",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "deactivation_reason",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "activate_observation_id",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "deactivate_observation_id",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "created_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ],
      "foreignKeys": [
        {
          "name": "fk_account_quota_window_activations_0",
          "column": "deactivate_observation_id",
          "refTable": "account_quota_observations",
          "refColumn": "id",
          "onUpdate": "NO ACTION",
          "onDelete": "NO ACTION"
        },
        {
          "name": "fk_account_quota_window_activations_1",
          "column": "activate_observation_id",
          "refTable": "account_quota_observations",
          "refColumn": "id",
          "onUpdate": "NO ACTION",
          "onDelete": "NO ACTION"
        },
        {
          "name": "fk_account_quota_window_activations_2",
          "column": "window_id",
          "refTable": "account_quota_windows",
          "refColumn": "id",
          "onUpdate": "NO ACTION",
          "onDelete": "NO ACTION"
        }
      ],
      "indexes": [
        {
          "name": "idx_quota_activations_active",
          "columns": [
            "window_id"
          ],
          "unique": true,
          "sourceDdl": "CREATE UNIQUE INDEX idx_quota_activations_active on account_quota_window_activations(window_id) where deactivated_at_ms is null"
        },
        {
          "name": "sqlite_autoindex_account_quota_window_activations_1",
          "columns": [
            "window_id",
            "generation"
          ],
          "unique": true
        }
      ],
      "sourceDdl": "CREATE TABLE account_quota_window_activations (\n\t\t\tid integer primary key autoincrement,\n\t\t\twindow_id integer not null,\n\t\t\tgeneration integer not null,\n\t\t\tstatus text not null,\n\t\t\tactivated_at_ms integer not null,\n\t\t\tdeactivated_at_ms integer,\n\t\t\tactivation_accuracy text not null,\n\t\t\tdeactivation_reason text,\n\t\t\tactivate_observation_id integer,\n\t\t\tdeactivate_observation_id integer,\n\t\t\tcreated_at_ms integer not null,\n\t\t\tupdated_at_ms integer not null,\n\t\t\tunique(window_id, generation),\n\t\t\tforeign key(window_id) references account_quota_windows(id),\n\t\t\tforeign key(activate_observation_id) references account_quota_observations(id),\n\t\t\tforeign key(deactivate_observation_id) references account_quota_observations(id)\n\t\t)"
    },
    {
      "name": "account_quota_windows",
      "class": "authoritative",
      "columns": [
        {
          "name": "id",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 1,
          "autoIncrement": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "account_key",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "provider",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "provider_window_id",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "window_kind",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "window_mode",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "model_scope_kind",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "model_scope_key",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "model_ids_json",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "scope_fingerprint",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "inventory_scope_key",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "relationship_kind",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "container_provider_window_id",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "availability",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "generation",
          "kind": "integer",
          "nullable": false,
          "default": "1",
          "mysqlType": "BIGINT"
        },
        {
          "name": "absence_count",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "first_seen_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "last_seen_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "missing_since_ms",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "deactivated_at_ms",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "last_observation_id",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "created_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ],
      "foreignKeys": [
        {
          "name": "fk_account_quota_windows_0",
          "column": "last_observation_id",
          "refTable": "account_quota_observations",
          "refColumn": "id",
          "onUpdate": "NO ACTION",
          "onDelete": "NO ACTION"
        }
      ],
      "indexes": [
        {
          "name": "idx_quota_windows_inventory",
          "columns": [
            "account_key",
            "provider",
            "inventory_scope_key",
            "availability"
          ],
          "sourceDdl": "CREATE INDEX idx_quota_windows_inventory on account_quota_windows(account_key, provider, inventory_scope_key, availability)"
        },
        {
          "name": "idx_quota_windows_account_state",
          "columns": [
            "account_key",
            "provider",
            "availability",
            "updated_at_ms"
          ],
          "sourceDdl": "CREATE INDEX idx_quota_windows_account_state on account_quota_windows(account_key, provider, availability, updated_at_ms desc)"
        },
        {
          "name": "sqlite_autoindex_account_quota_windows_1",
          "columns": [
            "account_key",
            "provider",
            "provider_window_id",
            "scope_fingerprint"
          ],
          "unique": true
        }
      ],
      "sourceDdl": "CREATE TABLE account_quota_windows (\n\t\t\tid integer primary key autoincrement,\n\t\t\taccount_key text not null,\n\t\t\tprovider text not null,\n\t\t\tprovider_window_id text not null,\n\t\t\twindow_kind text not null,\n\t\t\twindow_mode text not null,\n\t\t\tmodel_scope_kind text not null,\n\t\t\tmodel_scope_key text,\n\t\t\tmodel_ids_json text,\n\t\t\tscope_fingerprint text not null,\n\t\t\tinventory_scope_key text not null,\n\t\t\trelationship_kind text,\n\t\t\tcontainer_provider_window_id text,\n\t\t\tavailability text not null,\n\t\t\tgeneration integer not null default 1,\n\t\t\tabsence_count integer not null default 0,\n\t\t\tfirst_seen_at_ms integer not null,\n\t\t\tlast_seen_at_ms integer not null,\n\t\t\tmissing_since_ms integer,\n\t\t\tdeactivated_at_ms integer,\n\t\t\tlast_observation_id integer,\n\t\t\tcreated_at_ms integer not null,\n\t\t\tupdated_at_ms integer not null,\n\t\t\tunique(account_key, provider, provider_window_id, scope_fingerprint),\n\t\t\tforeign key(last_observation_id) references account_quota_observations(id)\n\t\t)"
    },
    {
      "name": "api_key_aliases",
      "class": "authoritative",
      "columns": [
        {
          "name": "api_key_hash",
          "kind": "text",
          "nullable": true,
          "primaryKeyPosition": 1,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "alias",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ],
      "indexes": [
        {
          "name": "sqlite_autoindex_api_key_aliases_1",
          "columns": [
            "api_key_hash"
          ],
          "unique": true
        }
      ],
      "sourceDdl": "CREATE TABLE api_key_aliases (\n\t\t\tapi_key_hash text primary key,\n\t\t\talias text not null,\n\t\t\tupdated_at_ms integer not null\n\t\t)"
    },
    {
      "name": "codex_inspection_disable_ownership",
      "class": "authoritative",
      "columns": [
        {
          "name": "file_name",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 1,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "provider",
          "kind": "text",
          "nullable": false,
          "default": "''",
          "primaryKeyPosition": 2,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_index",
          "kind": "text",
          "nullable": false,
          "default": "''",
          "primaryKeyPosition": 3,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "account_id",
          "kind": "text",
          "nullable": false,
          "default": "''",
          "primaryKeyPosition": 4,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "account_snapshot",
          "kind": "text",
          "nullable": false,
          "default": "''",
          "primaryKeyPosition": 5,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "disabled_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ],
      "indexes": [
        {
          "name": "sqlite_autoindex_codex_inspection_disable_ownership_1",
          "columns": [
            "file_name",
            "provider",
            "auth_index",
            "account_id",
            "account_snapshot"
          ],
          "unique": true
        }
      ],
      "sourceDdl": "CREATE TABLE codex_inspection_disable_ownership (\n\t\t\tfile_name text not null,\n\t\t\tprovider text not null default '',\n\t\t\tauth_index text not null default '',\n\t\t\taccount_id text not null default '',\n\t\t\taccount_snapshot text not null default '',\n\t\t\tdisabled_at_ms integer not null,\n\t\t\tupdated_at_ms integer not null,\n\t\t\tprimary key (file_name, provider, auth_index, account_id, account_snapshot)\n\t\t)"
    },
    {
      "name": "codex_inspection_leases",
      "class": "authoritative",
      "columns": [
        {
          "name": "id",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 1,
          "mysqlType": "BIGINT"
        },
        {
          "name": "run_id",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "owner_id",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "heartbeat_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "lease_expires_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ],
      "foreignKeys": [
        {
          "name": "fk_codex_inspection_leases_0",
          "column": "run_id",
          "refTable": "codex_inspection_runs",
          "refColumn": "id",
          "onUpdate": "NO ACTION",
          "onDelete": "SET NULL"
        }
      ],
      "indexes": [
        {
          "name": "idx_codex_inspection_leases_expiry",
          "columns": [
            "lease_expires_at_ms"
          ],
          "sourceDdl": "CREATE INDEX idx_codex_inspection_leases_expiry on codex_inspection_leases(lease_expires_at_ms)"
        }
      ],
      "sourceDdl": "CREATE TABLE codex_inspection_leases (\n\t\t\tid integer primary key check (id = 1),\n\t\t\trun_id integer,\n\t\t\towner_id text not null,\n\t\t\theartbeat_at_ms integer not null,\n\t\t\tlease_expires_at_ms integer not null,\n\t\t\tforeign key(run_id) references codex_inspection_runs(id) on delete set null\n\t\t)"
    },
    {
      "name": "codex_inspection_logs",
      "class": "authoritative",
      "columns": [
        {
          "name": "id",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 1,
          "autoIncrement": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "run_id",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "level",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "message",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "detail_json",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "created_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ],
      "foreignKeys": [
        {
          "name": "fk_codex_inspection_logs_0",
          "column": "run_id",
          "refTable": "codex_inspection_runs",
          "refColumn": "id",
          "onUpdate": "NO ACTION",
          "onDelete": "CASCADE"
        }
      ],
      "indexes": [
        {
          "name": "idx_codex_inspection_logs_run",
          "columns": [
            "run_id",
            "created_at_ms"
          ],
          "sourceDdl": "CREATE INDEX idx_codex_inspection_logs_run on codex_inspection_logs(run_id, created_at_ms)"
        }
      ],
      "sourceDdl": "CREATE TABLE codex_inspection_logs (\n\t\t\tid integer primary key autoincrement,\n\t\t\trun_id integer not null,\n\t\t\tlevel text not null,\n\t\t\tmessage text not null,\n\t\t\tdetail_json text,\n\t\t\tcreated_at_ms integer not null,\n\t\t\tforeign key(run_id) references codex_inspection_runs(id) on delete cascade\n\t\t)"
    },
    {
      "name": "codex_inspection_results",
      "class": "authoritative",
      "columns": [
        {
          "name": "id",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 1,
          "autoIncrement": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "run_id",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "account_key",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "file_name",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "display_account",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "account_snapshot",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_index",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "account_id",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "provider",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "disabled",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "TINYINT"
        },
        {
          "name": "status",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "state",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "action",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "action_reason",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "action_status",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "executed_action",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "action_error",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "status_code",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "used_percent",
          "kind": "real",
          "nullable": true,
          "mysqlType": "DOUBLE"
        },
        {
          "name": "is_quota",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "TINYINT"
        },
        {
          "name": "auto_recover_eligible",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "TINYINT"
        },
        {
          "name": "error",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "plan_type",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "quota_windows_json",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "error_kind",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "error_detail",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "created_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ],
      "foreignKeys": [
        {
          "name": "fk_codex_inspection_results_0",
          "column": "run_id",
          "refTable": "codex_inspection_runs",
          "refColumn": "id",
          "onUpdate": "NO ACTION",
          "onDelete": "CASCADE"
        }
      ],
      "indexes": [
        {
          "name": "idx_codex_inspection_results_run",
          "columns": [
            "run_id"
          ],
          "sourceDdl": "CREATE INDEX idx_codex_inspection_results_run on codex_inspection_results(run_id)"
        },
        {
          "name": "sqlite_autoindex_codex_inspection_results_1",
          "columns": [
            "run_id",
            "account_key"
          ],
          "unique": true
        }
      ],
      "sourceDdl": "CREATE TABLE codex_inspection_results (\n\t\t\tid integer primary key autoincrement,\n\t\t\trun_id integer not null,\n\t\t\taccount_key text not null,\n\t\t\tfile_name text not null,\n\t\t\tdisplay_account text not null,\n\t\t\taccount_snapshot text,\n\t\t\tauth_index text,\n\t\t\taccount_id text,\n\t\t\tprovider text,\n\t\t\tdisabled integer not null default 0,\n\t\t\tstatus text,\n\t\t\tstate text,\n\t\t\taction text not null,\n\t\t\taction_reason text,\n\t\t\taction_status text,\n\t\t\texecuted_action text,\n\t\t\taction_error text,\n\t\t\tstatus_code integer,\n\t\t\tused_percent real,\n\t\t\tis_quota integer not null default 0,\n\t\t\tauto_recover_eligible integer not null default 0,\n\t\t\terror text,\n\t\t\tplan_type text,\n\t\t\tquota_windows_json text,\n\t\t\terror_kind text,\n\t\t\terror_detail text,\n\t\t\tcreated_at_ms integer not null,\n\t\t\tforeign key(run_id) references codex_inspection_runs(id) on delete cascade,\n\t\t\tunique(run_id, account_key)\n\t\t)"
    },
    {
      "name": "codex_inspection_runs",
      "class": "authoritative",
      "columns": [
        {
          "name": "id",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 1,
          "autoIncrement": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "trigger_type",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "trigger_key",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "status",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "started_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "finished_at_ms",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "total_files",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "probe_set_count",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "sampled_count",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "disabled_count",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "enabled_count",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "delete_count",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "disable_count",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "enable_count",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "reauth_count",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "keep_count",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "error",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "settings_json",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "created_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ],
      "indexes": [
        {
          "name": "idx_codex_inspection_runs_trigger",
          "columns": [
            "trigger_type",
            "trigger_key"
          ],
          "sourceDdl": "CREATE INDEX idx_codex_inspection_runs_trigger on codex_inspection_runs(trigger_type, trigger_key)"
        },
        {
          "name": "idx_codex_inspection_runs_status",
          "columns": [
            "status"
          ],
          "sourceDdl": "CREATE INDEX idx_codex_inspection_runs_status on codex_inspection_runs(status)"
        },
        {
          "name": "idx_codex_inspection_runs_started_at",
          "columns": [
            "started_at_ms"
          ],
          "sourceDdl": "CREATE INDEX idx_codex_inspection_runs_started_at on codex_inspection_runs(started_at_ms)"
        }
      ],
      "sourceDdl": "CREATE TABLE codex_inspection_runs (\n\t\t\tid integer primary key autoincrement,\n\t\t\ttrigger_type text not null,\n\t\t\ttrigger_key text,\n\t\t\tstatus text not null,\n\t\t\tstarted_at_ms integer not null,\n\t\t\tfinished_at_ms integer,\n\t\t\ttotal_files integer not null default 0,\n\t\t\tprobe_set_count integer not null default 0,\n\t\t\tsampled_count integer not null default 0,\n\t\t\tdisabled_count integer not null default 0,\n\t\t\tenabled_count integer not null default 0,\n\t\t\tdelete_count integer not null default 0,\n\t\t\tdisable_count integer not null default 0,\n\t\t\tenable_count integer not null default 0,\n\t\t\treauth_count integer not null default 0,\n\t\t\tkeep_count integer not null default 0,\n\t\t\terror text,\n\t\t\tsettings_json text not null,\n\t\t\tcreated_at_ms integer not null,\n\t\t\tupdated_at_ms integer not null\n\t\t)"
    },
    {
      "name": "database_authority_write_context",
      "class": "internal",
      "sqliteOnly": true,
      "columns": [
        {
          "name": "id",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 1,
          "mysqlType": "BIGINT"
        },
        {
          "name": "enabled",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "active",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "transaction_id",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "source_epoch",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "started_at_ms",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "expires_at_ms",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "row_version_base",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "last_row_version",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "journal_suspended",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        }
      ],
      "sourceDdl": "CREATE TABLE database_authority_write_context (\n\t\t\tid INTEGER PRIMARY KEY CHECK (id = 1),\n\t\t\tenabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),\n\t\t\tactive INTEGER NOT NULL DEFAULT 0 CHECK (active IN (0, 1)),\n\t\t\ttransaction_id TEXT,\n\t\t\tsource_epoch INTEGER NOT NULL DEFAULT 0,\n\t\t\tstarted_at_ms INTEGER NOT NULL DEFAULT 0,\n\t\t\texpires_at_ms INTEGER NOT NULL DEFAULT 0,\n\t\t\trow_version_base INTEGER NOT NULL DEFAULT 0,\n\t\t\tlast_row_version INTEGER NOT NULL DEFAULT 0,\n\t\t\tjournal_suspended INTEGER NOT NULL DEFAULT 0 CHECK (journal_suspended IN (0, 1))\n\t\t)"
    },
    {
      "name": "database_cache_coverage",
      "class": "internal",
      "mysqlOnly": true,
      "columns": [
        {
          "name": "id",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 1,
          "mysqlType": "INT"
        },
        {
          "name": "earliest_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "latest_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "earliest_id",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "latest_id",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "watermark",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "complete",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "INT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ]
    },
    {
      "name": "database_cache_policy",
      "class": "internal",
      "mysqlOnly": true,
      "columns": [
        {
          "name": "id",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 1,
          "mysqlType": "INT"
        },
        {
          "name": "generation",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "enabled",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "INT"
        },
        {
          "name": "retention_days",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "INT"
        },
        {
          "name": "batch_size",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "INT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ]
    },
    {
      "name": "database_inbox",
      "class": "internal",
      "mysqlOnly": true,
      "columns": [
        {
          "name": "mutation_id",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 1,
          "mysqlType": "VARCHAR(96)"
        },
        {
          "name": "mutation_digest",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(128)"
        },
        {
          "name": "transaction_id",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(96)"
        },
        {
          "name": "sequence_no",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "INT"
        },
        {
          "name": "source_backend",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(16)"
        },
        {
          "name": "source_epoch",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "outbox_id",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "applied_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ],
      "indexes": [
        {
          "name": "uq_database_inbox_transaction_sequence",
          "columns": [
            "transaction_id",
            "sequence_no"
          ],
          "unique": true
        }
      ]
    },
    {
      "name": "database_inbox_sources",
      "class": "internal",
      "mysqlOnly": true,
      "columns": [
        {
          "name": "source_backend",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 1,
          "mysqlType": "VARCHAR(16)"
        },
        {
          "name": "accepted_epoch",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "watermark",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ]
    },
    {
      "name": "database_journal_contracts",
      "class": "internal",
      "mysqlOnly": true,
      "columns": [
        {
          "name": "table_name",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 1,
          "mysqlType": "VARCHAR(128)"
        },
        {
          "name": "manifest_hash",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(128)"
        },
        {
          "name": "provider_contract",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(128)"
        },
        {
          "name": "installed_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ]
    },
    {
      "name": "database_maintenance_tasks",
      "class": "internal",
      "mysqlOnly": true,
      "columns": [
        {
          "name": "id",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 1,
          "mysqlType": "VARCHAR(64)"
        },
        {
          "name": "idempotency_key",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(128)"
        },
        {
          "name": "generation",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "kind",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(16)"
        },
        {
          "name": "status",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(16)"
        },
        {
          "name": "retention_days",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "INT"
        },
        {
          "name": "validation_token",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(128)"
        },
        {
          "name": "preview_json",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "current_table",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(128)"
        },
        {
          "name": "processed_rows",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "created_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "finished_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "last_error",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        }
      ],
      "indexes": [
        {
          "name": "uq_database_maintenance_idempotency",
          "columns": [
            "idempotency_key"
          ],
          "unique": true
        }
      ]
    },
    {
      "name": "database_migration_events",
      "class": "internal",
      "mysqlOnly": true,
      "columns": [
        {
          "name": "event_id",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 1,
          "autoIncrement": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "migration_id",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(64)"
        },
        {
          "name": "event_type",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(32)"
        },
        {
          "name": "previous_phase",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(32)"
        },
        {
          "name": "previous_status",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(16)"
        },
        {
          "name": "phase",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(32)"
        },
        {
          "name": "status",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(16)"
        },
        {
          "name": "generation",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "table_name",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(128)"
        },
        {
          "name": "error_text",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "created_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ],
      "indexes": [
        {
          "name": "database_migration_events_migration_idx",
          "columns": [
            "migration_id",
            "event_id"
          ]
        }
      ]
    },
    {
      "name": "database_migration_tables",
      "class": "internal",
      "mysqlOnly": true,
      "columns": [
        {
          "name": "migration_id",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 1,
          "mysqlType": "VARCHAR(64)"
        },
        {
          "name": "table_name",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 2,
          "mysqlType": "VARCHAR(128)"
        },
        {
          "name": "checkpoint_json",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "source_watermark_json",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "rows_copied",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "bytes_copied",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "requests",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "batch_size",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "INT"
        },
        {
          "name": "completed",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "INT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ]
    },
    {
      "name": "database_migrations",
      "class": "internal",
      "mysqlOnly": true,
      "columns": [
        {
          "name": "id",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 1,
          "mysqlType": "VARCHAR(64)"
        },
        {
          "name": "idempotency_key",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(128)"
        },
        {
          "name": "generation",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "source_backend",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(16)"
        },
        {
          "name": "target_backend",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(16)"
        },
        {
          "name": "phase",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(32)"
        },
        {
          "name": "status",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(16)"
        },
        {
          "name": "batch_size",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "INT"
        },
        {
          "name": "frozen_price_hash",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(128)"
        },
        {
          "name": "final_outbox_watermark",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "validation_json",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "validation_token",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(128)"
        },
        {
          "name": "created_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "finished_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "last_error",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        }
      ],
      "indexes": [
        {
          "name": "uq_database_migrations_idempotency",
          "columns": [
            "idempotency_key"
          ],
          "unique": true
        }
      ]
    },
    {
      "name": "database_mysql_authority_counter",
      "class": "internal",
      "mysqlOnly": true,
      "columns": [
        {
          "name": "id",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 1,
          "mysqlType": "INT"
        },
        {
          "name": "last_row_version",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ]
    },
    {
      "name": "database_operation_idempotency",
      "class": "internal",
      "mysqlOnly": true,
      "columns": [
        {
          "name": "idempotency_key",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 1,
          "mysqlType": "VARCHAR(128)"
        },
        {
          "name": "operation_name",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(128)"
        },
        {
          "name": "request_hash",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(128)"
        },
        {
          "name": "result_json",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "result_generation",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "created_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ]
    },
    {
      "name": "database_outbox",
      "class": "internal",
      "mysqlOnly": true,
      "columns": [
        {
          "name": "outbox_id",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 1,
          "autoIncrement": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "mutation_id",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(96)"
        },
        {
          "name": "mutation_digest",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(128)"
        },
        {
          "name": "transaction_id",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(96)"
        },
        {
          "name": "sequence_no",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "INT"
        },
        {
          "name": "source_backend",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(16)"
        },
        {
          "name": "target_backend",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(16)"
        },
        {
          "name": "source_epoch",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "table_name",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(128)"
        },
        {
          "name": "mutation_operation",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(16)"
        },
        {
          "name": "primary_key_json",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "payload_json",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "schema_version",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "INT"
        },
        {
          "name": "row_version",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "payload_bytes",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "created_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "applied_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ],
      "indexes": [
        {
          "name": "uq_database_outbox_mutation",
          "columns": [
            "mutation_id"
          ],
          "unique": true
        },
        {
          "name": "uq_database_outbox_transaction_sequence",
          "columns": [
            "transaction_id",
            "sequence_no"
          ],
          "unique": true
        },
        {
          "name": "database_outbox_pending_idx",
          "columns": [
            "applied_at_ms",
            "outbox_id"
          ]
        },
        {
          "name": "database_outbox_group_idx",
          "columns": [
            "transaction_id",
            "sequence_no"
          ]
        }
      ]
    },
    {
      "name": "database_replication_state",
      "class": "internal",
      "mysqlOnly": true,
      "columns": [
        {
          "name": "direction",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 1,
          "mysqlType": "VARCHAR(64)"
        },
        {
          "name": "source_backend",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(16)"
        },
        {
          "name": "target_backend",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(16)"
        },
        {
          "name": "epoch",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "source_watermark",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "target_watermark",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "backlog_rows",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "backlog_bytes",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "oldest_backlog_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "throughput_rows_per_sec",
          "kind": "real",
          "nullable": false,
          "mysqlType": "DOUBLE"
        },
        {
          "name": "retries",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "heartbeat_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "last_progress_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "last_success_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "last_error",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        }
      ]
    },
    {
      "name": "database_routing_state",
      "class": "internal",
      "mysqlOnly": true,
      "columns": [
        {
          "name": "id",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 1,
          "mysqlType": "INT"
        },
        {
          "name": "generation",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "write_primary",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(16)"
        },
        {
          "name": "business_read",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(16)"
        },
        {
          "name": "system_read",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(16)"
        },
        {
          "name": "epoch",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ]
    },
    {
      "name": "database_row_versions",
      "class": "internal",
      "mysqlOnly": true,
      "columns": [
        {
          "name": "table_name",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 1,
          "mysqlType": "VARCHAR(128)"
        },
        {
          "name": "primary_key_hash",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 2,
          "mysqlType": "CHAR(64) CHARACTER SET ascii COLLATE ascii_bin"
        },
        {
          "name": "primary_key_json",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "source_epoch",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "row_version",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "mutation_id",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(96)"
        },
        {
          "name": "mutation_digest",
          "kind": "text",
          "nullable": false,
          "mysqlType": "VARCHAR(128)"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ]
    },
    {
      "name": "dead_letter_events",
      "class": "authoritative",
      "columns": [
        {
          "name": "id",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 1,
          "autoIncrement": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "payload",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "error",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "created_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ],
      "sourceDdl": "CREATE TABLE dead_letter_events (\n\t\t\tid integer primary key autoincrement,\n\t\t\tpayload text not null,\n\t\t\terror text not null,\n\t\t\tcreated_at_ms integer not null\n\t\t)"
    },
    {
      "name": "model_price_context_tiers",
      "class": "authoritative",
      "columns": [
        {
          "name": "model",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 1,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "threshold_tokens",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 2,
          "mysqlType": "BIGINT"
        },
        {
          "name": "prompt_per_1m",
          "kind": "real",
          "nullable": false,
          "default": "0",
          "mysqlType": "DOUBLE"
        },
        {
          "name": "completion_per_1m",
          "kind": "real",
          "nullable": false,
          "default": "0",
          "mysqlType": "DOUBLE"
        },
        {
          "name": "cache_per_1m",
          "kind": "real",
          "nullable": false,
          "default": "0",
          "mysqlType": "DOUBLE"
        },
        {
          "name": "cache_read_per_1m",
          "kind": "real",
          "nullable": false,
          "default": "0",
          "mysqlType": "DOUBLE"
        },
        {
          "name": "cache_creation_per_1m",
          "kind": "real",
          "nullable": false,
          "default": "0",
          "mysqlType": "DOUBLE"
        },
        {
          "name": "prompt_configured",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "TINYINT"
        },
        {
          "name": "completion_configured",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "TINYINT"
        },
        {
          "name": "cache_configured",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "TINYINT"
        },
        {
          "name": "cache_read_configured",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "TINYINT"
        },
        {
          "name": "cache_creation_configured",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "TINYINT"
        }
      ],
      "foreignKeys": [
        {
          "name": "fk_model_price_context_tiers_0",
          "column": "model",
          "refTable": "model_prices",
          "refColumn": "model",
          "onUpdate": "NO ACTION",
          "onDelete": "CASCADE"
        }
      ],
      "indexes": [
        {
          "name": "sqlite_autoindex_model_price_context_tiers_1",
          "columns": [
            "model",
            "threshold_tokens"
          ],
          "unique": true
        }
      ],
      "sourceDdl": "CREATE TABLE model_price_context_tiers (\n\t\t\tmodel text not null,\n\t\t\tthreshold_tokens integer not null,\n\t\t\tprompt_per_1m real not null default 0,\n\t\t\tcompletion_per_1m real not null default 0,\n\t\t\tcache_per_1m real not null default 0,\n\t\t\tcache_read_per_1m real not null default 0,\n\t\t\tcache_creation_per_1m real not null default 0,\n\t\t\tprompt_configured integer not null default 0,\n\t\t\tcompletion_configured integer not null default 0,\n\t\t\tcache_configured integer not null default 0,\n\t\t\tcache_read_configured integer not null default 0,\n\t\t\tcache_creation_configured integer not null default 0,\n\t\t\tprimary key (model, threshold_tokens),\n\t\t\tforeign key (model) references model_prices(model) on delete cascade\n\t\t)"
    },
    {
      "name": "model_price_service_tiers",
      "class": "authoritative",
      "columns": [
        {
          "name": "model",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 1,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "mode",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 2,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "service_tier",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 3,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "prompt_per_1m",
          "kind": "real",
          "nullable": false,
          "default": "0",
          "mysqlType": "DOUBLE"
        },
        {
          "name": "completion_per_1m",
          "kind": "real",
          "nullable": false,
          "default": "0",
          "mysqlType": "DOUBLE"
        },
        {
          "name": "cache_per_1m",
          "kind": "real",
          "nullable": false,
          "default": "0",
          "mysqlType": "DOUBLE"
        },
        {
          "name": "cache_read_per_1m",
          "kind": "real",
          "nullable": false,
          "default": "0",
          "mysqlType": "DOUBLE"
        },
        {
          "name": "cache_creation_per_1m",
          "kind": "real",
          "nullable": false,
          "default": "0",
          "mysqlType": "DOUBLE"
        },
        {
          "name": "prompt_configured",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "TINYINT"
        },
        {
          "name": "completion_configured",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "TINYINT"
        },
        {
          "name": "cache_configured",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "TINYINT"
        },
        {
          "name": "cache_read_configured",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "TINYINT"
        },
        {
          "name": "cache_creation_configured",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "TINYINT"
        }
      ],
      "foreignKeys": [
        {
          "name": "fk_model_price_service_tiers_0",
          "column": "model",
          "refTable": "model_prices",
          "refColumn": "model",
          "onUpdate": "NO ACTION",
          "onDelete": "CASCADE"
        }
      ],
      "indexes": [
        {
          "name": "sqlite_autoindex_model_price_service_tiers_1",
          "columns": [
            "model",
            "mode",
            "service_tier"
          ],
          "unique": true
        }
      ],
      "sourceDdl": "CREATE TABLE model_price_service_tiers (\n\t\t\tmodel text not null,\n\t\t\tmode text not null,\n\t\t\tservice_tier text not null,\n\t\t\tprompt_per_1m real not null default 0,\n\t\t\tcompletion_per_1m real not null default 0,\n\t\t\tcache_per_1m real not null default 0,\n\t\t\tcache_read_per_1m real not null default 0,\n\t\t\tcache_creation_per_1m real not null default 0,\n\t\t\tprompt_configured integer not null default 0,\n\t\t\tcompletion_configured integer not null default 0,\n\t\t\tcache_configured integer not null default 0,\n\t\t\tcache_read_configured integer not null default 0,\n\t\t\tcache_creation_configured integer not null default 0,\n\t\t\tprimary key (model, mode, service_tier),\n\t\t\tforeign key (model) references model_prices(model) on delete cascade\n\t\t)"
    },
    {
      "name": "model_prices",
      "class": "authoritative",
      "columns": [
        {
          "name": "model",
          "kind": "text",
          "nullable": true,
          "primaryKeyPosition": 1,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "prompt_per_1m",
          "kind": "real",
          "nullable": false,
          "mysqlType": "DOUBLE"
        },
        {
          "name": "completion_per_1m",
          "kind": "real",
          "nullable": false,
          "mysqlType": "DOUBLE"
        },
        {
          "name": "cache_per_1m",
          "kind": "real",
          "nullable": false,
          "mysqlType": "DOUBLE"
        },
        {
          "name": "cache_read_per_1m",
          "kind": "real",
          "nullable": false,
          "default": "0",
          "mysqlType": "DOUBLE"
        },
        {
          "name": "cache_creation_per_1m",
          "kind": "real",
          "nullable": false,
          "default": "0",
          "mysqlType": "DOUBLE"
        },
        {
          "name": "prompt_configured",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "TINYINT"
        },
        {
          "name": "completion_configured",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "TINYINT"
        },
        {
          "name": "cache_read_configured",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "TINYINT"
        },
        {
          "name": "cache_creation_configured",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "TINYINT"
        },
        {
          "name": "source",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "source_model_id",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "raw_json",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "synced_at_ms",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        }
      ],
      "indexes": [
        {
          "name": "sqlite_autoindex_model_prices_1",
          "columns": [
            "model"
          ],
          "unique": true
        }
      ],
      "sourceDdl": "CREATE TABLE model_prices (\n\t\t\tmodel text primary key,\n\t\t\tprompt_per_1m real not null,\n\t\t\tcompletion_per_1m real not null,\n\t\t\tcache_per_1m real not null,\n\t\t\tcache_read_per_1m real not null default 0,\n\t\t\tcache_creation_per_1m real not null default 0,\n\t\t\tprompt_configured integer not null default 0,\n\t\t\tcompletion_configured integer not null default 0,\n\t\t\tcache_read_configured integer not null default 0,\n\t\t\tcache_creation_configured integer not null default 0,\n\t\t\tsource text,\n\t\t\tsource_model_id text,\n\t\t\traw_json text,\n\t\t\tupdated_at_ms integer not null,\n\t\t\tsynced_at_ms integer\n\t\t)"
    },
    {
      "name": "quota_cooldowns",
      "class": "authoritative",
      "columns": [
        {
          "name": "id",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 1,
          "autoIncrement": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "auth_file_name",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_index",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "account_snapshot",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "provider",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "reason_code",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "window_kind",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "evidence_json",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "recover_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "owner",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "event_hash",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "pre_disabled_state",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "TINYINT"
        },
        {
          "name": "status",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "disabled_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "recovered_at_ms",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "last_error",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "created_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ],
      "indexes": [
        {
          "name": "idx_quota_cooldowns_active_identity",
          "unique": true,
          "sourceDdl": "CREATE UNIQUE INDEX idx_quota_cooldowns_active_identity\n\t\ton quota_cooldowns (\n\t\t\tauth_file_name,\n\t\t\towner,\n\t\t\tcoalesce(trim(auth_index), ''),\n\t\t\tcase\n\t\t\t\twhen coalesce(trim(auth_index), '') \u003c\u003e '' then ''\n\t\t\t\telse case coalesce(lower(replace(trim(provider), '_', '-')), '')\n\t\t\t\t\twhen 'x-ai' then 'xai'\n\t\t\t\t\twhen 'grok' then 'xai'\n\t\t\t\t\telse coalesce(lower(replace(trim(provider), '_', '-')), '')\n\t\t\t\tend\n\t\t\tend,\n\t\t\tcase\n\t\t\t\twhen coalesce(trim(auth_index), '') \u003c\u003e '' then ''\n\t\t\t\telse coalesce(trim(account_snapshot), '')\n\t\t\tend\n\t\t)\n\t\twhere status = 'active'"
        },
        {
          "name": "idx_quota_cooldowns_due",
          "columns": [
            "status",
            "recover_at_ms"
          ],
          "sourceDdl": "CREATE INDEX idx_quota_cooldowns_due on quota_cooldowns(status, recover_at_ms)"
        }
      ],
      "sourceDdl": "CREATE TABLE quota_cooldowns (\n\t\t\tid integer primary key autoincrement,\n\t\t\tauth_file_name text not null,\n\t\t\tauth_index text,\n\t\t\taccount_snapshot text,\n\t\t\tprovider text,\n\t\t\treason_code text,\n\t\t\twindow_kind text,\n\t\t\tevidence_json text,\n\t\t\trecover_at_ms integer not null,\n\t\t\towner text not null,\n\t\t\tevent_hash text,\n\t\t\tpre_disabled_state integer not null default 0,\n\t\t\tstatus text not null,\n\t\t\tdisabled_at_ms integer not null,\n\t\t\trecovered_at_ms integer,\n\t\t\tlast_error text,\n\t\t\tcreated_at_ms integer not null,\n\t\t\tupdated_at_ms integer not null\n\t\t)"
    },
    {
      "name": "settings",
      "class": "authoritative",
      "columns": [
        {
          "name": "key",
          "kind": "text",
          "nullable": true,
          "primaryKeyPosition": 1,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "value",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ],
      "indexes": [
        {
          "name": "sqlite_autoindex_settings_1",
          "columns": [
            "key"
          ],
          "unique": true
        }
      ],
      "sourceDdl": "CREATE TABLE settings (\n\t\t\tkey text primary key,\n\t\t\tvalue text not null,\n\t\t\tupdated_at_ms integer not null\n\t\t)"
    },
    {
      "name": "usage_account_model_rollups",
      "class": "derived",
      "columns": [
        {
          "name": "account_key",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 1,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "account_snapshot",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_label_snapshot",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_provider_snapshot",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_index",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "source",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "source_hash",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "model",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 2,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "billing_model",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 3,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "service_tier",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 4,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "calls",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "success_calls",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "failure_calls",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "input_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "output_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "reasoning_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "cached_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "cache_read_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "cache_creation_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_input_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_output_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_cached_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_cache_read_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_cache_creation_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "total_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "first_seen_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "last_seen_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ],
      "indexes": [
        {
          "name": "idx_usage_account_model_rollups_auth_index",
          "columns": [
            "auth_index"
          ],
          "sourceDdl": "CREATE INDEX idx_usage_account_model_rollups_auth_index on usage_account_model_rollups(auth_index)"
        },
        {
          "name": "idx_usage_account_model_rollups_last_seen",
          "columns": [
            "last_seen_ms"
          ],
          "sourceDdl": "CREATE INDEX idx_usage_account_model_rollups_last_seen on usage_account_model_rollups(last_seen_ms)"
        },
        {
          "name": "sqlite_autoindex_usage_account_model_rollups_1",
          "columns": [
            "account_key",
            "model",
            "billing_model",
            "service_tier"
          ],
          "unique": true
        }
      ],
      "sourceDdl": "CREATE TABLE usage_account_model_rollups (\n\t\taccount_key text not null,\n\t\taccount_snapshot text,\n\t\tauth_label_snapshot text,\n\t\tauth_provider_snapshot text,\n\t\tauth_index text,\n\t\tsource text,\n\t\tsource_hash text,\n\t\tmodel text not null,\n\t\tbilling_model text not null,\n\t\tservice_tier text not null,\n\t\tcalls integer not null default 0,\n\t\tsuccess_calls integer not null default 0,\n\t\tfailure_calls integer not null default 0,\n\t\tinput_tokens integer not null default 0,\n\t\toutput_tokens integer not null default 0,\n\t\treasoning_tokens integer not null default 0,\n\t\tcached_tokens integer not null default 0,\n\t\tcache_read_tokens integer not null default 0,\n\t\tcache_creation_tokens integer not null default 0,\n\t\tlong_input_tokens integer not null default 0,\n\t\tlong_output_tokens integer not null default 0,\n\t\tlong_cached_tokens integer not null default 0,\n\t\tlong_cache_read_tokens integer not null default 0,\n\t\tlong_cache_creation_tokens integer not null default 0,\n\t\ttotal_tokens integer not null default 0,\n\t\tfirst_seen_ms integer not null,\n\t\tlast_seen_ms integer not null,\n\t\tupdated_at_ms integer not null,\n\t\tprimary key (account_key, model, billing_model, service_tier)\n\t)"
    },
    {
      "name": "usage_cache_accounting_v2_changes",
      "class": "internal",
      "columns": [
        {
          "name": "event_id",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 1,
          "mysqlType": "BIGINT"
        },
        {
          "name": "cache_input_mode",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "normalized_uncached_input_tokens",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "normalized_total_input_tokens",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "normalized_cache_read_tokens",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "normalized_cache_creation_tokens",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "total_tokens",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ],
      "sourceDdl": "CREATE TABLE usage_cache_accounting_v2_changes (\n\t\t\tevent_id integer primary key,\n\t\t\tcache_input_mode text not null,\n\t\t\tnormalized_uncached_input_tokens integer not null,\n\t\t\tnormalized_total_input_tokens integer not null,\n\t\t\tnormalized_cache_read_tokens integer not null,\n\t\t\tnormalized_cache_creation_tokens integer not null,\n\t\t\ttotal_tokens integer not null\n\t\t)"
    },
    {
      "name": "usage_codex_legacy_identity_evidence_v1",
      "class": "derived",
      "columns": [
        {
          "name": "structure_revision",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 1,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "physical_kind",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 2,
          "mysqlType": "BIGINT"
        },
        {
          "name": "physical_file",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 3,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_index",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 4,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "provider",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 5,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_provider_snapshot",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 6,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_account_id_snapshot",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 7,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_project_id_snapshot",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 8,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "account_snapshot",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 9,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "min_evidence_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "max_evidence_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "chronology_unknown",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ],
      "indexes": [
        {
          "name": "sqlite_autoindex_usage_codex_legacy_identity_evidence_v1_1",
          "columns": [
            "structure_revision",
            "physical_kind",
            "physical_file",
            "auth_index",
            "provider",
            "auth_provider_snapshot",
            "auth_account_id_snapshot",
            "auth_project_id_snapshot",
            "account_snapshot"
          ],
          "unique": true
        }
      ],
      "sourceDdl": "CREATE TABLE usage_codex_legacy_identity_evidence_v1 (\n\t\tstructure_revision text not null,\n\t\tphysical_kind integer not null,\n\t\tphysical_file text collate nocase not null,\n\t\tauth_index text collate nocase not null,\n\t\tprovider text not null,\n\t\tauth_provider_snapshot text not null,\n\t\tauth_account_id_snapshot text not null,\n\t\tauth_project_id_snapshot text not null,\n\t\taccount_snapshot text not null,\n\t\tmin_evidence_at_ms integer not null,\n\t\tmax_evidence_at_ms integer not null,\n\t\tchronology_unknown integer not null,\n\t\tprimary key (\n\t\t\tstructure_revision, physical_kind, physical_file, auth_index,\n\t\t\tprovider, auth_provider_snapshot, auth_account_id_snapshot,\n\t\t\tauth_project_id_snapshot, account_snapshot\n\t\t)\n\t)"
    },
    {
      "name": "usage_dashboard_hourly_rollups",
      "class": "derived",
      "columns": [
        {
          "name": "bucket_ms",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 1,
          "mysqlType": "BIGINT"
        },
        {
          "name": "model",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 2,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "billing_model",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 3,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "service_tier",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 4,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "calls",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "success_calls",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "failure_calls",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "input_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "output_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "reasoning_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "cached_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "cache_read_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "cache_creation_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_input_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_output_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_cached_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_cache_read_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_cache_creation_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "total_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "latency_sum_ms",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "latency_samples",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "zero_token_calls",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ],
      "indexes": [
        {
          "name": "sqlite_autoindex_usage_dashboard_hourly_rollups_1",
          "columns": [
            "bucket_ms",
            "model",
            "billing_model",
            "service_tier"
          ],
          "unique": true
        }
      ],
      "sourceDdl": "CREATE TABLE usage_dashboard_hourly_rollups (\n\t\tbucket_ms integer not null,\n\t\tmodel text not null,\n\t\tbilling_model text not null,\n\t\tservice_tier text not null,\n\t\tcalls integer not null default 0,\n\t\tsuccess_calls integer not null default 0,\n\t\tfailure_calls integer not null default 0,\n\t\tinput_tokens integer not null default 0,\n\t\toutput_tokens integer not null default 0,\n\t\treasoning_tokens integer not null default 0,\n\t\tcached_tokens integer not null default 0,\n\t\tcache_read_tokens integer not null default 0,\n\t\tcache_creation_tokens integer not null default 0,\n\t\tlong_input_tokens integer not null default 0,\n\t\tlong_output_tokens integer not null default 0,\n\t\tlong_cached_tokens integer not null default 0,\n\t\tlong_cache_read_tokens integer not null default 0,\n\t\tlong_cache_creation_tokens integer not null default 0,\n\t\ttotal_tokens integer not null default 0,\n\t\tlatency_sum_ms integer not null default 0,\n\t\tlatency_samples integer not null default 0,\n\t\tzero_token_calls integer not null default 0,\n\t\tupdated_at_ms integer not null,\n\t\tprimary key (bucket_ms, model, billing_model, service_tier)\n\t)"
    },
    {
      "name": "usage_data_migrations",
      "class": "internal",
      "columns": [
        {
          "name": "name",
          "kind": "text",
          "nullable": true,
          "primaryKeyPosition": 1,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "status",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "last_event_id",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "target_event_id",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "processed_rows",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "changed_rows",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "applied_rows",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "started_at_ms",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "finished_at_ms",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "last_error",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        }
      ],
      "indexes": [
        {
          "name": "sqlite_autoindex_usage_data_migrations_1",
          "columns": [
            "name"
          ],
          "unique": true
        }
      ],
      "sourceDdl": "CREATE TABLE usage_data_migrations (\n\t\t\tname text primary key,\n\t\t\tstatus text not null,\n\t\t\tlast_event_id integer not null default 0,\n\t\t\ttarget_event_id integer not null default 0,\n\t\t\tprocessed_rows integer not null default 0,\n\t\t\tchanged_rows integer not null default 0,\n\t\t\tapplied_rows integer not null default 0,\n\t\t\tstarted_at_ms integer,\n\t\t\tupdated_at_ms integer not null default 0,\n\t\t\tfinished_at_ms integer,\n\t\t\tlast_error text\n\t\t)"
    },
    {
      "name": "usage_derived_cleanup_cursors",
      "class": "internal",
      "columns": [
        {
          "name": "target_name",
          "kind": "text",
          "nullable": true,
          "primaryKeyPosition": 1,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "table_name",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "revision_token",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "last_rowid",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        }
      ],
      "indexes": [
        {
          "name": "sqlite_autoindex_usage_derived_cleanup_cursors_1",
          "columns": [
            "target_name"
          ],
          "unique": true
        }
      ],
      "sourceDdl": "CREATE TABLE usage_derived_cleanup_cursors (\n\t\ttarget_name text primary key,\n\t\ttable_name text not null,\n\t\trevision_token text not null,\n\t\tlast_rowid integer not null default 0,\n\t\tupdated_at_ms integer not null default 0\n\t)"
    },
    {
      "name": "usage_derived_cleanup_jobs",
      "class": "internal",
      "columns": [
        {
          "name": "id",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 1,
          "autoIncrement": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "generation",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "kind",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "status",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "projection_table",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "fts_table",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "processed_rows",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "created_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "finished_at_ms",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "last_error",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        }
      ],
      "indexes": [
        {
          "name": "sqlite_autoindex_usage_derived_cleanup_jobs_2",
          "columns": [
            "fts_table"
          ],
          "unique": true
        },
        {
          "name": "sqlite_autoindex_usage_derived_cleanup_jobs_1",
          "columns": [
            "generation"
          ],
          "unique": true
        }
      ],
      "sourceDdl": "CREATE TABLE usage_derived_cleanup_jobs (\n\t\tid integer primary key autoincrement,\n\t\tgeneration integer not null unique,\n\t\tkind text not null,\n\t\tstatus text not null,\n\t\tprojection_table text,\n\t\tfts_table text not null unique,\n\t\tprocessed_rows integer not null default 0,\n\t\tcreated_at_ms integer not null,\n\t\tupdated_at_ms integer not null,\n\t\tfinished_at_ms integer,\n\t\tlast_error text\n\t)"
    },
    {
      "name": "usage_derived_deferred_indexes",
      "class": "internal",
      "columns": [
        {
          "name": "index_name",
          "kind": "text",
          "nullable": true,
          "primaryKeyPosition": 1,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "table_name",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "reason",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "created_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ],
      "indexes": [
        {
          "name": "sqlite_autoindex_usage_derived_deferred_indexes_1",
          "columns": [
            "index_name"
          ],
          "unique": true
        }
      ],
      "sourceDdl": "CREATE TABLE usage_derived_deferred_indexes (\n\t\tindex_name text primary key,\n\t\ttable_name text not null,\n\t\treason text not null,\n\t\tcreated_at_ms integer not null,\n\t\tupdated_at_ms integer not null\n\t)"
    },
    {
      "name": "usage_event_identity_ledger",
      "class": "derived",
      "columns": [
        {
          "name": "event_hash",
          "kind": "text",
          "nullable": true,
          "primaryKeyPosition": 1,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "raw_event_id",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "timestamp_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "bucket_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "aggregate_schema_version",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "aggregate_structure_revision",
          "kind": "text",
          "nullable": false,
          "default": "''",
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "first_seen_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ],
      "indexes": [
        {
          "name": "idx_usage_event_identity_ledger_bucket",
          "columns": [
            "bucket_ms"
          ],
          "sourceDdl": "CREATE INDEX idx_usage_event_identity_ledger_bucket on usage_event_identity_ledger(bucket_ms)"
        },
        {
          "name": "idx_usage_event_identity_ledger_raw_event_id",
          "columns": [
            "raw_event_id"
          ],
          "sourceDdl": "CREATE INDEX idx_usage_event_identity_ledger_raw_event_id on usage_event_identity_ledger(raw_event_id)"
        },
        {
          "name": "sqlite_autoindex_usage_event_identity_ledger_1",
          "columns": [
            "event_hash"
          ],
          "unique": true
        }
      ],
      "sourceDdl": "CREATE TABLE usage_event_identity_ledger (\n\t\t\tevent_hash text primary key,\n\t\t\traw_event_id integer,\n\t\t\ttimestamp_ms integer not null,\n\t\t\tbucket_ms integer not null,\n\t\t\taggregate_schema_version integer not null default 0,\n\t\t\taggregate_structure_revision text not null default '',\n\t\t\tfirst_seen_at_ms integer not null,\n\t\t\tupdated_at_ms integer not null\n\t\t)"
    },
    {
      "name": "usage_events",
      "class": "authoritative",
      "columns": [
        {
          "name": "id",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 1,
          "autoIncrement": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "request_id",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "event_hash",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "timestamp_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "timestamp",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "provider",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "executor_type",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "model",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "endpoint",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "method",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "path",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "client_ip",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "x_forwarded_for",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "user_agent",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_type",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_index",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "source",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "source_hash",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "api_key_hash",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "account_snapshot",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_label_snapshot",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_file_snapshot",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_provider_snapshot",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_account_id_snapshot",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_project_id_snapshot",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_snapshot_at_ms",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "requested_model",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "resolved_model",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "reasoning_effort",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "service_tier",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "request_service_tier",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "response_service_tier",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "cache_input_mode",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "input_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "output_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "reasoning_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "cached_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "cache_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "cache_read_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "cache_creation_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "normalized_uncached_input_tokens",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "normalized_total_input_tokens",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "normalized_cache_read_tokens",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "normalized_cache_creation_tokens",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "total_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "latency_ms",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "ttft_ms",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "failed",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "TINYINT"
        },
        {
          "name": "fail_status_code",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "fail_summary",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "response_metadata_json",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "header_quota_recover_at_ms",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "header_quota_used_percent",
          "kind": "real",
          "nullable": true,
          "mysqlType": "DOUBLE"
        },
        {
          "name": "header_quota_plan_type",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "header_error_kind",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "header_error_code",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "header_trace_id",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "fail_body",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "raw_json",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "response_model",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "session_id",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "parent_session_id",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "access_token_sha256",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "generate",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "stream",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "created_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ],
      "indexes": [
        {
          "name": "idx_usage_events_latest_request_source",
          "columns": [
            "source",
            "auth_index",
            "timestamp_ms",
            "id"
          ],
          "sourceDdl": "CREATE INDEX idx_usage_events_latest_request_source on usage_events(source collate nocase, auth_index collate nocase, timestamp_ms desc, id desc)"
        },
        {
          "name": "idx_usage_events_latest_request_auth_file",
          "columns": [
            "auth_file_snapshot",
            "auth_index",
            "timestamp_ms",
            "id"
          ],
          "sourceDdl": "CREATE INDEX idx_usage_events_latest_request_auth_file on usage_events(auth_file_snapshot collate nocase, auth_index collate nocase, timestamp_ms desc, id desc)"
        },
        {
          "name": "idx_usage_events_header_trace_id",
          "columns": [
            "header_trace_id"
          ],
          "sourceDdl": "CREATE INDEX idx_usage_events_header_trace_id on usage_events(header_trace_id)"
        },
        {
          "name": "idx_usage_events_header_error_kind",
          "columns": [
            "header_error_kind"
          ],
          "sourceDdl": "CREATE INDEX idx_usage_events_header_error_kind on usage_events(header_error_kind)"
        },
        {
          "name": "idx_usage_events_header_quota_recover",
          "columns": [
            "header_quota_recover_at_ms"
          ],
          "sourceDdl": "CREATE INDEX idx_usage_events_header_quota_recover on usage_events(header_quota_recover_at_ms)"
        },
        {
          "name": "idx_usage_events_endpoint",
          "columns": [
            "endpoint"
          ],
          "sourceDdl": "CREATE INDEX idx_usage_events_endpoint on usage_events(endpoint)"
        },
        {
          "name": "idx_usage_events_auth_index",
          "columns": [
            "auth_index"
          ],
          "sourceDdl": "CREATE INDEX idx_usage_events_auth_index on usage_events(auth_index)"
        },
        {
          "name": "idx_usage_events_model",
          "columns": [
            "model"
          ],
          "sourceDdl": "CREATE INDEX idx_usage_events_model on usage_events(model)"
        },
        {
          "name": "idx_usage_events_request_id",
          "columns": [
            "request_id"
          ],
          "sourceDdl": "CREATE INDEX idx_usage_events_request_id on usage_events(request_id)"
        },
        {
          "name": "idx_usage_events_timestamp",
          "columns": [
            "timestamp_ms"
          ],
          "sourceDdl": "CREATE INDEX idx_usage_events_timestamp on usage_events(timestamp_ms)"
        },
        {
          "name": "sqlite_autoindex_usage_events_1",
          "columns": [
            "event_hash"
          ],
          "unique": true
        }
      ],
      "sourceDdl": "CREATE TABLE usage_events (\n\t\t\tid integer primary key autoincrement,\n\t\t\trequest_id text,\n\t\t\tevent_hash text not null unique,\n\t\t\ttimestamp_ms integer not null,\n\t\t\ttimestamp text not null,\n\t\t\tprovider text,\n\t\t\texecutor_type text,\n\t\t\tmodel text not null,\n\t\t\tendpoint text,\n\t\t\tmethod text,\n\t\t\tpath text,\n\t\t\tclient_ip text,\n\t\t\tx_forwarded_for text,\n\t\t\tuser_agent text,\n\t\t\tauth_type text,\n\t\t\tauth_index text,\n\t\t\tsource text,\n\t\t\tsource_hash text,\n\t\t\tapi_key_hash text,\n\t\t\taccount_snapshot text,\n\t\t\tauth_label_snapshot text,\n\t\t\tauth_file_snapshot text,\n\t\t\tauth_provider_snapshot text,\n\t\t\tauth_account_id_snapshot text,\n\t\t\tauth_project_id_snapshot text,\n\t\t\tauth_snapshot_at_ms integer,\n\t\t\trequested_model text,\n\t\t\tresolved_model text,\n\t\t\treasoning_effort text,\n\t\t\tservice_tier text,\n\t\t\trequest_service_tier text,\n\t\t\tresponse_service_tier text,\n\t\t\tcache_input_mode text,\n\t\t\tinput_tokens integer not null default 0,\n\t\t\toutput_tokens integer not null default 0,\n\t\t\treasoning_tokens integer not null default 0,\n\t\t\tcached_tokens integer not null default 0,\n\t\t\tcache_tokens integer not null default 0,\n\t\t\tcache_read_tokens integer not null default 0,\n\t\t\tcache_creation_tokens integer not null default 0,\n\t\t\tnormalized_uncached_input_tokens integer,\n\t\t\tnormalized_total_input_tokens integer,\n\t\t\tnormalized_cache_read_tokens integer,\n\t\t\tnormalized_cache_creation_tokens integer,\n\t\t\ttotal_tokens integer not null default 0,\n\t\t\tlatency_ms integer,\n\t\t\tttft_ms integer,\n\t\t\tfailed integer not null default 0,\n\t\t\tfail_status_code integer,\n\t\t\tfail_summary text,\n\t\t\tresponse_metadata_json text,\n\t\t\theader_quota_recover_at_ms integer,\n\t\t\theader_quota_used_percent real,\n\t\t\theader_quota_plan_type text,\n\t\t\theader_error_kind text,\n\t\t\theader_error_code text,\n\t\t\theader_trace_id text,\n\t\t\tfail_body text,\n\t\t\traw_json text,\n\t\t\tresponse_model text,\n\t\t\tsession_id text,\n\t\t\tparent_session_id text,\n\t\t\taccess_token_sha256 text,\n\t\t\tgenerate integer,\n\t\t\tstream integer,\n\t\t\tcreated_at_ms integer not null\n\t\t)"
    },
    {
      "name": "usage_hourly_aggregate_state",
      "class": "derived",
      "columns": [
        {
          "name": "aggregate_name",
          "kind": "text",
          "nullable": true,
          "primaryKeyPosition": 1,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "schema_version",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "structure_revision",
          "kind": "text",
          "nullable": false,
          "default": "''",
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "status",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "backfill_last_event_id",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "coverage_event_id",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "target_event_id",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "processed_events",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "min_bucket_ms",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "max_bucket_ms",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "last_run_started_at_ms",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "finished_at_ms",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "last_error",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        }
      ],
      "indexes": [
        {
          "name": "sqlite_autoindex_usage_hourly_aggregate_state_1",
          "columns": [
            "aggregate_name"
          ],
          "unique": true
        }
      ],
      "sourceDdl": "CREATE TABLE usage_hourly_aggregate_state (\n\t\t\taggregate_name text primary key,\n\t\t\tschema_version integer not null,\n\t\t\tstructure_revision text not null default '',\n\t\t\tstatus text not null,\n\t\t\tbackfill_last_event_id integer not null default 0,\n\t\t\tcoverage_event_id integer not null default 0,\n\t\t\ttarget_event_id integer not null default 0,\n\t\t\tprocessed_events integer not null default 0,\n\t\t\tmin_bucket_ms integer,\n\t\t\tmax_bucket_ms integer,\n\t\t\tlast_run_started_at_ms integer,\n\t\t\tupdated_at_ms integer not null default 0,\n\t\t\tfinished_at_ms integer,\n\t\t\tlast_error text\n\t\t)"
    },
    {
      "name": "usage_hourly_aggregate_v1",
      "class": "derived",
      "columns": [
        {
          "name": "bucket_ms",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 1,
          "mysqlType": "BIGINT"
        },
        {
          "name": "model",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 2,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "billing_model",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 3,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "service_tier",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 4,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "failed",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 5,
          "mysqlType": "TINYINT"
        },
        {
          "name": "calls",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "input_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "output_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "reasoning_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "cached_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "cache_read_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "cache_creation_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_input_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_output_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_cached_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_cache_read_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_cache_creation_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "total_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "latency_sum_ms",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "latency_samples",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "zero_token_calls",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ],
      "indexes": [
        {
          "name": "sqlite_autoindex_usage_hourly_aggregate_v1_1",
          "columns": [
            "bucket_ms",
            "model",
            "billing_model",
            "service_tier",
            "failed"
          ],
          "unique": true
        }
      ],
      "sourceDdl": "CREATE TABLE usage_hourly_aggregate_v1 (\n\t\tbucket_ms integer not null,\n\t\tmodel text not null,\n\t\tbilling_model text not null,\n\t\tservice_tier text not null,\n\t\tfailed integer not null,\n\t\tcalls integer not null default 0,\n\t\tinput_tokens integer not null default 0,\n\t\toutput_tokens integer not null default 0,\n\t\treasoning_tokens integer not null default 0,\n\t\tcached_tokens integer not null default 0,\n\t\tcache_read_tokens integer not null default 0,\n\t\tcache_creation_tokens integer not null default 0,\n\t\tlong_input_tokens integer not null default 0,\n\t\tlong_output_tokens integer not null default 0,\n\t\tlong_cached_tokens integer not null default 0,\n\t\tlong_cache_read_tokens integer not null default 0,\n\t\tlong_cache_creation_tokens integer not null default 0,\n\t\ttotal_tokens integer not null default 0,\n\t\tlatency_sum_ms integer not null default 0,\n\t\tlatency_samples integer not null default 0,\n\t\tzero_token_calls integer not null default 0,\n\t\tupdated_at_ms integer not null,\n\t\tprimary key (bucket_ms, model, billing_model, service_tier, failed)\n\t)"
    },
    {
      "name": "usage_monitoring_account_daily_rollups_v1",
      "class": "derived",
      "columns": [
        {
          "name": "structure_revision",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 1,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "bucket_ms",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 2,
          "mysqlType": "BIGINT"
        },
        {
          "name": "account_snapshot",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 3,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_label_snapshot",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 4,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "provider",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 5,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_provider_snapshot",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 6,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_account_id_snapshot",
          "kind": "text",
          "nullable": false,
          "default": "''",
          "primaryKeyPosition": 7,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_index",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 8,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "source",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 9,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "source_hash",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 10,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_file_snapshot",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 11,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "api_key_hash",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 12,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "executor_type",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 13,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "model",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 14,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "billing_model",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 15,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "pricing_model",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 16,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "service_tier",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 17,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "context_threshold_tokens",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 18,
          "mysqlType": "BIGINT"
        },
        {
          "name": "failed",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 19,
          "mysqlType": "TINYINT"
        },
        {
          "name": "calls",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "input_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "output_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "reasoning_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "cached_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "cache_read_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "cache_creation_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_input_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_output_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_cached_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_cache_read_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_cache_creation_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "total_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "zero_token_calls",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "latency_sum_ms",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "latency_samples",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "last_seen_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ],
      "indexes": [
        {
          "name": "idx_usage_monitoring_account_daily_legacy_window",
          "sourceDdl": "CREATE INDEX idx_usage_monitoring_account_daily_legacy_window on usage_monitoring_account_daily_rollups_v1(structure_revision, trim(source), trim(auth_index), bucket_ms)"
        },
        {
          "name": "idx_usage_monitoring_account_daily_credential_window",
          "sourceDdl": "CREATE INDEX idx_usage_monitoring_account_daily_credential_window on usage_monitoring_account_daily_rollups_v1(structure_revision, trim(auth_file_snapshot), trim(auth_index), bucket_ms)"
        },
        {
          "name": "idx_usage_monitoring_account_daily_bucket",
          "columns": [
            "structure_revision",
            "bucket_ms"
          ],
          "sourceDdl": "CREATE INDEX idx_usage_monitoring_account_daily_bucket on usage_monitoring_account_daily_rollups_v1(structure_revision, bucket_ms)"
        },
        {
          "name": "sqlite_autoindex_usage_monitoring_account_daily_rollups_v1_1",
          "columns": [
            "structure_revision",
            "bucket_ms",
            "account_snapshot",
            "auth_label_snapshot",
            "provider",
            "auth_provider_snapshot",
            "auth_account_id_snapshot",
            "auth_index",
            "source",
            "source_hash",
            "auth_file_snapshot",
            "api_key_hash",
            "executor_type",
            "model",
            "billing_model",
            "pricing_model",
            "service_tier",
            "context_threshold_tokens",
            "failed"
          ],
          "unique": true
        }
      ],
      "sourceDdl": "CREATE TABLE usage_monitoring_account_daily_rollups_v1 (\n\t\t\tstructure_revision text not null,\n\t\t\tbucket_ms integer not null,\n\t\t\taccount_snapshot text not null,\n\t\t\tauth_label_snapshot text not null,\n\t\t\tprovider text not null,\n\t\t\tauth_provider_snapshot text not null,\n\t\t\tauth_account_id_snapshot text not null default '',\n\t\t\tauth_index text not null,\n\t\t\tsource text not null,\n\t\t\tsource_hash text not null,\n\t\t\tauth_file_snapshot text not null,\n\t\t\tapi_key_hash text not null,\n\t\t\texecutor_type text not null,\n\t\t\tmodel text not null,\n\t\t\tbilling_model text not null,\n\t\t\tpricing_model text not null,\n\t\t\tservice_tier text not null,\n\t\t\tcontext_threshold_tokens integer not null,\n\t\t\tfailed integer not null,\n\t\t\tcalls integer not null default 0,\n\t\t\tinput_tokens integer not null default 0,\n\t\t\toutput_tokens integer not null default 0,\n\t\t\treasoning_tokens integer not null default 0,\n\t\t\tcached_tokens integer not null default 0,\n\t\t\tcache_read_tokens integer not null default 0,\n\t\t\tcache_creation_tokens integer not null default 0,\n\t\t\tlong_input_tokens integer not null default 0,\n\t\t\tlong_output_tokens integer not null default 0,\n\t\t\tlong_cached_tokens integer not null default 0,\n\t\t\tlong_cache_read_tokens integer not null default 0,\n\t\t\tlong_cache_creation_tokens integer not null default 0,\n\t\t\ttotal_tokens integer not null default 0,\n\t\t\tzero_token_calls integer not null default 0,\n\t\t\tlatency_sum_ms integer not null default 0,\n\t\t\tlatency_samples integer not null default 0,\n\t\t\tlast_seen_ms integer not null,\n\t\t\tupdated_at_ms integer not null,\n\t\t\tprimary key (\n\t\t\t\tstructure_revision, bucket_ms, account_snapshot, auth_label_snapshot,\n\t\t\t\tprovider, auth_provider_snapshot, auth_account_id_snapshot, auth_index, source, source_hash,\n\t\t\t\tauth_file_snapshot, api_key_hash, executor_type, model, billing_model,\n\t\t\t\tpricing_model, service_tier, context_threshold_tokens, failed\n\t\t\t)\n\t\t)"
    },
    {
      "name": "usage_monitoring_api_key_daily_rollups_v1",
      "class": "derived",
      "columns": [
        {
          "name": "structure_revision",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 1,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "bucket_ms",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 2,
          "mysqlType": "BIGINT"
        },
        {
          "name": "api_key_hash",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 3,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "account_snapshot",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 4,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_label_snapshot",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 5,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "provider",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 6,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_provider_snapshot",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 7,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_account_id_snapshot",
          "kind": "text",
          "nullable": false,
          "default": "''",
          "primaryKeyPosition": 8,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_index",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 9,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "source",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 10,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "source_hash",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 11,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_file_snapshot",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 12,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "executor_type",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 13,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "model",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 14,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "billing_model",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 15,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "pricing_model",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 16,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "service_tier",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 17,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "context_threshold_tokens",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 18,
          "mysqlType": "BIGINT"
        },
        {
          "name": "failed",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 19,
          "mysqlType": "TINYINT"
        },
        {
          "name": "calls",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "input_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "output_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "reasoning_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "cached_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "cache_read_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "cache_creation_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_input_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_output_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_cached_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_cache_read_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_cache_creation_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "total_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "zero_token_calls",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "latency_sum_ms",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "latency_samples",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "last_seen_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ],
      "indexes": [
        {
          "name": "idx_usage_monitoring_api_key_daily_bucket",
          "columns": [
            "structure_revision",
            "bucket_ms"
          ],
          "sourceDdl": "CREATE INDEX idx_usage_monitoring_api_key_daily_bucket on usage_monitoring_api_key_daily_rollups_v1(structure_revision, bucket_ms)"
        },
        {
          "name": "sqlite_autoindex_usage_monitoring_api_key_daily_rollups_v1_1",
          "columns": [
            "structure_revision",
            "bucket_ms",
            "api_key_hash",
            "account_snapshot",
            "auth_label_snapshot",
            "provider",
            "auth_provider_snapshot",
            "auth_account_id_snapshot",
            "auth_index",
            "source",
            "source_hash",
            "auth_file_snapshot",
            "executor_type",
            "model",
            "billing_model",
            "pricing_model",
            "service_tier",
            "context_threshold_tokens",
            "failed"
          ],
          "unique": true
        }
      ],
      "sourceDdl": "CREATE TABLE usage_monitoring_api_key_daily_rollups_v1 (\n\t\t\tstructure_revision text not null,\n\t\t\tbucket_ms integer not null,\n\t\t\tapi_key_hash text not null,\n\t\t\taccount_snapshot text not null,\n\t\t\tauth_label_snapshot text not null,\n\t\t\tprovider text not null,\n\t\t\tauth_provider_snapshot text not null,\n\t\t\tauth_account_id_snapshot text not null default '',\n\t\t\tauth_index text not null,\n\t\t\tsource text not null,\n\t\t\tsource_hash text not null,\n\t\t\tauth_file_snapshot text not null,\n\t\t\texecutor_type text not null,\n\t\t\tmodel text not null,\n\t\t\tbilling_model text not null,\n\t\t\tpricing_model text not null,\n\t\t\tservice_tier text not null,\n\t\t\tcontext_threshold_tokens integer not null,\n\t\t\tfailed integer not null,\n\t\t\tcalls integer not null default 0,\n\t\t\tinput_tokens integer not null default 0,\n\t\t\toutput_tokens integer not null default 0,\n\t\t\treasoning_tokens integer not null default 0,\n\t\t\tcached_tokens integer not null default 0,\n\t\t\tcache_read_tokens integer not null default 0,\n\t\t\tcache_creation_tokens integer not null default 0,\n\t\t\tlong_input_tokens integer not null default 0,\n\t\t\tlong_output_tokens integer not null default 0,\n\t\t\tlong_cached_tokens integer not null default 0,\n\t\t\tlong_cache_read_tokens integer not null default 0,\n\t\t\tlong_cache_creation_tokens integer not null default 0,\n\t\t\ttotal_tokens integer not null default 0,\n\t\t\tzero_token_calls integer not null default 0,\n\t\t\tlatency_sum_ms integer not null default 0,\n\t\t\tlatency_samples integer not null default 0,\n\t\t\tlast_seen_ms integer not null,\n\t\t\tupdated_at_ms integer not null,\n\t\t\tprimary key (\n\t\t\t\tstructure_revision, bucket_ms, api_key_hash, account_snapshot,\n\t\t\t\tauth_label_snapshot, provider, auth_provider_snapshot, auth_account_id_snapshot, auth_index,\n\t\t\t\tsource, source_hash, auth_file_snapshot, executor_type, model,\n\t\t\t\tbilling_model, pricing_model, service_tier,\n\t\t\t\tcontext_threshold_tokens, failed\n\t\t\t)\n\t\t)"
    },
    {
      "name": "usage_monitoring_event_projection_v1",
      "class": "derived",
      "columns": [
        {
          "name": "event_id",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 1,
          "mysqlType": "BIGINT"
        },
        {
          "name": "timestamp_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "search_text",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "account_key",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "provider",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "executor_type",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "model",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "requested_model",
          "kind": "text",
          "nullable": false,
          "default": "''",
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "analytics_model",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "resolved_model",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_index",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "source",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "source_hash",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "api_key_hash",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "account_snapshot",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_label_snapshot",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_file_snapshot",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_provider_snapshot",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_account_id_snapshot",
          "kind": "text",
          "nullable": false,
          "default": "''",
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_project_id_snapshot",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "reasoning_effort",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "service_tier",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "failed",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "TINYINT"
        },
        {
          "name": "latency_ms",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "input_tokens",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "output_tokens",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "reasoning_tokens",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "cached_tokens",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "cache_tokens",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "cache_read_tokens",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "cache_creation_tokens",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "normalized_total_input_tokens",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "total_tokens",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "header_quota_plan_type",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "header_error_kind",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "header_error_code",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "header_trace_id",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ],
      "indexes": [
        {
          "name": "idx_usage_monitoring_event_projection_model_timestamp",
          "columns": [
            "analytics_model",
            "timestamp_ms",
            "event_id"
          ],
          "sourceDdl": "CREATE INDEX idx_usage_monitoring_event_projection_model_timestamp on usage_monitoring_event_projection_v1(analytics_model, timestamp_ms desc, event_id desc)"
        },
        {
          "name": "idx_usage_monitoring_event_projection_account_window",
          "columns": [
            "account_key",
            "timestamp_ms",
            "event_id"
          ],
          "sourceDdl": "CREATE INDEX idx_usage_monitoring_event_projection_account_window on usage_monitoring_event_projection_v1(account_key, timestamp_ms, event_id)"
        },
        {
          "name": "idx_usage_monitoring_event_projection_timestamp",
          "columns": [
            "timestamp_ms",
            "event_id"
          ],
          "sourceDdl": "CREATE INDEX idx_usage_monitoring_event_projection_timestamp on usage_monitoring_event_projection_v1(timestamp_ms desc, event_id desc)"
        }
      ],
      "sourceDdl": "CREATE TABLE usage_monitoring_event_projection_v1 (\n\t\t\tevent_id integer primary key,\n\t\t\ttimestamp_ms integer not null,\n\t\t\tsearch_text text not null,\n\t\t\taccount_key text not null,\n\t\t\tprovider text not null,\n\t\t\texecutor_type text not null,\n\t\t\tmodel text not null,\n\t\t\trequested_model text not null default '',\n\t\t\tanalytics_model text not null,\n\t\t\tresolved_model text not null,\n\t\t\tauth_index text not null,\n\t\t\tsource text not null,\n\t\t\tsource_hash text not null,\n\t\t\tapi_key_hash text not null,\n\t\t\taccount_snapshot text not null,\n\t\t\tauth_label_snapshot text not null,\n\t\t\tauth_file_snapshot text not null,\n\t\t\tauth_provider_snapshot text not null,\n\t\t\tauth_account_id_snapshot text not null default '',\n\t\t\tauth_project_id_snapshot text not null,\n\t\t\treasoning_effort text not null,\n\t\t\tservice_tier text not null,\n\t\t\tfailed integer not null,\n\t\t\tlatency_ms integer,\n\t\t\tinput_tokens integer not null,\n\t\t\toutput_tokens integer not null,\n\t\t\treasoning_tokens integer not null,\n\t\t\tcached_tokens integer not null,\n\t\t\tcache_tokens integer not null,\n\t\t\tcache_read_tokens integer not null,\n\t\t\tcache_creation_tokens integer not null,\n\t\t\tnormalized_total_input_tokens integer not null,\n\t\t\ttotal_tokens integer not null,\n\t\t\theader_quota_plan_type text not null,\n\t\t\theader_error_kind text not null,\n\t\t\theader_error_code text not null,\n\t\t\theader_trace_id text not null,\n\t\t\tupdated_at_ms integer not null\n\t\t)"
    },
    {
      "name": "usage_monitoring_event_search_v1",
      "class": "derived",
      "columns": [
        {
          "name": "search_text",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        }
      ],
      "sourceDdl": "CREATE VIRTUAL TABLE usage_monitoring_event_search_v1 using fts5(\n\t\t\tsearch_text,\n\t\t\tcontent = 'usage_monitoring_event_projection_v1',\n\t\t\tcontent_rowid = 'event_id',\n\t\t\tcolumnsize = 0,\n\t\t\tdetail = 'none',\n\t\t\ttokenize = 'trigram'\n\t\t)"
    },
    {
      "name": "usage_monitoring_header_latest_v1",
      "class": "derived",
      "columns": [
        {
          "name": "snapshot_key",
          "kind": "text",
          "nullable": true,
          "primaryKeyPosition": 1,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "event_id",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "event_hash",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "timestamp_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "auth_file_snapshot",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_index",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "account_snapshot",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_label_snapshot",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_provider_snapshot",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_account_id_snapshot",
          "kind": "text",
          "nullable": false,
          "default": "''",
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_project_id_snapshot",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "source",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "source_hash",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "response_metadata_json",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "header_quota_recover_at_ms",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "header_quota_used_percent",
          "kind": "real",
          "nullable": true,
          "mysqlType": "DOUBLE"
        },
        {
          "name": "header_quota_plan_type",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "header_error_kind",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "header_error_code",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "header_trace_id",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ],
      "indexes": [
        {
          "name": "idx_usage_monitoring_header_latest_timestamp",
          "columns": [
            "timestamp_ms",
            "event_id"
          ],
          "sourceDdl": "CREATE INDEX idx_usage_monitoring_header_latest_timestamp on usage_monitoring_header_latest_v1(timestamp_ms desc, event_id desc)"
        },
        {
          "name": "sqlite_autoindex_usage_monitoring_header_latest_v1_1",
          "columns": [
            "snapshot_key"
          ],
          "unique": true
        }
      ],
      "sourceDdl": "CREATE TABLE usage_monitoring_header_latest_v1 (\n\t\t\tsnapshot_key text primary key,\n\t\t\tevent_id integer not null,\n\t\t\tevent_hash text not null,\n\t\t\ttimestamp_ms integer not null,\n\t\t\tauth_file_snapshot text not null,\n\t\t\tauth_index text not null,\n\t\t\taccount_snapshot text not null,\n\t\t\tauth_label_snapshot text not null,\n\t\t\tauth_provider_snapshot text not null,\n\t\t\tauth_account_id_snapshot text not null default '',\n\t\t\tauth_project_id_snapshot text not null,\n\t\t\tsource text not null,\n\t\t\tsource_hash text not null,\n\t\t\tresponse_metadata_json text not null,\n\t\t\theader_quota_recover_at_ms integer,\n\t\t\theader_quota_used_percent real,\n\t\t\theader_quota_plan_type text not null,\n\t\t\theader_error_kind text not null,\n\t\t\theader_error_code text not null,\n\t\t\theader_trace_id text not null,\n\t\t\tupdated_at_ms integer not null\n\t\t)"
    },
    {
      "name": "usage_monitoring_rollup_state",
      "class": "derived",
      "columns": [
        {
          "name": "rollup_name",
          "kind": "text",
          "nullable": true,
          "primaryKeyPosition": 1,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "schema_version",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "structure_revision",
          "kind": "text",
          "nullable": false,
          "default": "''",
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "status",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "backfill_last_event_id",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "coverage_event_id",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "target_event_id",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "processed_events",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "last_run_started_at_ms",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "finished_at_ms",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "last_error",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        }
      ],
      "indexes": [
        {
          "name": "sqlite_autoindex_usage_monitoring_rollup_state_1",
          "columns": [
            "rollup_name"
          ],
          "unique": true
        }
      ],
      "sourceDdl": "CREATE TABLE usage_monitoring_rollup_state (\n\t\t\trollup_name text primary key,\n\t\t\tschema_version integer not null,\n\t\t\tstructure_revision text not null default '',\n\t\t\tstatus text not null,\n\t\t\tbackfill_last_event_id integer not null default 0,\n\t\t\tcoverage_event_id integer not null default 0,\n\t\t\ttarget_event_id integer not null default 0,\n\t\t\tprocessed_events integer not null default 0,\n\t\t\tlast_run_started_at_ms integer,\n\t\t\tupdated_at_ms integer not null default 0,\n\t\t\tfinished_at_ms integer,\n\t\t\tlast_error text\n\t\t)"
    },
    {
      "name": "usage_monitoring_search_index_state",
      "class": "derived",
      "columns": [
        {
          "name": "id",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 1,
          "mysqlType": "BIGINT"
        },
        {
          "name": "ready",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "TINYINT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        }
      ],
      "sourceDdl": "CREATE TABLE usage_monitoring_search_index_state (\n\t\t\tid integer primary key check (id = 1),\n\t\t\tready integer not null default 0,\n\t\t\tupdated_at_ms integer not null default 0\n\t\t)"
    },
    {
      "name": "usage_monitoring_selector_daily_rollups_v1",
      "class": "derived",
      "columns": [
        {
          "name": "model_format_revision",
          "kind": "text",
          "nullable": false,
          "default": "''",
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "bucket_ms",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 1,
          "mysqlType": "BIGINT"
        },
        {
          "name": "model",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 2,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "api_key_hash",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 3,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "provider",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 4,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_file_snapshot",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 5,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "account_snapshot",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 6,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_label_snapshot",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 7,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_index",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 8,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "source",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "source_hash",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 9,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ],
      "indexes": [
        {
          "name": "idx_usage_monitoring_selector_revision_bucket",
          "columns": [
            "model_format_revision",
            "bucket_ms"
          ],
          "sourceDdl": "CREATE INDEX idx_usage_monitoring_selector_revision_bucket on usage_monitoring_selector_daily_rollups_v1(model_format_revision, bucket_ms)"
        },
        {
          "name": "idx_usage_monitoring_selector_daily_bucket",
          "columns": [
            "bucket_ms"
          ],
          "sourceDdl": "CREATE INDEX idx_usage_monitoring_selector_daily_bucket on usage_monitoring_selector_daily_rollups_v1(bucket_ms)"
        },
        {
          "name": "sqlite_autoindex_usage_monitoring_selector_daily_rollups_v1_1",
          "columns": [
            "bucket_ms",
            "model",
            "api_key_hash",
            "provider",
            "auth_file_snapshot",
            "account_snapshot",
            "auth_label_snapshot",
            "auth_index",
            "source_hash"
          ],
          "unique": true
        }
      ],
      "sourceDdl": "CREATE TABLE usage_monitoring_selector_daily_rollups_v1 (\n\t\t\tmodel_format_revision text not null default '',\n\t\t\tbucket_ms integer not null,\n\t\t\tmodel text not null,\n\t\t\tapi_key_hash text not null,\n\t\t\tprovider text not null,\n\t\t\tauth_file_snapshot text not null,\n\t\t\taccount_snapshot text not null,\n\t\t\tauth_label_snapshot text not null,\n\t\t\tauth_index text not null,\n\t\t\tsource text not null,\n\t\t\tsource_hash text not null,\n\t\t\tupdated_at_ms integer not null,\n\t\t\tprimary key (\n\t\t\t\tbucket_ms, model, api_key_hash, provider, auth_file_snapshot,\n\t\t\t\taccount_snapshot, auth_label_snapshot, auth_index, source_hash\n\t\t\t)\n\t\t)"
    },
    {
      "name": "usage_pricing_account_rollups_v1",
      "class": "derived",
      "columns": [
        {
          "name": "structure_revision",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 1,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "account_key",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 2,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "account_snapshot",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_label_snapshot",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_provider_snapshot",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "auth_index",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "source",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "source_hash",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "model",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 3,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "billing_model",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 4,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "pricing_model",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 5,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "service_tier",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 6,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "context_threshold_tokens",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 7,
          "mysqlType": "BIGINT"
        },
        {
          "name": "calls",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "success_calls",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "failure_calls",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "input_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "output_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "reasoning_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "cached_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "cache_read_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "cache_creation_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_input_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_output_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_cached_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_cache_read_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_cache_creation_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "total_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "first_seen_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "last_seen_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ],
      "indexes": [
        {
          "name": "idx_usage_pricing_account_key",
          "columns": [
            "structure_revision",
            "account_key"
          ],
          "sourceDdl": "CREATE INDEX idx_usage_pricing_account_key on usage_pricing_account_rollups_v1(structure_revision, account_key)"
        },
        {
          "name": "sqlite_autoindex_usage_pricing_account_rollups_v1_1",
          "columns": [
            "structure_revision",
            "account_key",
            "model",
            "billing_model",
            "pricing_model",
            "service_tier",
            "context_threshold_tokens"
          ],
          "unique": true
        }
      ],
      "sourceDdl": "CREATE TABLE usage_pricing_account_rollups_v1 (\n\t\tstructure_revision text not null,\n\t\taccount_key text not null,\n\t\taccount_snapshot text,\n\t\tauth_label_snapshot text,\n\t\tauth_provider_snapshot text,\n\t\tauth_index text,\n\t\tsource text,\n\t\tsource_hash text,\n\t\tmodel text not null,\n\t\tbilling_model text not null,\n\t\tpricing_model text not null,\n\t\tservice_tier text not null,\n\t\tcontext_threshold_tokens integer not null,\n\t\tcalls integer not null default 0,\n\t\tsuccess_calls integer not null default 0,\n\t\tfailure_calls integer not null default 0,\n\t\tinput_tokens integer not null default 0,\n\t\toutput_tokens integer not null default 0,\n\t\treasoning_tokens integer not null default 0,\n\t\tcached_tokens integer not null default 0,\n\t\tcache_read_tokens integer not null default 0,\n\t\tcache_creation_tokens integer not null default 0,\n\t\tlong_input_tokens integer not null default 0,\n\t\tlong_output_tokens integer not null default 0,\n\t\tlong_cached_tokens integer not null default 0,\n\t\tlong_cache_read_tokens integer not null default 0,\n\t\tlong_cache_creation_tokens integer not null default 0,\n\t\ttotal_tokens integer not null default 0,\n\t\tfirst_seen_ms integer not null,\n\t\tlast_seen_ms integer not null,\n\t\tupdated_at_ms integer not null,\n\t\tprimary key (\n\t\t\tstructure_revision, account_key, model, billing_model, pricing_model,\n\t\t\tservice_tier, context_threshold_tokens\n\t\t)\n\t\t)"
    },
    {
      "name": "usage_pricing_hourly_rollups_v1",
      "class": "derived",
      "columns": [
        {
          "name": "structure_revision",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 1,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "bucket_ms",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 2,
          "mysqlType": "BIGINT"
        },
        {
          "name": "model",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 3,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "billing_model",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 4,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "pricing_model",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 5,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "service_tier",
          "kind": "text",
          "nullable": false,
          "primaryKeyPosition": 6,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "context_threshold_tokens",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 7,
          "mysqlType": "BIGINT"
        },
        {
          "name": "failed",
          "kind": "integer",
          "nullable": false,
          "primaryKeyPosition": 8,
          "mysqlType": "TINYINT"
        },
        {
          "name": "calls",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "input_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "output_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "reasoning_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "cached_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "cache_read_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "cache_creation_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_input_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_output_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_cached_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_cache_read_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "long_cache_creation_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "total_tokens",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "latency_sum_ms",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "latency_samples",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "zero_token_calls",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        }
      ],
      "indexes": [
        {
          "name": "idx_usage_pricing_hourly_bucket",
          "columns": [
            "structure_revision",
            "bucket_ms"
          ],
          "sourceDdl": "CREATE INDEX idx_usage_pricing_hourly_bucket on usage_pricing_hourly_rollups_v1(structure_revision, bucket_ms)"
        },
        {
          "name": "sqlite_autoindex_usage_pricing_hourly_rollups_v1_1",
          "columns": [
            "structure_revision",
            "bucket_ms",
            "model",
            "billing_model",
            "pricing_model",
            "service_tier",
            "context_threshold_tokens",
            "failed"
          ],
          "unique": true
        }
      ],
      "sourceDdl": "CREATE TABLE usage_pricing_hourly_rollups_v1 (\n\t\t\tstructure_revision text not null,\n\t\t\tbucket_ms integer not null,\n\t\t\tmodel text not null,\n\t\t\tbilling_model text not null,\n\t\t\tpricing_model text not null,\n\t\t\tservice_tier text not null,\n\t\t\tcontext_threshold_tokens integer not null,\n\t\t\tfailed integer not null,\n\t\t\tcalls integer not null default 0,\n\t\t\tinput_tokens integer not null default 0,\n\t\t\toutput_tokens integer not null default 0,\n\t\t\treasoning_tokens integer not null default 0,\n\t\t\tcached_tokens integer not null default 0,\n\t\t\tcache_read_tokens integer not null default 0,\n\t\t\tcache_creation_tokens integer not null default 0,\n\t\t\tlong_input_tokens integer not null default 0,\n\t\t\tlong_output_tokens integer not null default 0,\n\t\t\tlong_cached_tokens integer not null default 0,\n\t\t\tlong_cache_read_tokens integer not null default 0,\n\t\t\tlong_cache_creation_tokens integer not null default 0,\n\t\t\ttotal_tokens integer not null default 0,\n\t\t\tlatency_sum_ms integer not null default 0,\n\t\t\tlatency_samples integer not null default 0,\n\t\t\tzero_token_calls integer not null default 0,\n\t\t\tupdated_at_ms integer not null,\n\t\t\tprimary key (\n\t\t\t\tstructure_revision, bucket_ms, model, billing_model, pricing_model,\n\t\t\t\tservice_tier, context_threshold_tokens, failed\n\t\t\t)\n\t\t)"
    },
    {
      "name": "usage_pricing_rollup_state",
      "class": "derived",
      "columns": [
        {
          "name": "rollup_name",
          "kind": "text",
          "nullable": true,
          "primaryKeyPosition": 1,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "schema_version",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "structure_revision",
          "kind": "text",
          "nullable": false,
          "default": "''",
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "status",
          "kind": "text",
          "nullable": false,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "backfill_last_event_id",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "coverage_event_id",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "target_event_id",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "processed_events",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "min_bucket_ms",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "max_bucket_ms",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "last_run_started_at_ms",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "finished_at_ms",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "last_error",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        }
      ],
      "indexes": [
        {
          "name": "sqlite_autoindex_usage_pricing_rollup_state_1",
          "columns": [
            "rollup_name"
          ],
          "unique": true
        }
      ],
      "sourceDdl": "CREATE TABLE usage_pricing_rollup_state (\n\t\t\trollup_name text primary key,\n\t\t\tschema_version integer not null,\n\t\t\tstructure_revision text not null default '',\n\t\t\tstatus text not null,\n\t\t\tbackfill_last_event_id integer not null default 0,\n\t\t\tcoverage_event_id integer not null default 0,\n\t\t\ttarget_event_id integer not null default 0,\n\t\t\tprocessed_events integer not null default 0,\n\t\t\tmin_bucket_ms integer,\n\t\t\tmax_bucket_ms integer,\n\t\t\tlast_run_started_at_ms integer,\n\t\t\tupdated_at_ms integer not null default 0,\n\t\t\tfinished_at_ms integer,\n\t\t\tlast_error text\n\t\t)"
    },
    {
      "name": "usage_rollup_checkpoints",
      "class": "derived",
      "columns": [
        {
          "name": "name",
          "kind": "text",
          "nullable": true,
          "primaryKeyPosition": 1,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "last_event_id",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "mysqlType": "BIGINT"
        },
        {
          "name": "last_error",
          "kind": "text",
          "nullable": true,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "last_run_started_at_ms",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        },
        {
          "name": "last_run_finished_at_ms",
          "kind": "integer",
          "nullable": true,
          "mysqlType": "BIGINT"
        }
      ],
      "indexes": [
        {
          "name": "sqlite_autoindex_usage_rollup_checkpoints_1",
          "columns": [
            "name"
          ],
          "unique": true
        }
      ],
      "sourceDdl": "CREATE TABLE usage_rollup_checkpoints (\n\t\t\tname text primary key,\n\t\t\tlast_event_id integer not null default 0,\n\t\t\tupdated_at_ms integer not null,\n\t\t\tlast_error text,\n\t\t\tlast_run_started_at_ms integer,\n\t\t\tlast_run_finished_at_ms integer\n\t\t)"
    },
    {
      "name": "usage_rollup_rebuild_state",
      "class": "derived",
      "columns": [
        {
          "name": "name",
          "kind": "text",
          "nullable": true,
          "primaryKeyPosition": 1,
          "mysqlType": "LONGTEXT"
        },
        {
          "name": "target_event_id",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        },
        {
          "name": "updated_at_ms",
          "kind": "integer",
          "nullable": false,
          "default": "0",
          "mysqlType": "BIGINT"
        }
      ],
      "indexes": [
        {
          "name": "sqlite_autoindex_usage_rollup_rebuild_state_1",
          "columns": [
            "name"
          ],
          "unique": true
        }
      ],
      "sourceDdl": "CREATE TABLE usage_rollup_rebuild_state (\n\t\tname text primary key,\n\t\ttarget_event_id integer not null default 0,\n\t\tupdated_at_ms integer not null default 0\n\t)"
    }
  ]
}`
