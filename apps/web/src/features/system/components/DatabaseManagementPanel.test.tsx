import { renderToStaticMarkup } from 'react-dom/server';
import type { ReactElement, ReactNode } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { UsageServiceStatus } from '@/services/api/usageService';
import { DatabaseManagementPanel } from './DatabaseManagementPanel';

const mocks = vi.hoisted(() => ({
  showNotification: vi.fn(),
  showConfirmation: vi.fn(),
  testMySQLConnection: vi.fn(),
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, values?: Record<string, unknown>) =>
      values ? `${key}:${Object.values(values).join('|')}` : key,
    i18n: { language: 'en' },
  }),
}));

vi.mock('@/stores', () => ({
  useNotificationStore: () => ({
    showNotification: mocks.showNotification,
    showConfirmation: mocks.showConfirmation,
  }),
}));

vi.mock('@/services/api/usageService', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/services/api/usageService')>();
  return {
    ...actual,
    usageServiceApi: {
      ...actual.usageServiceApi,
      testMySQLConnection: mocks.testMySQLConnection,
    },
  };
});

const status: UsageServiceStatus = {
  databaseTopology: {
    generation: 7,
    writePrimary: 'sqlite',
    businessReadPrimary: 'mysql',
    systemReadPrimary: 'sqlite',
    fallbackActive: true,
    fallbackSource: 'sqlite',
    readCutoverReady: true,
    writeFailoverReady: false,
  },
  databases: {
    sqlite: { connected: true, totalBytes: 2048, effectiveBytes: 1024, reusableBytes: 512 },
    mysql: {
      configured: true,
      connected: true,
      maskedAddress: 'db.internal:3306',
      version: 'MySQL 8.4.3',
      databaseBytes: 4096,
      slowQueries: { permissionDenied: true },
    },
  },
  replication: { enabled: true, state: 'running', pendingRows: 4, pendingBytes: 128 },
  databaseMigration: {
    id: 'migration-1',
    state: 'validated',
    progressPercent: 100,
    validationValid: true,
    validationToken: 'validation-token',
  },
  cacheCoverage: {
    complete: false,
    fromMs: Date.UTC(2026, 7, 1),
    toMs: Date.UTC(2026, 7, 15),
    retentionDays: 15,
    cleanupEnabled: true,
  },
};

