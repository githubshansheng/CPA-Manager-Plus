import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { IconChevronDown, IconChevronUp, IconPlus, IconTrash2 } from '@/components/ui/icons';
import type { ManagerCustomPageConfig } from '@/services/api/usageService';
import {
  createManagerCustomPage,
  MANAGER_CUSTOM_PAGE_MAX_COUNT,
  MANAGER_CUSTOM_PAGE_MAX_TITLE_LENGTH,
  MANAGER_CUSTOM_PAGE_MAX_URL_LENGTH,
  validateManagerCustomPages,
  type ManagerCustomPageValidationCode,
} from '@/features/custom-pages/customPages';
import styles from '../ConfigPage.module.scss';

type CustomMenuConfigSectionProps = {
  pages: ManagerCustomPageConfig[];
  disabled: boolean;
  onChange: (pages: ManagerCustomPageConfig[]) => void;
};

const fieldErrorCode = (
  page: ManagerCustomPageConfig,
  field: 'title' | 'url'
): ManagerCustomPageValidationCode | null => {
  const error = validateManagerCustomPages([page]);
  if (!error) return null;
  if (field === 'title' && (error.code === 'title_required' || error.code === 'title_too_long')) {
    return error.code;
  }
  if (
    field === 'url' &&
    ['url_required', 'url_too_long', 'url_invalid', 'url_credentials'].includes(error.code)
  ) {
    return error.code;
  }
  return null;
};

