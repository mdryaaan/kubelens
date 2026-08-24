'use client';

import { Bar, BarChart, Cell, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts';

import { CATEGORY_LABELS, type Category } from '@/lib/types';

interface Props {
  byCategory: Record<string, number>;
  loading?: boolean;
}

/**
 * Colours follow what the category means operationally rather than assigning
 * six arbitrary hues: the failures that lose data or serve errors are red, the
 * ones that degrade capacity are amber, and configuration mistakes are the
 * accent colour.
 */
const CATEGORY_COLOURS: Record<string, string> = {
  OOMKilled: 'rgb(var(--critical))',
  CrashLoopBackOff: 'rgb(var(--critical))',
  DeploymentFailure: 'rgb(var(--warning))',
  ProbeFailure: 'rgb(var(--warning))',
  ImagePullBackOff: 'rgb(var(--accent))',
  PendingTimeout: 'rgb(var(--accent))',
};

export function IncidentsByCategoryChart({ byCategory, loading = false }: Props) {
  if (loading) {
    return <div className="h-64 animate-pulse rounded-xl border border-base-border bg-base-surface" />;
  }

  const data = Object.entries(byCategory)
    .map(([category, count]) => ({
      category,
      label: CATEGORY_LABELS[category as Category] ?? category,
      count,
    }))
    .sort((a, b) => b.count - a.count);

  if (data.length === 0) {
    return (
      <div className="flex h-64 flex-col items-center justify-center rounded-xl border border-dashed border-base-border bg-base-surface text-center">
        <p className="text-sm text-base-muted">Nothing has failed yet</p>
        <p className="mt-1 text-xs text-base-faint">
          Categories appear here as incidents are detected.
        </p>
      </div>
    );
  }

  return (
    <div className="h-64 rounded-xl border border-base-border bg-base-surface p-4">
      <ResponsiveContainer width="100%" height="100%">
        <BarChart data={data} layout="vertical" margin={{ top: 4, right: 16, left: 8, bottom: 4 }}>
          <XAxis type="number" hide allowDecimals={false} />
          <YAxis
            type="category"
            dataKey="label"
            width={110}
            tick={{ fill: 'rgb(var(--muted))', fontSize: 11 }}
            tickLine={false}
            axisLine={false}
          />
          <Tooltip
            cursor={{ fill: 'rgb(var(--raised))' }}
            contentStyle={{
              background: 'rgb(var(--raised))',
              border: '1px solid rgb(var(--border))',
              borderRadius: 10,
              fontSize: 12,
              color: 'rgb(var(--text))',
            }}
          />
          <Bar dataKey="count" name="Incidents" radius={[0, 6, 6, 0]} isAnimationActive={false}>
            {data.map((entry) => (
              <Cell
                key={entry.category}
                fill={CATEGORY_COLOURS[entry.category] ?? 'rgb(var(--accent))'}
              />
            ))}
          </Bar>
        </BarChart>
      </ResponsiveContainer>
    </div>
  );
}
