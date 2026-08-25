'use client';

import { useEffect } from 'react';

import { IncidentsByCategoryChart } from '@/components/charts/IncidentsByCategoryChart';
import { TopBar } from '@/components/layout/TopBar';
import { ClusterHealthChart } from '@/components/ui/ClusterHealthChart';
import { IncidentTimeline } from '@/components/ui/IncidentTimeline';
import { StatCard } from '@/components/ui/StatCard';
import { useClusterHealth } from '@/hooks/useClusterHealth';
import { useIncidentStream } from '@/hooks/useIncidentStream';
import { formatDuration } from '@/lib/api';

export default function OverviewPage() {
  const stream = useIncidentStream(30);
  const { health, samples, loading, error, appendSample } = useClusterHealth();

  // The chart moves as samples arrive over SSE rather than waiting for the next
  // poll, which is the difference between a live dashboard and a refreshing one.
  useEffect(() => {
    if (stream.latest) appendSample(stream.latest);
  }, [stream.latest, appendSample]);

  const stats = health?.stats;
  const cluster = health?.cluster;
  const unhealthy = cluster?.unhealthy_pods ?? 0;

  return (
    <>
      <TopBar
        title="Cluster overview"
        subtitle={health?.source}
        connection={stream.connection}
      />

      <main className="flex-1 space-y-6 p-4 md:p-6">
        {error && (
          <div className="rounded-xl border border-status-critical/30 bg-status-critical/10 p-4">
            <p className="text-sm font-medium text-status-critical">{error}</p>
            <p className="mt-1 text-xs text-base-muted">
              Start the server with <code className="font-mono">make demo</code>, then reload.
            </p>
          </div>
        )}

        <section className="grid grid-cols-2 gap-4 lg:grid-cols-4">
          <StatCard
            label="Pods"
            value={cluster?.total_pods ?? '—'}
            hint={
              health?.cluster_known
                ? `${cluster?.nodes ?? 0} nodes · ${cluster?.deployments ?? 0} deployments`
                : 'cluster size unknown'
            }
            loading={loading}
          />
          <StatCard
            label="Unhealthy pods"
            value={unhealthy}
            tone={unhealthy > 0 ? 'critical' : 'ok'}
            hint={unhealthy === 0 ? 'everything is running' : 'not Running, or waiting'}
            loading={loading}
          />
          <StatCard
            label="Incidents today"
            value={stats?.incidents_today ?? 0}
            hint={`${stats?.open_incidents ?? 0} still open`}
            tone={(stats?.open_incidents ?? 0) > 0 ? 'warning' : 'ok'}
            loading={loading}
          />
          <StatCard
            label="Mean time to detect"
            value={formatDuration(stats?.mean_time_to_detect_ms ?? 0)}
            hint="from the condition starting"
            loading={loading}
          />
        </section>

        <section className="grid gap-6 lg:grid-cols-3">
          <div className="lg:col-span-2">
            <div className="mb-2 flex items-baseline justify-between">
              <h2 className="text-sm font-semibold">Cluster health</h2>
              <span className="text-xs text-base-faint">last 6 hours</span>
            </div>
            <ClusterHealthChart samples={samples} loading={loading} />
          </div>

          <div>
            <div className="mb-2 flex items-baseline justify-between">
              <h2 className="text-sm font-semibold">By category</h2>
              <span className="text-xs text-base-faint">last 7 days</span>
            </div>
            <IncidentsByCategoryChart
              byCategory={stats?.by_category ?? {}}
              loading={loading}
            />
          </div>
        </section>

        <section>
          <div className="mb-3 flex items-baseline justify-between">
            <h2 className="text-sm font-semibold">Live incident feed</h2>
            {stats && stats.fabricated_citations > 0 && (
              <span className="text-xs text-status-warning">
                {stats.fabricated_citations} fabricated citation(s) dropped so far
              </span>
            )}
          </div>

          <IncidentTimeline records={stream.incidents.slice(0, 12)} loading={stream.loading} />
        </section>
      </main>
    </>
  );
}
