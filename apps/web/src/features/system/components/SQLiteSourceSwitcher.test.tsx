import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import type { ComponentProps, ReactNode } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type {
  SQLiteAdoptionPreflightResult,
  UsageServiceStatus,
} from '@/services/api/usageService';
import { SQLiteSourceSwitcher } from './SQLiteSourceSwitcher';

const mocks = vi.hoisted(() => ({
  preflight: vi.fn(),
  switchSource: vi.fn(),
  restart: vi.fn(),
  getInfo: vi.fn(),
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: Record<string, unknown>) =>
      options ? `${key}:${Object.values(options).join('|')}` : key,
    i18n: { language: 'en' },
  }),
}));

vi.mock('@/components/ui/Modal', () => ({
  Modal: ({
    open,
    title,
    children,
    footer,
  }: {
    open: boolean;
    title?: ReactNode;
    children?: ReactNode;
    footer?: ReactNode;
  }) =>
    open ? (
      <div data-modal-title={String(title)}>
        {children}
        {footer}
      </div>
    ) : null,
}));

vi.mock('@/services/api/usageService', async (importOriginal) => {
  const original = await importOriginal<typeof import('@/services/api/usageService')>();
  return {
    ...original,
    usageServiceApi: {
      ...original.usageServiceApi,
      preflightSQLiteSourceSwitch: mocks.preflight,
      switchSQLiteSource: mocks.switchSource,
      restartManagerServer: mocks.restart,
      getInfo: mocks.getInfo,
    },
  };
});

const safeStatus = (): UsageServiceStatus => ({
  dbPath: '/srv/current/usage.sqlite',
  databaseTopology: {
    generation: 7,
    writePrimary: 'sqlite',
    businessReadPrimary: 'sqlite',
    systemReadPrimary: 'sqlite',
    failoverState: 'idle',
  },
  databases: {
    sqlite: { connected: true, cleanupStatus: 'disabled', rebuildStatus: 'idle' },
    mysql: { configured: false, connected: false },
  },
  replication: { enabled: false, state: 'idle' },
});

const readyPreflight = (
  overrides: Partial<SQLiteAdoptionPreflightResult> = {}
): SQLiteAdoptionPreflightResult => ({
  ready: true,
  sourcePath: '/srv/old/usage.sqlite',
  dataKeyPath: '/srv/old/data.key',
  databaseBytes: 4_096,
  walBytes: 512,
  shmBytes: 128,
  hasHistoricalData: true,
  hasEncryptedConnection: true,
  dataKeyRequired: false,
  dataKeyVerified: false,
  adminKeyRequired: false,
  adminKeyVerified: false,
  adminKeyWillBeCreated: false,
  adminKeyWillBeRetained: true,
  projectInitialized: true,
  requiredParameters: [],
  lockRisk: true,
  restartRequired: true,
  ...overrides,
});

const renderSwitcher = (
  status = safeStatus(),
  props: Partial<ComponentProps<typeof SQLiteSourceSwitcher>> = {}
) => {
  let renderer: ReactTestRenderer;
  act(() => {
    renderer = create(
      <SQLiteSourceSwitcher
        status={status}
        base="http://manager.test"
        managementKey="current-admin"
        loading={false}
        onRefresh={async () => undefined}
        {...props}
      />
    );
  });
  return renderer!;
};

const buttonByText = (renderer: ReactTestRenderer, text: string) =>
  renderer.root
    .findAllByType('button')
    .find(
      (button) =>
        button.findAll((node) =>
          node.children.some((child) => typeof child === 'string' && child.includes(text))
        ).length > 0
    );

const openModalAndSetSource = (renderer: ReactTestRenderer) => {
  act(() => buttonByText(renderer, 'sqlite_switch_button')?.props.onClick());
  const source = renderer.root
    .findAllByType('input')
    .find((input) => input.props.placeholder?.includes('sqlite_switch_source_path_placeholder'));
  act(() => source?.props.onChange({ target: { value: '/srv/old/usage.sqlite' } }));
};

