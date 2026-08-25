'use client';

import { useMemo, useState } from 'react';

import { TopBar } from '@/components/layout/TopBar';
import { IncidentCard } from '@/components/ui/IncidentCard';
import { useIncidentStream } from '@/hooks/useIncidentStream';
import { CATEGORIES, CATEGORY_LABELS, type Category, type Severity } from '@/lib/types';

type SortKey = 'newest' | 'oldest' | 'severity';

const SEVERITY_ORDER: Record<Severity, number> = { critical: 3, warning: 2, info: 1 };

export default function IncidentsPage() {
  const { incidents, connection, loading, error } = useIncidentStream(200);

  const [category, setCategory] = useState<Category | ''>('');
  const [severity, setSeverity] = useState<Severity | ''>('');
  const [query, setQuery] = useState('');
  const [openOnly, setOpenOnly] = useState(false);
  const [sort, setSort] = useState<SortKey>('newest');

  // Filtering client-side because the stream already holds the working set, and
  // a round trip per keystroke would make the filters feel worse than they are.
  const visible = useMemo(() => {
    const needle = query.trim().toLowerCase();

    const filtered = incidents.filter(({ incident }) => {
      if (category && incident.category !== category) return false;
      if (severity && incident.severity !== severity) return false;
      if (openOnly && incident.resolved) return false;
      if (!needle) return true;

      return (
        incident.title.toLowerCase().includes(needle) ||
        incident.resource.toLowerCase().includes(needle) ||
        incident.namespace.toLowerCase().includes(needle)
      );
    });

    return [...filtered].sort((a, b) => {
      if (sort === 'severity') {
        const delta =
          (SEVERITY_ORDER[b.incident.severity] ?? 0) - (SEVERITY_ORDER[a.incident.severity] ?? 0);
        if (delta !== 0) return delta;
      }
      const left = Date.parse(a.incident.detected_at);
      const right = Date.parse(b.incident.detected_at);
      return sort === 'oldest' ? left - right : right - left;
    });
  }, [incidents, category, severity, query, openOnly, sort]);

  return (
    <>
      <TopBar
        title="Incidents"
        subtitle={`${visible.length} of ${incidents.length} shown`}
        connection={connection}
      />

      <main className="flex-1 space-y-4 p-4 md:p-6">
        <section className="flex flex-wrap items-center gap-2 rounded-xl border border-base-border bg-base-surface p-3">
          <input
            type="search"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="Search title, namespace, or resource"
            className="min-w-0 flex-1 rounded-lg border border-base-border bg-base-raised px-3 py-1.5 text-sm placeholder:text-base-faint focus:border-accent focus:outline-none"
          />

          <select
            value={category}
            onChange={(event) => setCategory(event.target.value as Category | '')}
            className="rounded-lg border border-base-border bg-base-raised px-2.5 py-1.5 text-sm"
          >
            <option value="">All categories</option>
            {CATEGORIES.map((value) => (
              <option key={value} value={value}>
                {CATEGORY_LABELS[value]}
              </option>
            ))}
          </select>

          <select
            value={severity}
            onChange={(event) => setSeverity(event.target.value as Severity | '')}
            className="rounded-lg border border-base-border bg-base-raised px-2.5 py-1.5 text-sm"
          >
            <option value="">All severities</option>
            <option value="critical">Critical</option>
            <option value="warning">Warning</option>
            <option value="info">Info</option>
          </select>

          <select
            value={sort}
            onChange={(event) => setSort(event.target.value as SortKey)}
            className="rounded-lg border border-base-border bg-base-raised px-2.5 py-1.5 text-sm"
          >
            <option value="newest">Newest first</option>
            <option value="oldest">Oldest first</option>
            <option value="severity">Most severe first</option>
          </select>

          <label className="flex cursor-pointer select-none items-center gap-2 px-1 text-sm text-base-muted">
            <input
              type="checkbox"
              checked={openOnly}
              onChange={(event) => setOpenOnly(event.target.checked)}
              className="h-3.5 w-3.5 rounded border-base-border bg-base-raised accent-[rgb(var(--accent))]"
            />
            Open only
          </label>
        </section>

        {error && (
          <div className="rounded-xl border border-status-critical/30 bg-status-critical/10 p-4 text-sm text-status-critical">
            {error}
          </div>
        )}

        {loading ? (
          <ul className="space-y-3">
            {[0, 1, 2, 3].map((index) => (
              <li key={index} className="h-28 animate-pulse rounded-xl bg-base-raised" />
            ))}
          </ul>
        ) : visible.length === 0 ? (
          <div className="rounded-xl border border-dashed border-base-border py-16 text-center">
            <p className="text-sm text-base-muted">
              {incidents.length === 0 ? 'No incidents recorded yet' : 'No incidents match these filters'}
            </p>
            <p className="mt-1 text-xs text-base-faint">
              {incidents.length === 0
                ? 'Run the server with --demo to see the product working against a simulated cluster.'
                : 'Try clearing the search or widening the category.'}
            </p>
          </div>
        ) : (
          <ul className="space-y-3">
            {visible.map((record) => (
              <li key={record.incident.id}>
                <IncidentCard record={record} />
              </li>
            ))}
          </ul>
        )}
      </main>
    </>
  );
}
