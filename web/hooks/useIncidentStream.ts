'use client';

import { useCallback, useEffect, useRef, useState } from 'react';

import { api, streamUrl } from '@/lib/api';
import type { Explanation, HealthSample, IncidentRecord } from '@/lib/types';

export type ConnectionState = 'connecting' | 'live' | 'offline';

interface StreamState {
  incidents: IncidentRecord[];
  latest: HealthSample | null;
  connection: ConnectionState;
  error: string | null;
  loading: boolean;
}

/** How many incidents the live feed keeps in memory. */
const MAX_INCIDENTS = 200;

/**
 * Subscribes to the live incident stream and seeds it with recent history.
 *
 * The seed matters: a dashboard opened at 3am should show what has already
 * happened, not an empty screen waiting for the next failure. The stream then
 * layers new incidents on top as they are detected.
 */
export function useIncidentStream(seedLimit = 50) {
  const [state, setState] = useState<StreamState>({
    incidents: [],
    latest: null,
    connection: 'connecting',
    error: null,
    loading: true,
  });

  // Held in a ref so the EventSource callbacks never close over stale state.
  const sourceRef = useRef<EventSource | null>(null);

  const upsert = useCallback((record: IncidentRecord) => {
    setState((previous) => {
      const without = previous.incidents.filter(
        (existing) => existing.incident.id !== record.incident.id,
      );
      return {
        ...previous,
        incidents: [record, ...without].slice(0, MAX_INCIDENTS),
      };
    });
  }, []);

  const attachExplanation = useCallback((explanation: Explanation) => {
    setState((previous) => ({
      ...previous,
      incidents: previous.incidents.map((record) =>
        record.incident.id === explanation.incident_id ? { ...record, explanation } : record,
      ),
    }));
  }, []);

  useEffect(() => {
    let cancelled = false;

    api
      .incidents({ limit: seedLimit })
      .then((response) => {
        if (cancelled) return;
        setState((previous) => ({
          ...previous,
          incidents: response.incidents ?? [],
          loading: false,
          error: null,
        }));
      })
      .catch((error: Error) => {
        if (cancelled) return;
        setState((previous) => ({ ...previous, loading: false, error: error.message }));
      });

    return () => {
      cancelled = true;
    };
  }, [seedLimit]);

  useEffect(() => {
    // EventSource reconnects on its own, which is most of why SSE was chosen
    // over a WebSocket here — there is no reconnection logic to get wrong.
    const source = new EventSource(streamUrl);
    sourceRef.current = source;

    source.addEventListener('hello', () => {
      setState((previous) => ({ ...previous, connection: 'live', error: null }));
    });

    source.addEventListener('incident', (event) => {
      try {
        upsert(JSON.parse((event as MessageEvent).data) as IncidentRecord);
      } catch {
        // One malformed frame must not tear down a live dashboard.
      }
    });

    source.addEventListener('explanation', (event) => {
      try {
        attachExplanation(JSON.parse((event as MessageEvent).data) as Explanation);
      } catch {
        // Same reasoning as above.
      }
    });

    source.addEventListener('health', (event) => {
      try {
        const sample = JSON.parse((event as MessageEvent).data) as HealthSample;
        setState((previous) => ({ ...previous, latest: sample }));
      } catch {
        // Same reasoning as above.
      }
    });

    source.onopen = () => {
      setState((previous) => ({ ...previous, connection: 'live' }));
    };

    source.onerror = () => {
      // EventSource fires onerror both for a dropped connection and while it
      // is retrying, so this reports "offline" rather than tearing down.
      setState((previous) => ({ ...previous, connection: 'offline' }));
    };

    return () => {
      source.close();
      sourceRef.current = null;
    };
  }, [upsert, attachExplanation]);

  return state;
}
