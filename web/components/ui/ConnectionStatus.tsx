'use client';

import type { ConnectionState } from '@/hooks/useIncidentStream';

const COPY: Record<ConnectionState, { label: string; className: string; pulse: boolean }> = {
  live: { label: 'Live', className: 'text-status-ok', pulse: true },
  connecting: { label: 'Connecting', className: 'text-status-warning', pulse: false },
  offline: { label: 'Offline', className: 'text-status-critical', pulse: false },
};

/**
 * Shows whether the incident stream is connected.
 *
 * Worth its own component because a dashboard that has silently stopped
 * receiving updates looks exactly like a cluster that has stopped having
 * problems — and those two are the most different states there are.
 */
export function ConnectionStatus({ state }: { state: ConnectionState }) {
  const copy = COPY[state];

  return (
    <div
      className={`inline-flex items-center gap-2 text-xs font-medium ${copy.className}`}
      role="status"
      aria-live="polite"
    >
      <span className="relative flex h-2 w-2">
        {copy.pulse && (
          <span className="absolute inline-flex h-full w-full animate-pulse-ring rounded-full bg-current" />
        )}
        <span className="relative inline-flex h-2 w-2 rounded-full bg-current" />
      </span>
      {copy.label}
    </div>
  );
}
