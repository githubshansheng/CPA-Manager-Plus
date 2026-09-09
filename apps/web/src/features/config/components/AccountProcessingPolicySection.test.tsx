import { act, create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Button } from '@/components/ui/Button';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import type { AccountProcessingPolicy } from '@/services/api/usageService';
import { AccountProcessingPolicySection } from './AccountProcessingPolicySection';

const mocks = vi.hoisted(() => ({
  getPolicy: vi.fn(),
  updatePolicy: vi.fn(),
  showNotification: vi.fn(),
  navigate: vi.fn(),
  translate: (key: string, options?: { returnObjects?: boolean }) =>
    options?.returnObjects ? [] : key,
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: mocks.translate,
  }),
}));

vi.mock('react-router-dom', () => ({
  useNavigate: () => mocks.navigate,
}));

vi.mock('@/components/ui/Modal', () => ({
  Modal: ({ children, footer, open }: { children: ReactNode; footer: ReactNode; open: boolean }) =>
    open ? (
      <div>
        {children}
        {footer}
      </div>
    ) : null,
}));

vi.mock('@/hooks/usePanelFeatureAvailability', () => ({
  usePanelFeatureAvailability: () => ({ managerServiceBase: 'http://manager.local:18317' }),
}));

vi.mock('@/stores', () => ({
  useAuthStore: (selector: (state: { managementKey: string }) => unknown) =>
    selector({ managementKey: 'manager-admin-key' }),
  useNotificationStore: () => ({ showNotification: mocks.showNotification }),
}));

vi.mock('@/services/api/usageService', async (importOriginal) => {
  const original = await importOriginal<typeof import('@/services/api/usageService')>();
  return {
    ...original,
    getUsageServiceErrorCode: () => '',
    usageServiceApi: {
      ...original.usageServiceApi,
      getAccountProcessingPolicy: mocks.getPolicy,
      updateAccountProcessingPolicy: mocks.updatePolicy,
    },
  };
});

const policy = (): AccountProcessingPolicy => ({
  source: 'database',
  codexQuotaCooldown: {
    enabled: false,
    configured: false,
    source: 'database',
    locked: false,
    envKey: 'USAGE_QUOTA_COOLDOWN_ENABLED',
    configFileKey: 'quotaCooldownEnabled',
  },
  authIssueQueue: {
    enabled: true,
    configured: true,
    source: 'database',
    locked: false,
    envKey: 'USAGE_ACCOUNT_ACTIONS_ENABLED',
    configFileKey: 'accountActionsEnabled',
  },
  authIssueAutoDisable: {
    enabled: false,
    configured: false,
    source: 'database',
    locked: false,
    envKey: 'USAGE_ACCOUNT_ACTIONS_AUTO_DISABLE',
    configFileKey: 'accountActionsAutoDisable',
    dependsOn: 'authIssueQueue',
  },
});

let renderer: ReactTestRenderer | null = null;

const flush = async () => {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
};

const click = async (node: ReactTestInstance, value?: boolean) => {
  await act(async () => {
    if (value === undefined) {
      node.props.onClick();
    } else {
      node.props.onChange(value);
    }
  });
  await flush();
};

beforeEach(() => {
  vi.clearAllMocks();
  mocks.getPolicy.mockResolvedValue(policy());
  mocks.updatePolicy.mockResolvedValue(policy());
});

afterEach(() => {
  if (renderer) {
    act(() => renderer?.unmount());
    renderer = null;
  }
});

describe('AccountProcessingPolicySection', () => {
  it('persists all three account-processing switches through their explicit patch fields', async () => {
    await act(async () => {
      renderer = create(<AccountProcessingPolicySection />);
    });
    await flush();
    if (!renderer) throw new Error('Account processing policy section was not rendered');
    const mounted = renderer;

    let toggles = mounted.root.findAllByType(ToggleSwitch);
    expect(toggles).toHaveLength(3);

    await click(toggles[0], true);
    toggles = mounted.root.findAllByType(ToggleSwitch);
    await click(toggles[1], false);
    toggles = mounted.root.findAllByType(ToggleSwitch);
    await click(toggles[2], true);

    const confirmButton = mounted.root
      .findAllByType(Button)
      .find((node) => node.props.variant === 'danger');
    if (!confirmButton) throw new Error('Auto-disable confirmation button not found');
    await click(confirmButton);

    expect(mocks.updatePolicy.mock.calls).toEqual([
      ['http://manager.local:18317', 'manager-admin-key', { codexQuotaCooldownEnabled: true }],
      ['http://manager.local:18317', 'manager-admin-key', { authIssueQueueEnabled: false }],
      ['http://manager.local:18317', 'manager-admin-key', { authIssueAutoDisableEnabled: true }],
    ]);
  });
});
