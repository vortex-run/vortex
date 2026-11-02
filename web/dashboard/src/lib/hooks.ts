import { useQueries, useQuery } from "@tanstack/react-query";
import {
  fetchHealth,
  fetchStatus,
  fetchAudit,
  fetchHealing,
  fetchTeamAgents,
  fetchAgentsCatalog,
  fetchOrchRuns,
  fetchOrchRunTasks,
  ApiAuthError,
} from "./api";
import type { OrchRun } from "../types";

// useHealth polls the management /health endpoint every 5 seconds.
export function useHealth() {
  return useQuery({
    queryKey: ["health"],
    queryFn: fetchHealth,
    refetchInterval: 5000,
  });
}

// useStatus polls extended /api/status (node id, trust domain, tls provider,
// secret backend, counts) every 5 seconds.
export function useStatus() {
  return useQuery({
    queryKey: ["status"],
    queryFn: fetchStatus,
    refetchInterval: 5000,
  });
}

// useAudit polls recent audit entries every 10 seconds.
export function useAudit(limit = 50) {
  return useQuery({
    queryKey: ["audit", limit],
    queryFn: () => fetchAudit(limit),
    refetchInterval: 10000,
  });
}

// useRoutes derives the routes array from the polled health document.
export function useRoutes() {
  const q = useHealth();
  return { ...q, routes: q.data?.routes ?? [] };
}

export function useHealing() {
  return useQuery({
    queryKey: ["healing"],
    queryFn: () => fetchHealing(),
    refetchInterval: 10000,
  });
}

export function useTeamAgents() {
  const q = useQuery({
    queryKey: ["team-agents"],
    queryFn: fetchTeamAgents,
    refetchInterval: 10000,
    retry: false,
  });
  return {
    ...q,
    authDenied: q.error instanceof ApiAuthError,
  };
}

export function useAgentsCatalog() {
  const q = useQuery({
    queryKey: ["agents-catalog"],
    queryFn: fetchAgentsCatalog,
    refetchInterval: 30000,
    retry: false,
  });
  return {
    ...q,
    authDenied: q.error instanceof ApiAuthError,
  };
}

/** Recent orchestration runs (running, done, failed) from GET /api/orchestration/runs. */
export function useOrchRuns() {
  const q = useQuery({
    queryKey: ["orch-runs"],
    queryFn: () => fetchOrchRuns(),
    refetchInterval: 10000,
    retry: false,
  });
  return {
    ...q,
    authDenied: q.error instanceof ApiAuthError,
  };
}

/** Loads tasks for each run returned by useOrchRuns (bounded by server list). */
export function useOrchRunTasksByRun(runs: OrchRun[], enabled: boolean) {
  return useQueries({
    queries: runs.map((r) => ({
      queryKey: ["orch-tasks", r.id],
      queryFn: () => fetchOrchRunTasks(r.id),
      refetchInterval: 10000,
      retry: false,
      enabled,
    })),
  });
}
