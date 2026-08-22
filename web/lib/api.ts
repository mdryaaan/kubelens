import type {
  Explanation,
  Health,
  HealthSample,
  IncidentRecord,
  IncidentsResponse,
  Settings,
} from './types';

/**
 * Base URL for the Go API.
 *
 * Empty in the browser: next.config.mjs rewrites /api to the server, so a
 * relative URL keeps requests same-origin and avoids CORS entirely. During a
 * production build there is no dev server to proxy through, so an explicit
 * origin can be supplied.
 */
const BASE = process.env.NEXT_PUBLIC_KUBELENS_API ?? '';

export class ApiError extends Error {
  readonly status: number;
  readonly hint?: string;

  constructor(message: string, status: number, hint?: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.hint = hint;
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  let response: Response;
  try {
    response = await fetch(`${BASE}${path}`, {
      ...init,
      // Incident data changes constantly; a cached response would show a
      // cluster that has already moved on.
      cache: 'no-store',
      headers: { Accept: 'application/json', ...(init?.headers ?? {}) },
    });
  } catch {
    // A failed fetch here almost always means the Go server is not running,
    // which is worth saying plainly rather than surfacing "Failed to fetch".
    throw new ApiError(
      'Cannot reach the kubelens server',
      0,
      'Start it with `make demo`, or `kubelens serve --demo`.',
    );
  }

  if (!response.ok) {
    let message = `Request failed with ${response.status}`;
    let hint: string | undefined;
    try {
      const body = (await response.json()) as { error?: string; hint?: string };
      if (body.error) message = body.error;
      hint = body.hint;
    } catch {
      // A non-JSON error body is not worth failing over.
    }
    throw new ApiError(message, response.status, hint);
  }

  return (await response.json()) as T;
}

export const api = {
  health: () => request<Health>('/api/health'),

  healthSeries: (hours = 6) =>
    request<{ samples: HealthSample[]; hours: number }>(`/api/health/series?hours=${hours}`),

  incidents: (params: Record<string, string | number | boolean | undefined> = {}) => {
    const query = new URLSearchParams();
    for (const [key, value] of Object.entries(params)) {
      if (value !== undefined && value !== '' && value !== false) {
        query.set(key, String(value));
      }
    }
    const suffix = query.toString() ? `?${query}` : '';
    return request<IncidentsResponse>(`/api/incidents${suffix}`);
  },

  incident: (id: string) => request<IncidentRecord>(`/api/incidents/${id}`),

  resolve: (id: string) =>
    request<{ id: string; resolved: boolean }>(`/api/incidents/${id}/resolve`, {
      method: 'POST',
    }),

  explain: (id: string) =>
    request<Explanation>(`/api/incidents/${id}/explain`, { method: 'POST' }),

  settings: () => request<Settings>('/api/settings'),
};

/** streamUrl is where the SSE endpoint lives. */
export const streamUrl = `${BASE}/api/stream`;

/** Formats a duration in milliseconds the way an operator reads it. */
export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '—';
  const seconds = Math.round(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours}h`;
  return `${Math.round(hours / 24)}d`;
}

/** Formats an ISO timestamp as a relative age. */
export function formatAge(iso: string, now = Date.now()): string {
  const then = Date.parse(iso);
  if (Number.isNaN(then)) return '—';
  const delta = Math.max(0, now - then);
  if (delta < 5000) return 'just now';
  return `${formatDuration(delta)} ago`;
}

/** Formats an ISO timestamp as a wall-clock time. */
export function formatTime(iso: string): string {
  const parsed = new Date(iso);
  if (Number.isNaN(parsed.getTime())) return '—';
  return parsed.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
}
