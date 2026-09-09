import { useEffect, useId, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { SelectionCheckbox } from '@/components/ui/SelectionCheckbox';
import { IconEye, IconEyeOff, IconTriangleAlert } from '@/components/ui/icons';
import {
  usageServiceApi,
  type SQLiteAdoptionPreflightResult,
  type UsageServiceApiError,
  type UsageServiceStatus,
} from '@/services/api/usageService';
import { formatFileSize } from '@/utils/format';
import { DatabaseActionButton } from './DatabaseActionButton';
import styles from './SQLiteSourceSwitcher.module.scss';

interface SQLiteSourceSwitcherProps {
  status: UsageServiceStatus;
  base: string;
  managementKey: string;
  loading: boolean;
  onRefresh: () => Promise<void>;
  onRestartRecovered?: (managementKey: string) => Promise<void>;
}

interface DetailedError {
  summary: string;
  code: string;
  stage: string;
  sourcePath: string;
  cause: string;
}

const RESTART_TIMEOUT_MS = 90_000;
const RESTART_POLL_INTERVAL_MS = 750;

const isRecord = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value);

const readString = (value: unknown) => (typeof value === 'string' ? value.trim() : '');

const readSQLiteSourceError = (error: unknown): DetailedError => {
  const apiError = error as UsageServiceApiError;
  const response = isRecord(apiError?.details) ? apiError.details : {};
  const details = isRecord(response.details) ? response.details : response;

  return {
    summary: error instanceof Error ? error.message : String(error),
    code: readString(apiError?.code) || readString(response.code),
    stage: readString(details.stage),
    sourcePath: readString(details.sourcePath),
    cause: readString(details.cause),
  };
};

const makeIdempotencyKey = () => {
  if (typeof crypto !== 'undefined' && 'randomUUID' in crypto) return crypto.randomUUID();
  return `sqlite-switch-${Date.now()}-${Math.random().toString(16).slice(2)}`;
};

const wait = (milliseconds: number) =>
  new Promise<void>((resolve) => globalThis.setTimeout(resolve, milliseconds));

const INACTIVE_MAINTENANCE_STATES = new Set([
  'idle',
  'disabled',
  'succeeded',
  'failed',
  'canceled',
  'cancelled',
  'completed',
]);

