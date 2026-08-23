import type { Category, Severity } from '@/lib/types';
import { CATEGORY_LABELS } from '@/lib/types';

const SEVERITY_STYLES: Record<Severity, string> = {
  critical: 'bg-status-critical/12 text-status-critical ring-status-critical/30',
  warning: 'bg-status-warning/12 text-status-warning ring-status-warning/30',
  info: 'bg-status-info/12 text-status-info ring-status-info/30',
};

const SEVERITY_LABELS: Record<Severity, string> = {
  critical: 'Critical',
  warning: 'Warning',
  info: 'Info',
};

export function SeverityBadge({
  severity,
  className = '',
}: {
  severity: Severity;
  className?: string;
}) {
  const style = SEVERITY_STYLES[severity] ?? SEVERITY_STYLES.info;

  return (
    <span
      className={`inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-medium ring-1 ring-inset ${style} ${className}`}
    >
      <span className="h-1.5 w-1.5 rounded-full bg-current" aria-hidden />
      {SEVERITY_LABELS[severity] ?? severity}
    </span>
  );
}

/** CategoryChip names the failure pattern in plain English, with the
 *  Kubernetes term kept as a tooltip for anyone who wants it. */
export function CategoryChip({ category }: { category: Category }) {
  return (
    <span
      title={category}
      className="inline-flex items-center rounded-md bg-base-raised px-2 py-0.5 text-xs font-medium text-base-muted ring-1 ring-inset ring-base-border"
    >
      {CATEGORY_LABELS[category] ?? category}
    </span>
  );
}
