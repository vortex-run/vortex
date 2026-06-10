import { useQuery } from "@tanstack/react-query";
import { fetchHealth, fetchStatus, fetchAudit, fetchHealing } from "./api";

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
