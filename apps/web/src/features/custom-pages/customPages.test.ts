import { describe, expect, it, vi } from 'vitest';
import {
  buildManagerCustomPageRoute,
  collectManagerCustomPageEntries,
  createManagerCustomPage,
  isAllowedManagerCustomPageURL,
  managerCustomPagesEqual,
  normalizeManagerCustomPages,
  validateManagerCustomPages,
} from './customPages';

describe('manager custom pages', () => {
  it('normalizes values, preserves order, and builds stable encoded routes', () => {
    expect(
      collectManagerCustomPageEntries([
        { id: ' status ', title: ' Status board ', url: ' https://status.example.test/path ' },
        { id: 'docs_zh', title: '文档', url: 'http://docs.example.test' },
      ])
    ).toEqual([
      {
        id: 'status',
        title: 'Status board',
        url: 'https://status.example.test/path',
        route: '/custom-pages/status',
      },
      {
        id: 'docs_zh',
        title: '文档',
        url: 'http://docs.example.test',
        route: '/custom-pages/docs_zh',
      },
    ]);
    expect(buildManagerCustomPageRoute('page one')).toBe('/custom-pages/page%20one');
  });

  it('only accepts HTTP(S) URLs without embedded credentials', () => {
    expect(isAllowedManagerCustomPageURL('https://example.test/path?q=1')).toBe(true);
    expect(isAllowedManagerCustomPageURL('http://127.0.0.1:9000')).toBe(true);
    expect(isAllowedManagerCustomPageURL('javascript:alert(1)')).toBe(false);
    expect(isAllowedManagerCustomPageURL('data:text/html,hello')).toBe(false);
    expect(isAllowedManagerCustomPageURL('https://user:secret@example.test')).toBe(false);
    expect(isAllowedManagerCustomPageURL('/relative')).toBe(false);
  });

  it('reports duplicate ids and field-specific validation errors', () => {
    expect(
      validateManagerCustomPages([
        { id: 'Page', title: 'One', url: 'https://one.example.test' },
        { id: 'page', title: 'Two', url: 'https://two.example.test' },
      ])
    ).toEqual({ code: 'duplicate_id', index: 1 });
    expect(validateManagerCustomPages([{ id: 'page', title: '', url: '' }])).toEqual({
      code: 'title_required',
      index: 0,
    });
    expect(
      validateManagerCustomPages([
        { id: 'page', title: 'Page', url: 'https://user:secret@example.test' },
      ])
    ).toEqual({ code: 'url_credentials', index: 0 });
  });

  it('compares canonical values and creates collision-resistant ids', () => {
    expect(
      managerCustomPagesEqual(
        [{ id: ' page ', title: ' Page ', url: ' https://example.test ' }],
        [{ id: 'page', title: 'Page', url: 'https://example.test' }]
      )
    ).toBe(true);

    const randomUUID = vi
      .spyOn(globalThis.crypto, 'randomUUID')
      .mockReturnValue('00000000-0000-4000-8000-000000000000');
    expect(
      createManagerCustomPage([
        {
          id: 'page-00000000-0000-4000-8000-000000000000',
          title: 'Old',
          url: 'https://old.test',
        },
      ])
    ).toEqual({ id: 'page-00000000-0000-4000-8000-000000000000-2', title: '', url: '' });
    randomUUID.mockRestore();
    expect(normalizeManagerCustomPages(undefined)).toEqual([]);
  });
});