export function SQLiteSourceSwitcher({
  status,
  base,
  managementKey,
  loading,
  onRefresh,
  onRestartRecovered,
}: SQLiteSourceSwitcherProps) {
  const { t } = useTranslation();
  const sqliteSource = status.sqliteSource;
  const topology = status.databaseTopology;
  const [open, setOpen] = useState(false);
  const [sourcePath, setSourcePath] = useState('');
  const [dataKeyPath, setDataKeyPath] = useState('');
  const [sourceAdminKey, setSourceAdminKey] = useState('');
  const [showSourceAdminKey, setShowSourceAdminKey] = useState(false);
  const [preflight, setPreflight] = useState<SQLiteAdoptionPreflightResult | null>(null);
  const [sourceStoppedConfirmed, setSourceStoppedConfirmed] = useState(false);
  const [operationKey, setOperationKey] = useState(makeIdempotencyKey);
  const [busy, setBusy] = useState<'preflight' | 'switch' | 'restart' | ''>('');
  const [error, setError] = useState<DetailedError | null>(null);
  const [restartMessage, setRestartMessage] = useState('');
  const [localPendingPath, setLocalPendingPath] = useState('');
  const [pendingManagementKey, setPendingManagementKey] = useState('');
  const mountedRef = useRef(true);
  const confirmationDescriptionId = useId();

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  useEffect(() => {
    if (!sqliteSource?.restartRequired) setLocalPendingPath('');
  }, [sqliteSource?.restartRequired]);

  const generation = topology?.generation ?? 0;
  const safetyBlockers = useMemo(() => {
    const blockers: string[] = [];
    if (!Number.isSafeInteger(generation) || generation <= 0) {
      blockers.push(t('system_info.database_management.sqlite_switch_block_generation'));
    }
    if (
      topology?.writePrimary !== 'sqlite' ||
      topology?.businessReadPrimary !== 'sqlite' ||
      topology?.systemReadPrimary !== 'sqlite'
    ) {
      blockers.push(t('system_info.database_management.sqlite_switch_block_routing'));
    }
    if (status.replication?.enabled) {
      blockers.push(t('system_info.database_management.sqlite_switch_block_replication'));
    }
    if (status.databases?.mysql?.configured) {
      blockers.push(t('system_info.database_management.sqlite_switch_block_mysql'));
    }
    if (status.databaseMigration?.id) {
      blockers.push(t('system_info.database_management.sqlite_switch_block_migration'));
    }
    if (topology?.failoverState?.toLowerCase() === 'switching') {
      blockers.push(t('system_info.database_management.sqlite_switch_block_failover'));
    }
    const maintenanceStates = [
      status.databases?.sqlite?.cleanupStatus,
      status.databases?.sqlite?.rebuildStatus,
      status.cacheCoverage?.cleanupState,
    ]
      .map((value) =>
        String(value ?? '')
          .trim()
          .toLowerCase()
      )
      .filter(Boolean);
    if (maintenanceStates.some((value) => !INACTIVE_MAINTENANCE_STATES.has(value))) {
      blockers.push(t('system_info.database_management.sqlite_switch_block_maintenance'));
    }
    return blockers;
  }, [generation, status, t, topology]);

  const restartRequired = Boolean(sqliteSource?.restartRequired || localPendingPath);
  const pendingPath = sqliteSource?.pendingPath || localPendingPath;
  const currentPath = sqliteSource?.currentPath || status.dbPath || '-';
  const hasRequiredParameters = Boolean(
    (!preflight?.dataKeyRequired || dataKeyPath.trim()) &&
    (!preflight?.adminKeyRequired || sourceAdminKey.trim())
  );
  const switchReady = Boolean(preflight?.ready && hasRequiredParameters);

  const invalidatePreflight = () => {
    setPreflight(null);
    setSourceStoppedConfirmed(false);
    setError(null);
    setOperationKey(makeIdempotencyKey());
  };

  const closeModal = () => {
    if (busy) return;
    setOpen(false);
    setPreflight(null);
    setSourceStoppedConfirmed(false);
    setError(null);
  };

  const handlePreflight = async () => {
    if (!sourcePath.trim() || safetyBlockers.length > 0) return;
    setBusy('preflight');
    setError(null);
    setSourceStoppedConfirmed(false);
    try {
      const result = await usageServiceApi.preflightSQLiteSourceSwitch(base, managementKey, {
        sourcePath: sourcePath.trim(),
        dataKeyPath: dataKeyPath.trim() || undefined,
        sourceAdminKey: sourceAdminKey.trim() || undefined,
        expectedGeneration: generation,
        idempotencyKey: operationKey,
      });
      if (!mountedRef.current) return;
      setPreflight(result);
    } catch (requestError) {
      if (!mountedRef.current) return;
      setPreflight(null);
      setError(readSQLiteSourceError(requestError));
    } finally {
      if (mountedRef.current) setBusy('');
    }
  };

  const handleSwitch = async () => {
    if (!switchReady || !sourceStoppedConfirmed) return;
    setBusy('switch');
    setError(null);
    try {
      const result = await usageServiceApi.switchSQLiteSource(base, managementKey, {
        sourcePath: sourcePath.trim(),
        dataKeyPath: dataKeyPath.trim() || undefined,
        sourceAdminKey: sourceAdminKey.trim() || undefined,
        confirmSourceStopped: true,
        expectedGeneration: generation,
        idempotencyKey: operationKey,
      });
      if (!mountedRef.current) return;
      setLocalPendingPath(result.sourcePath);
      setPendingManagementKey(preflight?.adminKeyRequired ? sourceAdminKey.trim() : managementKey);
      setRestartMessage(t('system_info.database_management.sqlite_switch_saved'));
      setOpen(false);
      setPreflight(null);
      setSourceStoppedConfirmed(false);
      try {
        await onRefresh();
      } catch (refreshError) {
        if (!mountedRef.current) return;
        const refreshDetails = readSQLiteSourceError(refreshError);
        setError({
          ...refreshDetails,
          summary: t('system_info.database_management.sqlite_switch_saved_refresh_failed'),
          sourcePath: refreshDetails.sourcePath || result.sourcePath,
          cause: refreshDetails.cause || refreshDetails.summary,
        });
      }
    } catch (requestError) {
      if (!mountedRef.current) return;
      setError(readSQLiteSourceError(requestError));
    } finally {
      if (mountedRef.current) setBusy('');
    }
  };

  const handleRestart = async () => {
    setBusy('restart');
    setError(null);
    setRestartMessage(t('system_info.database_management.sqlite_restart_requesting'));
    try {
      const before = await usageServiceApi.getInfo(base);
      const restart = await usageServiceApi.restartManagerServer(base, managementKey);
      const previousStartedAt = before.startedAt ?? restart.startedAt;
      const deadline = Date.now() + RESTART_TIMEOUT_MS;
      setRestartMessage(t('system_info.database_management.sqlite_restart_waiting'));

      let recovered = false;
      while (Date.now() < deadline && mountedRef.current) {
        await wait(RESTART_POLL_INTERVAL_MS);
        try {
          const info = await usageServiceApi.getInfo(base);
          if (info.startedAt && info.startedAt !== previousStartedAt) {
            recovered = true;
            break;
          }
        } catch {
          // A short connection failure is expected while graceful restart is in progress.
        }
      }
      if (!mountedRef.current) return;
      if (!recovered) {
        throw new Error(t('system_info.database_management.sqlite_restart_timeout'));
      }

      const nextManagementKey = pendingManagementKey || managementKey;
      if (onRestartRecovered) await onRestartRecovered(nextManagementKey || managementKey);
      setSourceAdminKey('');
      setPendingManagementKey('');
      setRestartMessage(t('system_info.database_management.sqlite_restart_recovered'));
      await onRefresh();
    } catch (requestError) {
      if (!mountedRef.current) return;
      setError(readSQLiteSourceError(requestError));
      setRestartMessage('');
    } finally {
      if (mountedRef.current) setBusy('');
    }
  };

  const missingParameters = preflight?.requiredParameters ?? [];
  const renderError = (details: DetailedError) => (
    <div className={styles.error} role="alert" aria-live="assertive">
      <strong>{details.summary}</strong>
      {details.code ? (
        <span>
          {t('system_info.database_management.sqlite_switch_error_code')}: {details.code}
        </span>
      ) : null}
      {details.stage ? (
        <span>
          {t('system_info.database_management.sqlite_switch_error_stage')}: {details.stage}
        </span>
      ) : null}
      {details.sourcePath ? (
        <span>
          {t('system_info.database_management.sqlite_switch_error_path')}: {details.sourcePath}
        </span>
      ) : null}
      {details.cause && details.cause !== details.summary ? (
        <span>
          {t('system_info.database_management.sqlite_switch_error_cause')}: {details.cause}
        </span>
      ) : null}
    </div>
  );

  return (
    <section className={styles.root} aria-labelledby={`${confirmationDescriptionId}-title`}>
      <div className={styles.header}>
        <div>
          <h3 id={`${confirmationDescriptionId}-title`}>
            {t('system_info.database_management.sqlite_switch_title')}
          </h3>
          <p>{t('system_info.database_management.sqlite_switch_description')}</p>
        </div>
        <DatabaseActionButton
          tip={
            safetyBlockers.length > 0
              ? `${t('system_info.database_management.tip_sqlite_source_switch')} ${safetyBlockers.join(' ')}`
              : t('system_info.database_management.tip_sqlite_source_switch')
          }
          variant="secondary"
          size="sm"
          disabled={loading || busy !== '' || safetyBlockers.length > 0}
          onClick={() => {
            setOpen(true);
            setError(null);
            setRestartMessage('');
          }}
        >
          {t('system_info.database_management.sqlite_switch_button')}
        </DatabaseActionButton>
      </div>

      <dl className={styles.pathList}>
        <div>
          <dt>{t('system_info.database_management.sqlite_switch_current_path')}</dt>
          <dd>{currentPath}</dd>
        </div>
        {restartRequired ? (
          <div>
            <dt>{t('system_info.database_management.sqlite_switch_pending_path')}</dt>
            <dd>{pendingPath || '-'}</dd>
          </div>
        ) : null}
      </dl>

      {safetyBlockers.length > 0 ? (
        <div className={styles.safetyNotice} role="note">
          <strong>{t('system_info.database_management.sqlite_switch_unavailable')}</strong>
          <ul>
            {safetyBlockers.map((blocker) => (
              <li key={blocker}>{blocker}</li>
            ))}
          </ul>
        </div>
      ) : (
        <p className={styles.safetyHint}>
          {t('system_info.database_management.sqlite_switch_safe_topology')}
        </p>
      )}

      {sqliteSource?.lastError ? (
        <div className={styles.error} role="alert">
          <strong>{t('system_info.database_management.sqlite_switch_last_error')}</strong>
          <span>{sqliteSource.lastError}</span>
          {sqliteSource.lastErrorStage ? (
            <span>
              {t('system_info.database_management.sqlite_switch_error_stage')}:{' '}
              {sqliteSource.lastErrorStage}
            </span>
          ) : null}
          {sqliteSource.failedPath ? (
            <span>
              {t('system_info.database_management.sqlite_switch_error_path')}:{' '}
              {sqliteSource.failedPath}
            </span>
          ) : null}
          {sqliteSource.lastErrorCause && sqliteSource.lastErrorCause !== sqliteSource.lastError ? (
            <span>
              {t('system_info.database_management.sqlite_switch_error_cause')}:{' '}
              {sqliteSource.lastErrorCause}
            </span>
          ) : null}
        </div>
      ) : null}

      {restartRequired ? (
        <div className={styles.pending} aria-live="polite">
          <div>
            <strong>{t('system_info.database_management.sqlite_switch_restart_required')}</strong>
            <span>{t('system_info.database_management.sqlite_switch_restart_hint')}</span>
          </div>
          <DatabaseActionButton
            tip={t('system_info.database_management.tip_sqlite_restart')}
            loading={busy === 'restart'}
            disabled={busy !== '' && busy !== 'restart'}
            onClick={() => void handleRestart()}
          >
            {busy === 'restart'
              ? t('system_info.database_management.sqlite_restart_waiting_button')
              : t('system_info.database_management.sqlite_restart_button')}
          </DatabaseActionButton>
        </div>
      ) : null}

      {restartMessage ? (
        <div className={styles.status} role="status" aria-live="polite">
          {restartMessage}
        </div>
      ) : null}
      {!open && error ? renderError(error) : null}

      <Modal
        open={open}
        title={t('system_info.database_management.sqlite_switch_modal_title')}
        width={680}
        closeDisabled={busy !== ''}
        onClose={closeModal}
        footer={
          <>
            <Button variant="secondary" disabled={busy !== ''} onClick={closeModal}>
              {t('common.cancel')}
            </Button>
            {switchReady ? (
              <Button
                loading={busy === 'switch'}
                disabled={!sourceStoppedConfirmed || busy !== ''}
                onClick={() => void handleSwitch()}
              >
                {t('system_info.database_management.sqlite_switch_confirm_button')}
              </Button>
            ) : (
              <Button
                loading={busy === 'preflight'}
                disabled={!sourcePath.trim() || busy !== '' || safetyBlockers.length > 0}
                onClick={() => void handlePreflight()}
              >
                {t('system_info.database_management.sqlite_switch_preflight_button')}
              </Button>
            )}
          </>
        }
      >
        <div className={styles.modalBody}>
          <div className={styles.warning} role="alert">
            <IconTriangleAlert size={22} />
            <div>
              <strong>
                {t('system_info.database_management.sqlite_switch_lock_warning_title')}
              </strong>
              <p>{t('system_info.database_management.sqlite_switch_lock_warning')}</p>
            </div>
          </div>

          <Input
            autoFocus
            label={t('system_info.database_management.sqlite_switch_source_path_label')}
            hint={t('system_info.database_management.sqlite_switch_source_path_hint')}
            placeholder={t('system_info.database_management.sqlite_switch_source_path_placeholder')}
            value={sourcePath}
            autoComplete="off"
            disabled={busy !== ''}
            onChange={(event) => {
              setSourcePath(event.target.value);
              invalidatePreflight();
            }}
          />
          <Input
            label={t('system_info.database_management.sqlite_switch_data_key_label')}
            hint={
              preflight?.dataKeyRequired
                ? t('system_info.database_management.sqlite_switch_data_key_required')
                : t('system_info.database_management.sqlite_switch_data_key_hint')
            }
            placeholder={t('system_info.database_management.sqlite_switch_data_key_placeholder')}
            value={dataKeyPath}
            autoComplete="off"
            disabled={busy !== ''}
            error={
              preflight?.dataKeyRequired && !dataKeyPath.trim()
                ? t('system_info.database_management.sqlite_switch_data_key_missing')
                : undefined
            }
            onChange={(event) => {
              setDataKeyPath(event.target.value);
              invalidatePreflight();
            }}
          />
          <Input
            label={t('system_info.database_management.sqlite_switch_admin_key_label')}
            hint={
              preflight?.adminKeyRequired
                ? t('system_info.database_management.sqlite_switch_admin_key_required')
                : t('system_info.database_management.sqlite_switch_admin_key_hint')
            }
            placeholder={t('system_info.database_management.sqlite_switch_admin_key_placeholder')}
            type={showSourceAdminKey ? 'text' : 'password'}
            value={sourceAdminKey}
            autoComplete="off"
            disabled={busy !== ''}
            error={
              preflight?.adminKeyRequired && !sourceAdminKey.trim()
                ? t('system_info.database_management.sqlite_switch_admin_key_missing')
                : undefined
            }
            rightElement={
              <Button
                type="button"
                variant="ghost"
                size="xs"
                iconOnly
                aria-label={t(showSourceAdminKey ? 'common.hide' : 'common.show')}
                onClick={() => setShowSourceAdminKey((value) => !value)}
              >
                {showSourceAdminKey ? <IconEyeOff size={16} /> : <IconEye size={16} />}
              </Button>
            }
            onChange={(event) => {
              setSourceAdminKey(event.target.value);
              invalidatePreflight();
            }}
          />

          {missingParameters.length > 0 ? (
            <div className={styles.requiredParameters} role="status" aria-live="polite">
              <strong>
                {t('system_info.database_management.sqlite_switch_parameters_required')}
              </strong>
              <ul>
                {missingParameters.map((parameter) => (
                  <li key={parameter}>
                    {t(
                      parameter === 'dataKeyPath'
                        ? 'system_info.database_management.sqlite_switch_data_key_label'
                        : parameter === 'sourceAdminKey'
                          ? 'system_info.database_management.sqlite_switch_admin_key_label'
                          : 'system_info.database_management.sqlite_switch_unknown_parameter',
                      { parameter }
                    )}
                  </li>
                ))}
              </ul>
            </div>
          ) : null}

          {preflight?.ready ? (
            <div className={styles.preflight} aria-live="polite">
              <strong>{t('system_info.database_management.sqlite_switch_preflight_passed')}</strong>
              <dl>
                <div>
                  <dt>{t('system_info.database_management.sqlite_switch_database_size')}</dt>
                  <dd>{formatFileSize(preflight.databaseBytes)}</dd>
                </div>
                <div>
                  <dt>{t('system_info.database_management.sqlite_switch_wal_size')}</dt>
                  <dd>{formatFileSize(preflight.walBytes ?? 0)}</dd>
                </div>
                <div>
                  <dt>{t('system_info.database_management.sqlite_switch_shm_size')}</dt>
                  <dd>{formatFileSize(preflight.shmBytes ?? 0)}</dd>
                </div>
                <div>
                  <dt>{t('system_info.database_management.sqlite_switch_history_status')}</dt>
                  <dd>
                    {t(
                      preflight.hasHistoricalData
                        ? 'system_info.database_management.sqlite_switch_history_present'
                        : 'system_info.database_management.sqlite_switch_history_empty'
                    )}
                  </dd>
                </div>
                <div>
                  <dt>{t('system_info.database_management.sqlite_switch_data_key_status')}</dt>
                  <dd>
                    {t(
                      preflight.dataKeyRequired
                        ? 'system_info.database_management.sqlite_switch_parameter_verified'
                        : 'system_info.database_management.sqlite_switch_parameter_not_required'
                    )}
                  </dd>
                </div>
                <div>
                  <dt>{t('system_info.database_management.sqlite_switch_admin_key_status')}</dt>
                  <dd>
                    {t(
                      preflight.adminKeyRequired
                        ? 'system_info.database_management.sqlite_switch_parameter_verified'
                        : preflight.adminKeyWillBeRetained
                          ? 'system_info.database_management.sqlite_switch_admin_key_retained'
                          : 'system_info.database_management.sqlite_switch_parameter_not_required'
                    )}
                  </dd>
                </div>
              </dl>
            </div>
          ) : null}

          {switchReady ? (
            <div className={styles.confirmation}>
              <p id={confirmationDescriptionId}>
                {t('system_info.database_management.sqlite_switch_confirmation_description')}
              </p>
              <SelectionCheckbox
                checked={sourceStoppedConfirmed}
                disabled={busy !== ''}
                onChange={setSourceStoppedConfirmed}
                ariaLabel={t(
                  'system_info.database_management.sqlite_switch_source_stopped_confirmation'
                )}
                label={t(
                  'system_info.database_management.sqlite_switch_source_stopped_confirmation'
                )}
              />
            </div>
          ) : null}

          {error ? renderError(error) : null}
        </div>
      </Modal>
    </section>
  );
}
