import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  BarChart,
  Bar,
  XAxis,
  YAxis,
  ResponsiveContainer,
  Tooltip,
} from "recharts";
import { fetchMetrics } from "../lib/api";
import { parsePrometheusText, type MetricFamily } from "../lib/prometheus";
import { MetricCard } from "../components/ui/MetricCard";

function byRoute(fam: MetricFamily | undefined): { route: string; value: number }[] {
  if (!fam) return [];
  const totals = new Map<string, number>();
  for (const s of fam.samples) {
    const route = s.labels.route ?? "unknown";
    totals.set(route, (totals.get(route) ?? 0) + s.value);
  }
  return [...totals.entries()]
    .map(([route, value]) => ({ route, value }))
    .sort((a, b) => b.value - a.value);
}

function sum(fam: MetricFamily | undefined): number {
  if (!fam) return 0;
  return fam.samples.reduce((a, s) => a + s.value, 0);
}

/** Live edge traffic from Prometheus counters (same source as Metrics). */
export function Traffic() {
  const { data: text, isLoading, isError } = useQuery({
    queryKey: ["metrics"],
    queryFn: fetchMetrics,
    refetchInterval: 5000,
  });

  const families = useMemo(() => {
    const list = text ? parsePrometheusText(text) : [];
    return new Map(list.map((f) => [f.name, f]));
  }, [text]);

  const requests = byRoute(families.get("vortex_requests_total"));
  const active = byRoute(families.get("vortex_active_connections"));
  const totalReq = sum(families.get("vortex_requests_total"));
  const bytesIn = sum(families.get("vortex_bytes_in_total"));
  const bytesOut = sum(families.get("vortex_bytes_out_total"));

  if (isError) {
    return (
      <div className="rounded-lg border border-border bg-card p-8 text-center text-muted-foreground">
        Could not load traffic metrics. Check that the management API is reachable.
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-semibold text-foreground">Traffic</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Edge request and connection counters from the live Prometheus scrape.
        </p>
      </div>

      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <MetricCard label="Requests (total)" value={isLoading ? "…" : totalReq} />
        <MetricCard label="Active connections" value={isLoading ? "…" : sum(families.get("vortex_active_connections"))} />
        <MetricCard label="Bytes in" value={isLoading ? "…" : bytesIn} />
        <MetricCard label="Bytes out" value={isLoading ? "…" : bytesOut} />
      </div>

      <div className="rounded-lg border border-border bg-card p-4">
        <div className="mb-3 text-sm font-medium text-muted-foreground">Requests by route</div>
        {requests.length === 0 ? (
          <div className="text-sm text-muted-foreground">
            {isLoading ? "Loading…" : "No request traffic yet."}
          </div>
        ) : (
          <div className="h-64">
            <ResponsiveContainer width="100%" height="100%">
              <BarChart data={requests}>
                <XAxis dataKey="route" tick={{ fontSize: 11 }} />
                <YAxis tick={{ fontSize: 11 }} />
                <Tooltip />
                <Bar dataKey="value" fill="hsl(var(--primary))" radius={[4, 4, 0, 0]} />
              </BarChart>
            </ResponsiveContainer>
          </div>
        )}
      </div>

      <div className="rounded-lg border border-border bg-card p-4">
        <div className="mb-3 text-sm font-medium text-muted-foreground">Active connections by route</div>
        {active.length === 0 ? (
          <div className="text-sm text-muted-foreground">No active connections.</div>
        ) : (
          <div className="grid grid-cols-2 gap-3 md:grid-cols-3">
            {active.map((a) => (
              <MetricCard key={a.route} label={a.route} value={a.value} />
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
