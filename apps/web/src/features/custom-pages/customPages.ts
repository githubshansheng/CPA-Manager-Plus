import type { ManagerCustomPageConfig } from '@/services/api/usageService';

export const MANAGER_CUSTOM_PAGE_MAX_COUNT = 50;
export const MANAGER_CUSTOM_PAGE_MAX_ID_LENGTH = 64;
export const MANAGER_CUSTOM_PAGE_MAX_TITLE_LENGTH = 80;
export const MANAGER_CUSTOM_PAGE_MAX_URL_LENGTH = 2048;

const CUSTOM_PAGE_ID_PATTERN = /^[a-zA-Z0-9][a-zA-Z0-9_-]*$/;

export type ManagerCustomPageValidationCode =
  | 'too_many'
  | 'invalid_id'
  | 'duplicate_id'
  | 'title_required'
  | 'title_too_long'
  | 'url_required'
  | 'url_too_long'
  | 'url_invalid'
  | 'url_credentials';

export interface ManagerCustomPageValidationError {
  code: ManagerCustomPageValidationCode;
  index?: number;
}

export interface ManagerCustomPageEntry extends ManagerCustomPageConfig {
  route: string;
}

export const normalizeManagerCustomPages = (
  pages: readonly ManagerCustomPageConfig[] | null | undefined
): ManagerCustomPageConfig[] =>
  (pages ?? []).map((page) => ({
    id: page.id.trim(),
    title: page.title.trim(),
    url: page.url.trim(),
  }));

export const isAllowedManagerCustomPageURL = (value: string): boolean => {
  const trimmed = value.trim();
  if (!trimmed || trimmed.length > MANAGER_CUSTOM_PAGE_MAX_URL_LENGTH) return false;

  try {
    const parsed = new URL(trimmed);
    return (
      (parsed.protocol === 'http:' || parsed.protocol === 'https:') &&
      Boolean(parsed.hostname) &&
      !parsed.username &&
      !parsed.password
    );
  } catch {
    return false;
  }
};

export const validateManagerCustomPages = (
  pages: readonly ManagerCustomPageConfig[]
): ManagerCustomPageValidationError | null => {
  if (pages.length > MANAGER_CUSTOM_PAGE_MAX_COUNT) return { code: 'too_many' };

  const seenIDs = new Set<string>();
  for (const [index, rawPage] of pages.entries()) {
    const page = normalizeManagerCustomPages([rawPage])[0];
    if (
      !page.id ||
      page.id.length > MANAGER_CUSTOM_PAGE_MAX_ID_LENGTH ||
      !CUSTOM_PAGE_ID_PATTERN.test(page.id)
    ) {
      return { code: 'invalid_id', index };
    }
    const canonicalID = page.id.toLocaleLowerCase('en-US');
    if (seenIDs.has(canonicalID)) return { code: 'duplicate_id', index };
    seenIDs.add(canonicalID);

    if (!page.title) return { code: 'title_required', index };
    if (Array.from(page.title).length > MANAGER_CUSTOM_PAGE_MAX_TITLE_LENGTH) {
      return { code: 'title_too_long', index };
    }
    if (!page.url) return { code: 'url_required', index };
    if (page.url.length > MANAGER_CUSTOM_PAGE_MAX_URL_LENGTH) {
      return { code: 'url_too_long', index };
    }
    try {
      const parsed = new URL(page.url);
      if (parsed.username || parsed.password) return { code: 'url_credentials', index };
    } catch {
      return { code: 'url_invalid', index };
    }
    if (!isAllowedManagerCustomPageURL(page.url)) return { code: 'url_invalid', index };
  }
  return null;
};

export const buildManagerCustomPageRoute = (id: string): string =>
  `/custom-pages/${encodeURIComponent(id)}`;

export const collectManagerCustomPageEntries = (
  pages: readonly ManagerCustomPageConfig[] | null | undefined
): ManagerCustomPageEntry[] => {
  const normalized = normalizeManagerCustomPages(pages);
  if (validateManagerCustomPages(normalized)) return [];
  return normalized.map((page) => ({ ...page, route: buildManagerCustomPageRoute(page.id) }));
};

export const createManagerCustomPage = (
  existing: readonly ManagerCustomPageConfig[]
): ManagerCustomPageConfig => {
  const existingIDs = new Set(existing.map((page) => page.id.trim().toLocaleLowerCase('en-US')));
  const randomUUID = globalThis.crypto?.randomUUID?.();
  const baseID = randomUUID
    ? `page-${randomUUID}`
    : `page-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
  let id = baseID;
  let suffix = 2;
  while (existingIDs.has(id.toLocaleLowerCase('en-US'))) {
    id = `${baseID}-${suffix}`;
    suffix += 1;
  }
  return { id, title: '', url: '' };
};

export const managerCustomPagesEqual = (
  left: readonly ManagerCustomPageConfig[] | null | undefined,
  right: readonly ManagerCustomPageConfig[] | null | undefined
): boolean =>
  JSON.stringify(normalizeManagerCustomPages(left)) ===
  JSON.stringify(normalizeManagerCustomPages(right));
