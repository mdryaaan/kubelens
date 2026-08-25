'use client';

import Link from 'next/link';
import { useParams } from 'next/navigation';
import { useCallback, useEffect, useState } from 'react';

import { TopBar } from '@/components/layout/TopBar';
import { CitedLogViewer } from '@/components/ui/CitedLogViewer';
import { ExplanationPanel } from '@/components/ui/ExplanationPanel';
import { CategoryChip, SeverityBadge } from '@/components/ui/SeverityBadge';
import { api, formatAge, formatDuration } from '@/lib/api';
import type { Explanation, IncidentRecord, ResourceSpec } from '@/lib/types';

export default function IncidentDetailPage() {
  const params = useParams<{ id: string }>();
  const id = params?.id ?? '';

  const [record, setRecord] = useState<IncidentRecord | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [resolving, setResolving] = useState(false);

  const load = useCallback(async () => {
    if (!id) return;
    try {
      setRecord(await api.incident(id));
      setError(null);
    } catch (caught) {
      setError((caught as Error).message);
    } finally {
      setLoading(false);
    }
  }, [id]);

  useEffect(() => {
    void load();
  }, [load]);

  const onExplained = useCallback((explanation: Explanation) => {
    setRecord((previous) => (previous ? { ...previous, explanation } : previous));
  }, []);

  async function resolve() {
    if (!record) return;
    setResolving(true);
    try {
      await api.resolve(record.incident.id);
      await load();
    } catch (caught) {
      setError((caught as Error).message);
    } finally {
      setResolving(false);
    }
  }

  if (loading) {
    return (
      <>
        <TopBar title="Incident" />
        <main className="flex-1 space-y-4 p-4 md:p-6">
          <div className="h-32 animate-pulse rounded-xl bg-base-raised" />
          <div className="h-48 animate-pulse rounded-xl bg-base-raised" />
        </main>
      </>
    );
  }

  if (error || !record) {
    return (
      <>
        <TopBar title="Incident" />
        <main className="flex-1 p-4 md:p-6">
          <div className="rounded-xl border border-status-critical/30 bg-status-critical/10 p-6">
            <p className="text-sm font-medium text-status-critical">
              {error ?? 'This incident could not be loaded'}
            </p>
            <Link href="/incidents" className="mt-3 inline-block text-xs text-accent underline">
              Back to all incidents
            </Link>
          </div>
        </main>
      </>
    );
  }

  const { incident, evidence, explanation } = record;

  return (
    <>
      <TopBar title={incident.title} subtitle={`${incident.namespace}/${incident.resource}`} />

      <main className="flex-1 space-y-6 p-4 md:p-6">
        <Link href="/incidents" className="inline-block text-xs text-base-muted hover:text-accent">
          ← All incidents
        </Link>

        <section className="rounded-xl border border-base-border bg-base-surface p-5">
          <div className="flex flex-wrap items-start justify-between gap-4">
            <div className="min-w-0">
              <div className="flex flex-wrap items-center gap-2">
                <SeverityBadge severity={incident.severity} />
                <CategoryChip category={incident.category} />
                {incident.resolved && (
                  <span className="rounded-md bg-status-ok/10 px-2 py-0.5 text-xs text-status-ok">
                    Resolved
                  </span>
                )}
                {incident.pre_existing && (
                  <span
                    className="rounded-md bg-base-raised px-2 py-0.5 text-xs text-base-muted"
                    title="This condition was already true when kubelens started watching, so it is excluded from detection-latency statistics."
                  >
                    Pre-existing
                  </span>
                )}
              </div>

              <h2 className="mt-3 text-lg font-semibold">{incident.title}</h2>

              {/* The rule's own words, kept visually distinct from the generated
                  prose below so a reader can always tell them apart. */}
              <p className="mt-2 max-w-3xl text-sm leading-relaxed text-base-muted">
                {incident.detail}
              </p>
            </div>

            {!incident.resolved && (
              <button
                type="button"
                onClick={() => void resolve()}
                disabled={resolving}
                className="shrink-0 rounded-lg border border-base-border px-3 py-2 text-sm text-base-muted transition hover:bg-base-raised hover:text-base-text disabled:opacity-60"
              >
                {resolving ? 'Resolving…' : 'Mark resolved'}
              </button>
            )}
          </div>

          <dl className="mt-5 grid grid-cols-2 gap-4 border-t border-base-border pt-4 text-xs sm:grid-cols-4">
            <Field label="Detected" value={formatAge(incident.detected_at)} />
            <Field label="Condition began" value={formatAge(incident.first_seen)} />
            <Field label="Times seen" value={String(incident.count)} />
            <Field
              label="Detection latency"
              value={
                incident.pre_existing
                  ? 'n/a'
                  : formatDuration(
                      Date.parse(incident.detected_at) - Date.parse(incident.first_seen),
                    )
              }
            />
          </dl>
        </section>

        <ExplanationPanel
          incidentId={incident.id}
          explanation={explanation}
          onExplained={onExplained}
        />

        <CitedLogViewer
          logs={evidence.logs ?? []}
          events={evidence.events ?? []}
          citations={explanation?.citations ?? []}
        />

        <SpecPanel spec={evidence.spec ?? {}} />
      </main>
    </>
  );
}

function Field({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="text-base-faint">{label}</dt>
      <dd className="tnum mt-0.5 text-base-text">{value}</dd>
    </div>
  );
}

const SPEC_LABELS: Array<[keyof ResourceSpec, string]> = [
  ['image', 'Image'],
  ['memory_limit', 'Memory limit'],
  ['memory_request', 'Memory request'],
  ['cpu_limit', 'CPU limit'],
  ['cpu_request', 'CPU request'],
  ['readiness_probe', 'Readiness probe'],
  ['liveness_probe', 'Liveness probe'],
  ['restart_policy', 'Restart policy'],
  ['replicas', 'Replicas'],
];

/**
 * The spec fields that explain this class of failure — a memory limit next to
 * an OOM kill, a probe's timing next to a failing probe. Anything the incident
 * does not raise a question about is left out.
 */
function SpecPanel({ spec }: { spec: ResourceSpec }) {
  const rows = SPEC_LABELS.filter(([key]) => spec[key]);

  if (rows.length === 0) return null;

  return (
    <section className="rounded-xl border border-base-border bg-base-surface p-5">
      <h3 className="text-sm font-semibold">Resource spec</h3>
      <dl className="mt-3 grid gap-3 sm:grid-cols-2">
        {rows.map(([key, label]) => (
          <div key={key} className="min-w-0">
            <dt className="text-xs text-base-faint">{label}</dt>
            <dd className="mt-0.5 break-all font-mono text-xs text-base-text">{spec[key]}</dd>
          </div>
        ))}
      </dl>
    </section>
  );
}
