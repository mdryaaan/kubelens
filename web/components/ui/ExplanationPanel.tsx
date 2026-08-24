'use client';

import { motion } from 'framer-motion';
import { useState } from 'react';

import { api } from '@/lib/api';
import type { Explanation } from '@/lib/types';
import { CATEGORY_LABELS } from '@/lib/types';

interface Props {
  incidentId: string;
  explanation?: Explanation;
  onExplained: (explanation: Explanation) => void;
}

/**
 * Shows the analysis, and is honest about how much it is worth.
 *
 * Three things are surfaced that a tool trying to look confident would hide:
 * whether the analysis cited any evidence at all, whether the model quoted
 * something that was not there, and whether it disagreed with the rule that
 * detected the incident. All three change how much weight a reader should give
 * the prose, so all three are shown next to it rather than buried.
 */
export function ExplanationPanel({ incidentId, explanation, onExplained }: Props) {
  const [running, setRunning] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function explain() {
    setRunning(true);
    setError(null);
    try {
      onExplained(await api.explain(incidentId));
    } catch (caught) {
      setError((caught as Error).message);
    } finally {
      setRunning(false);
    }
  }

  if (!explanation) {
    return (
      <section className="rounded-xl border border-base-border bg-base-surface p-6">
        <h3 className="text-sm font-semibold">Root cause analysis</h3>
        <p className="mt-1 text-sm text-base-muted">
          This incident has not been explained yet. Detection does not depend on it — the finding
          above stands on its own.
        </p>

        <button
          type="button"
          onClick={() => void explain()}
          disabled={running}
          className="mt-4 inline-flex items-center gap-2 rounded-lg bg-accent px-3 py-2 text-sm font-medium text-white transition hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-60"
        >
          {running ? 'Analysing…' : 'Explain this incident'}
        </button>

        {error && (
          <p className="mt-3 rounded-lg bg-status-critical/10 px-3 py-2 text-xs text-status-critical">
            {error}
          </p>
        )}
      </section>
    );
  }

  const unsupported = explanation.citations.length === 0;
  const fabricated = explanation.rejected_citations?.length ?? 0;

  return (
    <motion.section
      initial={{ opacity: 0, y: 8 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: 0.25 }}
      className="rounded-xl border border-base-border bg-base-surface"
    >
      <header className="flex flex-wrap items-center justify-between gap-3 border-b border-base-border px-5 py-3">
        <h3 className="text-sm font-semibold">Root cause analysis</h3>
        <div className="flex items-center gap-3 text-xs text-base-faint">
          <span className="tnum">confidence {(explanation.confidence * 100).toFixed(0)}%</span>
          <span className="font-mono">{explanation.model}</span>
        </div>
      </header>

      {explanation.disclaimer && (
        <p className="border-b border-status-warning/25 bg-status-warning/10 px-5 py-2.5 text-xs text-status-warning">
          {explanation.disclaimer}
        </p>
      )}

      <div className="space-y-4 p-5">
        <p className="text-sm leading-relaxed text-base-text">{explanation.summary}</p>

        {explanation.suggested_fix && (
          <div className="rounded-lg border border-base-border bg-base-raised/60 p-4">
            <p className="text-xs font-medium uppercase tracking-wide text-base-faint">
              Suggested fix
            </p>
            <p className="mt-1.5 text-sm text-base-text">{explanation.suggested_fix}</p>
          </div>
        )}

        <div className="flex flex-wrap gap-2">
          {unsupported ? (
            <Flag tone="warning">
              Cited no evidence — treat this as a hypothesis, not a finding
            </Flag>
          ) : (
            <Flag tone="ok">
              {explanation.citations.length} claim(s) verified against the evidence below
            </Flag>
          )}

          {fabricated > 0 && (
            <Flag tone="critical">
              {fabricated} fabricated quote(s) dropped before display
            </Flag>
          )}

          {!explanation.agrees_with_rule && (
            <Flag tone="warning">
              Disagrees with the rule, which detected{' '}
              {CATEGORY_LABELS[explanation.rule_category] ?? explanation.rule_category}
            </Flag>
          )}
        </div>

        {fabricated > 0 && (
          <details className="rounded-lg border border-status-critical/30 bg-status-critical/5 p-3">
            <summary className="cursor-pointer text-xs font-medium text-status-critical">
              What was dropped
            </summary>
            <ul className="mt-2 space-y-1">
              {explanation.rejected_citations?.map((quote, index) => (
                <li
                  key={index}
                  className="break-words font-mono text-[11px] leading-relaxed text-base-muted line-through"
                >
                  {quote}
                </li>
              ))}
            </ul>
            <p className="mt-2 text-[11px] text-base-faint">
              These lines do not appear anywhere in the evidence this analysis was given, so they
              were removed before you saw them.
            </p>
          </details>
        )}
      </div>
    </motion.section>
  );
}

function Flag({
  tone,
  children,
}: {
  tone: 'ok' | 'warning' | 'critical';
  children: React.ReactNode;
}) {
  const styles = {
    ok: 'bg-status-ok/10 text-status-ok ring-status-ok/25',
    warning: 'bg-status-warning/10 text-status-warning ring-status-warning/25',
    critical: 'bg-status-critical/10 text-status-critical ring-status-critical/25',
  } as const;

  return (
    <span
      className={`inline-flex items-center rounded-md px-2.5 py-1 text-xs ring-1 ring-inset ${styles[tone]}`}
    >
      {children}
    </span>
  );
}
