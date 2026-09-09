import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  pageId: 'status',
  availability: {
    checking: false,
    managerServiceAvailable: true,
    customPages: [
      { id: 'status', title: 'Status board', url: 'https://status.example.test/overview' },
    ],
  },
}));

vi.mock('react-router-dom', () => ({
  useParams: () => ({ pageId: mocks.pageId }),
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock('@/hooks/useHeaderRefresh', () => ({
  useHeaderRefresh: vi.fn(),
}));

vi.mock('@/hooks/usePanelFeatureAvailability', () => ({
  usePanelFeatureAvailability: () => mocks.availability,
}));

const { CustomPage } = await import('./CustomPage');

let renderer: ReactTestRenderer | null = null;

afterEach(() => {
  if (renderer) {
    act(() => renderer?.unmount());
    renderer = null;
  }
  mocks.pageId = 'status';
});

describe('CustomPage', () => {
  it('embeds the configured page with a restrictive iframe boundary', () => {
    act(() => {
      renderer = create(<CustomPage />);
    });

    const frame = renderer?.root.findByType('iframe');
    expect(frame?.props).toMatchObject({
      src: 'https://status.example.test/overview',
      title: 'Status board',
      referrerPolicy: 'no-referrer',
      sandbox:
        'allow-downloads allow-forms allow-modals allow-popups allow-same-origin allow-scripts',
    });
    expect(frame?.props.allow).toBeUndefined();
  });

  it('renders a safe not-found state instead of embedding an unknown id', () => {
    mocks.pageId = 'missing';
    act(() => {
      renderer = create(<CustomPage />);
    });

    expect(renderer?.root.findAllByType('iframe')).toHaveLength(0);
    expect(renderer?.root.findByProps({ className: 'empty-title' }).children).toContain(
      'custom_page.not_found'
    );
  });
});
