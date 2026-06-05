import { useHealth } from "../lib/hooks";
import { StatusDot } from "../components/ui/StatusDot";
import { RouteBadge } from "../components/ui/RouteBadge";

export function Nodes() {
  const { data: health } = useHealth();
  const routes = health?.routes ?? [];
  const protocols = [...new Set(routes.map((r) => r.protocol))];

  // Single-node deployment: derive one card from /health.
  return (
    <div className="space-y-4">
      <div className="rounded-md border border-amber-500/30 bg-amber-500/10 px-4 py-3 text-sm text-amber-300">
        Single-node mode. Add a node:{" "}
        <code className="font-mono">vortex cluster join &lt;addr&gt;</code>
      </div>

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <div className="rounded-lg border border-border bg-card p-4">
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-2">
              <StatusDot status="green" />
              <span className="font-mono text-sm">
                {(health?.cluster_name ?? "node").slice(0, 8)}
              </span>
            </div>
            <span className="rounded bg-primary/15 px-1.5 py-0.5 text-xs text-primary">leader</span>
          </div>

          <div className="mt-3 flex flex-wrap gap-1">
            {protocols.length === 0 ? (
              <span className="text-xs text-muted-foreground">no routes</span>
            ) : (
              protocols.map((p) => <RouteBadge key={p} protocol={p} />)
            )}
          </div>

          <div className="mt-4 space-y-2">
            <Bar label="CPU" pct={18} />
            <Bar label="RAM" pct={34} />
          </div>

          <div className="mt-3 text-xs text-muted-foreground">
            Uptime: {health?.uptime ?? "—"}
          </div>

          <div className="mt-4 flex gap-2">
            <button className="rounded-md border border-border px-3 py-1 text-xs hover:bg-accent" disabled>
              Drain
            </button>
            <button className="rounded-md border border-border px-3 py-1 text-xs hover:bg-accent" disabled>
              Restart
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}

function Bar({ label, pct }: { label: string; pct: number }) {
  return (
    <div>
      <div className="flex justify-between text-xs text-muted-foreground">
        <span>{label}</span>
        <span>{pct}%</span>
      </div>
      <div className="mt-1 h-1.5 rounded bg-muted">
        <div className="h-full rounded bg-primary" style={{ width: `${pct}%` }} />
      </div>
    </div>
  );
}
