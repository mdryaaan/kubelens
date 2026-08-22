// Types mirroring the Go API. They are written by hand rather than generated,
// because the surface is small and a generator would be one more thing to run
// before the dashboard compiles.

export type Category =
  | 'CrashLoopBackOff'
  | 'OOMKilled'
  | 'ImagePullBackOff'
  | 'ProbeFailure'
  | 'PendingTimeout'
  | 'DeploymentFailure';

export type Severity = 'critical' | 'warning' | 'info';

export const CATEGORIES: Category[] = [
  'CrashLoopBackOff',
  'OOMKilled',
  'ImagePullBackOff',
  'ProbeFailure',
  'PendingTimeout',
  'DeploymentFailure',
];

/** Human labels, because a category name is a Kubernetes term, not English. */
export const CATEGORY_LABELS: Record<Category, string> = {
  CrashLoopBackOff: 'Crash loop',
  OOMKilled: 'Out of memory',
  ImagePullBackOff: 'Image pull failure',
  ProbeFailure: 'Probe failure',
  PendingTimeout: 'Stuck pending',
  DeploymentFailure: 'Rollout stalled',
};

export interface Incident {
  id: string;
  fingerprint: string;
  category: Category;
  severity: Severity;
  namespace: string;
  resource: string;
  container?: string;
  title: string;
  detail: string;
  detected_at: string;
  first_seen: string;
  count: number;
  resolved: boolean;
  pre_existing: boolean;
}

export interface LogLine {
  number: number;
  text: string;
}

export interface EventRecord {
  type: string;
  reason: string;
  message: string;
  count: number;
  timestamp: string;
}

export interface ResourceSpec {
  image?: string;
  memory_limit?: string;
  memory_request?: string;
  cpu_limit?: string;
  cpu_request?: string;
  liveness_probe?: string;
  readiness_probe?: string;
  restart_policy?: string;
  replicas?: string;
}

export interface Evidence {
  logs: LogLine[];
  events: EventRecord[];
  spec: ResourceSpec;
}

export interface Citation {
  text: string;
  /** Absent when the quote came from an event rather than a log line. */
  line_number?: number;
  source: 'log' | 'event';
}

export interface Explanation {
  incident_id: string;
  category: Category;
  rule_category: Category;
  agrees_with_rule: boolean;
  confidence: number;
  summary: string;
  suggested_fix: string;
  citations: Citation[];
  /** Quotes the model made up. Kept and shown, not swept away. */
  rejected_citations?: string[];
  citation_accuracy: number;
  provider: string;
  model: string;
  /** Non-empty when no model was involved; must be rendered wherever the
   *  explanation is. */
  disclaimer?: string;
  generated_at: string;
  duration_ms: number;
}

export interface IncidentRecord {
  incident: Incident;
  explanation?: Explanation;
  evidence: Evidence;
}

export interface ClusterSnapshot {
  nodes: number;
  total_pods: number;
  unhealthy_pods: number;
  deployments: number;
}

export interface Stats {
  total_incidents: number;
  open_incidents: number;
  incidents_today: number;
  by_category: Record<string, number>;
  by_severity: Record<string, number>;
  explained: number;
  mean_time_to_detect_ms: number;
  fabricated_citations: number;
}

export interface Health {
  source: string;
  mode: string;
  uptime_ms: number;
  cluster: ClusterSnapshot;
  cluster_known: boolean;
  stats: Stats;
  subscribers: number;
}

export interface HealthSample {
  sampled_at: string;
  total_pods: number;
  unhealthy_pods: number;
  open_incidents: number;
  nodes: number;
}

export interface Settings {
  mode: string;
  source: string;
  provider: string;
  model: string;
  disclaimer?: string;
  explain_enabled: boolean;
  categories: string[];
  pending_threshold: string;
  cooldown: string;
}

export interface IncidentsResponse {
  incidents: IncidentRecord[];
  total: number;
  limit: number;
  offset: number;
}
