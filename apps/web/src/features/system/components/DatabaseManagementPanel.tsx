import { useCallback, useEffect, useId, useMemo, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { useNotificationStore } from '@/stores';
import {
  usageServiceApi,
  type DatabaseMigrationHistoryResponse,
  type DatabaseMutationControl,
  type MySQLConnectionInput,
  type SQLiteCacheCleanupPreview,
  type UsageServiceMetricValue,
  type UsageServiceStatus,
} from '@/services/api/usageService';
import { formatFileSize } from '@/utils/format';
import { DatabaseActionButton } from './DatabaseActionButton';
import { DatabaseMigrationHistoryModal } from './DatabaseMigrationHistoryModal';
import { SQLiteSourceSwitcher } from './SQLiteSourceSwitcher';
import styles from './DatabaseManagementPanel.module.scss';

interface DatabaseManagementPanelProps {
  status: UsageServiceStatus;
  base: string;
  managementKey: string;
  loading: boolean;
  onRefresh: () => Promise<void>;
  onRestartRecovered?: (managementKey: string) => Promise<void>;
}

type AsyncAction = () => Promise<unknown>;

const DEFAULT_MYSQL_CONNECTION: Readonly<MySQLConnectionInput> = {
  address: '127.0.0.1:3306',
  database: 'cpamanage',
  username: 'root',
  password: 'root123',
  tlsMode: 'disabled',
  caCertificate: '',
  confirmInsecureTls: true,
};

const formatBytes = (value: number | undefined) =>
  Number.isFinite(value) && Number(value) >= 0 ? formatFileSize(Number(value)) : '-';

const formatNumber = (value: number | undefined, locale: string, maximumFractionDigits = 1) =>
  Number.isFinite(value) ? Number(value).toLocaleString(locale, { maximumFractionDigits }) : '-';

const formatTime = (value: number | undefined, locale: string) =>
  Number.isFinite(value) && Number(value) > 0
    ? new Date(Number(value)).toLocaleString(locale)
    : '-';

const makeIdempotencyKey = () => {
  if (typeof crypto !== 'undefined' && 'randomUUID' in crypto) return crypto.randomUUID();
  return `db-${Date.now()}-${Math.random().toString(16).slice(2)}`;
};

const Metric = ({
  label,
  metric,
  locale,
  bytes = false,
}: {
  label: string;
  metric?: UsageServiceMetricValue;
  locale: string;
  bytes?: boolean;
}) => {
  const { t } = useTranslation();
  let value = '-';
  let unavailable = false;
  if (metric?.permissionDenied) {
    value = t('system_info.database_management.permission_denied');
    unavailable = true;
  } else if (metric?.error) {
    value = metric.error;
    unavailable = true;
  } else if (metric?.available === false) {
    value = t('system_info.database_management.unavailable');
    unavailable = true;
  } else if (Number.isFinite(metric?.value)) {
    value = bytes ? formatBytes(metric?.value) : formatNumber(metric?.value, locale);
  }
  return (
    <div className={styles.metric}>
      <span>{label}</span>
      <strong className={unavailable ? styles.mutedValue : ''}>{value}</strong>
    </div>
  );
};

const Section = ({ title, children }: { title: string; children: React.ReactNode }) => (
  <section className={styles.section} aria-label={title}>
    <h3>{title}</h3>
    {children}
  </section>
);

export function DatabaseManagementPanel({
  status,
  base,
  managementKey,
  loading,
  onRefresh,
  onRestartRecovered,
}: DatabaseManagementPanelProps) {
  const { t, i18n } = useTranslation();
  const { showNotification, showConfirmation } = useNotificationStore();
  const topology = status.databaseTopology;
  const sqlite = status.databases?.sqlite;
  const mysql = status.databases?.mysql;
  const replication = status.replication;
  const migration = status.databaseMigration;
  const coverage = status.cacheCoverage;
  const generation = topology?.generation ?? 0;
  const [busyAction, setBusyAction] = useState('');
  const [cleanupPreview, setCleanupPreview] = useState<SQLiteCacheCleanupPreview | null>(null);
  const [migrationHistoryOpen, setMigrationHistoryOpen] = useState(false);
  const [migrationHistoryLoading, setMigrationHistoryLoading] = useState(false);
  const [migrationHistoryError, setMigrationHistoryError] = useState('');
  const [migrationHistory, setMigrationHistory] = useState<DatabaseMigrationHistoryResponse | null>(
    null
  );
  const [retentionDays, setRetentionDays] = useState(coverage?.retentionDays ?? 15);
  const [cleanupEnabled, setCleanupEnabled] = useState(coverage?.cleanupEnabled ?? false);
  const [mysqlInput, setMySQLInput] = useState<MySQLConnectionInput>(() => ({
    ...DEFAULT_MYSQL_CONNECTION,
    // Never invent or expose a password for an existing connection. An empty
    // password keeps the encrypted configured password when the form is saved.
    password: mysql?.configured ? '' : DEFAULT_MYSQL_CONNECTION.password,
  }));
  const addressId = useId();
  const databaseId = useId();
  const usernameId = useId();
  const passwordId = useId();
  const tlsId = useId();
  const caId = useId();
  const retentionId = useId();

  useEffect(() => {
    setRetentionDays(coverage?.retentionDays ?? 15);
    setCleanupEnabled(coverage?.cleanupEnabled ?? false);
  }, [coverage?.cleanupEnabled, coverage?.retentionDays]);

  const loadMigrationHistory = useCallback(
    async (showLoading: boolean) => {
      if (showLoading) setMigrationHistoryLoading(true);
      try {
        const result = await usageServiceApi.getDatabaseMigrationHistory(base, managementKey, 20);
        setMigrationHistory(result);
        setMigrationHistoryError('');
      } catch (error) {
        setMigrationHistoryError(error instanceof Error ? error.message : String(error));
      } finally {
        if (showLoading) setMigrationHistoryLoading(false);
      }
    },
    [base, managementKey]
  );

  useEffect(() => {
    if (!migrationHistoryOpen) return;
    void loadMigrationHistory(true);
    const timer = window.setInterval(() => void loadMigrationHistory(false), 5_000);
    return () => window.clearInterval(timer);
  }, [loadMigrationHistory, migrationHistoryOpen]);

  const mutationControl = (): DatabaseMutationControl => {
    if (!Number.isSafeInteger(generation) || generation <= 0) {
      throw new Error(t('system_info.database_management.generation_unavailable'));
    }
    return { expectedGeneration: generation, idempotencyKey: makeIdempotencyKey() };
  };

  const run = async (name: string, action: AsyncAction) => {
    setBusyAction(name);
    try {
      await action();
      showNotification(t('system_info.database_management.action_succeeded'), 'success');
      await onRefresh();
    } catch (error) {
      showNotification(error instanceof Error ? error.message : String(error), 'error');
      await onRefresh();
    } finally {
      setBusyAction('');
    }
  };

  const confirm = (
    name: string,
    message: ReactNode,
    action: AsyncAction,
    options: { danger?: boolean; double?: boolean; secondMessage?: ReactNode } = {}
  ) => {
    showConfirmation({
      title: t('system_info.database_management.confirm_title'),
      message,
      variant: options.danger ? 'danger' : 'primary',
      confirmText: t('common.confirm'),
      secondConfirmation: options.double
        ? {
            title: t('system_info.database_management.confirm_again_title'),
            message:
              options.secondMessage ?? t('system_info.database_management.confirm_again_message'),
            variant: 'danger',
            confirmText: t('common.confirm'),
          }
        : undefined,
      onConfirm: () => run(name, action),
    });
  };

  const mysqlInputValid = Boolean(
    mysqlInput.address.trim() && mysqlInput.database.trim() && mysqlInput.username.trim()
  );
  const saveMySQL = () => {
    const action = async () => {
      await usageServiceApi.saveMySQLConfig(base, managementKey, {
        ...mysqlInput,
        password: mysqlInput.password || undefined,
        confirmInsecureTls: mysqlInput.tlsMode === 'disabled',
        ...mutationControl(),
      });
      setMySQLInput((value) => ({ ...value, password: '' }));
    };
    if (mysqlInput.tlsMode === 'disabled') {
      confirm('save-mysql', t('system_info.database_management.tls_disabled_warning'), action, {
        danger: true,
        double: true,
      });
      return;
    }
    void run('save-mysql', action);
  };

  const testMySQL = async () => {
    const action = async () => {
      const result = await usageServiceApi.testMySQLConnection(base, managementKey, {
        ...mysqlInput,
        confirmInsecureTls: mysqlInput.tlsMode === 'disabled',
      });
      if (!result.success) {
        throw new Error(result.error || t('system_info.database_management.test_failed'));
      }
      showNotification(
        t('system_info.database_management.test_succeeded', {
          version: result.version || '-',
          latency: result.latencyMs ?? '-',
        }),
        'success'
      );
    };
    await run('test-mysql', action);
  };

  const migrationState = migration?.state ?? migration?.status ?? '';
  const migrationTables = migration?.tables ?? [];
  const migrationDerivedTables = migration?.derivedTables ?? [];
  const migrationCopiedRows =
    migration?.copiedRows ??
    migrationTables.reduce((sum, table) => sum + (table.copiedRows ?? 0), 0);
  const migrationTotalRows =
    migration?.totalRows ?? migrationTables.reduce((sum, table) => sum + (table.totalRows ?? 0), 0);
  const migrationCopiedBytes =
    migration?.copiedBytes ??
    migrationTables.reduce((sum, table) => sum + (table.copiedBytes ?? 0), 0);
  const migrationTokenValues = [
    migration?.inputTokens,
    migration?.outputTokens,
    migration?.cachedTokens,
    migration?.reasoningTokens,
  ];
  const migrationTokens = migrationTokenValues.some(Number.isFinite)
    ? migrationTokenValues.reduce<number>((sum, value) => sum + (value ?? 0), 0)
    : undefined;
  const migrationProgress = Number.isFinite(migration?.progressPercent)
    ? Math.max(0, Math.min(100, Number(migration?.progressPercent)))
    : migrationTotalRows > 0
      ? (migrationCopiedRows / migrationTotalRows) * 100
      : 0;
  const migrationCompletedSteps =
    migration?.completedSteps ?? migrationDerivedTables.filter((table) => table.completed).length;
  const migrationTotalSteps = migration?.totalSteps ?? migrationDerivedTables.length;
  const migrationPhaseLabels: Record<string, string> = {
    enable_dual_write: t('system_info.database_management.migration_phase_enable_dual_write'),
    copy_history: t('system_info.database_management.migration_phase_copy_history'),
    rebuild_derived: t('system_info.database_management.migration_phase_rebuild_derived'),
    validate: t('system_info.database_management.migration_phase_validate'),
    ready_to_cutover: t('system_info.database_management.migration_phase_ready_to_cutover'),
    completed: t('system_info.database_management.migration_phase_completed'),
  };
  const validationStageLabels: Record<string, string> = {
    preparing: t('system_info.database_management.validation_stage_preparing'),
    catch_up: t('system_info.database_management.validation_stage_catch_up'),
    table_snapshot: t('system_info.database_management.validation_stage_table_snapshot'),
    aggregates: t('system_info.database_management.validation_stage_aggregates'),
    watermark: t('system_info.database_management.validation_stage_watermark'),
    derived: t('system_info.database_management.validation_stage_derived'),
    completed: t('system_info.database_management.validation_stage_completed'),
  };
  const validationSideLabels: Record<string, string> = {
    source: t('system_info.database_management.validation_side_source'),
    target: t('system_info.database_management.validation_side_target'),
  };
  const migrationStateLabels: Record<string, string> = {
    running: t('system_info.database_management.migration_state_running'),
    validating: t('system_info.database_management.migration_state_validating'),
    paused: t('system_info.database_management.migration_state_paused'),
    failed: t('system_info.database_management.migration_state_failed'),
    canceled: t('system_info.database_management.migration_state_canceled'),
    succeeded: t('system_info.database_management.migration_state_succeeded'),
  };
  const migrationPhaseLabel = migration?.phase
    ? migrationPhaseLabels[migration.phase] || migration.phase
    : t('system_info.database_management.idle');
  const validationProgress = migration?.validationProgress;
  const validationInProgress = Boolean(validationProgress?.running);
  const awaitingValidation =
    migration?.phase === 'validate' && migrationState === 'running' && !validationInProgress;
  const awaitingCutover =
    migration?.phase === 'ready_to_cutover' &&
    migrationState === 'running' &&
    !validationInProgress;
  const migrationStateLabel = validationInProgress
    ? t('system_info.database_management.migration_state_validating')
    : awaitingValidation || awaitingCutover
      ? t('system_info.database_management.migration_state_awaiting_admin')
      : migrationStateLabels[migrationState] || migrationState;
  const rebuildingDerived = migration?.phase === 'rebuild_derived';
  const derivedStepInFlight = Boolean(
    rebuildingDerived && migrationState === 'running' && migration?.currentTableActive
  );
  const validationValid =
    migration?.validationValid ??
    Boolean(
      migration?.validationToken &&
      (migration?.phase === 'ready_to_cutover' || migration?.phase === 'completed')
    );
  const validationStale = Boolean(
    !validationInProgress &&
    migration?.phase === 'ready_to_cutover' &&
    migration?.validationToken &&
    !validationValid
  );
  const migrationActive = ['pending', 'running', 'paused', 'validating'].includes(migrationState);
  const migrationResumable = ['paused', 'failed'].includes(migrationState);
  const migrationBlocksNewStart = Boolean(
    migration?.id &&
    ['pending', 'running', 'paused', 'validating', 'failed'].includes(migrationState)
  );
  const migrationCancelable = ['pending', 'running', 'paused', 'validating', 'failed'].includes(
    migrationState
  );
  const canValidate = Boolean(
    mysql?.connected &&
    replication?.enabled &&
    !validationInProgress &&
    migration?.id &&
    (migration?.phase === 'validate' || validationStale) &&
    migrationState === 'running'
  );
  const cachePolicyValid =
    Number.isInteger(retentionDays) && retentionDays >= 1 && retentionDays <= 3650;
  const canStartMigration = Boolean(
    replication?.enabled && mysql?.connected && !migrationBlocksNewStart
  );
  const canReinitializeMySQLSchema = Boolean(
    Number.isSafeInteger(generation) &&
    generation > 0 &&
    mysql?.configured &&
    mysql?.database &&
    !replication?.enabled &&
    !migrationActive &&
    topology?.writePrimary !== 'mysql' &&
    topology?.businessReadPrimary !== 'mysql' &&
    topology?.systemReadPrimary !== 'mysql'
  );
  const canCutover = Boolean(
    mysql?.connected &&
    migration?.id &&
    migrationState === 'running' &&
    !validationInProgress &&
    validationValid &&
    migration.validationToken &&
    topology?.readCutoverReady &&
    topology?.businessReadPrimary !== 'mysql'
  );
  const fallbackActive =
    topology?.fallbackActive ??
    ['active', 'fallback', 'degraded'].includes(topology?.failoverState?.toLowerCase() ?? '');
  const partialCoverage = Boolean(fallbackActive && coverage?.complete === false);
  const replicationPendingRows = replication?.pendingRows ?? replication?.pendingMutations;
  const replicationThroughput =
    replication?.throughputRowsPerSecond ?? replication?.mutationsPerSecond;
  const replicationRetries = replication?.retries ?? replication?.retryCount;
  const heartbeatAgeMs = replication?.heartbeatAtMs
    ? Math.max(0, Date.now() - replication.heartbeatAtMs)
    : undefined;
  const computedReplicationState =
    replication?.stalled ||
    (Number(replicationPendingRows) > 0 && Number(heartbeatAgeMs) >= 120_000)
      ? 'stalled'
      : Number(replicationPendingRows) > 0 && Number(heartbeatAgeMs) >= 60_000
        ? 'warning'
        : replication?.state;

  const tipWithBusyState = (tip: string) =>
    busyAction ? `${tip} ${t('system_info.database_management.tip_operation_busy')}` : tip;
  const validationTip = (() => {
    if (!migration?.id) {
      return t('system_info.database_management.tip_validate_no_task');
    }
    if (validationInProgress) {
      return t('system_info.database_management.tip_validate_in_progress');
    }
    if (!mysql?.connected) {
      return t('system_info.database_management.tip_validate_mysql_unavailable');
    }
    if (!replication?.enabled) {
      return t('system_info.database_management.tip_validate_replication_required');
    }
    if (validationValid && migration?.phase === 'ready_to_cutover') {
      return t('system_info.database_management.tip_validate_current');
    }
    if (migrationResumable) {
      return t('system_info.database_management.tip_validate_resume_first', {
        state: migrationStateLabel,
      });
    }
    if (migrationState !== 'running') {
      return t('system_info.database_management.tip_validate_running_only');
    }
    if (migration?.phase !== 'validate' && !validationStale) {
      return t('system_info.database_management.tip_validate_wait');
    }
    return t('system_info.database_management.tip_validate');
  })();

  const failoverConfirmationMessage = (
    <div className={styles.confirmationMessage}>
      <p>{t('system_info.database_management.failover_confirm')}</p>
      <strong>{t('system_info.database_management.failover_impact_title')}</strong>
      <ul>
        <li>{t('system_info.database_management.failover_impact_write')}</li>
        <li>{t('system_info.database_management.failover_impact_sync')}</li>
        <li>{t('system_info.database_management.failover_impact_read_route')}</li>
        <li>{t('system_info.database_management.failover_impact_outage')}</li>
        <li>{t('system_info.database_management.failover_impact_return')}</li>
      </ul>
    </div>
  );

  const topologyItems = useMemo(
    () => [
      [t('system_info.database_management.write_primary'), topology?.writePrimary ?? 'sqlite'],
      [
        t('system_info.database_management.business_read_primary'),
        topology?.businessReadPrimary ?? 'sqlite',
      ],
      [
        t('system_info.database_management.system_read_primary'),
        topology?.systemReadPrimary ?? 'sqlite',
      ],
      [
        t('system_info.database_management.actual_source'),
        fallbackActive
          ? topology?.fallbackSource || 'sqlite'
          : topology?.businessReadPrimary || 'sqlite',
      ],
    ],
    [fallbackActive, t, topology]
  );

  return (
    <Card
      title={t('system_info.database_management.title')}
      extra={
        <div className={styles.headerActions}>
          <DatabaseActionButton
            tip={t('system_info.database_management.tip_history')}
            variant="secondary"
            size="sm"
            onClick={() => setMigrationHistoryOpen(true)}
          >
            {t('system_info.database_management.history_button')}
          </DatabaseActionButton>
          <DatabaseActionButton
            tip={t('system_info.database_management.tip_refresh')}
            variant="secondary"
            size="sm"
            onClick={() => void onRefresh()}
            loading={loading}
          >
            {t('common.refresh')}
          </DatabaseActionButton>
        </div>
      }
      className={styles.card}
    >
      <div className={styles.liveRegion} role="status" aria-live="polite">
        {busyAction
          ? t('system_info.database_management.action_running')
          : t('system_info.database_management.generation', { generation })}
      </div>

      {partialCoverage ? (
        <div className={styles.partialWarning} role="alert">
          <strong>{t('system_info.database_management.partial_title')}</strong>
          <span>
            {t('system_info.database_management.partial_message', {
              from: formatTime(coverage?.fromMs, i18n.language),
              to: formatTime(coverage?.toMs, i18n.language),
            })}
          </span>
        </div>
      ) : null}

      <Section title={t('system_info.database_management.topology_title')}>
        <div className={styles.topologyGrid}>
          {topologyItems.map(([label, value]) => (
            <div className={styles.topologyItem} key={label}>
              <span>{label}</span>
              <strong>{value}</strong>
            </div>
          ))}
        </div>
      </Section>

      <SQLiteSourceSwitcher
        status={status}
        base={base}
        managementKey={managementKey}
        loading={loading}
        onRefresh={onRefresh}
        onRestartRecovered={onRestartRecovered}
      />

      <div className={styles.columns}>
        <Section title={t('system_info.database_management.sqlite_title')}>
          <div className={styles.metricsGrid}>
            <Metric
              label={t('system_info.database_management.total_size')}
              metric={{ value: sqlite?.totalBytes }}
              locale={i18n.language}
              bytes
            />
            <Metric
              label={t('system_info.database_management.database_file')}
              metric={{ value: sqlite?.databaseBytes }}
              locale={i18n.language}
              bytes
            />
            <Metric
              label={t('system_info.database_management.effective_size')}
              metric={{ value: sqlite?.effectiveBytes }}
              locale={i18n.language}
              bytes
            />
            <Metric
              label={t('system_info.database_management.reusable_size')}
              metric={{ value: sqlite?.reusableBytes }}
              locale={i18n.language}
              bytes
            />
            <Metric
              label={t('system_info.database_management.wal_size')}
              metric={{ value: sqlite?.walBytes }}
              locale={i18n.language}
              bytes
            />
            <Metric
              label={t('system_info.database_management.shm_size')}
              metric={{ value: sqlite?.shmBytes }}
              locale={i18n.language}
              bytes
            />
          </div>
          <p className={styles.hint}>
            {t('system_info.database_management.coverage', {
              from: formatTime(coverage?.fromMs, i18n.language),
              to: formatTime(coverage?.toMs, i18n.language),
            })}
          </p>
          <p className={styles.hint}>
            {t('system_info.database_management.checkpoint_status', {
              mode: sqlite?.checkpoint?.mode ?? status.database?.checkpoint?.mode ?? '-',
              time: formatTime(
                sqlite?.checkpoint?.executedAtMs ?? status.database?.checkpoint?.executedAtMs,
                i18n.language
              ),
              cleanup: sqlite?.cleanupStatus ?? coverage?.cleanupState ?? '-',
            })}
          </p>
          {sqlite?.lastError || sqlite?.checkpoint?.error || status.database?.checkpoint?.error ? (
            <div className={styles.error} role="alert">
              {sqlite?.lastError || sqlite?.checkpoint?.error || status.database?.checkpoint?.error}
            </div>
          ) : null}
          <div className={styles.inlineForm}>
            <label htmlFor={retentionId}>
              {t('system_info.database_management.retention_days')}
            </label>
            <input
              id={retentionId}
              type="number"
              min={1}
              max={3650}
              value={retentionDays}
              onChange={(event) => setRetentionDays(Number(event.target.value))}
            />
            <label className={styles.checkboxLabel}>
              <input
                type="checkbox"
                checked={cleanupEnabled}
                onChange={(event) => setCleanupEnabled(event.target.checked)}
              />
              {t('system_info.database_management.scheduled_cleanup')}
            </label>
            <DatabaseActionButton
              tip={tipWithBusyState(t('system_info.database_management.tip_cache_policy'))}
              size="sm"
              variant="secondary"
              disabled={!cachePolicyValid || busyAction !== ''}
              loading={busyAction === 'cache-policy'}
              onClick={() =>
                void run('cache-policy', () =>
                  usageServiceApi.updateSQLiteCachePolicy(base, managementKey, {
                    enabled: cleanupEnabled,
                    retentionDays,
                    ...mutationControl(),
                  })
                )
              }
            >
              {t('common.save')}
            </DatabaseActionButton>
          </div>
          {cleanupPreview ? (
            <div className={styles.preview} aria-live="polite">
              {cleanupPreview.eligible
                ? t('system_info.database_management.cleanup_preview_result', {
                    rows: formatNumber(cleanupPreview.estimatedRows, i18n.language, 0),
                    bytes: formatBytes(cleanupPreview.estimatedBytes),
                  })
                : cleanupPreview.blockedReason ||
                  t('system_info.database_management.cleanup_blocked')}
            </div>
          ) : null}
        </Section>

        <Section title={t('system_info.database_management.mysql_title')}>
          <div className={styles.connectionLine}>
            <span className={mysql?.connected ? styles.good : styles.warn}>
              {mysql?.connected
                ? t('system_info.database_management.connected')
                : mysql?.configured
                  ? t('system_info.database_management.disconnected')
                  : t('system_info.database_management.not_configured')}
            </span>
            <span>{mysql?.maskedAddress || '-'}</span>
            <span>{mysql?.version || '-'}</span>
          </div>
          <div className={styles.metricsGrid}>
            <Metric
              label={t('system_info.database_management.ping')}
              metric={{ value: mysql?.pingLatencyMs }}
              locale={i18n.language}
            />
            <Metric
              label={t('system_info.database_management.uptime')}
              metric={{ value: mysql?.uptimeSeconds }}
              locale={i18n.language}
            />
            <Metric
              label={t('system_info.database_management.mysql_size')}
              metric={{ value: mysql?.databaseBytes }}
              locale={i18n.language}
              bytes
            />
            <Metric
              label={t('system_info.database_management.table_size')}
              metric={{ value: mysql?.tableBytes }}
              locale={i18n.language}
              bytes
            />
            <Metric
              label={t('system_info.database_management.index_size')}
              metric={{ value: mysql?.indexBytes }}
              locale={i18n.language}
              bytes
            />
            <Metric
              label={t('system_info.database_management.connections')}
              metric={mysql?.connections}
              locale={i18n.language}
            />
            <Metric
              label={t('system_info.database_management.max_connections')}
              metric={mysql?.maxConnections}
              locale={i18n.language}
            />
            <Metric label="QPS" metric={mysql?.queriesPerSecond} locale={i18n.language} />
            <Metric label="TPS" metric={mysql?.transactionsPerSecond} locale={i18n.language} />
            <Metric
              label={t('system_info.database_management.slow_queries')}
              metric={mysql?.slowQueries}
              locale={i18n.language}
            />
            <Metric
              label={t('system_info.database_management.buffer_pool')}
              metric={mysql?.bufferPoolBytes}
              locale={i18n.language}
              bytes
            />
            <Metric
              label={t('system_info.database_management.lock_waits')}
              metric={mysql?.lockWaits}
              locale={i18n.language}
            />
            <Metric
              label={t('system_info.database_management.deadlocks')}
              metric={mysql?.deadlocks}
              locale={i18n.language}
            />
            <Metric
              label={t('system_info.database_management.data_lock_waits')}
              metric={mysql?.dataLockWaits}
              locale={i18n.language}
            />
            <Metric
              label={t('system_info.database_management.metadata_lock_waits')}
              metric={mysql?.metadataLockWaits}
              locale={i18n.language}
            />
          </div>
          <div className={styles.poolLine}>
            {t('system_info.database_management.pool', {
              open: formatNumber(mysql?.pool?.open, i18n.language, 0),
              inUse: formatNumber(mysql?.pool?.inUse, i18n.language, 0),
              idle: formatNumber(mysql?.pool?.idle, i18n.language, 0),
              waits: formatNumber(mysql?.pool?.waitCount, i18n.language, 0),
            })}
          </div>
          {mysql?.lastError ? (
            <div className={styles.error} role="alert">
              {mysql.lastError}
            </div>
          ) : null}
        </Section>
      </div>

      <Section title={t('system_info.database_management.mysql_config_title')}>
        <div className={styles.formGrid}>
          <label htmlFor={addressId}>{t('system_info.database_management.address')}</label>
          <input
            id={addressId}
            autoComplete="off"
            value={mysqlInput.address}
            placeholder="mysql.example.com:3306"
            onChange={(event) =>
              setMySQLInput((value) => ({ ...value, address: event.target.value }))
            }
          />
          <label htmlFor={databaseId}>{t('system_info.database_management.database')}</label>
          <input
            id={databaseId}
            autoComplete="off"
            value={mysqlInput.database}
            onChange={(event) =>
              setMySQLInput((value) => ({ ...value, database: event.target.value }))
            }
          />
          <label htmlFor={usernameId}>{t('system_info.database_management.username')}</label>
          <input
            id={usernameId}
            autoComplete="username"
            value={mysqlInput.username}
            onChange={(event) =>
              setMySQLInput((value) => ({ ...value, username: event.target.value }))
            }
          />
          <label htmlFor={passwordId}>{t('system_info.database_management.password')}</label>
          <input
            id={passwordId}
            type="password"
            autoComplete="new-password"
            value={mysqlInput.password}
            placeholder={t('system_info.database_management.password_placeholder')}
            onChange={(event) =>
              setMySQLInput((value) => ({ ...value, password: event.target.value }))
            }
          />
          <label htmlFor={tlsId}>TLS</label>
          <select
            id={tlsId}
            value={mysqlInput.tlsMode}
            onChange={(event) =>
              setMySQLInput((value) => ({
                ...value,
                tlsMode: event.target.value as MySQLConnectionInput['tlsMode'],
              }))
            }
          >
            <option value="verify_identity">
              {t('system_info.database_management.tls_verify')}
            </option>
            <option value="disabled">{t('system_info.database_management.tls_disabled')}</option>
          </select>
          <label htmlFor={caId}>{t('system_info.database_management.ca_certificate')}</label>
          <textarea
            id={caId}
            rows={3}
            value={mysqlInput.caCertificate}
            placeholder={t('system_info.database_management.ca_certificate_placeholder')}
            onChange={(event) =>
              setMySQLInput((value) => ({ ...value, caCertificate: event.target.value }))
            }
          />
        </div>
        <p className={styles.hint}>{t('system_info.database_management.password_hint')}</p>
        <div className={styles.actions}>
          <DatabaseActionButton
            tip={tipWithBusyState(t('system_info.database_management.tip_test_mysql'))}
            size="sm"
            variant="secondary"
            disabled={!mysqlInputValid || busyAction !== ''}
            loading={busyAction === 'test-mysql'}
            onClick={() => void testMySQL()}
          >
            {t('system_info.database_management.test_mysql')}
          </DatabaseActionButton>
          <DatabaseActionButton
            tip={tipWithBusyState(t('system_info.database_management.tip_save_mysql'))}
            size="sm"
            disabled={!mysqlInputValid || busyAction !== ''}
            loading={busyAction === 'save-mysql'}
            onClick={saveMySQL}
          >
            {t('system_info.database_management.save_mysql')}
          </DatabaseActionButton>
        </div>
      </Section>

      <div className={styles.columns}>
        <Section title={t('system_info.database_management.replication_title')}>
          <div className={styles.progressHeader} aria-live="polite">
            <strong>{computedReplicationState || t('system_info.database_management.idle')}</strong>
            <span>{replication?.direction || '-'}</span>
          </div>
          <div className={styles.metricsGrid}>
            <Metric
              label={t('system_info.database_management.pending_rows')}
              metric={{ value: replicationPendingRows }}
              locale={i18n.language}
            />
            <Metric
              label={t('system_info.database_management.pending_bytes')}
              metric={{ value: replication?.pendingBytes }}
              locale={i18n.language}
              bytes
            />
            <Metric
              label={t('system_info.database_management.throughput')}
              metric={{ value: replicationThroughput }}
              locale={i18n.language}
            />
            <Metric
              label={t('system_info.database_management.retries')}
              metric={{ value: replicationRetries }}
              locale={i18n.language}
            />
          </div>
          <p className={styles.hint}>
            {t('system_info.database_management.watermarks', {
              source: replication?.sourceWatermark ?? '-',
              target: replication?.targetWatermark ?? '-',
              epoch: replication?.epoch ?? '-',
            })}
          </p>
          <p className={styles.hint}>
            {t('system_info.database_management.replication_times', {
              oldest: formatTime(replication?.oldestPendingAtMs, i18n.language),
              success: formatTime(replication?.lastSuccessAtMs, i18n.language),
              heartbeat: formatTime(replication?.heartbeatAtMs, i18n.language),
            })}
          </p>
          {computedReplicationState === 'warning' || computedReplicationState === 'stalled' ? (
            <div className={styles.error} role="alert">
              {t(
                computedReplicationState === 'stalled'
                  ? 'system_info.database_management.replication_stalled'
                  : 'system_info.database_management.replication_delayed'
              )}
            </div>
          ) : null}
          {replication?.lastError || replication?.error ? (
            <div className={styles.error} role="alert">
              {replication.lastError || replication.error}
            </div>
          ) : null}
        </Section>

        <Section title={t('system_info.database_management.migration_title')}>
          <div className={styles.progressHeader} aria-live="polite">
            <strong>
              {migrationPhaseLabel}
              {migrationStateLabel ? ` · ${migrationStateLabel}` : ''}
            </strong>
            <span>
              {rebuildingDerived && migrationTotalSteps > 0
                ? t('system_info.database_management.migration_step_count', {
                    completed: migrationCompletedSteps,
                    total: migrationTotalSteps,
                  })
                : `${formatNumber(migrationProgress, i18n.language)}%`}
            </span>
          </div>
          <progress
            max={100}
            value={derivedStepInFlight ? undefined : migrationProgress}
            aria-label={t('system_info.database_management.migration_progress')}
          />
          {awaitingValidation ? (
            <p className={styles.hint} role="status">
              {t('system_info.database_management.migration_validation_ready_hint')}
            </p>
          ) : null}
          {validationInProgress ? (
            <div className={styles.validationProgress} role="status" aria-live="polite">
              <div className={styles.progressHeader}>
                <strong>
                  {validationStageLabels[validationProgress?.stage || ''] ||
                    validationProgress?.stage ||
                    t('system_info.database_management.validation_stage_preparing')}
                </strong>
                <span>{formatNumber(validationProgress?.progressPercent, i18n.language)}%</span>
              </div>
              <progress
                max={100}
                value={Math.max(0, Math.min(100, Number(validationProgress?.progressPercent) || 0))}
                aria-label={t('system_info.database_management.validation_progress')}
              />
              <p className={styles.hint}>
                {t('system_info.database_management.validation_steps', {
                  completed: validationProgress?.completedSteps ?? 0,
                  total: validationProgress?.totalSteps ?? 0,
                })}
                {validationProgress?.side
                  ? ` · ${validationSideLabels[validationProgress.side] || validationProgress.side}`
                  : ''}
              </p>
              {validationProgress?.currentTable ? (
                <p className={styles.hint}>
                  {t('system_info.database_management.validation_current_table', {
                    table: validationProgress.currentTable,
                  })}
                  {validationProgress.currentTableSinceMs
                    ? ` · ${formatTime(validationProgress.currentTableSinceMs, i18n.language)}`
                    : ''}
                  {validationProgress.totalRows
                    ? ` · ${formatNumber(validationProgress.processedRows, i18n.language, 0)} / ${formatNumber(validationProgress.totalRows, i18n.language, 0)}`
                    : ''}
                </p>
              ) : null}
            </div>
          ) : null}
          {migration?.currentTable ? (
            <p className={styles.hint} aria-live="polite">
              {t(
                migration.currentTableActive
                  ? 'system_info.database_management.migration_current_table_active'
                  : 'system_info.database_management.migration_current_table_pending',
                {
                  table: migration.currentTable,
                  time: formatTime(migration.currentTableSinceMs, i18n.language),
                }
              )}
            </p>
          ) : null}
          <p className={styles.hint}>
            {t('system_info.database_management.migration_rows', {
              copied: formatNumber(migrationCopiedRows, i18n.language, 0),
              total: formatNumber(migrationTotalRows, i18n.language, 0),
              speed: formatNumber(migration?.rowsPerSecond, i18n.language),
            })}
          </p>
          <div className={styles.metricsGrid}>
            <Metric
              label={t('system_info.database_management.migrated_bytes')}
              metric={{ value: migrationCopiedBytes }}
              locale={i18n.language}
              bytes
            />
            <Metric
              label={t('system_info.database_management.requests')}
              metric={{ value: migration?.requests }}
              locale={i18n.language}
            />
            <Metric
              label={t('system_info.database_management.tokens')}
              metric={{ value: migrationTokens }}
              locale={i18n.language}
            />
            <Metric
              label={t('system_info.database_management.eta')}
              metric={{ value: migration?.etaSeconds }}
              locale={i18n.language}
            />
            <Metric
              label={t('system_info.database_management.cost')}
              metric={{ value: migration?.cost }}
              locale={i18n.language}
            />
          </div>
          {migrationTables.length ? (
            <details className={styles.tableProgress}>
              <summary>{t('system_info.database_management.table_progress')}</summary>
              <div>
                {migrationTables.map((table) => (
                  <span key={table.table || table.name}>
                    {table.table || table.name || '-'}:{' '}
                    {formatNumber(table.copiedRows, i18n.language, 0)} /{' '}
                    {formatNumber(table.totalRows, i18n.language, 0)}
                  </span>
                ))}
              </div>
            </details>
          ) : null}
          {migrationDerivedTables.length ? (
            <details className={styles.tableProgress} open={rebuildingDerived}>
              <summary>{t('system_info.database_management.derived_table_progress')}</summary>
              <div>
                {migrationDerivedTables.map((table) => (
                  <span key={table.table || table.name}>
                    {table.active ? '… ' : table.completed ? '✓ ' : '○ '}
                    {table.table || table.name || '-'}
                  </span>
                ))}
              </div>
            </details>
          ) : null}
          {validationValid && !validationInProgress ? (
            <div className={styles.success} role="status">
              {t('system_info.database_management.validation_passed')}
            </div>
          ) : null}
          {validationStale ? (
            <div className={styles.partialWarning} role="alert">
              {t(
                migrationResumable
                  ? 'system_info.database_management.validation_stale_resume_first'
                  : 'system_info.database_management.validation_stale',
                { state: migrationStateLabel }
              )}
            </div>
          ) : null}
          {migration?.validationError || migration?.lastError || migration?.error ? (
            <div className={styles.error} role="alert">
              {migration.validationError || migration.lastError || migration.error}
            </div>
          ) : null}
        </Section>
      </div>

      <Section title={t('system_info.database_management.operations_title')}>
        <div className={styles.actions}>
          <DatabaseActionButton
            tip={tipWithBusyState(
              t('system_info.database_management.tip_reinitialize_mysql_schema')
            )}
            size="sm"
            variant="danger"
            disabled={!canReinitializeMySQLSchema || busyAction !== ''}
            loading={busyAction === 'reinitialize-mysql-schema'}
            onClick={() =>
              confirm(
                'reinitialize-mysql-schema',
                t('system_info.database_management.reinitialize_mysql_schema_confirm', {
                  database: mysql?.database || '-',
                }),
                () =>
                  usageServiceApi.reinitializeMySQLSchema(base, managementKey, {
                    ...mutationControl(),
                    target: 'mysql',
                    confirmDatabase: mysql!.database!,
                    confirmDrop: true,
                  }),
                {
                  danger: true,
                  double: true,
                  secondMessage: t(
                    'system_info.database_management.reinitialize_mysql_schema_confirm_again',
                    { database: mysql?.database || '-' }
                  ),
                }
              )
            }
          >
            {t('system_info.database_management.reinitialize_mysql_schema')}
          </DatabaseActionButton>
          <DatabaseActionButton
            tip={tipWithBusyState(t('system_info.database_management.tip_enable_replication'))}
            size="sm"
            disabled={
              !mysql?.configured || !mysql?.connected || replication?.enabled || busyAction !== ''
            }
            onClick={() =>
              confirm(
                'enable-replication',
                t('system_info.database_management.enable_replication_confirm'),
                () =>
                  usageServiceApi.enableDatabaseReplication(base, managementKey, mutationControl())
              )
            }
          >
            {t('system_info.database_management.enable_replication')}
          </DatabaseActionButton>
          <DatabaseActionButton
            tip={tipWithBusyState(t('system_info.database_management.tip_start_migration'))}
            size="sm"
            disabled={!canStartMigration || busyAction !== ''}
            onClick={() =>
              confirm(
                'start-migration',
                t('system_info.database_management.start_migration_confirm'),
                () => usageServiceApi.startDatabaseMigration(base, managementKey, mutationControl())
              )
            }
          >
            {t('system_info.database_management.start_migration')}
          </DatabaseActionButton>
          <DatabaseActionButton
            tip={tipWithBusyState(t('system_info.database_management.tip_pause'))}
            size="sm"
            variant="secondary"
            disabled={migrationState !== 'running' || !migration?.id || busyAction !== ''}
            onClick={() =>
              void run('pause-migration', () =>
                usageServiceApi.updateDatabaseMigration(
                  base,
                  managementKey,
                  migration!.id!,
                  'pause',
                  mutationControl()
                )
              )
            }
          >
            {t('system_info.database_management.pause')}
          </DatabaseActionButton>
          <DatabaseActionButton
            tip={tipWithBusyState(t('system_info.database_management.tip_resume'))}
            size="sm"
            variant="secondary"
            disabled={!migrationResumable || !migration?.id || busyAction !== ''}
            onClick={() =>
              void run('resume-migration', () =>
                usageServiceApi.updateDatabaseMigration(
                  base,
                  managementKey,
                  migration!.id!,
                  'resume',
                  mutationControl()
                )
              )
            }
          >
            {t('system_info.database_management.resume')}
          </DatabaseActionButton>
          <DatabaseActionButton
            tip={tipWithBusyState(t('system_info.database_management.tip_cancel'))}
            size="sm"
            variant="danger"
            disabled={!migrationCancelable || !migration?.id || busyAction !== ''}
            onClick={() =>
              confirm(
                'cancel-migration',
                t('system_info.database_management.cancel_migration_confirm'),
                () =>
                  usageServiceApi.updateDatabaseMigration(
                    base,
                    managementKey,
                    migration!.id!,
                    'cancel',
                    mutationControl()
                  ),
                { danger: true, double: true }
              )
            }
          >
            {t('system_info.database_management.cancel')}
          </DatabaseActionButton>
          <DatabaseActionButton
            tip={tipWithBusyState(validationTip)}
            size="sm"
            variant="secondary"
            disabled={!canValidate || busyAction !== ''}
            loading={busyAction === 'validate-migration'}
            onClick={() =>
              void run('validate-migration', () =>
                usageServiceApi.updateDatabaseMigration(
                  base,
                  managementKey,
                  migration!.id!,
                  'validate',
                  mutationControl()
                )
              )
            }
          >
            {t('system_info.database_management.validate')}
          </DatabaseActionButton>
          <DatabaseActionButton
            tip={tipWithBusyState(t('system_info.database_management.tip_cutover'))}
            size="sm"
            variant="danger"
            disabled={!canCutover || busyAction !== ''}
            onClick={() =>
              confirm(
                'cutover',
                t('system_info.database_management.cutover_confirm'),
                () =>
                  usageServiceApi.cutoverDatabaseReads(base, managementKey, {
                    ...mutationControl(),
                    target: 'mysql',
                    migrationId: migration!.id!,
                    validationToken: migration!.validationToken!,
                  }),
                { danger: true, double: true }
              )
            }
          >
            {t('system_info.database_management.cutover')}
          </DatabaseActionButton>
          <DatabaseActionButton
            tip={tipWithBusyState(t('system_info.database_management.tip_failover'))}
            size="sm"
            variant="danger"
            disabled={
              !mysql?.connected ||
              !topology?.writeFailoverReady ||
              topology?.writePrimary === 'mysql' ||
              validationInProgress ||
              !migration?.id ||
              !validationValid ||
              !migration.validationToken ||
              busyAction !== ''
            }
            onClick={() =>
              confirm(
                'failover',
                failoverConfirmationMessage,
                () =>
                  usageServiceApi.failoverDatabaseWrites(base, managementKey, {
                    ...mutationControl(),
                    target: 'mysql',
                    migrationId: migration!.id!,
                    validationToken: migration!.validationToken!,
                  }),
                {
                  danger: true,
                  double: true,
                  secondMessage: t('system_info.database_management.failover_confirm_again'),
                }
              )
            }
          >
            {t('system_info.database_management.failover')}
          </DatabaseActionButton>
          <DatabaseActionButton
            tip={tipWithBusyState(t('system_info.database_management.tip_rebuild_cache'))}
            size="sm"
            variant="secondary"
            disabled={
              !mysql?.connected ||
              topology?.businessReadPrimary !== 'mysql' ||
              validationInProgress ||
              !migration?.id ||
              !validationValid ||
              !migration.validationToken ||
              busyAction !== ''
            }
            onClick={() =>
              confirm(
                'rebuild-cache',
                t('system_info.database_management.rebuild_confirm', { days: retentionDays }),
                () =>
                  usageServiceApi.rebuildSQLiteCache(base, managementKey, {
                    ...mutationControl(),
                    target: 'sqlite',
                    migrationId: migration!.id!,
                    validationToken: migration!.validationToken!,
                    retentionDays,
                  }),
                { danger: true, double: true }
              )
            }
          >
            {t('system_info.database_management.rebuild_cache')}
          </DatabaseActionButton>
          <DatabaseActionButton
            tip={tipWithBusyState(t('system_info.database_management.tip_preview_cleanup'))}
            size="sm"
            variant="secondary"
            disabled={
              !validationValid || topology?.businessReadPrimary !== 'mysql' || busyAction !== ''
            }
            onClick={() =>
              void run('preview-cleanup', async () =>
                setCleanupPreview(
                  await usageServiceApi.previewSQLiteCacheCleanup(
                    base,
                    managementKey,
                    mutationControl()
                  )
                )
              )
            }
          >
            {t('system_info.database_management.preview_cleanup')}
          </DatabaseActionButton>
          <DatabaseActionButton
            tip={tipWithBusyState(t('system_info.database_management.tip_cleanup_cache'))}
            size="sm"
            variant="danger"
            disabled={
              !cleanupPreview?.eligible ||
              !migration?.id ||
              validationInProgress ||
              !validationValid ||
              !migration.validationToken ||
              busyAction !== ''
            }
            onClick={() =>
              confirm(
                'cleanup-cache',
                t('system_info.database_management.cleanup_confirm'),
                () =>
                  usageServiceApi.cleanupSQLiteCache(base, managementKey, {
                    ...mutationControl(),
                    target: 'sqlite',
                    migrationId: migration!.id!,
                    validationToken: migration!.validationToken!,
                  }),
                { danger: true, double: true }
              )
            }
          >
            {t('system_info.database_management.cleanup_cache')}
          </DatabaseActionButton>
        </div>
        <p className={styles.hint}>
          {t('system_info.database_management.reinitialize_mysql_schema_hint')}
        </p>
      </Section>
      <DatabaseMigrationHistoryModal
        open={migrationHistoryOpen}
        history={migrationHistory}
        loading={migrationHistoryLoading}
        error={migrationHistoryError}
        onRefresh={() => void loadMigrationHistory(true)}
        onClose={() => setMigrationHistoryOpen(false)}
      />
    </Card>
  );
}