export function CustomMenuConfigSection({
  pages,
  disabled,
  onChange,
}: CustomMenuConfigSectionProps) {
  const { t } = useTranslation();
  const listError = useMemo(() => validateManagerCustomPages(pages), [pages]);
  const updatePage = (index: number, patch: Partial<ManagerCustomPageConfig>) => {
    onChange(pages.map((page, pageIndex) => (pageIndex === index ? { ...page, ...patch } : page)));
  };
  const movePage = (index: number, offset: -1 | 1) => {
    const targetIndex = index + offset;
    if (targetIndex < 0 || targetIndex >= pages.length) return;
    const next = [...pages];
    [next[index], next[targetIndex]] = [next[targetIndex], next[index]];
    onChange(next);
  };
  const removePage = (index: number) => {
    onChange(pages.filter((_, pageIndex) => pageIndex !== index));
  };
  const addPage = () => {
    if (pages.length >= MANAGER_CUSTOM_PAGE_MAX_COUNT) return;
    onChange([...pages, createManagerCustomPage(pages)]);
  };
  const validationMessage = (code: ManagerCustomPageValidationCode) =>
    t(`config_management.manager.custom_menu_validation_${code}`, {
      index: (listError?.index ?? 0) + 1,
      maxCount: MANAGER_CUSTOM_PAGE_MAX_COUNT,
      maxTitleLength: MANAGER_CUSTOM_PAGE_MAX_TITLE_LENGTH,
      maxUrlLength: MANAGER_CUSTOM_PAGE_MAX_URL_LENGTH,
    });

  return (
    <section className={styles.managerSection} id="custom-menu-config">
      <div className={styles.managerSectionHeader}>
        <div>
          <h3>{t('config_management.manager.custom_menu_title')}</h3>
          <p>{t('config_management.manager.custom_menu_hint')}</p>
        </div>
        <Button
          type="button"
          variant="secondary"
          size="sm"
          className={styles.customMenuAddButton}
          onClick={addPage}
          disabled={disabled || pages.length >= MANAGER_CUSTOM_PAGE_MAX_COUNT}
          aria-label={t('config_management.manager.custom_menu_add')}
          title={
            pages.length >= MANAGER_CUSTOM_PAGE_MAX_COUNT
              ? validationMessage('too_many')
              : t('config_management.manager.custom_menu_add')
          }
        >
          <span className={styles.customMenuAddLabel}>
            <IconPlus size={16} aria-hidden="true" />
            {t('config_management.manager.custom_menu_add')}
          </span>
        </Button>
      </div>

      {pages.length === 0 ? (
        <div className={styles.customMenuEmpty}>
          <strong>{t('config_management.manager.custom_menu_empty')}</strong>
          <span>{t('config_management.manager.custom_menu_empty_hint')}</span>
        </div>
      ) : (
        <div className={styles.customMenuList} role="list">
          {pages.map((page, index) => {
            const titleError = fieldErrorCode(page, 'title');
            const urlError = fieldErrorCode(page, 'url');
            return (
              <article
                key={`${page.id || 'custom-page'}-${index}`}
                className={styles.customMenuItem}
                role="listitem"
              >
                <div className={styles.customMenuItemHeader}>
                  <strong>
                    {t('config_management.manager.custom_menu_item', { index: index + 1 })}
                  </strong>
                  <div className={styles.customMenuItemActions}>
                    <Button
                      type="button"
                      variant="ghost"
                      size="xs"
                      iconOnly
                      onClick={() => movePage(index, -1)}
                      disabled={disabled || index === 0}
                      aria-label={t('config_management.manager.custom_menu_move_up', {
                        index: index + 1,
                      })}
                      title={t('config_management.manager.custom_menu_move_up', {
                        index: index + 1,
                      })}
                    >
                      <IconChevronUp size={16} aria-hidden="true" />
                    </Button>
                    <Button
                      type="button"
                      variant="ghost"
                      size="xs"
                      iconOnly
                      onClick={() => movePage(index, 1)}
                      disabled={disabled || index === pages.length - 1}
                      aria-label={t('config_management.manager.custom_menu_move_down', {
                        index: index + 1,
                      })}
                      title={t('config_management.manager.custom_menu_move_down', {
                        index: index + 1,
                      })}
                    >
                      <IconChevronDown size={16} aria-hidden="true" />
                    </Button>
                    <Button
                      type="button"
                      variant="ghost"
                      size="xs"
                      iconOnly
                      onClick={() => removePage(index)}
                      disabled={disabled}
                      aria-label={t('config_management.manager.custom_menu_delete', {
                        index: index + 1,
                      })}
                      title={t('config_management.manager.custom_menu_delete', {
                        index: index + 1,
                      })}
                    >
                      <IconTrash2 size={16} aria-hidden="true" />
                    </Button>
                  </div>
                </div>
                <div className={styles.customMenuFields}>
                  <Input
                    label={t('config_management.manager.custom_menu_name')}
                    value={page.title}
                    maxLength={MANAGER_CUSTOM_PAGE_MAX_TITLE_LENGTH}
                    placeholder={t('config_management.manager.custom_menu_name_placeholder')}
                    onChange={(event) => updatePage(index, { title: event.target.value })}
                    disabled={disabled}
                    required
                    error={titleError ? validationMessage(titleError) : undefined}
                  />
                  <Input
                    label={t('config_management.manager.custom_menu_url')}
                    type="url"
                    inputMode="url"
                    value={page.url}
                    maxLength={MANAGER_CUSTOM_PAGE_MAX_URL_LENGTH}
                    placeholder="https://example.com/dashboard"
                    onChange={(event) => updatePage(index, { url: event.target.value })}
                    disabled={disabled}
                    autoCapitalize="none"
                    autoCorrect="off"
                    spellCheck={false}
                    required
                    error={urlError ? validationMessage(urlError) : undefined}
                  />
                </div>
              </article>
            );
          })}
        </div>
      )}

      {listError && ['too_many', 'invalid_id', 'duplicate_id'].includes(listError.code) ? (
        <div className={styles.customMenuValidationSummary} role="alert">
          {validationMessage(listError.code)}
        </div>
      ) : null}
      <div className={styles.customMenuSecurityHint}>
        {t('config_management.manager.custom_menu_security_hint')}
      </div>
    </section>
  );
}