describe('DatabaseManagementPanel', () => {
  beforeEach(() => {
    mocks.showNotification.mockReset();
    mocks.showConfirmation.mockReset();
    mocks.testMySQLConnection.mockReset();
    mocks.testMySQLConnection.mockResolvedValue({
      success: true,
      version: 'MySQL 8.0.12',
      latencyMs: 5,
    });
  });

  it('renders topology, partial coverage, MySQL permission gaps, and write-only password input', () => {
    const markup = renderToStaticMarkup(
      <DatabaseManagementPanel
        status={status}
        base="http://manager.test"
        managementKey="secret"
        loading={false}
        onRefresh={async () => undefined}
      />
    );

    expect(markup).toContain('system_info.database_management.topology_title');
    expect(markup).toContain('system_info.database_management.partial_title');
    expect(markup).toContain('system_info.database_management.permission_denied');
    expect(markup).toContain('db.internal:3306');
    expect(markup).toContain('type="password"');
    expect(markup).toContain('autoComplete="new-password"');
    expect(markup).toContain('value="127.0.0.1:3306"');
    expect(markup).toContain('value="cpamanage"');
    expect(markup).toContain('value="root"');
    expect(markup).not.toContain('value="root123"');
    expect(markup).toContain('aria-live="polite"');
    expect(markup).toMatch(
      /<button[^>]*disabled=""[^>]*>.*system_info\.database_management\.reinitialize_mysql_schema/
    );
  });

  it('provides trigger guidance for every database action button', () => {
    let renderer: ReactTestRenderer;
    act(() => {
      renderer = create(
        <DatabaseManagementPanel
          status={status}
          base="http://manager.test"
          managementKey="secret"
          loading={false}
          onRefresh={async () => undefined}
        />
      );
    });

    const buttons = renderer!.root.findAllByType('button');
    expect(buttons.length).toBeGreaterThan(10);
    for (const button of buttons) {
      expect(button.props.title).toMatch(/^system_info\.database_management\.tip_/);
    }
  });

  it('blocks invalid cache retention above the server limit', () => {
    let renderer: ReactTestRenderer;
    act(() => {
      renderer = create(
        <DatabaseManagementPanel
          status={status}
          base="http://manager.test"
          managementKey="secret"
          loading={false}
          onRefresh={async () => undefined}
        />
      );
    });

    const retention = renderer!.root
      .findAllByType('input')
      .find((input) => input.props.type === 'number');
    act(() => retention?.props.onChange({ target: { value: '3651' } }));
    const save = renderer!.root
      .findAllByType('button')
      .find((button) =>
        button.findAllByType('span').some((span) => span.children.includes('common.save'))
      );
    expect(save?.props.disabled).toBe(true);
  });

  it('prefills the requested local MySQL defaults before the first configuration', () => {
    let renderer: ReactTestRenderer;
    act(() => {
      renderer = create(
        <DatabaseManagementPanel
          status={{
            ...status,
            databases: {
              ...status.databases,
              mysql: { configured: false, connected: false },
            },
          }}
          base="http://manager.test"
          managementKey="secret"
          loading={false}
          onRefresh={async () => undefined}
        />
      );
    });

    const inputs = renderer!.root.findAllByType('input');
    expect(
      inputs.find((input) => input.props.placeholder === 'mysql.example.com:3306')?.props.value
    ).toBe('127.0.0.1:3306');
    expect(inputs.find((input) => input.props.autoComplete === 'username')?.props.value).toBe(
      'root'
    );
    expect(inputs.find((input) => input.props.autoComplete === 'new-password')?.props.value).toBe(
      'root123'
    );
    expect(
      renderer!.root.findAllByType('select').some((select) => select.props.value === 'disabled')
    ).toBe(true);
    expect(
      inputs.find(
        (input) => input.props.autoComplete === 'off' && input.props.placeholder === undefined
      )?.props.value
    ).toBe('cpamanage');
  });

  it('leaves scheduled cleanup unchecked when the server has no policy yet', () => {
    let renderer: ReactTestRenderer;
    act(() => {
      renderer = create(
        <DatabaseManagementPanel
          status={{ ...status, cacheCoverage: undefined }}
          base="http://manager.test"
          managementKey="secret"
          loading={false}
          onRefresh={async () => undefined}
        />
      );
    });

    const cleanupCheckbox = renderer!.root
      .findAllByType('input')
      .find((input) => input.props.type === 'checkbox');
    expect(cleanupCheckbox?.props.checked).toBe(false);
  });

  it('requires two dangerous confirmations before reinitializing a recoverable MySQL schema', () => {
    let renderer: ReactTestRenderer;
    act(() => {
      renderer = create(
        <DatabaseManagementPanel
          status={{
            ...status,
            databaseTopology: {
              ...status.databaseTopology,
              writePrimary: 'sqlite',
              businessReadPrimary: 'sqlite',
              systemReadPrimary: 'sqlite',
            },
            databases: {
              ...status.databases,
              mysql: {
                ...status.databases?.mysql,
                configured: true,
                connected: false,
                database: 'cpamp',
              },
            },
            replication: { enabled: false, state: 'idle' },
            databaseMigration: undefined,
          }}
          base="http://manager.test"
          managementKey="secret"
          loading={false}
          onRefresh={async () => undefined}
        />
      );
    });

    const button = renderer!.root
      .findAllByType('button')
      .find((candidate) =>
        candidate
          .findAllByType('span')
          .some((span) =>
            span.children.includes('system_info.database_management.reinitialize_mysql_schema')
          )
      );
    expect(button).toBeDefined();
    expect(button?.props.disabled).toBe(false);
    act(() => button?.props.onClick());

    expect(mocks.showConfirmation).toHaveBeenCalledTimes(1);
    expect(mocks.showConfirmation.mock.calls[0]?.[0]).toMatchObject({
      variant: 'danger',
      message: 'system_info.database_management.reinitialize_mysql_schema_confirm:cpamp',
      secondConfirmation: {
        variant: 'danger',
        message: 'system_info.database_management.reinitialize_mysql_schema_confirm_again:cpamp',
      },
    });
  });

  it('explains the impact of moving the write primary to MySQL before confirming', () => {
    let renderer: ReactTestRenderer;
    act(() => {
      renderer = create(
        <DatabaseManagementPanel
          status={{
            ...status,
            databaseTopology: {
              ...status.databaseTopology,
              writePrimary: 'sqlite',
              writeFailoverReady: true,
            },
          }}
          base="http://manager.test"
          managementKey="secret"
          loading={false}
          onRefresh={async () => undefined}
        />
      );
    });

    const button = renderer!.root
      .findAllByType('button')
      .find((candidate) =>
        candidate
          .findAllByType('span')
          .some((span) => span.children.includes('system_info.database_management.failover'))
      );
    expect(button?.props.disabled).toBe(false);

    act(() => button?.props.onClick());

    const confirmation = mocks.showConfirmation.mock.calls[0]?.[0] as {
      message: ReactNode;
      secondConfirmation?: { message?: ReactNode };
    };
    expect(confirmation).toMatchObject({
      variant: 'danger',
      secondConfirmation: {
        variant: 'danger',
        message: 'system_info.database_management.failover_confirm_again',
      },
    });
    expect(renderToStaticMarkup(confirmation.message as ReactElement)).toContain(
      'system_info.database_management.failover_impact_write'
    );
    expect(renderToStaticMarkup(confirmation.message as ReactElement)).toContain(
      'system_info.database_management.failover_impact_return'
    );
  });

  it('tests a connection without confirmation even when TLS is disabled', async () => {
    const onRefresh = vi.fn().mockResolvedValue(undefined);
    let renderer: ReactTestRenderer;
    act(() => {
      renderer = create(
        <DatabaseManagementPanel
          status={status}
          base="http://manager.test"
          managementKey="secret"
          loading={false}
          onRefresh={onRefresh}
        />
      );
    });

    const inputs = renderer!.root.findAllByType('input');
    const address = inputs.find((input) => input.props.placeholder === 'mysql.example.com:3306');
    const database = inputs.find(
      (input) => input.props.autoComplete === 'off' && input.props.placeholder === undefined
    );
    const username = inputs.find((input) => input.props.autoComplete === 'username');
    const password = inputs.find((input) => input.props.autoComplete === 'new-password');
    const tls = renderer!.root
      .findAllByType('select')
      .find((select) => select.props.value === 'disabled');
    expect(address).toBeDefined();
    expect(database).toBeDefined();
    expect(username).toBeDefined();
    expect(password).toBeDefined();
    expect(tls).toBeDefined();

    act(() => {
      address?.props.onChange({ target: { value: '127.0.0.1:3306' } });
      database?.props.onChange({ target: { value: 'cpamp' } });
      username?.props.onChange({ target: { value: 'root' } });
      password?.props.onChange({ target: { value: 'password' } });
      tls?.props.onChange({ target: { value: 'disabled' } });
    });

    const button = renderer!.root
      .findAllByType('button')
      .find((candidate) =>
        candidate
          .findAllByType('span')
          .some((span) => span.children.includes('system_info.database_management.test_mysql'))
      );
    expect(button).toBeDefined();
    expect(button?.props.disabled).toBe(false);

    act(() => button?.props.onClick());
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.showConfirmation).not.toHaveBeenCalled();
    expect(mocks.testMySQLConnection).toHaveBeenCalledWith(
      'http://manager.test',
      'secret',
      expect.objectContaining({
        address: '127.0.0.1:3306',
        database: 'cpamp',
        username: 'root',
        password: 'password',
        tlsMode: 'disabled',
        confirmInsecureTls: true,
      })
    );
    expect(onRefresh).toHaveBeenCalledTimes(1);
  });

  it('keeps cutover gated when validation has not passed', () => {
    const markup = renderToStaticMarkup(
      <DatabaseManagementPanel
        status={{
          ...status,
          databaseTopology: { ...status.databaseTopology, businessReadPrimary: 'sqlite' },
          databaseMigration: {
            ...status.databaseMigration,
            validationValid: false,
            validationToken: undefined,
          },
        }}
        base="http://manager.test"
        managementKey="secret"
        loading={false}
        onRefresh={async () => undefined}
      />
    );

    expect(markup).toMatch(
      /<button[^>]*disabled=""[^>]*>.*system_info\.database_management\.cutover/
    );
  });

  it('keeps cutover gated while a validated migration is paused', () => {
    const markup = renderToStaticMarkup(
      <DatabaseManagementPanel
        status={{
          ...status,
          databaseTopology: { ...status.databaseTopology, businessReadPrimary: 'sqlite' },
          databaseMigration: {
            ...status.databaseMigration,
            state: 'paused',
            status: 'paused',
            phase: 'ready_to_cutover',
            validationValid: true,
          },
        }}
        base="http://manager.test"
        managementKey="secret"
        loading={false}
        onRefresh={async () => undefined}
      />
    );

    expect(markup).toMatch(
      /<button[^>]*disabled=""[^>]*>.*system_info\.database_management\.cutover/
    );
  });

  it('explains that a stale paused migration must be resumed before validation', () => {
    let renderer: ReactTestRenderer;
    act(() => {
      renderer = create(
        <DatabaseManagementPanel
          status={{
            ...status,
            databaseTopology: { ...status.databaseTopology, businessReadPrimary: 'sqlite' },
            databaseMigration: {
              id: 'migration-stale-paused',
              state: 'paused',
              status: 'paused',
              phase: 'ready_to_cutover',
              validationValid: false,
              validationToken: 'stale-token',
            },
          }}
          base="http://manager.test"
          managementKey="secret"
          loading={false}
          onRefresh={async () => undefined}
        />
      );
    });

    const button = (translationKey: string) =>
      renderer!.root
        .findAllByType('button')
        .find((candidate) =>
          candidate.findAllByType('span').some((span) => span.children.includes(translationKey))
        );

    expect(JSON.stringify(renderer!.toJSON())).toContain(
      'system_info.database_management.validation_stale_resume_first'
    );
    expect(button('system_info.database_management.validate')?.props.disabled).toBe(true);
    expect(button('system_info.database_management.validate')?.props.title).toContain(
      'system_info.database_management.tip_validate_resume_first'
    );
    expect(button('system_info.database_management.resume')?.props.disabled).toBe(false);
  });

  it('offers revalidation when a ready-to-cutover token became stale', () => {
    let renderer: ReactTestRenderer;
    act(() => {
      renderer = create(
        <DatabaseManagementPanel
          status={{
            ...status,
            databaseMigration: {
              id: 'migration-stale-validation',
              status: 'running',
              phase: 'ready_to_cutover',
              validationValid: false,
              validationToken: 'stale-token',
            },
          }}
          base="http://manager.test"
          managementKey="secret"
          loading={false}
          onRefresh={async () => undefined}
        />
      );
    });

    const markup = renderer!.toJSON();
    expect(JSON.stringify(markup)).toContain('system_info.database_management.validation_stale');
    const validate = renderer!.root
      .findAllByType('button')
      .find((candidate) =>
        candidate
          .findAllByType('span')
          .some((span) => span.children.includes('system_info.database_management.validate'))
      );
    expect(validate?.props.disabled).toBe(false);
  });

  it('offers resume and blocks a new migration when the persisted task failed', () => {
    let renderer: ReactTestRenderer;
    act(() => {
      renderer = create(
        <DatabaseManagementPanel
          status={{
            ...status,
            databaseMigration: {
              id: 'migration-failed-after-restart',
              status: 'failed',
              phase: 'copy_history',
              progressPercent: 100,
              lastError: 'temporary target failure',
            },
          }}
          base="http://manager.test"
          managementKey="secret"
          loading={false}
          onRefresh={async () => undefined}
        />
      );
    });

    const button = (translationKey: string) =>
      renderer!.root
        .findAllByType('button')
        .find((candidate) =>
          candidate.findAllByType('span').some((span) => span.children.includes(translationKey))
        );

    expect(button('system_info.database_management.start_migration')?.props.disabled).toBe(true);
    expect(button('system_info.database_management.resume')?.props.disabled).toBe(false);
    expect(button('system_info.database_management.pause')?.props.disabled).toBe(true);
  });

  it('renders live validation stage, side, rows, and progress', () => {
    const markup = renderToStaticMarkup(
      <DatabaseManagementPanel
        status={{
          ...status,
          databaseMigration: {
            id: 'migration-validating',
            status: 'running',
            phase: 'ready_to_cutover',
            validationValid: true,
            validationToken: 'old-token',
            validationProgress: {
              running: true,
              stage: 'table_snapshot',
              currentTable: 'usage_events',
              side: 'source',
              completedSteps: 10,
              totalSteps: 42,
              processedRows: 500,
              totalRows: 1000,
              progressPercent: 24.4,
            },
          },
        }}
        base="http://manager.test"
        managementKey="secret"
        loading={false}
        onRefresh={async () => undefined}
      />
    );

    expect(markup).toContain('system_info.database_management.validation_progress');
    expect(markup).toContain('system_info.database_management.migration_state_validating');
    expect(markup).toContain('system_info.database_management.validation_stage_table_snapshot');
    expect(markup).toContain('system_info.database_management.validation_side_source');
    expect(markup).toContain('system_info.database_management.validation_steps:10|42');
    expect(markup).toContain(
      'system_info.database_management.validation_current_table:usage_events'
    );
    expect(markup).not.toContain('system_info.database_management.validation_passed');
  });

  it('shows the active derived-table step instead of a frozen 100 percent history bar', () => {
    const markup = renderToStaticMarkup(
      <DatabaseManagementPanel
        status={{
          ...status,
          databaseMigration: {
            id: 'migration-rebuilding',
            status: 'running',
            phase: 'rebuild_derived',
            progressPercent: 0,
            copiedRows: 1_035_976,
            totalRows: 1_035_976,
            completedSteps: 0,
            totalSteps: 18,
            currentTable: 'usage_account_model_rollups',
            currentTableActive: true,
            currentTableSinceMs: Date.UTC(2026, 8, 5),
            derivedTables: [
              { name: 'usage_account_model_rollups', stage: 'derived', active: true },
              { name: 'usage_dashboard_hourly_rollups', stage: 'derived' },
            ],
          },
        }}
        base="http://manager.test"
        managementKey="secret"
        loading={false}
        onRefresh={async () => undefined}
      />
    );

    expect(markup).toContain('system_info.database_management.migration_phase_rebuild_derived');
    expect(markup).toContain('system_info.database_management.migration_current_table_active');
    expect(markup).toContain('usage_account_model_rollups');
    expect(markup).toContain('system_info.database_management.migration_step_count:0|18');
    expect(markup).toContain('system_info.database_management.derived_table_progress');
    expect(markup).toMatch(/<progress max="100" aria-label=/);
    expect(markup).not.toMatch(/<progress[^>]*value="100"/);
  });

  it('shows completed copy work as awaiting administrator validation', () => {
    let renderer: ReactTestRenderer;
    act(() => {
      renderer = create(
        <DatabaseManagementPanel
          status={{
            ...status,
            databaseMigration: {
              id: 'migration-awaiting-validation',
              status: 'running',
              phase: 'validate',
              progressPercent: 100,
              copiedRows: 1_035_976,
              totalRows: 1_035_976,
              completedSteps: 18,
              totalSteps: 18,
            },
          }}
          base="http://manager.test"
          managementKey="secret"
          loading={false}
          onRefresh={async () => undefined}
        />
      );
    });

    const markup = renderer!.toJSON();
    expect(JSON.stringify(markup)).toContain(
      'system_info.database_management.migration_state_awaiting_admin'
    );
    expect(JSON.stringify(markup)).toContain(
      'system_info.database_management.migration_validation_ready_hint'
    );

    const button = (translationKey: string) =>
      renderer!.root
        .findAllByType('button')
        .find((candidate) =>
          candidate.findAllByType('span').some((span) => span.children.includes(translationKey))
        );
    expect(button('system_info.database_management.validate')?.props.disabled).toBe(false);
    expect(button('system_info.database_management.resume')?.props.disabled).toBe(true);
  });
});
