import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import type { ComponentProps } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { Input } from '@/components/ui/Input';
import { Select } from '@/components/ui/Select';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { ManagerConfigPanel } from './ManagerConfigPanel';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock('./AccountProcessingPolicySection', () => ({
  AccountProcessingPolicySection: () => <div data-test="account-processing-policy" />,
}));

const buildProps = (): ComponentProps<typeof ManagerConfigPanel> => ({
  managerLoading: false,
  managerSaving: false,
  panelHostedByUsageService: true,
  detectedPanelBase: 'http://manager.local:18317',
  managerRuntimeModeLabel: 'embedded',
  managerHasBoundCPAManagementKey: true,
  managerCPABaseInput: 'http://cpa.local:8317',
  managerCPAManagementKeyInput: '',
  managerCPAManagementKeyVisible: false,
  managerBoundCPABase: 'http://cpa.local:8317',
  disableControls: false,
  canConfigureRequestMonitoring: true,
  managerRequestMonitoringEnabled: true,
  managerCollectorMode: 'auto',
  managerCollectorModeOptions: [
    { value: 'auto', label: 'Auto' },
    { value: 'http', label: 'HTTP' },
  ],
  managerPollIntervalMs: '500',
  managerBatchSize: '100',
  managerQueryLimit: '50000',
  managerRetentionSeconds: 60,
  managerConfigSourceLabel: 'database',
  managerUsageStatisticsEnabled: true,
  managerCustomPages: [],
  onRefresh: vi.fn(),
  onRequestMonitoringChange: vi.fn(),
  onCPABaseInputChange: vi.fn(),
  onCPAManagementKeyInputChange: vi.fn(),
  onCPAManagementKeyClear: vi.fn(),
  onCPAManagementKeyVisibilityToggle: vi.fn(),
  onCollectorModeChange: vi.fn(),
  onPollIntervalMsChange: vi.fn(),
  onBatchSizeChange: vi.fn(),
  onQueryLimitChange: vi.fn(),
  onManagerCustomPagesChange: vi.fn(),
});

let renderer: ReactTestRenderer | null = null;

const renderPanel = (props: ComponentProps<typeof ManagerConfigPanel>) => {
  act(() => {
    renderer = create(<ManagerConfigPanel {...props} />);
  });
  return renderer;
};

const inputByLabel = (label: string) => {
  const input = renderer?.root.findAllByType(Input).find((node) => node.props.label === label);
  if (!input) throw new Error(`Input not found: ${label}`);
  return input;
};

afterEach(() => {
  if (renderer) {
    act(() => renderer?.unmount());
    renderer = null;
  }
});

describe('ManagerConfigPanel', () => {
  it('wires every editable connection and collector field to its callback', () => {
    const props = buildProps();
    renderPanel(props);

    act(() => {
      inputByLabel('config_management.manager.cpa_base_url_label').props.onChange({
        target: { value: 'http://cpa-next.local:8317' },
      });
      inputByLabel('config_management.manager.cpa_management_key_label').props.onChange({
        target: { value: 'next-key' },
      });
      inputByLabel('config_management.manager.poll_interval_ms').props.onChange({
        target: { value: '750' },
      });
      inputByLabel('config_management.manager.batch_size').props.onChange({
        target: { value: '250' },
      });
      inputByLabel('config_management.manager.query_limit').props.onChange({
        target: { value: '60000' },
      });
      renderer?.root.findByType(Select).props.onChange('http');
      renderer?.root.findByType(ToggleSwitch).props.onChange(false);
    });

    expect(props.onCPABaseInputChange).toHaveBeenCalledWith('http://cpa-next.local:8317');
    expect(props.onCPAManagementKeyInputChange).toHaveBeenCalledWith('next-key');
    expect(props.onPollIntervalMsChange).toHaveBeenCalledWith('750');
    expect(props.onBatchSizeChange).toHaveBeenCalledWith('250');
    expect(props.onQueryLimitChange).toHaveBeenCalledWith('60000');
    expect(props.onCollectorModeChange).toHaveBeenCalledWith('http');
    expect(props.onRequestMonitoringChange).toHaveBeenCalledWith(false);
  });

  it('locks every editable field while a Manager config save is in progress', () => {
    renderPanel({ ...buildProps(), managerSaving: true });

    expect(renderer?.root.findAllByType(Input).every((node) => node.props.disabled)).toBe(true);
    expect(renderer?.root.findByType(Select).props.disabled).toBe(true);
    expect(renderer?.root.findByType(ToggleSwitch).props.disabled).toBe(true);
  });
});
