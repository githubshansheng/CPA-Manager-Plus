import { useTranslation } from 'react-i18next';
import { Modal } from '@/components/ui/Modal';
import type {
  DatabaseMigrationHistoryRecord,
  DatabaseMigrationHistoryResponse,
} from '@/services/api/usageService';
import { formatFileSize } from '@/utils/format';
import { DatabaseActionButton } from './DatabaseActionButton';
import styles from './DatabaseManagementPanel.module.scss';

interface DatabaseMigrationHistoryModalProps {
  open: boolean;
  history: DatabaseMigrationHistoryResponse | null;
  loading: boolean;
  error: string;
  onRefresh: () => void;
  onClose: () => void;
}

const formatTime = (value: number | undefined, locale: string) =>
  Number.isFinite(value) && Number(value) > 0
    ? new Date(Number(value)).toLocaleString(locale)
    : '-';

const formatNumber = (value: number | undefined, locale: string) =>
  Number.isFinite(value) ? Number(value).toLocaleString(locale) : '-';

function TaskRecord({
  record,
  active,
}: {
  record: DatabaseMigrationHistoryRecord;
  active: boolean;
}) {
  const { t, i18n } = useTranslation();
  const errors = record.events.filter((event) => Boolean(event.error));
  const awaitingCutover = record.phase === 'ready_to_cutover' && record.status === 'running';
  const statusLabel = awaitingCutover
    ? t('system_info.database_management.migration_state_awaiting_admin')
    : t(`system_info.database_management.migration_state_${record.status}`, {
        defaultValue: record.status,
      });
  return (
    <article className={styles.historyTask}>
      <header className={styles.historyTaskHeader}>
        <div>
          <code>{record.id}</code>
          {active ? (
            <span className={styles.activeBadge}>
              {t('system_info.database_management.history_active')}
            </span>
          ) : null}
        </div>
        <strong data-status={record.status}>{statusLabel}</strong>
      </header>
      <div className={styles.historySummary}>
        <span>
          {t('system_info.database_management.history_route', {
            source: record.source,
            target: record.target,
          })}
        </span>
        <span>
          {t('system_info.database_management.history_phase', {
            phase: t(`system_info.database_management.migration_phase_${record.phase}`, {
              defaultValue: record.phase,
            }),
          })}
        </span>
        <span>
          {t('system_info.database_management.history_updated', {
            time: formatTime(record.updatedAtMs, i18n.language),
          })}
        </span>
      </div>
      <div className={styles.historyProgressHeader}>
        <span>
          {t('system_info.database_management.history_rows', {
            copied: formatNumber(record.copiedRows, i18n.language),
            total: formatNumber(record.totalRows, i18n.language),
          })}
        </span>
        <strong>{Math.max(0, Math.min(100, record.progressPercent || 0)).toFixed(1)}%</strong>
      </div>
      <progress max={100} value={Math.max(0, Math.min(100, record.progressPercent || 0))} />
      <div className={styles.historyMetrics}>
        <span>
          {t('system_info.database_management.history_tables', {
            completed: record.completedTables,
            total: record.totalTables,
          })}
        </span>
        <span>
          {t('system_info.database_management.migrated_bytes')}:{' '}
          {formatFileSize(record.copiedBytes)}
        </span>
        <span>
          {t('system_info.database_management.requests')}:{' '}
          {formatNumber(record.requests, i18n.language)}
        </span>
      </div>
      {record.lastError ? (
        <div className={styles.error} role="alert">
          <strong>{t('system_info.database_management.history_latest_error')}</strong>
          <span>{record.lastError}</span>
        </div>
      ) : null}
      {errors.length > 0 ? (
        <details className={styles.historyDetails} open={record.status === 'failed'}>
          <summary>
            {t('system_info.database_management.history_errors', { count: errors.length })}
          </summary>
          <div className={styles.historyEventList}>
            {errors.map((event) => (
              <div className={styles.historyErrorEvent} key={event.id} role="alert">
                <time>{formatTime(event.createdAtMs, i18n.language)}</time>
                <span>{event.table || event.phase || '-'}</span>
                <code>{event.error}</code>
              </div>
            ))}
          </div>
        </details>
      ) : null}
      <details className={styles.historyDetails}>
        <summary>
          {t('system_info.database_management.history_table_details', {
            count: record.tables.length,
          })}
        </summary>
        <div className={styles.historyTableList}>
          {record.tables.length > 0 ? (
            record.tables.map((table) => (
              <div key={`${record.id}:${table.name}`}>
                <span>{table.completed ? '✓' : '○'}</span>
                <code>{table.name}</code>
                <span>{table.stage}</span>
                <span>
                  {formatNumber(table.rowsCopied, i18n.language)}
                  {table.totalRows ? ` / ${formatNumber(table.totalRows, i18n.language)}` : ''}
                </span>
              </div>
            ))
          ) : (
            <p>{t('system_info.database_management.history_no_table_records')}</p>
          )}
        </div>
      </details>
      <details className={styles.historyDetails}>
        <summary>
          {t('system_info.database_management.history_timeline', { count: record.events.length })}
        </summary>
        <ol className={styles.historyTimeline}>
          {record.events.map((event) => (
            <li key={event.id}>
              <time>{formatTime(event.createdAtMs, i18n.language)}</time>
              <span>
                {t(`system_info.database_management.migration_event_${event.eventType}`, {
                  defaultValue: event.eventType,
                })}
              </span>
              <code>
                {event.phase} / {event.status}
                {event.table ? ` / ${event.table}` : ''}
              </code>
            </li>
          ))}
        </ol>
      </details>
    </article>
  );
}

export function DatabaseMigrationHistoryModal({
  open,
  history,
  loading,
  error,
  onRefresh,
  onClose,
}: DatabaseMigrationHistoryModalProps) {
  const { t } = useTranslation();
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t('system_info.database_management.history_title')}
      width={920}
      className={styles.historyModal}
      footer={
        <div className={styles.historyFooter}>
          <DatabaseActionButton
            tip={t('system_info.database_management.tip_history_refresh')}
            variant="secondary"
            size="sm"
            onClick={onRefresh}
            loading={loading}
          >
            {t('common.refresh')}
          </DatabaseActionButton>
          <DatabaseActionButton
            tip={t('system_info.database_management.tip_history_close')}
            variant="primary"
            size="sm"
            onClick={onClose}
          >
            {t('common.close')}
          </DatabaseActionButton>
        </div>
      }
    >
      <div className={styles.historyBody} aria-live="polite">
        {error ? (
          <div className={styles.error} role="alert">
            {error}
          </div>
        ) : null}
        {loading && !history ? <p>{t('system_info.database_management.history_loading')}</p> : null}
        {history && history.migrations.length === 0 ? (
          <p>{t('system_info.database_management.history_empty')}</p>
        ) : null}
        {history?.migrations.map((record) => (
          <TaskRecord
            key={record.id}
            record={record}
            active={record.id === history.activeMigrationId}
          />
        ))}
      </div>
    </Modal>
  );
}
