import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Navigate, useLocation, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { SelectionCheckbox } from '@/components/ui/SelectionCheckbox';
import {
  IconCheck,
  IconDatabaseZap,
  IconEye,
  IconEyeOff,
  IconInfo,
  IconKey,
  IconLanguages,
  IconMoon,
  IconShield,
  IconSun,
  IconTimer,
  IconTriangleAlert,
} from '@/components/ui/icons';
import {
  useAuthStore,
  useLanguageStore,
  useNotificationStore,
  useThemeStore,
  useUsageServiceStore,
} from '@/stores';
import {
  LEGACY_USAGE_SERVICE_LAST_CPA_BASE_KEY,
  USAGE_SERVICE_LAST_CPA_BASE_KEY,
  getUsageServiceErrorCode,
  usageServiceApi,
  type SQLiteAdoptionPreflightResult,
  type SQLiteAdoptionResult,
} from '@/services/api/usageService';
import {
  detectApiBaseFromLocation,
  normalizeApiBase,
  resolveDefaultCPAConnectionBase,
} from '@/utils/connection';
import { LANGUAGE_LABEL_KEYS, LANGUAGE_ORDER } from '@/utils/constants';
import { isSupportedLanguage } from '@/utils/language';
import {
  CPAMP_HORIZONTAL_LOGO_ON_DARK_PNG_SRC_SET,
  CPAMP_HORIZONTAL_LOGO_ON_DARK_PNG_URL,
  CPAMP_HORIZONTAL_LOGO_PNG_SRC_SET,
  CPAMP_HORIZONTAL_LOGO_PNG_URL,
  CPAMP_VERTICAL_LOGO_ON_DARK_URL,
  CPAMP_VERTICAL_LOGO_ON_DARK_SRC_SET,
  CPAMP_VERTICAL_LOGO_SRC_SET,
  CPAMP_VERTICAL_LOGO_URL,
} from '@/assets/brand';
import type { ApiError, LoginResult } from '@/types';
import { resolveUsageServiceLoginMode } from './loginMode';
import styles from './LoginPage.module.scss';

type RedirectState = { from?: { pathname?: string; search?: string; hash?: string } };
type InitializationMode = 'new' | 'adopt';
type UsageSetupStep =
  | 'mode'
  | 'admin'
  | 'sqliteSource'
  | 'connection'
  | 'cpaKey'
  | 'monitoring'
  | 'polling'
  | 'review';
const CONFIG_TAB_STORAGE_KEY = 'config-management:tab';

function resolveRedirectPath(state: unknown): string {
  const from = (state as RedirectState | null)?.from;
  const pathname = typeof from?.pathname === 'string' ? from.pathname : '';
  if (!pathname || pathname === '/login' || !pathname.startsWith('/')) return '/';

  const search = typeof from?.search === 'string' ? from.search : '';
  const hash = typeof from?.hash === 'string' ? from.hash : '';
  return `${pathname}${search}${hash}`;
}

function resolvePostLoginPath(result: LoginResult, fallback: string): string {
  if (result.recoveryMode === 'database_recovery') return '/system';
  if (result.recoveryMode === 'manager_config') return '/config';
  return fallback;
}

function getLocalizedErrorMessage(
  error: unknown,
  t: (key: string, options?: Record<string, unknown>) => string
): string {
  const usageServiceCode = getUsageServiceErrorCode(error);
  if (usageServiceCode) {
    return t(`usage_service_errors.${usageServiceCode}`, {
      defaultValue: t('usage_service_errors.request_failed'),
    });
  }

  const apiError = error as Partial<ApiError>;
  const status = typeof apiError.status === 'number' ? apiError.status : undefined;
  const code = typeof apiError.code === 'string' ? apiError.code : undefined;
  const message =
    error instanceof Error
      ? error.message
      : typeof apiError.message === 'string'
        ? apiError.message
        : typeof error === 'string'
          ? error
          : '';

  const withHttpStatus = (summary: string) => {
    if (!status) return summary;

    const genericAxiosMessage = `Request failed with status code ${status}`;
    const detail = message.trim();
    const backendDetail =
      detail && detail !== genericAxiosMessage
        ? ` (${t('login.error_backend_detail')}: ${detail})`
        : '';

    return `HTTP ${status}: ${summary}${backendDetail}`;
  };

  if (status === 401) return withHttpStatus(t('login.error_unauthorized'));
  if (status === 403) return withHttpStatus(t('login.error_forbidden'));
  if (status === 404) return withHttpStatus(t('login.error_not_found'));
  if (status && status >= 500) return withHttpStatus(t('login.error_server'));
  if (code === 'ECONNABORTED' || message.toLowerCase().includes('timeout')) {
    return t('login.error_timeout');
  }
  if (code === 'ERR_NETWORK' || message.toLowerCase().includes('network error')) {
    return t('login.error_network');
  }
  if (code === 'ERR_CERT_AUTHORITY_INVALID' || message.toLowerCase().includes('certificate')) {
    return t('login.error_ssl');
  }
  if (message.toLowerCase().includes('cors') || message.toLowerCase().includes('cross-origin')) {
    return t('login.error_cors');
  }

  return withHttpStatus(t('login.error_invalid'));
}

function getDetailedSQLiteAdoptionErrorMessage(
  error: unknown,
  t: (key: string, options?: Record<string, unknown>) => string
): string {
  const summary = getLocalizedErrorMessage(error, t);
  const detail = error instanceof Error ? error.message.trim() : '';
  if (!detail || detail === summary || summary.includes(detail)) return summary;
  return `${summary}\n${t('login.error_backend_detail')}: ${detail}`;
}

function formatFileSize(value: number, locale: string): string {
  if (!Number.isFinite(value) || value <= 0) return '0 B';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  const unitIndex = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1);
  const amount = value / 1024 ** unitIndex;
  return `${new Intl.NumberFormat(locale, {
    maximumFractionDigits: unitIndex === 0 ? 0 : 2,
  }).format(amount)} ${units[unitIndex]}`;
}

