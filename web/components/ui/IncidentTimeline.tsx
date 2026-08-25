'use client';

import { AnimatePresence, motion } from 'framer-motion';
import Link from 'next/link';

import { formatAge } from '@/lib/api';
import type { IncidentRecord, Severity } from '@/lib/types';
import { CATEGORY_LABELS } from '@/lib/types';

const DOT: Record<Severity, string> = {
  critical: 'bg-status-critical',
  warning: 'bg-status-warning',
  info: 'bg-status-info',
};

/**
 * The live feed on the overview page.
 *
 * Deliberately a timeline rather than a table: on the overview the question is
 * "what is happening", which is a sequence. The table on the incidents page
 * answers "what happened", which is a set to filter.
 */
export function IncidentTimeline({
  records,
  loading = false,
  emptyHint,
}: {
  records: IncidentRecord[];
  loading?: boolean;
  emptyHint?: string;
}) {
  if (loading) {
    return (
      <ul className="space-y-3">
        {[0, 1, 2].map((index) => (
          <li key={index} className="h-16 animate-pulse rounded-lg bg-base-raised" />
        ))}
      </ul>
    );
  }

  if (records.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center rounded-xl border border-dashed border-base-border py-12 text-center">
        <p className="text-sm text-base-muted">No incidents yet</p>
        <p className="mt-1 max-w-sm text-xs text-base-faint">
          {emptyHint ?? 'Nothing in the cluster is failing. New incidents appear here the moment they are detected.'}
        </p>
      </div>
    );
  }

  return (
    <ol className="relative space-y-1 pl-5">
      <span
        className="absolute bottom-2 left-[6px] top-2 w-px bg-base-border"
        aria-hidden
      />

      <AnimatePresence initial={false}>
        {records.map((record) => (
          <motion.li
            key={record.incident.id}
            layout
            initial={{ opacity: 0, x: -8 }}
            animate={{ opacity: 1, x: 0 }}
            exit={{ opacity: 0 }}
            transition={{ duration: 0.2 }}
            className="relative"
          >
            <span
              className={`absolute -left-5 top-3.5 h-3 w-3 rounded-full ring-4 ring-base-bg ${
                DOT[record.incident.severity] ?? DOT.info
              }`}
              aria-hidden
            />

            <Link
              href={`/incidents/${record.incident.id}`}
              className="block rounded-lg px-3 py-2.5 transition hover:bg-base-raised/60"
            >
              <div className="flex items-baseline justify-between gap-3">
                <p className="truncate text-sm text-base-text">{record.incident.title}</p>
                <span className="shrink-0 text-xs text-base-faint">
                  {formatAge(record.incident.detected_at)}
                </span>
              </div>
              <p className="mt-0.5 truncate font-mono text-xs text-base-muted">
                {CATEGORY_LABELS[record.incident.category] ?? record.incident.category} ·{' '}
                {record.incident.namespace}/{record.incident.resource}
              </p>
            </Link>
          </motion.li>
        ))}
      </AnimatePresence>
    </ol>
  );
}
