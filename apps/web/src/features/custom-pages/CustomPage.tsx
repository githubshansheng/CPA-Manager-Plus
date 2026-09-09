import { useCallback, useMemo, useState } from 'react';
import { useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { EmptyState } from '@/components/ui/EmptyState';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { usePanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import { collectManagerCustomPageEntries } from './customPages';
import styles from './CustomPage.module.scss';

const safeDecodeURIComponent = (value = '') => {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
};

export function CustomPage() {
  const { t } = useTranslation();
  const params = useParams<{ pageId: string }>();
  const availability = usePanelFeatureAvailability();
  const [frameRevision, setFrameRevision] = useState(0);
  const pageID = useMemo(() => safeDecodeURIComponent(params.pageId), [params.pageId]);
  const pages = useMemo(
    () => collectManagerCustomPageEntries(availability.customPages),
    [availability.customPages]
  );
  const page = useMemo(
    () =>
      pages.find(
        (entry) => entry.id.toLocaleLowerCase('en-US') === pageID.toLocaleLowerCase('en-US')
      ),
    [pageID, pages]
  );
  const refreshFrame = useCallback(() => {
    setFrameRevision((revision) => revision + 1);
  }, []);

  useHeaderRefresh(refreshFrame, Boolean(page));

  return (
    <div className={styles.page}>
      {availability.checking ? (
        <div className={styles.stateShell}>
          <div className={styles.statusPanel}>{t('common.loading')}</div>
        </div>
      ) : !availability.managerServiceAvailable ? (
        <div className={styles.stateShell}>
          <EmptyState
            title={t('custom_page.unavailable')}
            description={t('custom_page.unavailable_desc')}
          />
        </div>
      ) : !page ? (
        <div className={styles.stateShell}>
          <EmptyState
            title={t('custom_page.not_found')}
            description={t('custom_page.not_found_desc')}
          />
        </div>
      ) : (
        <iframe
          key={`${page.id}:${frameRevision}`}
          className={styles.frame}
          src={page.url}
          title={page.title}
          referrerPolicy="no-referrer"
          sandbox="allow-downloads allow-forms allow-modals allow-popups allow-same-origin allow-scripts"
        />
      )}
    </div>
  );
}
