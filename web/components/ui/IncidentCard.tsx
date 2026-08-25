'use client';

import { motion } from 'framer-motion';
import Link from 'next/link';

import { formatAge } from '@/lib/api';
import type { IncidentRecord } from '@/lib/types';

import { CategoryChip, SeverityBadge } from './SeverityBadge';

export function IncidentCard({ record }: { record: IncidentRecord }) {
  const { incident, explanation } = record;

  return (
    <motion.div
      layout
      initial={{ opacity: 0, y: 12 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: 0.22 }}
    >
      <Link
        href={`/incidents/${incident.id}`}
        className="block rounded-xl border border-base-border bg-base-surface p-4 transition hover:border-accent/40 hover:bg-base-raised/40"
      >
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <SeverityBadge severity={incident.severity} />
              <CategoryChip category={incident.category} />
              {incident.resolved && (
                <span className="rounded-md bg-status-ok/10 px-2 py-0.5 text-xs text-status-ok">
                  Resolved
                </span>
              )}
            </div>

            <h3 className="mt-2 truncate text-sm font-medium text-base-text">{incident.title}</h3>

            <p className="mt-1 truncate font-mono text-xs text-base-muted">
              {incident.namespace}/{incident.resource}
              {incident.container ? ` · ${incident.container}` : ''}
            </p>
          </div>

          <div className="shrink-0 text-right">
            <p className="text-xs text-base-faint">{formatAge(incident.detected_at)}</p>
            {incident.count > 1 && (
              <p className="tnum mt-1 text-xs text-base-muted">seen {incident.count}×</p>
            )}
          </div>
        </div>

        {explanation ? (
          <p className="mt-3 line-clamp-2 border-t border-base-border pt-3 text-xs leading-relaxed text-base-muted">
            {explanation.summary}
          </p>
        ) : (
          <p className="mt-3 border-t border-base-border pt-3 text-xs text-base-faint">
            Not yet explained — open to analyse
          </p>
        )}
      </Link>
    </motion.div>
  );
}
