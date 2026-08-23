'use client';

import { useCallback, useEffect, useState } from 'react';

import { api } from '@/lib/api';
import type { Health, HealthSample } from '@/lib/types';

interface HealthState {
  health: Health | null;
  samples: HealthSample[];
  loading: boolean;
  error: string | null;
}

/**
 * Loads the cluster health snapshot and its history.
 *
 * Polled rather than streamed: the snapshot is a summary that changes slowly,
 * and a fifteen-second refresh is indistinguishable from live for a number
 * like "unhealthy pods" while costing one request instead of a second socket.
 */
export function useClusterHealth(hours = 6, refreshMs = 15000) {
  const [state, setState] = useState<HealthState>({
    health: null,
    samples: [],
    loading: true,
    error: null,
  });

  const load = useCallback(async () => {
    try {
      const [health, series] = await Promise.all([api.health(), api.healthSeries(hours)]);
      setState({ health, samples: series.samples ?? [], loading: false, error: null });
    } catch (error) {
      setState((previous) => ({
        ...previous,
        loading: false,
        error: (error as Error).message,
      }));
    }
  }, [hours]);

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), refreshMs);
    return () => window.clearInterval(timer);
  }, [load, refreshMs]);

  /** appendSample merges a sample pushed over SSE, so the chart moves between
   *  polls rather than jumping every fifteen seconds. */
  const appendSample = useCallback((sample: HealthSample) => {
    setState((previous) => {
      if (previous.samples.some((existing) => existing.sampled_at === sample.sampled_at)) {
        return previous;
      }
      return { ...previous, samples: [...previous.samples, sample].slice(-500) };
    });
  }, []);

  return { ...state, reload: load, appendSample };
}
