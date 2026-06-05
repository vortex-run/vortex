// Typed API client for the VORTEX management API. All requests are relative so
// they hit the same origin serving the dashboard (or the dev proxy on :9090).
import type { HealthResponse } from "../types";

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
