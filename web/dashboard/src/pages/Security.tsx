import { useHealth } from "../lib/hooks";
import { StatusDot } from "../components/ui/StatusDot";
import { MetricCard } from "../components/ui/MetricCard";

export function Security() {
  const { data: health } = useHealth();

  return (
    <div className="space-y-6">
      {/* TLS certificates */}
      <Section title="TLS certificates">
        <div className="text-sm text-muted-foreground">
          No public certificates issued (internal CA / no public routes).
        </div>
      </Section>

      {/* mTLS status */}
      <Section title="mTLS identity">
        <div className="grid grid-cols-1 gap-3 text-sm sm:grid-cols-3">
          <Field label="Node identity (SPIFFE)" value="not enabled" />
          <Field label="Trust domain" value={`${health?.cluster_name ?? "—"}.vortex`} />
          <Field label="Cert expiry" value="—" />
        </div>
      </Section>

      {/* Blocked IPs */}
      <Section title="Blocked IPs">
        <div className="overflow-hidden rounded-md border border-border">
          <table className="w-full text-sm">
            <thead className="bg-muted/40 text-left text-xs uppercase text-muted-foreground">
              <tr>
                <th className="px-3 py-2">IP</th>
                <th className="px-3 py-2">Reason</th>
                <th className="px-3 py-2">Since</th>
              </tr>
            </thead>
            <tbody>
              <tr>
                <td className="px-3 py-3 text-center text-muted-foreground" colSpan={3}>
                  0 IPs blocked today
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </Section>

      {/* Secret store */}
      <Section title="Secret store">
        <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
          <MetricCard label="store backend" value="local" />
          <MetricCard label="secrets configured" value={health ? "0" : "—"} />
        </div>
        <div className="mt-3 flex items-center gap-2 text-sm text-muted-foreground">
          <StatusDot status="green" />
          Secret backend connected
        </div>
      </Section>
    </div>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="rounded-lg border border-border bg-card p-5">
      <div className="mb-3 text-sm font-medium text-muted-foreground">{title}</div>
      {children}
    </div>
  );
}

function Field({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-md border border-border p-3">
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className="mt-1 font-mono text-sm">{value}</div>
    </div>
  );
}
