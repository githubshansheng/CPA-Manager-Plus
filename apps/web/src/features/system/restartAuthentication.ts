interface RestartManagementKeyOptions {
  preferredKey: string;
  fallbackKey: string;
  verify: (managementKey: string) => Promise<unknown>;
}

export async function resolveRestartManagementKey({
  preferredKey,
  fallbackKey,
  verify,
}: RestartManagementKeyOptions): Promise<string> {
  const candidateKeys = Array.from(
    new Set([preferredKey.trim(), fallbackKey.trim()].filter(Boolean))
  );
  let lastVerificationError: unknown;

  for (const candidateKey of candidateKeys) {
    try {
      await verify(candidateKey);
      return candidateKey;
    } catch (error) {
      lastVerificationError = error;
    }
  }

  throw lastVerificationError ?? new Error('Manager Server authentication failed');
}
