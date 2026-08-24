'use client';

import {
  Area,
  AreaChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts';

import { formatTime } from '@/lib/api';
import type { HealthSample } from '@/lib/types';

interface Props {
  samples: HealthSample[];
  loading?: boolean;
}

/**
 * Plots unhealthy pods and open incidents over time.
 *
 * Two series rather than one, because they answer different questions: how much
 * of the cluster is broken right now, and how much of it kubelens is still
 * telling you about. A gap between them is a cluster that recovered without
 * anyone closing the incident.
 */
export function ClusterHealthChart({ samples, loading = false }: Props) {
  if (loading) {
    return (
      <div className="h-64 animate-pulse rounded-xl border border-base-border bg-base-surface" />
    );
  }

  if (samples.length === 0) {
    return (
      <div className="flex h-64 flex-col items-center justify-center rounded-xl border border-dashed border-base-border bg-base-surface text-center">
        <p className="text-sm text-base-muted">No health samples yet</p>
        <p className="mt-1 text-xs text-base-faint">
          The first point is recorded a few seconds after the server starts.
        </p>
      </div>
    );
  }

  const data = samples.map((sample) => ({
    time: formatTime(sample.sampled_at),
    unhealthy: sample.unhealthy_pods,
    open: sample.open_incidents,
    total: sample.total_pods,
  }));

  return (
    <div className="h-64 rounded-xl border border-base-border bg-base-surface p-4">
      <ResponsiveContainer width="100%" height="100%">
        <AreaChart data={data} margin={{ top: 8, right: 8, left: -20, bottom: 0 }}>
          <defs>
            <linearGradient id="unhealthyFill" x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor="rgb(var(--critical))" stopOpacity={0.35} />
              <stop offset="100%" stopColor="rgb(var(--critical))" stopOpacity={0} />
            </linearGradient>
            <linearGradient id="openFill" x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor="rgb(var(--accent))" stopOpacity={0.3} />
              <stop offset="100%" stopColor="rgb(var(--accent))" stopOpacity={0} />
            </linearGradient>
          </defs>

          <CartesianGrid stroke="rgb(var(--border))" strokeDasharray="3 3" vertical={false} />
          <XAxis
            dataKey="time"
            tick={{ fill: 'rgb(var(--faint))', fontSize: 11 }}
            tickLine={false}
            axisLine={false}
            minTickGap={40}
          />
          <YAxis
            tick={{ fill: 'rgb(var(--faint))', fontSize: 11 }}
            tickLine={false}
            axisLine={false}
            allowDecimals={false}
          />
          <Tooltip
            contentStyle={{
              background: 'rgb(var(--raised))',
              border: '1px solid rgb(var(--border))',
              borderRadius: 10,
              fontSize: 12,
              color: 'rgb(var(--text))',
            }}
            labelStyle={{ color: 'rgb(var(--muted))' }}
          />

          <Area
            type="monotone"
            dataKey="unhealthy"
            name="Unhealthy pods"
            stroke="rgb(var(--critical))"
            strokeWidth={2}
            fill="url(#unhealthyFill)"
            isAnimationActive={false}
          />
          <Area
            type="monotone"
            dataKey="open"
            name="Open incidents"
            stroke="rgb(var(--accent))"
            strokeWidth={2}
            fill="url(#openFill)"
            isAnimationActive={false}
          />
        </AreaChart>
      </ResponsiveContainer>
    </div>
  );
}
