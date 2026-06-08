import { useState } from "react";
import { useHealth, useStatus } from "../lib/hooks";
import { reloadConfig } from "../lib/api";

export function Settings() {
  const { data: health } = useHealth();
  const { data: status } = useStatus();
  const [msg, setMsg] = useState("");
  const [confirmShutdown, setConfirmShutdown] = useState(false);

  const doReload = async () => {
    try {
      await reloadConfig();
      setMsg("Config reloaded.");
    } catch (e) {
      setMsg(`Reload failed: ${(e as Error).message}`);
    }
  };

  const doShutdown = async () => {
    await fetch("/internal/shutdown", { method: "POST" });
    setMsg("Shutdown initiated.");
    setConfirmShutdown(false);
  };

  return (
    <div className="max-w-2xl space-y-6">
      <Section title="Cluster">
        <ReadOnly label="Cluster name" value={status?.cluster_name ?? health?.cluster_name ?? "—"} />
        <ReadOnly label="Node ID" value={status?.node_id ?? "—"} />
        <ReadOnly label="Trust domain" value={status?.trust_domain ?? "—"} />
        <ReadOnly label="Version" value={status?.version ?? health?.version ?? "—"} />
      </Section>

      <Section title="TLS">
        <ReadOnly label="Provider" value={status?.tls_provider ?? "—"} />
        <ReadOnly label="Secret backend" value={status?.secret_backend ?? "—"} />
        <ReadOnly label="Default policy" value={status ? (status.policy_default ? "allow-all" : "restrictive") : "—"} />
      </Section>

      <Section title="Observability">
        <ReadOnly label="Metrics path" value="/metrics" />
        <div className="flex items-center justify-between">
          <span className="text-sm text-muted-foreground">Tracing enabled</span>
          <input type="checkbox" disabled className="accent-primary" />
        </div>
        <div className="flex items-center justify-between">
          <span className="text-sm text-muted-foreground">Log level</span>
          <select disabled className="rounded-md border border-border bg-background px-2 py-1 text-sm">
            <option>info</option>
          </select>
        </div>
      </Section>

      {msg && <div className="rounded-md border border-border bg-card px-3 py-2 text-sm">{msg}</div>}

      <Section title="Danger zone" danger>
        <div className="flex gap-3">
          <button
            onClick={doReload}
            className="rounded-md border border-border px-4 py-1.5 text-sm hover:bg-accent"
          >
            Reload config
          </button>
          <button
            onClick={() => setConfirmShutdown(true)}
            className="rounded-md border border-red-500/40 px-4 py-1.5 text-sm text-red-400 hover:bg-red-500/10"
          >
            Shutdown
          </button>
        </div>
      </Section>

      {confirmShutdown && (
        <div className="fixed inset-0 z-40 grid place-items-center bg-black/50">
          <div className="w-96 rounded-lg border border-border bg-card p-5">
            <div className="font-semibold">Shut down VORTEX?</div>
            <p className="mt-1 text-sm text-muted-foreground">
              This stops the server. You'll need shell access to restart it.
            </p>
            <div className="mt-4 flex justify-end gap-2">
              <button
                onClick={() => setConfirmShutdown(false)}
                className="rounded-md border border-border px-3 py-1.5 text-sm hover:bg-accent"
              >
                Cancel
              </button>
              <button
                onClick={doShutdown}
                className="rounded-md bg-red-500 px-3 py-1.5 text-sm text-white hover:opacity-90"
              >
                Shutdown
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

function Section({
  title,
  children,
  danger,
}: {
  title: string;
  children: React.ReactNode;
  danger?: boolean;
}) {
  return (
    <div
      className={[
        "space-y-3 rounded-lg border bg-card p-5",
        danger ? "border-red-500/30" : "border-border",
      ].join(" ")}
    >
      <div className={danger ? "text-sm font-medium text-red-400" : "text-sm font-medium text-muted-foreground"}>
        {title}
      </div>
      {children}
    </div>
  );
}

function ReadOnly({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center justify-between text-sm">
      <span className="text-muted-foreground">{label}</span>
      <span className="font-mono">{value}</span>
    </div>
  );
}