describe('SQLiteSourceSwitcher', () => {
  beforeEach(() => {
    mocks.preflight.mockReset();
    mocks.switchSource.mockReset();
    mocks.restart.mockReset();
    mocks.getInfo.mockReset();
  });

  it('disables switching outside a stable SQLite-only topology and explains every blocker', () => {
    const renderer = renderSwitcher({
      ...safeStatus(),
      databaseTopology: {
        generation: 7,
        writePrimary: 'mysql',
        businessReadPrimary: 'mysql',
        systemReadPrimary: 'sqlite',
      },
      databases: {
        sqlite: { cleanupStatus: 'idle', rebuildStatus: 'queued' },
        mysql: { configured: true, connected: true },
      },
      replication: { enabled: true },
      databaseMigration: { id: 'migration-1', status: 'running' },
    });

    expect(buttonByText(renderer, 'sqlite_switch_button')?.props.disabled).toBe(true);
    const markup = JSON.stringify(renderer.toJSON());
    expect(markup).toContain('sqlite_switch_block_routing');
    expect(markup).toContain('sqlite_switch_block_replication');
    expect(markup).toContain('sqlite_switch_block_mysql');
    expect(markup).toContain('sqlite_switch_block_migration');
    expect(markup).toContain('sqlite_switch_block_maintenance');
  });

  it('keeps pending restart state and detailed recovery diagnostics visible after refresh', () => {
    const renderer = renderSwitcher({
      ...safeStatus(),
      sqliteSource: {
        currentPath: '/srv/current/usage.sqlite',
        pendingPath: '/srv/pending/usage.sqlite',
        restartRequired: true,
        failedPath: '/srv/broken/usage.sqlite',
        lastErrorStage: 'database open',
        lastErrorCause: 'database is locked',
        lastError: 'could not activate selected SQLite source',
      },
    });

    const markup = JSON.stringify(renderer.toJSON());
    expect(markup).toContain('/srv/pending/usage.sqlite');
    expect(markup).toContain('/srv/broken/usage.sqlite');
    expect(markup).toContain('database open');
    expect(markup).toContain('database is locked');
    expect(buttonByText(renderer, 'sqlite_restart_button')).toBeDefined();
  });

  it('invalidates a successful preflight as soon as any source parameter changes', async () => {
    mocks.preflight.mockResolvedValue(readyPreflight());
    const renderer = renderSwitcher();
    openModalAndSetSource(renderer);

    await act(async () => {
      buttonByText(renderer, 'sqlite_switch_preflight_button')?.props.onClick();
      await Promise.resolve();
    });
    expect(buttonByText(renderer, 'sqlite_switch_confirm_button')).toBeDefined();

    const dataKey = renderer.root
      .findAllByType('input')
      .find((input) => input.props.placeholder?.includes('sqlite_switch_data_key_placeholder'));
    act(() => dataKey?.props.onChange({ target: { value: '/srv/old/data.key' } }));

    expect(buttonByText(renderer, 'sqlite_switch_confirm_button')).toBeUndefined();
    expect(buttonByText(renderer, 'sqlite_switch_preflight_button')).toBeDefined();
  });

  it('stays in preflight when the server asks for a data key and old administrator key', async () => {
    mocks.preflight.mockResolvedValue(
      readyPreflight({
        ready: false,
        dataKeyRequired: true,
        adminKeyRequired: true,
        requiredParameters: ['dataKeyPath', 'sourceAdminKey'],
      })
    );
    const renderer = renderSwitcher();
    openModalAndSetSource(renderer);

    await act(async () => {
      buttonByText(renderer, 'sqlite_switch_preflight_button')?.props.onClick();
      await Promise.resolve();
    });

    const markup = JSON.stringify(renderer.toJSON());
    expect(markup).toContain('sqlite_switch_data_key_missing');
    expect(markup).toContain('sqlite_switch_admin_key_missing');
    expect(buttonByText(renderer, 'sqlite_switch_confirm_button')).toBeUndefined();
    expect(mocks.switchSource).not.toHaveBeenCalled();
  });

  it('requires explicit stop confirmation and sends the complete switch request', async () => {
    mocks.preflight.mockResolvedValue(readyPreflight());
    mocks.switchSource.mockResolvedValue({
      ok: true,
      sourcePath: '/srv/old/usage.sqlite',
      restartRequired: true,
      selectionState: 'pending',
    });
    const onRefresh = vi.fn().mockResolvedValue(undefined);
    const renderer = renderSwitcher(safeStatus(), { onRefresh });
    openModalAndSetSource(renderer);

    const dataKey = renderer.root
      .findAllByType('input')
      .find((input) => input.props.placeholder?.includes('sqlite_switch_data_key_placeholder'));
    const adminKey = renderer.root
      .findAllByType('input')
      .find((input) => input.props.placeholder?.includes('sqlite_switch_admin_key_placeholder'));
    act(() => {
      dataKey?.props.onChange({ target: { value: '/srv/old/data.key' } });
      adminKey?.props.onChange({ target: { value: 'old-admin' } });
    });

    await act(async () => {
      buttonByText(renderer, 'sqlite_switch_preflight_button')?.props.onClick();
      await Promise.resolve();
    });
    expect(buttonByText(renderer, 'sqlite_switch_confirm_button')?.props.disabled).toBe(true);

    const checkbox = renderer.root
      .findAllByType('input')
      .find((input) => input.props.type === 'checkbox');
    act(() => checkbox?.props.onChange({ target: { checked: true } }));

    await act(async () => {
      buttonByText(renderer, 'sqlite_switch_confirm_button')?.props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.switchSource).toHaveBeenCalledWith(
      'http://manager.test',
      'current-admin',
      expect.objectContaining({
        sourcePath: '/srv/old/usage.sqlite',
        dataKeyPath: '/srv/old/data.key',
        sourceAdminKey: 'old-admin',
        confirmSourceStopped: true,
        expectedGeneration: 7,
        idempotencyKey: expect.any(String),
      })
    );
    expect(onRefresh).toHaveBeenCalledTimes(1);
    expect(buttonByText(renderer, 'sqlite_restart_button')).toBeDefined();
  });

  it('keeps the pending restart visible when saving succeeds but status refresh fails', async () => {
    mocks.preflight.mockResolvedValue(readyPreflight());
    mocks.switchSource.mockResolvedValue({
      ok: true,
      sourcePath: '/srv/old/usage.sqlite',
      restartRequired: true,
      selectionState: 'pending',
    });
    const renderer = renderSwitcher(safeStatus(), {
      onRefresh: vi.fn().mockRejectedValue(new Error('refresh unavailable')),
    });
    openModalAndSetSource(renderer);

    await act(async () => {
      buttonByText(renderer, 'sqlite_switch_preflight_button')?.props.onClick();
      await Promise.resolve();
    });
    const checkbox = renderer.root
      .findAllByType('input')
      .find((input) => input.props.type === 'checkbox');
    act(() => checkbox?.props.onChange({ target: { checked: true } }));

    await act(async () => {
      buttonByText(renderer, 'sqlite_switch_confirm_button')?.props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });

    const markup = JSON.stringify(renderer.toJSON());
    expect(markup).toContain('sqlite_switch_saved_refresh_failed');
    expect(markup).toContain('refresh unavailable');
    expect(markup).toContain('/srv/old/usage.sqlite');
    expect(buttonByText(renderer, 'sqlite_restart_button')).toBeDefined();
  });

  it('shows the backend failure stage, source path, and underlying cause', async () => {
    const requestError = Object.assign(new Error('SQLite source preflight failed'), {
      code: 'sqlite_source_locked',
      details: {
        error: 'SQLite source preflight failed',
        code: 'sqlite_source_locked',
        details: {
          stage: 'write-lock probe',
          sourcePath: '/srv/locked/usage.sqlite',
          cause: 'database is locked',
        },
      },
    });
    mocks.preflight.mockRejectedValue(requestError);
    const renderer = renderSwitcher();
    openModalAndSetSource(renderer);

    await act(async () => {
      buttonByText(renderer, 'sqlite_switch_preflight_button')?.props.onClick();
      await Promise.resolve();
    });

    const markup = JSON.stringify(renderer.toJSON());
    expect(markup).toContain('sqlite_source_locked');
    expect(markup).toContain('write-lock probe');
    expect(markup).toContain('/srv/locked/usage.sqlite');
    expect(markup).toContain('database is locked');
  });

  it('waits through a temporary outage and reauthenticates only after startedAt changes', async () => {
    vi.useFakeTimers();
    try {
      mocks.getInfo
        .mockResolvedValueOnce({ startedAt: 100 })
        .mockRejectedValueOnce(new Error('network error'))
        .mockResolvedValueOnce({ startedAt: 200 });
      mocks.restart.mockResolvedValue({ ok: true, restarting: true, startedAt: 100 });
      const onRestartRecovered = vi.fn().mockResolvedValue(undefined);
      const onRefresh = vi.fn().mockResolvedValue(undefined);
      const renderer = renderSwitcher(
        {
          ...safeStatus(),
          sqliteSource: {
            currentPath: '/srv/current/usage.sqlite',
            pendingPath: '/srv/old/usage.sqlite',
            restartRequired: true,
          },
        },
        { onRestartRecovered, onRefresh }
      );

      act(() => buttonByText(renderer, 'sqlite_restart_button')?.props.onClick());
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
        await Promise.resolve();
      });
      expect(mocks.restart).toHaveBeenCalledWith('http://manager.test', 'current-admin');

      for (let attempt = 0; attempt < 10 && vi.getTimerCount() === 0; attempt += 1) {
        await act(async () => {
          await Promise.resolve();
        });
      }

      for (
        let attempt = 0;
        attempt < 4 && onRestartRecovered.mock.calls.length === 0;
        attempt += 1
      ) {
        await act(async () => {
          await vi.runOnlyPendingTimersAsync();
          await Promise.resolve();
          await Promise.resolve();
        });
      }

      expect(mocks.getInfo).toHaveBeenCalledTimes(3);
      expect(onRestartRecovered).toHaveBeenCalledWith('current-admin');
      expect(onRefresh).toHaveBeenCalledTimes(1);
      expect(JSON.stringify(renderer.toJSON())).toContain('sqlite_restart_recovered');
    } finally {
      vi.useRealTimers();
    }
  });
});
