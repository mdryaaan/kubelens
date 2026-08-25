'use client';

import { useEffect, useState } from 'react';

import { TopBar } from '@/components/layout/TopBar';
import { useTheme } from '@/hooks/useTheme';
import { api, formatDuration } from '@/lib/api';
import type { Health, Settings } from '@/lib/types';

export default function SettingsPage() {
  const [settings, setSettings] = useState<Settings | null>(null);
  const [health, setHealth] = useState<Health | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const { theme, setTheme, mounted } = useTheme();

  useEffect(() => {
    Promise.all([api.settings(), api.health()])
      .then(([nextSettings, nextHealth]) => {
        setSettings(nextSettings);
        setHealth(nextHealth);
        setError(null);
      })
      .catch((caught: Error) => setError(caught.message))
      .finally(() => setLoading(false));
  }, []);

  return (
    <>
      <TopBar title="Settings" subtitle={settings?.source} />

      <main className="flex-1 space-y-6 p-4 md:p-6">
        {error && (
          <div className="rounded-xl border border-status-critical/30 bg-status-critical/10 p-4 text-sm text-status-critical">
            {error}
          </div>
        )}

        {loading ? (
          <div className="h-48 animate-pulse rounded-xl bg-base-raised" />
        ) : (
          <>
            {/* These are read-only on purpose. They are process-level choices
                made when the server started; letting a browser tab change how a
                cluster is watched would be a surprising amount of authority for
                a dashboard to hold. */}
            <Panel
              title="Cluster source"
              hint="Set with --demo or --kubeconfig when the server starts"
            >
              <Row label="Mode" value={settings?.mode ?? '—'} />
              <Row label="Source" value={settings?.source ?? '—'} mono />
              <Row
                label="Pods watched"
                value={
                  health?.cluster_known
                    ? `${health.cluster.total_pods} across ${health.cluster.nodes} node(s)`
                    : 'unknown'
                }
              />
              <Row label="Pending threshold" value={settings?.pending_threshold ?? '—'} />
              <Row label="Incident cooldown" value={settings?.cooldown ?? '—'} />
            </Panel>

            <Panel
              title="Explanation provider"
              hint="Set with --provider and --model when the server starts"
            >
              {settings?.disclaimer && (
                <p className="mb-3 rounded-lg border border-status-warning/25 bg-status-warning/10 px-3 py-2 text-xs text-status-warning">
                  {settings.disclaimer}
                </p>
              )}
              <Row label="Provider" value={settings?.provider ?? '—'} />
              <Row label="Model" value={settings?.model || 'not configured'} mono />
              <Row
                label="Explaining incidents"
                value={settings?.explain_enabled ? 'yes' : 'no — detection only'}
              />
              <Row
                label="Fabricated citations dropped"
                value={String(health?.stats.fabricated_citations ?? 0)}
              />
            </Panel>

            <Panel title="Detection" hint="Six deterministic rules, always on">
              <ul className="flex flex-wrap gap-2">
                {(settings?.categories ?? []).map((category) => (
                  <li
                    key={category}
                    className="rounded-md bg-base-raised px-2.5 py-1 font-mono text-xs text-base-muted"
                  >
                    {category}
                  </li>
                ))}
              </ul>
              <p className="mt-3 text-xs text-base-faint">
                Detection is deterministic and does not depend on any model being available.
              </p>
            </Panel>

            <Panel title="Appearance">
              <div className="flex gap-2">
                {(['dark', 'light'] as const).map((option) => (
                  <button
                    key={option}
                    type="button"
                    onClick={() => setTheme(option)}
                    className={`rounded-lg border px-3 py-1.5 text-sm capitalize transition ${
                      mounted && theme === option
                        ? 'border-accent bg-accent-soft text-base-text'
                        : 'border-base-border text-base-muted hover:bg-base-raised'
                    }`}
                  >
                    {option}
                  </button>
                ))}
              </div>
            </Panel>

            <Panel title="Server">
              <Row label="Uptime" value={formatDuration(health?.uptime_ms ?? 0)} />
              <Row label="Connected dashboards" value={String(health?.subscribers ?? 0)} />
              <Row label="Incidents stored" value={String(health?.stats.total_incidents ?? 0)} />
              <Row label="Explained" value={String(health?.stats.explained ?? 0)} />
            </Panel>
          </>
        )}
      </main>
    </>
  );
}

function Panel({
  title,
  hint,
  children,
}: {
  title: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <section className="rounded-xl border border-base-border bg-base-surface p-5">
      <div className="mb-4">
        <h2 className="text-sm font-semibold">{title}</h2>
        {hint && <p className="mt-0.5 text-xs text-base-faint">{hint}</p>}
      </div>
      <div className="space-y-2">{children}</div>
    </section>
  );
}

function Row({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex flex-wrap items-baseline justify-between gap-2 border-b border-base-border/60 pb-2 last:border-0 last:pb-0">
      <span className="text-xs text-base-muted">{label}</span>
      <span className={`text-xs text-base-text ${mono ? 'break-all font-mono' : ''}`}>{value}</span>
    </div>
  );
}