export function LoginPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const { showNotification } = useNotificationStore();
  const language = useLanguageStore((state) => state.language);
  const setLanguage = useLanguageStore((state) => state.setLanguage);
  const theme = useThemeStore((state) => state.theme);
  const cycleTheme = useThemeStore((state) => state.cycleTheme);
  const isAuthenticated = useAuthStore((state) => state.isAuthenticated);
  const login = useAuthStore((state) => state.login);
  const restoreSession = useAuthStore((state) => state.restoreSession);
  const storedBase = useAuthStore((state) => state.apiBase);
  const storedKey = useAuthStore((state) => state.managementKey);
  const storedRememberPassword = useAuthStore((state) => state.rememberPassword);
  const setUsageServiceConfig = useUsageServiceStore((state) => state.setUsageServiceConfig);
  const languageMenuRef = useRef<HTMLDivElement | null>(null);

  const [apiBase, setApiBase] = useState('');
  const [adminKey, setAdminKey] = useState('');
  const [cpaManagementKey, setCPAManagementKey] = useState('');
  const [showCustomBase, setShowCustomBase] = useState(false);
  const [showAdminKey, setShowAdminKey] = useState(false);
  const [showCPAManagementKey, setShowCPAManagementKey] = useState(false);
  const [rememberCredential, setRememberCredential] = useState(false);
  const [requestMonitoringEnabled, setRequestMonitoringEnabled] = useState(true);
  const [pollIntervalMs, setPollIntervalMs] = useState('500');
  const [loading, setLoading] = useState(false);
  const [autoLoading, setAutoLoading] = useState(true);
  const [autoLoginSuccess, setAutoLoginSuccess] = useState(false);
  const [error, setError] = useState('');
  const [hostedByUsageService, setHostedByUsageService] = useState(false);
  const [usageServiceNeedsSetup, setUsageServiceNeedsSetup] = useState(false);
  const [languageMenuOpen, setLanguageMenuOpen] = useState(false);
  const [hasHistoricalData, setHasHistoricalData] = useState(false);
  const [migrationStatus, setMigrationStatus] = useState('');
  const [initializationMode, setInitializationMode] = useState<InitializationMode>('new');
  const [usageSetupStep, setUsageSetupStep] = useState<UsageSetupStep>('mode');
  const [sqliteSourcePath, setSQLiteSourcePath] = useState('');
  const [sqliteDataKeyPath, setSQLiteDataKeyPath] = useState('');
  const [sqliteSourceAdminKey, setSQLiteSourceAdminKey] = useState('');
  const [showSQLiteSourceAdminKey, setShowSQLiteSourceAdminKey] = useState(false);
  const [sqlitePreflight, setSQLitePreflight] = useState<SQLiteAdoptionPreflightResult | null>(
    null
  );
  const [sqliteAdoptionResult, setSQLiteAdoptionResult] = useState<SQLiteAdoptionResult | null>(
    null
  );
  const [sqliteConfirmationOpen, setSQLiteConfirmationOpen] = useState(false);
  const [sqliteSourceStoppedConfirmed, setSQLiteSourceStoppedConfirmed] = useState(false);

  const detectedBase = useMemo(() => detectApiBaseFromLocation(), []);
  const isManagerServerMode = hostedByUsageService;
  const loginCredential = isManagerServerMode ? adminKey : cpaManagementKey;
  const redirectAfterLogin = useMemo(() => resolveRedirectPath(location.state), [location.state]);
  const loginCredentialLabel = isManagerServerMode
    ? t('login.admin_key_label')
    : t('login.cpa_management_key_label');
  const loginCredentialPlaceholder = isManagerServerMode
    ? t('login.admin_key_placeholder')
    : t('login.cpa_management_key_placeholder');
  const loginCredentialHint = isManagerServerMode
    ? t('login.admin_key_hint')
    : t('login.cpa_management_key_hint');

  const usageSetupSteps = useMemo<UsageSetupStep[]>(
    () =>
      initializationMode === 'adopt'
        ? ['mode', 'admin', 'sqliteSource', 'review']
        : [
            'mode',
            'admin',
            'connection',
            'cpaKey',
            'monitoring',
            ...(requestMonitoringEnabled ? (['polling'] as UsageSetupStep[]) : []),
            'review',
          ],
    [initializationMode, requestMonitoringEnabled]
  );
  const usageSetupStepIndex = Math.max(0, usageSetupSteps.indexOf(usageSetupStep));
  const usageSetupIsFirstStep = usageSetupStepIndex <= 0;
  const usageSetupIsLastStep = usageSetupStep === 'review';
  const usageSetupStepLabels = useMemo<Record<UsageSetupStep, string>>(
    () => ({
      mode: t('login.step_initialization_mode'),
      admin: t('login.step_admin_key'),
      sqliteSource: t('login.step_sqlite_source'),
      connection: t('login.step_connection'),
      cpaKey: t('login.step_cpa_key'),
      monitoring: t('login.step_monitoring'),
      polling: t('login.step_polling'),
      review: t('login.step_review'),
    }),
    [t]
  );
  const toggleLanguageMenu = useCallback(() => {
    setLanguageMenuOpen((prev) => !prev);
  }, []);

  const handleLanguageSelect = useCallback(
    (selectedLanguage: string) => {
      if (!isSupportedLanguage(selectedLanguage)) {
        return;
      }

      setLanguage(selectedLanguage);
      setLanguageMenuOpen(false);
    },
    [setLanguage]
  );

  useEffect(() => {
    if (!languageMenuOpen) {
      return;
    }

    const handlePointerDown = (event: MouseEvent) => {
      if (!languageMenuRef.current?.contains(event.target as Node)) {
        setLanguageMenuOpen(false);
      }
    };

    const handleEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setLanguageMenuOpen(false);
      }
    };

    document.addEventListener('mousedown', handlePointerDown);
    document.addEventListener('keydown', handleEscape);

    return () => {
      document.removeEventListener('mousedown', handlePointerDown);
      document.removeEventListener('keydown', handleEscape);
    };
  }, [languageMenuOpen]);

  useEffect(() => {
    const init = async () => {
      try {
        let detectedUsageService = false;
        let detectedUsageServiceConfigured = false;
        try {
          const info = await usageServiceApi.getInfo(detectedBase);
          const mode = resolveUsageServiceLoginMode(info);
          detectedUsageService = mode.hostedByUsageService;
          detectedUsageServiceConfigured = detectedUsageService && !mode.usageServiceNeedsSetup;
          setHostedByUsageService(mode.hostedByUsageService);
          setUsageServiceNeedsSetup(mode.usageServiceNeedsSetup);
          setHasHistoricalData(Boolean(info.hasHistoricalData));
          setMigrationStatus(info.migrationStatus || '');
        } catch {
          detectedUsageService = false;
          detectedUsageServiceConfigured = false;
          setHostedByUsageService(false);
          setUsageServiceNeedsSetup(false);
          setHasHistoricalData(false);
          setMigrationStatus('');
        }

        const hostedManagementPage =
          typeof window !== 'undefined' && /\/management\.html$/i.test(window.location.pathname);
        const autoLoginExpectedPanelBase =
          detectedUsageService || hostedManagementPage ? detectedBase : undefined;
        const autoLoggedIn = await restoreSession({
          expectedMode: detectedUsageService ? 'manager_embedded' : 'external_panel',
          expectedPanelBase: autoLoginExpectedPanelBase,
        });
        if (detectedUsageService) {
          setUsageServiceConfig(
            { enabled: true, serviceBase: detectedBase },
            { panelBase: detectedBase, panelHostMode: 'manager_embedded' }
          );
        }
        if (autoLoggedIn) {
          setAutoLoginSuccess(true);
          setTimeout(() => {
            if (autoLoggedIn.recoveryMode === 'manager_config') {
              localStorage.setItem(CONFIG_TAB_STORAGE_KEY, 'manager');
            }
            navigate(resolvePostLoginPath(autoLoggedIn, redirectAfterLogin), { replace: true });
          }, 1500);
          return;
        }

        const lastCPAForUsageService =
          localStorage.getItem(USAGE_SERVICE_LAST_CPA_BASE_KEY) ||
          localStorage.getItem(LEGACY_USAGE_SERVICE_LAST_CPA_BASE_KEY) ||
          '';
        const defaultCPAConnectionBase = resolveDefaultCPAConnectionBase({
          hostedByUsageService: detectedUsageService,
          currentBase: detectedBase,
        });
        setApiBase(
          detectedUsageService
            ? detectedUsageServiceConfigured
              ? detectedBase
              : lastCPAForUsageService || defaultCPAConnectionBase
            : storedBase || detectedBase
        );
        setShowCustomBase(detectedUsageService && !detectedUsageServiceConfigured);
        if (detectedUsageService) {
          setAdminKey(storedKey || '');
          setCPAManagementKey('');
        } else {
          setAdminKey('');
          setCPAManagementKey(storedKey || '');
        }
        setRememberCredential(storedRememberPassword || Boolean(storedKey));
      } finally {
        if (!autoLoginSuccess) {
          setAutoLoading(false);
        }
      }
    };

    init();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (!usageSetupSteps.includes(usageSetupStep)) {
      setUsageSetupStep('review');
    }
  }, [usageSetupStep, usageSetupSteps]);

  const validateUsageSetupStep = useCallback(
    (step: UsageSetupStep) => {
      if (step === 'admin' && !adminKey.trim()) {
        setError(t('login.admin_key_required'));
        return false;
      }
      if (step === 'sqliteSource' && !sqliteSourcePath.trim()) {
        setError(t('login.sqlite_source_path_required'));
        return false;
      }
      if (step === 'connection' && !apiBase.trim()) {
        setError(t('login.cpa_address_required'));
        return false;
      }
      if (step === 'cpaKey' && !cpaManagementKey.trim()) {
        setError(t('login.cpa_management_key_required'));
        return false;
      }
      if (step === 'polling') {
        const parsedPollIntervalMs = Number(pollIntervalMs);
        if (
          !/^\d+$/.test(pollIntervalMs.trim()) ||
          !Number.isFinite(parsedPollIntervalMs) ||
          parsedPollIntervalMs <= 0
        ) {
          setError(t('login.poll_interval_invalid'));
          return false;
        }
      }
      setError('');
      return true;
    },
    [adminKey, apiBase, cpaManagementKey, pollIntervalMs, sqliteSourcePath, t]
  );

  const handleUsageSetupNext = useCallback(async () => {
    if (!validateUsageSetupStep(usageSetupStep)) return;
    if (usageSetupStep === 'sqliteSource') {
      setLoading(true);
      setError('');
      try {
        const result = await usageServiceApi.preflightSQLiteSource(
          detectedBase,
          {
            sourcePath: sqliteSourcePath.trim(),
            dataKeyPath: sqliteDataKeyPath.trim() || undefined,
            sourceAdminKey: sqliteSourceAdminKey.trim() || undefined,
          },
          adminKey.trim()
        );
        setSQLitePreflight(result);
        if (!result.ready) {
          const parameters = result.requiredParameters
            .map((parameter) =>
              parameter === 'dataKeyPath'
                ? t('login.sqlite_data_key_path_label')
                : t('login.sqlite_source_admin_key_label')
            )
            .join(t('login.list_separator'));
          setError(t('login.sqlite_source_parameters_required', { parameters }));
          return;
        }
      } catch (err: unknown) {
        const message = getDetailedSQLiteAdoptionErrorMessage(err, t);
        setSQLitePreflight(null);
        setError(message);
        showNotification(`${t('login.sqlite_preflight_failed')}: ${message}`, 'error');
        return;
      } finally {
        setLoading(false);
      }
    }
    const currentIndex = usageSetupSteps.indexOf(usageSetupStep);
    const nextStep = usageSetupSteps[Math.min(currentIndex + 1, usageSetupSteps.length - 1)];
    setUsageSetupStep(nextStep);
  }, [
    adminKey,
    detectedBase,
    showNotification,
    sqliteDataKeyPath,
    sqliteSourceAdminKey,
    sqliteSourcePath,
    t,
    usageSetupStep,
    usageSetupSteps,
    validateUsageSetupStep,
  ]);

  const handleUsageSetupBack = useCallback(() => {
    setError('');
    const currentIndex = usageSetupSteps.indexOf(usageSetupStep);
    const previousStep = usageSetupSteps[Math.max(currentIndex - 1, 0)];
    setUsageSetupStep(previousStep);
  }, [usageSetupStep, usageSetupSteps]);

  const handleSubmit = useCallback(async () => {
    if (usageServiceNeedsSetup && !usageSetupIsLastStep) {
      handleUsageSetupNext();
      return;
    }

    const trimmedAdminKey = adminKey.trim();
    const trimmedCPAKey = cpaManagementKey.trim();
    const baseToUse = apiBase ? normalizeApiBase(apiBase) : detectedBase;

    if (usageServiceNeedsSetup && initializationMode === 'adopt') {
      if (!sqlitePreflight?.ready) {
        setUsageSetupStep('sqliteSource');
        setError(t('login.sqlite_source_preflight_required'));
        return;
      }
      setError('');
      setSQLiteSourceStoppedConfirmed(false);
      setSQLiteConfirmationOpen(true);
      return;
    }

    if (usageServiceNeedsSetup) {
      if (!trimmedAdminKey) {
        setError(t('login.admin_key_required'));
        return;
      }
      if (!apiBase.trim()) {
        setError(t('login.cpa_address_required'));
        return;
      }
      if (!trimmedCPAKey) {
        setError(t('login.cpa_management_key_required'));
        return;
      }
    } else if (isManagerServerMode) {
      if (!trimmedAdminKey) {
        setError(t('login.admin_key_required'));
        return;
      }
    } else if (!trimmedCPAKey) {
      setError(t('login.cpa_management_key_required'));
      return;
    }

    const parsedPollIntervalMs = Number(pollIntervalMs);
    if (
      usageServiceNeedsSetup &&
      requestMonitoringEnabled &&
      (!/^\d+$/.test(pollIntervalMs.trim()) ||
        !Number.isFinite(parsedPollIntervalMs) ||
        parsedPollIntervalMs <= 0)
    ) {
      setError(t('login.poll_interval_invalid'));
      return;
    }

    setLoading(true);
    setError('');
    try {
      if (usageServiceNeedsSetup) {
        await usageServiceApi.setup(
          detectedBase,
          {
            cpaBaseUrl: baseToUse,
            cpaManagementKey: trimmedCPAKey,
            pollIntervalMs: requestMonitoringEnabled ? parsedPollIntervalMs : undefined,
            ensureUsageStatisticsEnabled: requestMonitoringEnabled,
            requestMonitoringEnabled,
          },
          trimmedAdminKey
        );
        setUsageServiceConfig(
          { enabled: true, serviceBase: detectedBase },
          { panelBase: detectedBase, panelHostMode: 'manager_embedded' }
        );
        localStorage.setItem(USAGE_SERVICE_LAST_CPA_BASE_KEY, baseToUse);
      } else if (isManagerServerMode) {
        setUsageServiceConfig(
          { enabled: true, serviceBase: detectedBase },
          { panelBase: detectedBase, panelHostMode: 'manager_embedded' }
        );
      }

      const loginResult = await login({
        apiBase: isManagerServerMode ? detectedBase : baseToUse,
        managementKey: isManagerServerMode ? trimmedAdminKey : trimmedCPAKey,
        rememberPassword: rememberCredential,
        sessionMode: isManagerServerMode ? 'manager_embedded' : 'external_panel',
        sessionPanelBase: detectedBase,
      });
      showNotification(t('common.connected_status'), 'success');
      if (loginResult.recoveryMode === 'manager_config') {
        localStorage.setItem(CONFIG_TAB_STORAGE_KEY, 'manager');
      }
      navigate(resolvePostLoginPath(loginResult, redirectAfterLogin), { replace: true });
    } catch (err: unknown) {
      const message = getLocalizedErrorMessage(err, t);
      setError(message);
      showNotification(`${t('notification.login_failed')}: ${message}`, 'error');
    } finally {
      setLoading(false);
    }
  }, [
    adminKey,
    apiBase,
    cpaManagementKey,
    detectedBase,
    handleUsageSetupNext,
    initializationMode,
    isManagerServerMode,
    login,
    navigate,
    pollIntervalMs,
    rememberCredential,
    requestMonitoringEnabled,
    redirectAfterLogin,
    setUsageServiceConfig,
    showNotification,
    sqlitePreflight,
    t,
    usageServiceNeedsSetup,
    usageSetupIsLastStep,
  ]);

  const handleConfirmSQLiteAdoption = useCallback(async () => {
    if (!sqliteSourceStoppedConfirmed || !sqlitePreflight?.ready) return;
    setLoading(true);
    setError('');
    try {
      const result = await usageServiceApi.adoptSQLiteSource(
        detectedBase,
        {
          sourcePath: sqlitePreflight.sourcePath,
          dataKeyPath: sqlitePreflight.dataKeyPath || sqliteDataKeyPath.trim() || undefined,
          sourceAdminKey:
            sqliteSourceAdminKey.trim() ||
            (sqlitePreflight.adminKeyWillBeRetained ? adminKey.trim() : undefined),
          confirmSourceStopped: true,
        },
        adminKey.trim()
      );
      setSQLiteAdoptionResult(result);
      setSQLiteConfirmationOpen(false);
      showNotification(t('login.sqlite_adoption_saved'), 'success');
    } catch (err: unknown) {
      const message = getDetailedSQLiteAdoptionErrorMessage(err, t);
      setError(message);
      showNotification(`${t('login.sqlite_adoption_failed')}: ${message}`, 'error');
    } finally {
      setLoading(false);
    }
  }, [
    adminKey,
    detectedBase,
    showNotification,
    sqliteDataKeyPath,
    sqlitePreflight,
    sqliteSourceAdminKey,
    sqliteSourceStoppedConfirmed,
    t,
  ]);

  const handleSubmitKeyDown = useCallback(
    (event: React.KeyboardEvent) => {
      if (event.key === 'Enter' && !loading) {
        event.preventDefault();
        handleSubmit();
      }
    },
    [handleSubmit, loading]
  );

  if (isAuthenticated && !autoLoading && !autoLoginSuccess) {
    return <Navigate to={redirectAfterLogin} replace />;
  }

  const showSplash = autoLoading || autoLoginSuccess;

  const renderKeyToggle = (visible: boolean, toggle: () => void) => (
    <button
      type="button"
      className="btn btn-ghost btn-xs btn-icon-only"
      onClick={toggle}
      aria-label={visible ? t('login.hide_key') : t('login.show_key')}
      title={visible ? t('login.hide_key') : t('login.show_key')}
    >
      {visible ? <IconEyeOff size={16} /> : <IconEye size={16} />}
    </button>
  );

  return (
    <div className={styles.container}>
      <div className={styles.toolBar}>
        <button
          type="button"
          className={styles.toolButton}
          onClick={cycleTheme}
          aria-label={t('theme.switch')}
          title={t('theme.switch')}
        >
          {theme === 'dark' ? <IconMoon size={17} /> : <IconSun size={17} />}
        </button>
        <div className={styles.languageMenu} ref={languageMenuRef}>
          <button
            type="button"
            className={styles.toolButton}
            onClick={toggleLanguageMenu}
            aria-label={t('language.switch')}
            title={t('language.switch')}
            aria-haspopup="menu"
            aria-expanded={languageMenuOpen}
          >
            <IconLanguages size={17} />
          </button>
          {languageMenuOpen && (
            <div className={styles.languagePopover} role="menu" aria-label={t('language.switch')}>
              {LANGUAGE_ORDER.map((lang) => (
                <button
                  key={lang}
                  type="button"
                  className={`${styles.languageOption} ${
                    language === lang ? styles.languageOptionActive : ''
                  }`}
                  onClick={() => handleLanguageSelect(lang)}
                  role="menuitemradio"
                  aria-checked={language === lang}
                >
                  {t(LANGUAGE_LABEL_KEYS[lang])}
                </button>
              ))}
            </div>
          )}
        </div>
      </div>

      <div className={styles.formPanel}>
        {showSplash ? (
          <div className={styles.splashContent}>
            <img
              src={CPAMP_VERTICAL_LOGO_URL}
              srcSet={CPAMP_VERTICAL_LOGO_SRC_SET}
              alt="CPA Manager Plus"
              className={[styles.splashLogo, styles.splashLogoLight].join(' ')}
            />
            <img
              src={CPAMP_VERTICAL_LOGO_ON_DARK_URL}
              srcSet={CPAMP_VERTICAL_LOGO_ON_DARK_SRC_SET}
              alt="CPA Manager Plus"
              className={[styles.splashLogo, styles.splashLogoDark].join(' ')}
            />
            <div className={styles.splashLoader}>
              <div className={styles.splashLoaderBar} />
            </div>
          </div>
        ) : (
          <div
            className={`${styles.formContent} ${
              usageServiceNeedsSetup ? styles.setupFormContent : ''
            }`}
          >
            <div
              className={`${styles.loginCard} ${usageServiceNeedsSetup ? styles.setupCard : ''}`}
            >
              <div className={styles.cardBranding}>
                <img
                  src={CPAMP_HORIZONTAL_LOGO_PNG_URL}
                  srcSet={CPAMP_HORIZONTAL_LOGO_PNG_SRC_SET}
                  alt="CPA Manager Plus"
                  className={[styles.brandLogo, styles.brandLogoLight].join(' ')}
                />
                <img
                  src={CPAMP_HORIZONTAL_LOGO_ON_DARK_PNG_URL}
                  srcSet={CPAMP_HORIZONTAL_LOGO_ON_DARK_PNG_SRC_SET}
                  alt="CPA Manager Plus"
                  className={[styles.brandLogo, styles.brandLogoDark].join(' ')}
                />
              </div>

              {usageServiceNeedsSetup &&
                (sqliteAdoptionResult ? (
                  <div className={styles.adoptionComplete} role="status" aria-live="polite">
                    <span className={styles.adoptionCompleteIcon}>
                      <IconCheck size={30} />
                    </span>
                    <div className={styles.adoptionCompleteCopy}>
                      <span className={styles.stepEyebrow}>{t('login.sqlite_adoption_saved')}</span>
                      <h2>{t('login.sqlite_restart_title')}</h2>
                      <p>{t('login.sqlite_restart_description')}</p>
                    </div>
                    <div className={styles.adoptionSourceSummary}>
                      <span>{t('login.sqlite_source_path_label')}</span>
                      <strong>{sqliteAdoptionResult.sourcePath}</strong>
                    </div>
                    <div className={styles.warningBox}>
                      <IconTriangleAlert size={20} />
                      <div>
                        <strong>{t('login.sqlite_restart_warning_title')}</strong>
                        <p>{t('login.sqlite_restart_warning')}</p>
                      </div>
                    </div>
                    <Button fullWidth onClick={() => window.location.reload()}>
                      {t('login.sqlite_restart_recheck')}
                    </Button>
                  </div>
                ) : (
                  <div className={styles.setupFlow}>
                    <div className={styles.stepper} aria-label={t('login.setup_steps')}>
                      {usageSetupSteps.map((step, index) => {
                        const isActive = index === usageSetupStepIndex;
                        const isDone = index < usageSetupStepIndex;
                        return (
                          <div
                            key={step}
                            className={`${styles.stepItem} ${isActive ? styles.stepItemActive : ''} ${
                              isDone ? styles.stepItemDone : ''
                            }`}
                            aria-current={isActive ? 'step' : undefined}
                          >
                            <span className={styles.stepIndex}>
                              {isDone ? <IconCheck size={18} /> : index + 1}
                            </span>
                            <span className={styles.stepLabel}>{usageSetupStepLabels[step]}</span>
                          </div>
                        );
                      })}
                    </div>

                    <div className={styles.stepPanel}>
                      <div className={styles.stepHeader}>
                        <span className={styles.stepEyebrow}>
                          {t('login.step_count', {
                            current: usageSetupStepIndex + 1,
                            total: usageSetupSteps.length,
                          })}
                        </span>
                        <h2>{usageSetupStepLabels[usageSetupStep]}</h2>
                      </div>

                      {usageSetupStep === 'mode' && (
                        <fieldset className={styles.modeFieldset}>
                          <legend>{t('login.initialization_mode_legend')}</legend>
                          <label
                            className={`${styles.modeOption} ${
                              initializationMode === 'new' ? styles.modeOptionSelected : ''
                            }`}
                          >
                            <input
                              type="radio"
                              name="initialization-mode"
                              value="new"
                              checked={initializationMode === 'new'}
                              onChange={() => {
                                setInitializationMode('new');
                                setSQLitePreflight(null);
                                setError('');
                              }}
                            />
                            <span className={styles.modeIcon}>
                              <IconShield size={22} />
                            </span>
                            <span className={styles.modeCopy}>
                              <strong>{t('login.initialization_mode_new')}</strong>
                              <small>{t('login.initialization_mode_new_hint')}</small>
                            </span>
                          </label>
                          <label
                            className={`${styles.modeOption} ${
                              initializationMode === 'adopt' ? styles.modeOptionSelected : ''
                            }`}
                          >
                            <input
                              type="radio"
                              name="initialization-mode"
                              value="adopt"
                              checked={initializationMode === 'adopt'}
                              onChange={() => {
                                setInitializationMode('adopt');
                                setSQLitePreflight(null);
                                setError('');
                              }}
                            />
                            <span className={styles.modeIcon}>
                              <IconDatabaseZap size={22} />
                            </span>
                            <span className={styles.modeCopy}>
                              <strong>{t('login.initialization_mode_adopt')}</strong>
                              <small>{t('login.initialization_mode_adopt_hint')}</small>
                            </span>
                          </label>
                        </fieldset>
                      )}

                      {usageSetupStep === 'admin' && (
                        <div className={styles.stepFields}>
                          <div className={styles.connectionBox}>
                            <div className={styles.connectionIcon}>
                              <IconShield size={18} />
                            </div>
                            <div className={styles.connectionCopy}>
                              <div className={styles.label}>{t('login.usage_service_address')}</div>
                              <div className={styles.value}>{detectedBase}</div>
                              <div className={styles.hint}>
                                {hasHistoricalData || migrationStatus
                                  ? t('login.migration_detected_hint')
                                  : t('login.admin_key_setup_hint')}
                              </div>
                            </div>
                          </div>
                          <Input
                            autoFocus
                            label={t('login.admin_key_label')}
                            placeholder={t('login.admin_key_placeholder')}
                            type={showAdminKey ? 'text' : 'password'}
                            value={adminKey}
                            onChange={(event) => setAdminKey(event.target.value)}
                            onKeyDown={handleSubmitKeyDown}
                            hint={t('login.admin_key_hint')}
                            rightElement={renderKeyToggle(showAdminKey, () =>
                              setShowAdminKey((prev) => !prev)
                            )}
                          />
                        </div>
                      )}

                      {usageSetupStep === 'sqliteSource' && (
                        <div className={styles.stepFields}>
                          <div className={styles.warningBox}>
                            <IconTriangleAlert size={20} />
                            <div>
                              <strong>{t('login.sqlite_lock_warning_title')}</strong>
                              <p>{t('login.sqlite_lock_warning')}</p>
                            </div>
                          </div>
                          <Input
                            autoFocus
                            label={t('login.sqlite_source_path_label')}
                            placeholder={t('login.sqlite_source_path_placeholder')}
                            value={sqliteSourcePath}
                            onChange={(event) => {
                              setSQLiteSourcePath(event.target.value);
                              setSQLitePreflight(null);
                            }}
                            onKeyDown={handleSubmitKeyDown}
                            hint={t('login.sqlite_source_path_hint')}
                            autoComplete="off"
                          />
                          <Input
                            label={t('login.sqlite_data_key_path_label')}
                            placeholder={t('login.sqlite_data_key_path_placeholder')}
                            value={sqliteDataKeyPath}
                            onChange={(event) => {
                              setSQLiteDataKeyPath(event.target.value);
                              setSQLitePreflight(null);
                            }}
                            onKeyDown={handleSubmitKeyDown}
                            hint={t('login.sqlite_data_key_path_hint')}
                            autoComplete="off"
                          />
                          <Input
                            label={t('login.sqlite_source_admin_key_label')}
                            placeholder={t('login.sqlite_source_admin_key_placeholder')}
                            type={showSQLiteSourceAdminKey ? 'text' : 'password'}
                            value={sqliteSourceAdminKey}
                            onChange={(event) => {
                              setSQLiteSourceAdminKey(event.target.value);
                              setSQLitePreflight(null);
                            }}
                            onKeyDown={handleSubmitKeyDown}
                            hint={t('login.sqlite_source_admin_key_hint')}
                            autoComplete="off"
                            rightElement={renderKeyToggle(showSQLiteSourceAdminKey, () =>
                              setShowSQLiteSourceAdminKey((previous) => !previous)
                            )}
                          />
                          {sqlitePreflight?.ready && (
                            <div className={styles.preflightSummary} aria-live="polite">
                              <div>
                                <IconCheck size={18} />
                                <strong>{t('login.sqlite_preflight_valid')}</strong>
                              </div>
                              <span>
                                {t('login.sqlite_preflight_size', {
                                  size: formatFileSize(sqlitePreflight.databaseBytes, language),
                                })}
                              </span>
                            </div>
                          )}
                        </div>
                      )}

                      {usageSetupStep === 'connection' && (
                        <div className={styles.stepFields}>
                          <Input
                            autoFocus
                            label={t('login.cpa_connection_label')}
                            placeholder={t('login.cpa_connection_placeholder')}
                            value={apiBase}
                            onChange={(event) => setApiBase(event.target.value)}
                            onKeyDown={handleSubmitKeyDown}
                            hint={t('login.cpa_connection_hint')}
                          />
                        </div>
                      )}

                      {usageSetupStep === 'cpaKey' && (
                        <div className={styles.stepFields}>
                          <Input
                            autoFocus
                            label={t('login.cpa_management_key_label')}
                            placeholder={t('login.cpa_management_key_placeholder')}
                            type={showCPAManagementKey ? 'text' : 'password'}
                            value={cpaManagementKey}
                            onChange={(event) => setCPAManagementKey(event.target.value)}
                            onKeyDown={handleSubmitKeyDown}
                            hint={t('login.cpa_management_key_hint')}
                            rightElement={renderKeyToggle(showCPAManagementKey, () =>
                              setShowCPAManagementKey((prev) => !prev)
                            )}
                          />
                        </div>
                      )}

                      {usageSetupStep === 'monitoring' && (
                        <div className={styles.stepFields}>
                          <div className={styles.optionBox}>
                            <SelectionCheckbox
                              checked={requestMonitoringEnabled}
                              onChange={setRequestMonitoringEnabled}
                              ariaLabel={t('login.request_monitoring_enabled')}
                              label={t('login.request_monitoring_enabled')}
                              labelClassName={styles.toggleLabel}
                            />
                            <p>
                              {requestMonitoringEnabled
                                ? t('login.request_monitoring_enabled_hint')
                                : t('login.request_monitoring_disabled_hint')}
                            </p>
                          </div>
                        </div>
                      )}

                      {usageSetupStep === 'polling' && (
                        <div className={styles.stepFields}>
                          <Input
                            autoFocus
                            label={t('login.poll_interval_label')}
                            type="number"
                            min="1"
                            placeholder="500"
                            value={pollIntervalMs}
                            onChange={(event) => setPollIntervalMs(event.target.value)}
                            onKeyDown={handleSubmitKeyDown}
                            hint={t('login.poll_interval_hint')}
                          />
                        </div>
                      )}

                      {usageSetupStep === 'review' && (
                        <div className={styles.stepFields}>
                          {initializationMode === 'adopt' ? (
                            <>
                              <div className={styles.warningBox}>
                                <IconTriangleAlert size={20} />
                                <div>
                                  <strong>{t('login.sqlite_review_warning_title')}</strong>
                                  <p>{t('login.sqlite_review_warning')}</p>
                                </div>
                              </div>
                              <div className={styles.reviewGrid}>
                                <div>
                                  <span className={styles.reviewIcon}>
                                    <IconDatabaseZap size={18} />
                                  </span>
                                  <span>{t('login.sqlite_source_path_label')}</span>
                                  <strong>{sqlitePreflight?.sourcePath || sqliteSourcePath}</strong>
                                </div>
                                <div>
                                  <span className={styles.reviewIcon}>
                                    <IconInfo size={18} />
                                  </span>
                                  <span>{t('login.sqlite_database_size')}</span>
                                  <strong>
                                    {formatFileSize(sqlitePreflight?.databaseBytes || 0, language)}
                                  </strong>
                                </div>
                                <div>
                                  <span className={styles.reviewIcon}>
                                    <IconKey size={18} />
                                  </span>
                                  <span>{t('login.sqlite_data_key_status')}</span>
                                  <strong>
                                    {sqlitePreflight?.dataKeyRequired
                                      ? t('login.sqlite_parameter_verified')
                                      : t('login.sqlite_parameter_not_required')}
                                  </strong>
                                </div>
                                <div>
                                  <span className={styles.reviewIcon}>
                                    <IconShield size={18} />
                                  </span>
                                  <span>{t('login.sqlite_source_admin_status')}</span>
                                  <strong>
                                    {sqlitePreflight?.adminKeyRequired
                                      ? t('login.sqlite_parameter_verified')
                                      : sqlitePreflight?.adminKeyWillBeRetained
                                        ? t('login.sqlite_parameter_will_retain')
                                        : t('login.sqlite_parameter_will_create')}
                                  </strong>
                                </div>
                                <div>
                                  <span className={styles.reviewIcon}>
                                    <IconCheck size={18} />
                                  </span>
                                  <span>{t('login.sqlite_project_status')}</span>
                                  <strong>
                                    {sqlitePreflight?.projectInitialized
                                      ? t('login.sqlite_project_initialized')
                                      : t('login.sqlite_project_needs_setup')}
                                  </strong>
                                </div>
                              </div>
                            </>
                          ) : (
                            <>
                              <div className={styles.optionBox}>
                                <SelectionCheckbox
                                  checked={rememberCredential}
                                  onChange={setRememberCredential}
                                  ariaLabel={t('login.remember_credential_label')}
                                  label={t('login.remember_credential_label')}
                                  labelClassName={styles.toggleLabel}
                                />
                              </div>
                              <div className={styles.reviewGrid}>
                                <div>
                                  <span className={styles.reviewIcon}>
                                    <IconShield size={18} />
                                  </span>
                                  <span>{t('login.admin_key_label')}</span>
                                  <strong>{adminKey ? '************' : '-'}</strong>
                                </div>
                                <div>
                                  <span className={styles.reviewIcon}>
                                    <IconKey size={18} />
                                  </span>
                                  <span>{t('login.remember_credential_label')}</span>
                                  <strong>
                                    {rememberCredential
                                      ? t('common.enabled')
                                      : t('common.disabled')}
                                  </strong>
                                </div>
                                <div>
                                  <span className={styles.reviewIcon}>
                                    <IconInfo size={18} />
                                  </span>
                                  <span>{t('login.cpa_connection_label')}</span>
                                  <strong>{apiBase || '-'}</strong>
                                </div>
                                <div>
                                  <span className={styles.reviewIcon}>
                                    <IconKey size={18} />
                                  </span>
                                  <span>{t('login.cpa_management_key_label')}</span>
                                  <strong>{cpaManagementKey ? '************' : '-'}</strong>
                                </div>
                                <div>
                                  <span className={styles.reviewIcon}>
                                    <IconEye size={18} />
                                  </span>
                                  <span>{t('login.request_monitoring_enabled')}</span>
                                  <strong>
                                    {requestMonitoringEnabled
                                      ? t('common.enabled')
                                      : t('common.disabled')}
                                  </strong>
                                </div>
                                {requestMonitoringEnabled && (
                                  <div>
                                    <span className={styles.reviewIcon}>
                                      <IconTimer size={18} />
                                    </span>
                                    <span>{t('login.poll_interval_label')}</span>
                                    <strong>{pollIntervalMs}</strong>
                                  </div>
                                )}
                              </div>
                            </>
                          )}
                        </div>
                      )}
                    </div>

                    {error && <div className={styles.errorBox}>{error}</div>}

                    <div className={styles.stepActions}>
                      <Button
                        variant="secondary"
                        className={styles.setupBackButton}
                        onClick={handleUsageSetupBack}
                        disabled={usageSetupIsFirstStep || loading}
                      >
                        {t('common.previous')}
                      </Button>
                      {usageSetupIsLastStep ? (
                        <Button
                          className={styles.setupNextButton}
                          onClick={handleSubmit}
                          loading={loading}
                        >
                          {loading
                            ? initializationMode === 'adopt'
                              ? t('login.sqlite_adopting')
                              : t('login.initializing')
                            : initializationMode === 'adopt'
                              ? t('login.sqlite_adopt_button')
                              : t('login.initialize_button')}
                        </Button>
                      ) : (
                        <Button
                          className={styles.setupNextButton}
                          onClick={handleUsageSetupNext}
                          loading={loading && usageSetupStep === 'sqliteSource'}
                          disabled={loading}
                        >
                          {loading && usageSetupStep === 'sqliteSource'
                            ? t('login.sqlite_validating')
                            : t('common.next')}
                        </Button>
                      )}
                    </div>
                  </div>
                ))}

              {!usageServiceNeedsSetup && (
                <div className={styles.loginForm}>
                  <div className={styles.connectionBox}>
                    <div className={styles.label}>{t('login.connection_current')}</div>
                    <div className={styles.value}>{apiBase || detectedBase}</div>
                    <div className={styles.hint}>
                      {isManagerServerMode
                        ? t('login.usage_service_configured_hint')
                        : t('login.connection_auto_hint')}
                    </div>
                  </div>

                  {!isManagerServerMode && (
                    <>
                      <div className={styles.toggleAdvanced}>
                        <SelectionCheckbox
                          checked={showCustomBase}
                          onChange={setShowCustomBase}
                          ariaLabel={t('login.custom_connection_label')}
                          label={t('login.custom_connection_label')}
                          labelClassName={styles.toggleLabel}
                        />
                      </div>

                      {showCustomBase && (
                        <Input
                          label={t('login.custom_connection_label')}
                          placeholder={t('login.custom_connection_placeholder')}
                          value={apiBase}
                          onChange={(event) => setApiBase(event.target.value)}
                          hint={t('login.custom_connection_hint')}
                        />
                      )}
                    </>
                  )}

                  <Input
                    autoFocus
                    label={loginCredentialLabel}
                    placeholder={loginCredentialPlaceholder}
                    type={
                      (isManagerServerMode ? showAdminKey : showCPAManagementKey)
                        ? 'text'
                        : 'password'
                    }
                    value={loginCredential}
                    onChange={(event) =>
                      isManagerServerMode
                        ? setAdminKey(event.target.value)
                        : setCPAManagementKey(event.target.value)
                    }
                    onKeyDown={handleSubmitKeyDown}
                    hint={loginCredentialHint}
                    rightElement={renderKeyToggle(
                      isManagerServerMode ? showAdminKey : showCPAManagementKey,
                      () =>
                        isManagerServerMode
                          ? setShowAdminKey((prev) => !prev)
                          : setShowCPAManagementKey((prev) => !prev)
                    )}
                  />

                  <div className={styles.toggleAdvanced}>
                    <SelectionCheckbox
                      checked={rememberCredential}
                      onChange={setRememberCredential}
                      ariaLabel={t('login.remember_credential_label')}
                      label={t('login.remember_credential_label')}
                      labelClassName={styles.toggleLabel}
                    />
                  </div>

                  <Button fullWidth onClick={handleSubmit} loading={loading}>
                    {loading ? t('login.submitting') : t('login.submit_button')}
                  </Button>

                  {error && <div className={styles.errorBox}>{error}</div>}
                </div>
              )}
            </div>
          </div>
        )}
      </div>
      <Modal
        open={sqliteConfirmationOpen}
        title={t('login.sqlite_confirmation_title')}
        width={620}
        closeDisabled={loading}
        onClose={() => {
          setSQLiteConfirmationOpen(false);
          setSQLiteSourceStoppedConfirmed(false);
          setError('');
        }}
        footer={
          <>
            <Button
              variant="secondary"
              onClick={() => {
                setSQLiteConfirmationOpen(false);
                setSQLiteSourceStoppedConfirmed(false);
                setError('');
              }}
              disabled={loading}
            >
              {t('common.cancel')}
            </Button>
            <Button
              onClick={handleConfirmSQLiteAdoption}
              loading={loading}
              disabled={!sqliteSourceStoppedConfirmed}
            >
              {loading ? t('login.sqlite_adopting') : t('login.sqlite_confirm_adoption')}
            </Button>
          </>
        }
      >
        <div className={styles.confirmationContent}>
          <div className={styles.warningBox} role="alert">
            <IconTriangleAlert size={22} />
            <div>
              <strong>{t('login.sqlite_confirmation_warning_title')}</strong>
              <p>{t('login.sqlite_confirmation_warning')}</p>
            </div>
          </div>
          <div className={styles.adoptionSourceSummary}>
            <span>{t('login.sqlite_source_path_label')}</span>
            <strong>{sqlitePreflight?.sourcePath || sqliteSourcePath}</strong>
          </div>
          <SelectionCheckbox
            checked={sqliteSourceStoppedConfirmed}
            onChange={setSQLiteSourceStoppedConfirmed}
            ariaLabel={t('login.sqlite_source_stopped_confirmation')}
            label={t('login.sqlite_source_stopped_confirmation')}
            labelClassName={styles.toggleLabel}
            disabled={loading}
          />
          <p className={styles.confirmationHint}>{t('login.sqlite_confirmation_restart_hint')}</p>
          {error && (
            <div className={styles.errorBox} role="alert" aria-live="assertive">
              {error}
            </div>
          )}
        </div>
      </Modal>
    </div>
  );
}
