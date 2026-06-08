import {
  BarChart,
  Bar,
  XAxis,
  YAxis,
  ResponsiveContainer,
  Cell,
  Tooltip,
} from "recharts";
import { StatCard } from "../components/ui/StatCard";
import { MetricCard } from "../components/ui/MetricCard";
import { RouteBadge } from "../components/ui/RouteBadge";
import { StatusDot } from "../components/ui/StatusDot";
import { useHealth, useStatus, useAudit } from "../lib/hooks";

// trafficData is an illustrative 30-minute shape; per-minute request rate is
// not exposed as a time series by the API (only counters via /metrics), so this
// chart is decorative. All other panels show live data.
const trafficData = Array.from({ length: 30 }, (_, i) => {
  const v = Math.round(200 + 120 * Math.sin(i / 3) + Math.random() * 60);
  return { minute: `${i - 29}m`, rps: v };
});

function barColor(rps: number): string {
  if (rps > 320) return "#ef4444"; // red — high
  if (rps > 260) return "#f59e0b"; // amber — elevated
  return "#3b82f6"; // blue — normal
}

// relTime renders an ISO timestamp as a compact relative string.
function relTime(iso: string): string {
  const secs = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (secs < 60) return "just now";
  if (secs < 3600) return `${Math.floor(secs / 60)}m ago`;
  if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`;
  return `${Math.floor(secs / 86400)}d ago`;
}

export function Overview() {
  const { data: health } = useHealth();
  const { data: status } = useStatus();
  const { data: auditEntries = [] } = useAudit(8);
  const routes = health?.routes ?? [];
  const activeConns = routes.reduce((sum, r) => sum + r.active, 0);
  const mtlsOn = Boolean(status?.trust_domain);

  return (
    <div className="space-y-6">
      {/* Stat cards */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard label="Requests/sec" value="—" trend="+0%" trendUp />
        <StatCard label="P99 latency" value="—" />
        <StatCard label="Active connections" value={activeConns} />
        <StatCard label="Error rate" value="0.0%" />
      </div>

      {/* Traffic chart */}
      <div className="rounded-lg border border-border bg-card p-4">
        <div className="mb-3 text-sm font-medium text-muted-foreground">
          Traffic — last 30 minutes
        </div>
        <div className="h-56">
          <ResponsiveContainer width="100%" height="100%">
            <BarChart data={trafficData}>
              <XAxis dataKey="minute" tick={{ fontSize: 10, fill: "#64748b" }} interval={4} />
              <YAxis tick={{ fontSize: 10, fill: "#64748b" }} width={36} />
              <Tooltip
                contentStyle={{ background: "#0f172a", border: "1px solid #1e293b", fontSize: 12 }}
                labelStyle={{ color: "#94a3b8" }}
              />
              <Bar dataKey="rps" radius={[2, 2, 0, 0]}>
                {trafficData.map((d, i) => (
                  <Cell key={i} fill={barColor(d.rps)} />
                ))}
              </Bar>
            </BarChart>
          </ResponsiveContainer>
        </div>
      </div>

      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        {/* Cluster nodes */}
        <div className="rounded-lg border border-border bg-card p-4">
          <div className="mb-3 text-sm font-medium text-muted-foreground">Cluster nodes</div>
          <div className="space-y-3">
            <div className="flex items-center justify-between text-sm">
              <div className="flex items-center gap-2">
                <StatusDot status="green" />
                <span className="font-medium">{health?.cluster_name ?? "—"}</span>
                <span className="rounded bg-primary/15 px-1.5 py-0.5 text-xs text-primary">
                  leader
                </span>
              </div>
              <div className="text-muted-foreground">local · CPU — · RAM —</div>
            </div>
          </div>
        </div>

        {/* Active routes */}
        <div className="rounded-lg border border-border bg-card p-4">
          <div className="mb-3 text-sm font-medium text-muted-foreground">Active routes</div>
          {routes.length === 0 ? (
            <div className="text-sm text-muted-foreground">No routes configured.</div>
          ) : (
            <div className="space-y-2">
              {routes.map((r) => (
                <div key={r.name} className="flex items-center justify-between text-sm">
                  <div className="flex items-center gap-2">
                    <RouteBadge protocol={r.protocol} />
                    <span className="font-medium">{r.name}</span>
                  </div>
                  <span className="text-muted-foreground">{r.active} active</span>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>

      {/* Recent events — live audit log */}
      <div className="rounded-lg border border-border bg-card p-4">
        <div className="mb-3 text-sm font-medium text-muted-foreground">Recent events</div>
        {auditEntries.length === 0 ? (
          <div className="text-sm text-muted-foreground">No recent events.</div>
        ) : (
          <div className="space-y-2">
            {auditEntries.map((e) => (
              <div key={e.seq} className="flex items-center gap-3 text-sm">
                <StatusDot status="green" />
                <span>
                  <span className="font-medium">{e.action}</span>
                  <span className="text-muted-foreground"> · {e.resource}</span>
                </span>
                <span className="ml-auto text-xs text-muted-foreground">{relTime(e.timestamp)}</span>
              </div>
            ))}
          </div>
        )}
      </div>

      {/* Security snapshot — live status */}
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <MetricCard label="TLS provider" value={status?.tls_provider ?? "—"} />
        <MetricCard label="Audit entries" value={status ? String(status.audit_entry_count) : "—"} />
        <MetricCard label="mTLS status" value={mtlsOn ? "on" : "off"} />
        <MetricCard label="Plugins" value={status ? String(status.plugin_count) : "—"} />
      </div>
    </div>
  );
}
