import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it, vi } from 'vitest';
import type { DatabaseMigrationHistoryResponse } from '@/services/api/usageService';
import { DatabaseMigrationHistoryModal } from './DatabaseMigrationHistoryModal';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) => key,
    i18n: { language: 'en' },
  }),
}));

describe('DatabaseMigrationHistoryModal', () => {
  it('keeps a full failure visible after the task has been resumed', () => {
    const history: DatabaseMigrationHistoryResponse = {
      activeMigrationId: 'migration-resumed',
      migrations: [
        {
          id: 'migration-resumed',
          source: 'sqlite',
          target: 'mysql',
          phase: 'copy_history',
          status: 'running',
          generation: 9,
          batchSize: 1_000,
          progressPercent: 50,
          copiedRows: 500,
          totalRows: 1_000,
          copiedBytes: 4_096,
          requests: 3,
          completedTables: 1,
          totalTables: 2,
          createdAtMs: 1_700_000_000_000,
          updatedAtMs: 1_700_000_002_000,
          tables: [
            {
              name: 'settings',
              stage: 'history',
              rowsCopied: 500,
              totalRows: 500,
              copiedBytes: 4_096,
              requests: 3,
              completed: true,
              updatedAtMs: 1_700_000_001_000,
            },
          ],
          events: [
            {
              id: 1,
              migrationId: 'migration-resumed',
              eventType: 'failed',
              phase: 'copy_history',
              status: 'failed',
              generation: 8,
              table: 'settings',
              error: 'mysql batch failed: exact diagnostic detail',
              createdAtMs: 1_700_000_001_000,
            },
            {
              id: 2,
              migrationId: 'migration-resumed',
              eventType: 'resumed',
              previousPhase: 'copy_history',
              previousStatus: 'failed',
              phase: 'copy_history',
              status: 'running',
              generation: 9,
              createdAtMs: 1_700_000_002_000,
            },
          ],
        },
      ],
    };

    const markup = renderToStaticMarkup(
      <DatabaseMigrationHistoryModal
        open
        history={history}
        loading={false}
        error=""
        onRefresh={() => undefined}
        onClose={() => undefined}
      />
    );

    expect(markup).toContain('migration-resumed');
    expect(markup).toContain('settings');
    expect(markup).toContain('mysql batch failed: exact diagnostic detail');
    expect(markup).toContain('system_info.database_management.migration_event_failed');
    expect(markup).toContain('system_info.database_management.migration_event_resumed');
    expect(markup).toContain('title="system_info.database_management.tip_history_refresh"');
    expect(markup).toContain('title="system_info.database_management.tip_history_close"');
  });

  it('labels a ready running task as awaiting administrator action', () => {
    const history: DatabaseMigrationHistoryResponse = {
      activeMigrationId: 'migration-ready',
      migrations: [
        {
          id: 'migration-ready',
          source: 'sqlite',
          target: 'mysql',
          phase: 'ready_to_cutover',
          status: 'running',
          generation: 10,
          batchSize: 1_000,
          progressPercent: 100,
          copiedRows: 1,
          totalRows: 1,
          copiedBytes: 1,
          requests: 1,
          completedTables: 1,
          totalTables: 1,
          createdAtMs: 1_700_000_000_000,
          updatedAtMs: 1_700_000_001_000,
          tables: [],
          events: [],
        },
      ],
    };

    const markup = renderToStaticMarkup(
      <DatabaseMigrationHistoryModal
        open
        history={history}
        loading={false}
        error=""
        onRefresh={() => undefined}
        onClose={() => undefined}
      />
    );

    expect(markup).toContain('system_info.database_management.migration_state_awaiting_admin');
  });
});
