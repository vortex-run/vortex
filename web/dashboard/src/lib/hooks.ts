import { useQuery } from "@tanstack/react-query";
import { fetchHealth } from "./api";

// useHealth polls the management /health endpoint every 5 seconds.
export function useHealth() {
  return useQuery({
    queryKey: ["health"],
    queryFn: fetchHealth,
    refetchInterval: 5000,
  });
}

// useRoutes derives the routes array from the polled health document.
export function useRoutes() {
  const q = useHealth();
  return { ...q, routes: q.data?.routes ?? [] };
}
