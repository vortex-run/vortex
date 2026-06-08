// Typed API client for the VORTEX management API. All requests are relative so
// they hit the same origin serving the dashboard (or the dev proxy on :9090).
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
