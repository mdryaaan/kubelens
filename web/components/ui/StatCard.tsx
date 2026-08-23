'use client';

import { motion } from 'framer-motion';

import type { ReactNode } from 'react';

interface StatCardProps {
  label: string;
  value: ReactNode;
  hint?: string;
  tone?: 'default' | 'critical' | 'warning' | 'ok';
  loading?: boolean;
}

const TONES = {
  default: 'text-base-text',
  critical: 'text-status-critical',
  warning: 'text-status-warning',
  ok: 'text-status-ok',
} as const;

export function StatCard({ label, value, hint, tone = 'default', loading = false }: StatCardProps) {
  return (
    <motion.div
      initial={{ opacity: 0, y: 8 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: 0.25 }}
      className="rounded-xl border border-base-border bg-base-surface p-4"
    >
      <p className="text-xs font-medium uppercase tracking-wide text-base-faint">{label}</p>

      {loading ? (
        <div className="mt-2 h-8 w-20 overflow-hidden rounded bg-base-raised">
          <div className="h-full w-full -translate-x-full animate-shimmer bg-gradient-to-r from-transparent via-base-border to-transparent" />
        </div>
      ) : (
        <p className={`tnum mt-1 text-3xl font-semibold ${TONES[tone]}`}>{value}</p>
      )}

      {hint && <p className="mt-1 text-xs text-base-muted">{hint}</p>}
    </motion.div>
  );
}
