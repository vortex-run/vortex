import { useHealing } from "../lib/hooks";
import { StatusDot } from "../components/ui/StatusDot";

// Healing renders the M14 self-healing dashboard: overall health, per-route
// check status, SLO compliance, and recovery activity. Auto-refreshes (10s).
export function Healing() {
  const { data, isLoading } = useHealing();
  const checks = data?.checks ?? [];
  const sloAlerts = data?.slo_alerts ?? [];
  const stats = data?.recovery_stats ?? { total_events: 0, actions_executed: 0 };
  const healthy = data?.healthy ?? true;
  const passing = checks.filter((c) => c.healthy).length;
  const score = checks.length === 0 ? 100 : Math.round((passing / checks.length) * 100);

  function rowClass(failures: number, isHealthy: boolean): string {
    if (!isHealthy) return "bg-red-500/10";
    if (failures > 0) return "bg-amber-500/10";
    return "";
  }

  function ago(iso: string): string {
    if (!iso) return "—";
    const secs = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 1000));
    return secs < 60 ? `${secs}s ago` : `${Math.round(secs / 60)}m ago`;
  }

  return (
    <div className="space-y-4">
      {/* Overall health score */}
      <div className="rounded-lg border border-border bg-card p-4">
        <div className="flex items-center justify-between">
          <span className="text-sm text-muted-foreground">Overall health</span>
          <div className="flex items-center gap-2">
            <StatusDot status={healthy ? "green" : "red"} />
            <span className={`text-2xl font-bold ${healthy ? "text-green-400" : "text-red-400"}`}>
              {score}% {healthy ? "✅" : "⚠"}
            </span>
          </div>
        </div>
      </div>

      {/* Route health table */}
      <div className="rounded-lg border border-border bg-card p-4">
        <h2 className="mb-3 text-sm font-semibold text-foreground">Route Health</h2>
        {isLoading ? (
          <p className="text-sm text-muted-foreground">Loading…</p>
        ) : checks.length === 0 ? (
          <p className="text-sm text-muted-foreground">No monitored checks.</p>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="text-left text-xs text-muted-foreground">
                <th className="pb-2">NAME</th>
                <th className="pb-2">STATUS</th>
                <th className="pb-2">LATENCY</th>
                <th className="pb-2">FAILURES</th>
                <th className="pb-2">LAST CHECK</th>
              </tr>
            </thead>
            <tbody>
              {checks.map((c) => (
                <tr key={c.name} className={rowClass(c.consecutive_failures, c.healthy)}>
                  <td className="py-1.5 font-mono">{c.name}</td>
                  <td className="py-1.5">
                    {c.healthy ? (
                      <span className="text-green-400">✅ OK</span>
                    ) : (
                      <span className="text-red-400">✗ DOWN</span>
                    )}
                  </td>
                  <td className="py-1.5">{c.latency_ms}ms</td>
                  <td className="py-1.5">{c.consecutive_failures}</td>
                  <td className="py-1.5 text-muted-foreground">{ago(c.last_check)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {/* SLO compliance */}
      <div className="rounded-lg border border-border bg-card p-4">
        <h2 className="mb-3 text-sm font-semibold text-foreground">SLO Compliance</h2>
        {sloAlerts.length === 0 ? (
          <p className="text-sm text-green-400">All routes within their SLO targets ✅</p>
        ) : (
          <ul className="space-y-1 text-sm">
            {sloAlerts.map((a) => (
              <li key={a.route_name} className="text-red-400">
                🔴 {a.route_name}: {(a.current * 100).toFixed(1)}% (target {(a.target * 100).toFixed(1)}%) —
                burn {a.burn_rate.toFixed(1)}x [{a.alert_level}]
              </li>
            ))}
          </ul>
        )}
      </div>

      {/* Recovery activity */}
      <div className="rounded-lg border border-border bg-card p-4">
        <h2 className="mb-2 text-sm font-semibold text-foreground">Recovery Activity</h2>
        <p className="text-sm text-muted-foreground">
          Events: <span className="text-foreground">{stats.total_events}</span> · Actions executed:{" "}
          <span className="text-foreground">{stats.actions_executed}</span>
        </p>
      </div>
    </div>
  );
}
