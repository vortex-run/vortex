import { useMemo, useState } from "react";
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

const RANGES = ["5m", "15m", "1h"];

// byRoute sums a counter family's samples grouped by the "route" label.
function byRoute(fam: MetricFamily | undefined): { route: string; value: number }[] {
  if (!fam) return [];
  const totals = new Map<string, number>();
  for (const s of fam.samples) {
    const route = s.labels.route ?? "unknown";
    totals.set(route, (totals.get(route) ?? 0) + s.value);
  }
  return [...totals.entries()].map(([route, value]) => ({ route, value }));
}

export function Metrics() {
  const [range, setRange] = useState("15m");
  const { data: text } = useQuery({
    queryKey: ["metrics"],
    queryFn: fetchMetrics,
    refetchInterval: 10000,
  });

  const families = useMemo(() => {
    const list = text ? parsePrometheusText(text) : [];
    const map = new Map(list.map((f) => [f.name, f]));
    return map;
  }, [text]);

  const requests = byRoute(families.get("vortex_requests_total"));
  const active = byRoute(families.get("vortex_active_connections"));
  const bytesIn = byRoute(families.get("vortex_bytes_in_total")).reduce((a, b) => a + b.value, 0);
  const bytesOut = byRoute(families.get("vortex_bytes_out_total")).reduce((a, b) => a + b.value, 0);

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-2">
        {RANGES.map((r) => (
          <button
            key={r}
            onClick={() => setRange(r)}
            className={[
              "rounded-md border px-3 py-1 text-sm",
              range === r ? "border-primary bg-primary/15 text-primary" : "border-border text-muted-foreground",
            ].join(" ")}
          >
            {r}
          </button>
        ))}
        <span className="ml-2 text-xs text-muted-foreground">(range label only)</span>
      </div>

      <ChartCard title="Requests by route" data={requests} />

      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        <div className="rounded-lg border border-border bg-card p-4">
          <div className="mb-3 text-sm font-medium text-muted-foreground">
            Active connections by route
          </div>
          {active.length === 0 ? (
            <div className="text-sm text-muted-foreground">No active connections.</div>
          ) : (
            <div className="grid grid-cols-2 gap-3">
              {active.map((a) => (
                <MetricCard key={a.route} label={a.route} value={a.value} />
              ))}
            </div>
          )}
        </div>
        <div className="rounded-lg border border-border bg-card p-4">
          <div className="mb-3 text-sm font-medium text-muted-foreground">Throughput</div>
          <div className="grid grid-cols-2 gap-3">
            <MetricCard label="bytes in" value={bytesIn.toLocaleString()} />
            <MetricCard label="bytes out" value={bytesOut.toLocaleString()} />
          </div>
        </div>
      </div>
    </div>
  );
}

function ChartCard({ title, data }: { title: string; data: { route: string; value: number }[] }) {
  return (
    <div className="rounded-lg border border-border bg-card p-4">
      <div className="mb-3 text-sm font-medium text-muted-foreground">{title}</div>
      {data.length === 0 ? (
        <div className="text-sm text-muted-foreground">No data yet — send some traffic.</div>
      ) : (
        <div className="h-56">
          <ResponsiveContainer width="100%" height="100%">
            <BarChart data={data}>
              <XAxis dataKey="route" tick={{ fontSize: 11, fill: "#64748b" }} />
              <YAxis tick={{ fontSize: 11, fill: "#64748b" }} width={40} />
              <Tooltip
                contentStyle={{ background: "#0f172a", border: "1px solid #1e293b", fontSize: 12 }}
              />
              <Bar dataKey="value" fill="#3b82f6" radius={[2, 2, 0, 0]} />
            </BarChart>
          </ResponsiveContainer>
        </div>
      )}
    </div>
  );
}
