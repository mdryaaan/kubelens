'use client';

import { useTheme } from '@/hooks/useTheme';
import type { ConnectionState } from '@/hooks/useIncidentStream';

import { ConnectionStatus } from '../ui/ConnectionStatus';

interface Props {
  title: string;
  subtitle?: string;
  connection?: ConnectionState;
}

export function TopBar({ title, subtitle, connection }: Props) {
  const { theme, toggle, mounted } = useTheme();

  return (
    <header className="flex h-14 items-center justify-between gap-4 border-b border-base-border px-4 md:px-6">
      <div className="min-w-0">
        <h1 className="truncate text-sm font-semibold">{title}</h1>
        {subtitle && <p className="truncate text-xs text-base-muted">{subtitle}</p>}
      </div>

      <div className="flex shrink-0 items-center gap-4">
        {connection && <ConnectionStatus state={connection} />}

        {/* The theme toggle lives here rather than buried in settings: it is a
            comfort control, and comfort controls belong where the eye already is. */}
        <button
          type="button"
          onClick={toggle}
          aria-label={`Switch to ${theme === 'dark' ? 'light' : 'dark'} theme`}
          className="rounded-lg border border-base-border px-2.5 py-1.5 text-xs text-base-muted transition hover:bg-base-raised hover:text-base-text"
        >
          {!mounted ? '—' : theme === 'dark' ? 'Light' : 'Dark'}
        </button>
      </div>
    </header>
  );
}
