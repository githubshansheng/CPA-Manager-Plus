import { beforeEach, describe, expect, it, vi } from 'vitest';

const axiosMocks = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  put: vi.fn(),
}));

vi.mock('axios', () => ({
  default: {
    ...axiosMocks,
    create: vi.fn(() => ({
      defaults: { timeout: 0 },
      interceptors: {
        request: { use: vi.fn() },
        response: { use: vi.fn() },
      },
      get: vi.fn(),
      post: vi.fn(),
      put: vi.fn(),
      patch: vi.fn(),
      delete: vi.fn(),
      request: vi.fn(),
    })),
    isAxiosError: () => false,
  },
}));

import { usageServiceApi, type DatabaseMutationControl } from './usageService';

describe('usageServiceApi database management', () => {
  const base = 'http://manager.test';
  const key = 'management-key';
  const control: DatabaseMutationControl = {
    expectedGeneration: 9,
    idempotencyKey: 'operation-9',
  };

  beforeEach(() => {
    axiosMocks.get.mockReset().mockResolvedValue({ data: {} });
    axiosMocks.post.mockReset().mockResolvedValue({ data: {} });
    axiosMocks.put.mockReset().mockResolvedValue({ data: {} });
  });

  it('uses the expected MySQL endpoints and preserves explicit insecure-TLS confirmation', async () => {
    const connection = {
      address: 'mysql.internal:3306',
      database: 'cpamp',
      username: 'cpamp',
      password: 'write-only',
      tlsMode: 'disabled' as const,
      confirmInsecureTls: true,
    };

    await usageServiceApi.testMySQLConnection(base, key, connection);
    await usageServiceApi.saveMySQLConfig(base, key, { ...connection, ...control });

    expect(axiosMocks.post).toHaveBeenCalledWith(
      'http://manager.test/v0/management/databases/mysql/test',
      expect.objectContaining({ tlsMode: 'disabled', confirmInsecureTls: true }),
      expect.objectContaining({ headers: { Authorization: `Bearer ${key}` } })
    );
    expect(axiosMocks.put).toHaveBeenCalledWith(
      'http://manager.test/v0/management/databases/mysql/config',
      expect.objectContaining(control),
      expect.any(Object)
    );
  });

  it('covers replication, migration, routing, and SQLite cache management routes', async () => {
    await usageServiceApi.reinitializeMySQLSchema(base, key, {
      ...control,
      target: 'mysql',
      confirmDatabase: 'cpamp',
      confirmDrop: true,
    });
    await usageServiceApi.enableDatabaseReplication(base, key, control);
    await usageServiceApi.startDatabaseMigration(base, key, control);
    await usageServiceApi.getDatabaseMigrationHistory(base, key, 12);
    await usageServiceApi.updateDatabaseMigration(base, key, 'migration/a', 'pause', control);
    await usageServiceApi.cutoverDatabaseReads(base, key, {
      ...control,
      target: 'mysql',
      migrationId: 'migration/a',
      validationToken: 'validated',
    });
    const mysqlConfirmation = {
      ...control,
      target: 'mysql' as const,
      migrationId: 'migration/a',
      validationToken: 'validated',
    };
    const sqliteConfirmation = { ...mysqlConfirmation, target: 'sqlite' as const };
    await usageServiceApi.failoverDatabaseWrites(base, key, mysqlConfirmation);
    await usageServiceApi.updateSQLiteCachePolicy(base, key, {
      ...control,
      enabled: true,
      retentionDays: 15,
    });
    await usageServiceApi.previewSQLiteCacheCleanup(base, key, control);
    await usageServiceApi.cleanupSQLiteCache(base, key, sqliteConfirmation);
    await usageServiceApi.rebuildSQLiteCache(base, key, {
      ...sqliteConfirmation,
      retentionDays: 15,
    });

    const postURLs = axiosMocks.post.mock.calls.map(([url]) => url);
    expect(postURLs).toEqual(
      expect.arrayContaining([
        'http://manager.test/v0/management/databases/mysql/schema/reinitialize',
        'http://manager.test/v0/management/databases/replication/enable',
        'http://manager.test/v0/management/databases/migrations',
        'http://manager.test/v0/management/databases/migrations/migration%2Fa/pause',
        'http://manager.test/v0/management/databases/routing/cutover',
        'http://manager.test/v0/management/databases/routing/failover',
        'http://manager.test/v0/management/databases/sqlite-cache/cleanup/preview',
        'http://manager.test/v0/management/databases/sqlite-cache/cleanup',
        'http://manager.test/v0/management/databases/sqlite-cache/rebuild',
      ])
    );
    const reinitializeCall = axiosMocks.post.mock.calls.find(([url]) =>
      url.endsWith('/v0/management/databases/mysql/schema/reinitialize')
    );
    expect(reinitializeCall?.[1]).toEqual({
      ...control,
      target: 'mysql',
      confirmDatabase: 'cpamp',
      confirmDrop: true,
    });
    expect(axiosMocks.put).toHaveBeenCalledWith(
      'http://manager.test/v0/management/databases/sqlite-cache/policy',
      expect.objectContaining(control),
      expect.any(Object)
    );
    expect(axiosMocks.get).toHaveBeenCalledWith(
      'http://manager.test/v0/management/databases/migrations?limit=12',
      expect.objectContaining({ headers: { Authorization: `Bearer ${key}` } })
    );
    for (const route of [
      '/v0/management/databases/routing/failover',
      '/v0/management/databases/sqlite-cache/cleanup',
      '/v0/management/databases/sqlite-cache/rebuild',
    ]) {
      const call = axiosMocks.post.mock.calls.find(([url]) => url.endsWith(route));
      expect(call?.[1]).toEqual(
        expect.objectContaining({
          migrationId: 'migration/a',
          validationToken: 'validated',
        })
      );
    }
  });

  it('does not abort final migration validation at the ordinary 30-second timeout', async () => {
    await usageServiceApi.updateDatabaseMigration(base, key, 'migration/a', 'validate', control);

    expect(axiosMocks.post).toHaveBeenCalledWith(
      'http://manager.test/v0/management/databases/migrations/migration%2Fa/validate',
      control,
      expect.objectContaining({
        timeout: 6 * 60 * 60 * 1000,
        headers: { Authorization: `Bearer ${key}` },
      })
    );
  });

  it('preserves SQLite source switch parameters, mutation control, authentication, and timeouts', async () => {
    const payload = {
      sourcePath: '/srv/old/usage.sqlite',
      dataKeyPath: '/srv/old/data.key',
      sourceAdminKey: 'old-admin-key',
      confirmSourceStopped: true,
      ...control,
    };

    await usageServiceApi.preflightSQLiteSourceSwitch(base, key, payload);
    await usageServiceApi.switchSQLiteSource(base, key, payload);
    await usageServiceApi.restartManagerServer(base, key);

    expect(axiosMocks.post).toHaveBeenNthCalledWith(
      1,
      'http://manager.test/v0/management/databases/sqlite-source/preflight',
      payload,
      {
        timeout: 60_000,
        headers: { Authorization: `Bearer ${key}` },
      }
    );
    expect(axiosMocks.post).toHaveBeenNthCalledWith(
      2,
      'http://manager.test/v0/management/databases/sqlite-source/switch',
      payload,
      {
        timeout: 60_000,
        headers: { Authorization: `Bearer ${key}` },
      }
    );
    expect(axiosMocks.post).toHaveBeenNthCalledWith(
      3,
      'http://manager.test/v0/management/system/restart',
      {},
      expect.objectContaining({ headers: { Authorization: `Bearer ${key}` } })
    );
  });
});
