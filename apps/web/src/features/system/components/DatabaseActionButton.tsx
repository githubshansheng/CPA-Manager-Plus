import { useId, type ComponentProps } from 'react';
import { Button } from '@/components/ui/Button';
import styles from './DatabaseManagementPanel.module.scss';

type DatabaseActionButtonProps = ComponentProps<typeof Button> & {
  tip: string;
};

/**
 * Keeps operation guidance available even when the underlying button is
 * disabled. Native disabled buttons do not consistently receive hover or
 * keyboard focus, so the wrapper owns the fallback title and focus target.
 */
export function DatabaseActionButton({
  tip,
  disabled,
  loading,
  ...props
}: DatabaseActionButtonProps) {
  const descriptionId = useId();
  const unavailable = Boolean(disabled || loading);

  return (
    <span
      className={styles.actionTip}
      title={tip}
      tabIndex={unavailable ? 0 : undefined}
      aria-describedby={unavailable ? descriptionId : undefined}
    >
      <Button
        {...props}
        disabled={disabled}
        loading={loading}
        title={tip}
        aria-describedby={descriptionId}
      />
      <span id={descriptionId} className={styles.visuallyHidden}>
        {tip}
      </span>
    </span>
  );
}
