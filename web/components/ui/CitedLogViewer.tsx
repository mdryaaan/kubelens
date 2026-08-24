'use client';

import { useMemo, useState } from 'react';

import type { Citation, EventRecord, LogLine } from '@/lib/types';

interface Props {
  logs: LogLine[];
  events: EventRecord[];
  citations: Citation[];
}

/**
 * Renders the evidence with the cited lines highlighted.
 *
 * This is the component the whole product exists to make possible. An
 * explanation that says "the JVM heap was exhausted" is a claim; the same
 * sentence next to the highlighted line reading
 * `java.lang.OutOfMemoryError: Java heap space` is a claim you can check in one
 * second without leaving the page. Everything else here — the detector, the
 * schema, the citation verification — is in service of this view being
 * trustworthy.
 *
 * Every highlighted line has already been verified against this exact evidence
 * on the server, so anything shown as cited is genuinely present below.
 */
export function CitedLogViewer({ logs, events, citations }: Props) {
  const [onlyCited, setOnlyCited] = useState(false);

  const citedLines = useMemo(() => {
    const lines = new Set<number>();
    for (const citation of citations) {
      if (citation.line_number && citation.line_number > 0) lines.add(citation.line_number);
    }
    return lines;
  }, [citations]);

  const citedEvents = useMemo(() => {
    const quotes = citations.filter((citation) => citation.source === 'event');
    return new Set(
      events
        .filter((event) => quotes.some((quote) => event.message.includes(quote.text)))
        .map((event) => event.message),
    );
  }, [citations, events]);

  const visibleLogs = onlyCited ? logs.filter((line) => citedLines.has(line.number)) : logs;
  const hasEvidence = logs.length > 0 || events.length > 0;

  if (!hasEvidence) {
    return (
      <section className="rounded-xl border border-dashed border-base-border bg-base-surface p-6 text-center">
        <p className="text-sm text-base-muted">No evidence was captured for this incident</p>
        <p className="mt-1 text-xs text-base-faint">
          Some failures produce nothing to read — a container whose image never pulled has never
          written a line.
        </p>
      </section>
    );
  }

  return (
    <section className="overflow-hidden rounded-xl border border-base-border bg-base-surface">
      <header className="flex flex-wrap items-center justify-between gap-3 border-b border-base-border px-4 py-3">
        <div>
          <h3 className="text-sm font-semibold">Evidence</h3>
          <p className="mt-0.5 text-xs text-base-muted">
            {citedLines.size + citedEvents.size > 0 ? (
              <>
                <span className="text-accent">
                  {citedLines.size + citedEvents.size} line(s) cited
                </span>{' '}
                by the explanation, verified against this text
              </>
            ) : (
              'The explanation cited nothing from this evidence'
            )}
          </p>
        </div>

        {citedLines.size > 0 && (
          <label className="flex cursor-pointer select-none items-center gap-2 text-xs text-base-muted">
            <input
              type="checkbox"
              checked={onlyCited}
              onChange={(event) => setOnlyCited(event.target.checked)}
              className="h-3.5 w-3.5 rounded border-base-border bg-base-raised accent-[rgb(var(--accent))]"
            />
            Show only cited lines
          </label>
        )}
      </header>

      {events.length > 0 && (
        <div className="border-b border-base-border">
          <h4 className="px-4 pt-3 text-xs font-medium uppercase tracking-wide text-base-faint">
            Kubernetes events
          </h4>
          <ul className="space-y-1 p-3">
            {events.map((event, index) => {
              const cited = citedEvents.has(event.message);
              return (
                <li
                  key={`${event.reason}-${index}`}
                  className={`rounded-lg px-3 py-2 text-xs ring-1 ring-inset ${
                    cited
                      ? 'bg-accent-soft/60 ring-accent/40'
                      : 'bg-base-raised/60 ring-transparent'
                  }`}
                >
                  <div className="flex flex-wrap items-center gap-2">
                    <span
                      className={
                        event.type === 'Warning' ? 'text-status-warning' : 'text-base-muted'
                      }
                    >
                      {event.type}
                    </span>
                    <span className="font-medium">{event.reason}</span>
                    {event.count > 1 && (
                      <span className="tnum text-base-faint">×{event.count}</span>
                    )}
                    {cited && (
                      <span className="ml-auto rounded bg-accent/15 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-accent">
                        cited
                      </span>
                    )}
                  </div>
                  <p className="mt-1 break-words font-mono text-[11px] leading-relaxed text-base-muted">
                    {event.message}
                  </p>
                </li>
              );
            })}
          </ul>
        </div>
      )}

      {logs.length > 0 && (
        <div>
          <h4 className="px-4 pt-3 text-xs font-medium uppercase tracking-wide text-base-faint">
            Container log
          </h4>

          <div className="max-h-[28rem] overflow-auto p-3">
            <pre className="min-w-full font-mono text-[11.5px] leading-[1.7]">
              {visibleLogs.map((line) => {
                const cited = citedLines.has(line.number);
                return (
                  <div
                    key={line.number}
                    id={cited ? `log-line-${line.number}` : undefined}
                    className={`flex gap-3 rounded px-2 ${
                      cited
                        ? 'bg-accent-soft/70 ring-1 ring-inset ring-accent/40'
                        : 'hover:bg-base-raised/50'
                    }`}
                  >
                    <span
                      className={`tnum w-8 shrink-0 select-none text-right ${
                        cited ? 'text-accent' : 'text-base-faint'
                      }`}
                      aria-hidden
                    >
                      {line.number}
                    </span>
                    <code
                      className={`whitespace-pre-wrap break-all ${
                        cited ? 'text-base-text' : 'text-base-muted'
                      }`}
                    >
                      {line.text || ' '}
                    </code>
                  </div>
                );
              })}
            </pre>

            {onlyCited && visibleLogs.length === 0 && (
              <p className="px-2 py-4 text-center text-xs text-base-faint">
                No log lines were cited — the explanation rests on the events above.
              </p>
            )}
          </div>
        </div>
      )}
    </section>
  );
}
