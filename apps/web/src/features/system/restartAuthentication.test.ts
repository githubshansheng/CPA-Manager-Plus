import { describe, expect, it, vi } from 'vitest';
import { resolveRestartManagementKey } from './restartAuthentication';

describe('resolveRestartManagementKey', () => {
  it('uses the target SQLite administrator key when activation succeeds', async () => {
    const verify = vi.fn().mockResolvedValue(undefined);

    await expect(
      resolveRestartManagementKey({
        preferredKey: 'target-admin',
        fallbackKey: 'current-admin',
        verify,
      })
    ).resolves.toBe('target-admin');
    expect(verify).toHaveBeenCalledTimes(1);
    expect(verify).toHaveBeenCalledWith('target-admin');
  });

  it('falls back to the original administrator key after an automatic rollback', async () => {
    const targetError = new Error('target key rejected');
    const verify = vi.fn().mockRejectedValueOnce(targetError).mockResolvedValueOnce(undefined);

    await expect(
      resolveRestartManagementKey({
        preferredKey: 'target-admin',
        fallbackKey: 'current-admin',
        verify,
      })
    ).resolves.toBe('current-admin');
    expect(verify.mock.calls).toEqual([['target-admin'], ['current-admin']]);
  });

  it('does not retry the same key and reports the last verification failure', async () => {
    const verificationError = new Error('administrator key rejected');
    const verify = vi.fn().mockRejectedValue(verificationError);

    await expect(
      resolveRestartManagementKey({
        preferredKey: 'same-admin',
        fallbackKey: 'same-admin',
        verify,
      })
    ).rejects.toBe(verificationError);
    expect(verify).toHaveBeenCalledTimes(1);
  });
});
