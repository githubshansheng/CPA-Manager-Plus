import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import type { ManagerCustomPageConfig } from '@/services/api/usageService';
import { CustomMenuConfigSection } from './CustomMenuConfigSection';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

const pages: ManagerCustomPageConfig[] = [
  { id: 'status', title: 'Status', url: 'https://status.example.test' },
  { id: 'docs', title: 'Docs', url: 'https://docs.example.test' },
];

let renderer: ReactTestRenderer | null = null;

afterEach(() => {
  if (renderer) {
    act(() => renderer?.unmount());
    renderer = null;
  }
});

const renderSection = (disabled = false) => {
  const onChange = vi.fn();
  act(() => {
    renderer = create(
      <CustomMenuConfigSection pages={pages} disabled={disabled} onChange={onChange} />
    );
  });
  return onChange;
};

describe('CustomMenuConfigSection', () => {
  it('edits, reorders, deletes, and adds submenu items through one controlled callback', () => {
    const onChange = renderSection();
    const nameInputs = renderer?.root
      .findAllByType(Input)
      .filter((node) => node.props.label === 'config_management.manager.custom_menu_name');

    act(() => {
      nameInputs?.[0].props.onChange({ target: { value: 'Service health' } });
    });
    expect(onChange).toHaveBeenLastCalledWith([{ ...pages[0], title: 'Service health' }, pages[1]]);

    const moveDown = renderer?.root
      .findAllByType(Button)
      .find(
        (node) => node.props['aria-label'] === 'config_management.manager.custom_menu_move_down'
      );
    act(() => moveDown?.props.onClick());
    expect(onChange).toHaveBeenLastCalledWith([pages[1], pages[0]]);

    const deleteButton = renderer?.root
      .findAllByType(Button)
      .find((node) => node.props['aria-label'] === 'config_management.manager.custom_menu_delete');
    act(() => deleteButton?.props.onClick());
    expect(onChange).toHaveBeenLastCalledWith([pages[1]]);

    const addButton = renderer?.root
      .findAllByType(Button)
      .find((node) => node.props['aria-label'] === 'config_management.manager.custom_menu_add');
    act(() => addButton?.props.onClick());
    const added = onChange.mock.calls[
      onChange.mock.calls.length - 1
    ]?.[0] as ManagerCustomPageConfig[];
    expect(added).toHaveLength(3);
    expect(added[2]).toMatchObject({ title: '', url: '' });
    expect(added[2].id).toMatch(/^page-/);
  });

  it('locks every custom-menu control while the Manager config is busy', () => {
    renderSection(true);

    expect(renderer?.root.findAllByType(Input).every((node) => node.props.disabled)).toBe(true);
    expect(renderer?.root.findAllByType(Button).every((node) => node.props.disabled)).toBe(true);
  });
});
