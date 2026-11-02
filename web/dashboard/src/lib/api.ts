// Typed API client for the VORTEX management API. All requests are relative so
// they hit the same origin serving the dashboard (or the dev proxy on :9090).
//
// Agent/orchestration routes use requireAPIKey on the server. This client does
// not yet attach X-API-Key; operators must configure the dashboard origin (or
// dev proxy) to inject credentials, or these calls return 401 and the UI shows
// auth-denied states.
import type { HealthResponse, StatusInfo, AuditEntry } from "../types";

async function getJSON<T>(path: string): Promise<T> {
  const res = await fetch(path, { headers: { Accept: "application/json" } });
  if (!res.ok) {
    throw new Error(`${path} returned ${res.status}`);
  }
  return (await res.json()) as T;
}

// fetchHealth retrieves the management /health document.
export function fetchHealth(): Promise<HealthResponse> {
  return getJSON<HealthResponse>("/health");
}

// fetchStatus retrieves extended node/cluster status (GET /api/status).
export function fetchStatus(): Promise<StatusInfo> {
  return getJSON<StatusInfo>("/api/status");
}

// fetchAudit retrieves recent audit entries (GET /api/audit). A 404 yields an
// empty list so the UI degrades gracefully when audit is unconfigured.
export async function fetchAudit(limit = 50): Promise<AuditEntry[]> {
  const res = await fetch(`/api/audit?limit=${limit}`, { headers: { Accept: "application/json" } });
  if (res.status === 404) return [];
  if (!res.ok) throw new Error(`/api/audit returned ${res.status}`);
  const body = (await res.json()) as { entries?: AuditEntry[] };
  return body.entries ?? [];
}

// fetchMetrics retrieves the raw Prometheus exposition text from /metrics.
export async function fetchMetrics(): Promise<string> {
  const res = await fetch("/metrics");
  if (!res.ok) {
    throw new Error(`/metrics returned ${res.status}`);
  }
  return res.text();
}

// fetchRoutes returns the routes array from /health (empty when none).
export async function fetchRoutes() {
  const health = await fetchHealth();
  return health.routes ?? [];
}

// reloadConfig triggers a configuration reload via the control plane.
export async function reloadConfig(): Promise<void> {
  const res = await fetch("/internal/reload", { method: "POST" });
  if (!res.ok) {
    throw new Error(`reload returned ${res.status}`);
  }
}

// fetchHealing retrieves self-healing status (GET /api/healing/status). A 404
// yields a healthy empty status so the UI degrades gracefully.
export async function fetchHealing(): Promise<import("../types").HealingStatus> {
  const res = await fetch("/api/healing/status", { headers: { Accept: "application/json" } });
  if (res.status === 404) return { healthy: true, checks: [], slo_alerts: [], recovery_stats: { total_events: 0, actions_executed: 0 } };
  if (!res.ok) throw new Error(`/api/healing/status returned ${res.status}`);
  return (await res.json()) as import("../types").HealingStatus;
}

export class ApiAuthError extends Error {
  readonly status: number;
  constructor(path: string, status: number) {
    super(`${path} returned ${status}`);
    this.name = "ApiAuthError";
    this.status = status;
  }
}

// fetchTeamAgents retrieves live team agent cards (GET /api/team/agents).
// 401/403 surface as ApiAuthError; 404 yields an empty list.
export async function fetchTeamAgents(): Promise<{ agents: import("../types").TeamAgent[] }> {
  const res = await fetch("/api/team/agents", { headers: { Accept: "application/json" } });
  if (res.status === 404) return { agents: [] };
  if (res.status === 401 || res.status === 403) throw new ApiAuthError("/api/team/agents", res.status);
  if (!res.ok) throw new Error(`/api/team/agents returned ${res.status}`);
  const body = (await res.json()) as { agents?: import("../types").TeamAgent[] };
  return { agents: body.agents ?? [] };
}

// fetchAgentsCatalog retrieves the static capability catalog.
export async function fetchAgentsCatalog(): Promise<{ agents: import("../types").AgentCatalogEntry[] }> {
  const res = await fetch("/api/agents/catalog", { headers: { Accept: "application/json" } });
  if (res.status === 404) return { agents: [] };
  if (res.status === 401 || res.status === 403) throw new ApiAuthError("/api/agents/catalog", res.status);
  if (!res.ok) throw new Error(`/api/agents/catalog returned ${res.status}`);
  const body = (await res.json()) as { agents?: import("../types").AgentCatalogEntry[] };
  return { agents: body.agents ?? [] };
}

// fetchOrchRuns lists recent orchestration runs (GET /api/orchestration/runs).
export async function fetchOrchRuns(limit = 50): Promise<{ runs: import("../types").OrchRun[] }> {
  const res = await fetch(`/api/orchestration/runs?limit=${limit}`, {
    headers: { Accept: "application/json" },
  });
  if (res.status === 404) return { runs: [] };
  if (res.status === 401 || res.status === 403) {
    throw new ApiAuthError("/api/orchestration/runs", res.status);
  }
  if (!res.ok) throw new Error(`/api/orchestration/runs returned ${res.status}`);
  const body = (await res.json()) as { runs?: import("../types").OrchRun[] };
  return { runs: body.runs ?? [] };
}

// fetchOrchRunTasks returns plan-ordered tasks for one run.
export async function fetchOrchRunTasks(
  runId: string,
  limit = 200,
): Promise<{ run_id: string; tasks: import("../types").OrchTask[] }> {
  const path = `/api/orchestration/runs/${encodeURIComponent(runId)}/tasks?limit=${limit}`;
  const res = await fetch(path, { headers: { Accept: "application/json" } });
  if (res.status === 404) return { run_id: runId, tasks: [] };
  if (res.status === 401 || res.status === 403) throw new ApiAuthError(path, res.status);
  if (!res.ok) throw new Error(`${path} returned ${res.status}`);
  const body = (await res.json()) as { run_id?: string; tasks?: import("../types").OrchTask[] };
  return { run_id: body.run_id ?? runId, tasks: body.tasks ?? [] };
}
