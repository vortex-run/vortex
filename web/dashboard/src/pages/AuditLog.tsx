import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ShieldCheck, Download } from "lucide-react";

interface AuditEntry {
  seq: number;
  timestamp: string;
  actor: string;
  action: string;
  resource: string;
  detail?: Record<string, unknown>;
}

// fetchAudit calls the audit API, returning an empty list (not throwing) when
// the endpoint is not yet wired (404).
async function fetchAudit(): Promise<AuditEntry[]> {
  const res = await fetch("/api/audit?limit=100", { headers: { Accept: "application/json" } });
  if (res.status === 404) return [];
  if (!res.ok) throw new Error(`/api/audit returned ${res.status}`);
  const body = (await res.json()) as { entries?: AuditEntry[] };
  return body.entries ?? [];
}

export function AuditLog() {
  const [verifyMsg, setVerifyMsg] = useState("");
  const { data: entries = [] } = useQuery({ queryKey: ["audit"], queryFn: fetchAudit });

  const verify = async () => {
    const res = await fetch("/api/audit/verify", { method: "POST" });
    if (res.status === 404) {
      setVerifyMsg("Audit verify not yet wired.");
      return;
    }
    const body = (await res.json()) as { valid?: boolean; error?: string };
    setVerifyMsg(body.valid ? "Audit log integrity verified." : `Integrity FAILED: ${body.error}`);
  };

  return (
    <div className="space-y-4">
      {/* Filter bar (UI only for now) */}
      <div className="flex flex-wrap items-center gap-3">
        <input placeholder="Actor" className="rounded-md border border-border bg-card px-3 py-1.5 text-sm" />
        <input placeholder="Action" className="rounded-md border border-border bg-card px-3 py-1.5 text-sm" />
        <input type="date" className="rounded-md border border-border bg-card px-3 py-1.5 text-sm" />
        <div className="ml-auto flex gap-2">
          <button
            onClick={verify}
            className="inline-flex items-center gap-1 rounded-md border border-border px-3 py-1.5 text-sm hover:bg-accent"
          >
            <ShieldCheck size={16} /> Verify integrity
          </button>
          <button className="inline-flex items-center gap-1 rounded-md border border-border px-3 py-1.5 text-sm hover:bg-accent">
            <Download size={16} /> Export
          </button>
        </div>
      </div>

      {verifyMsg && (
        <div className="rounded-md border border-border bg-card px-3 py-2 text-sm">{verifyMsg}</div>
      )}

      {entries.length === 0 ? (
        <div className="rounded-lg border border-border bg-card p-8 text-center text-muted-foreground">
          No audit entries yet.
        </div>
      ) : (
        <div className="overflow-hidden rounded-lg border border-border">
          <table className="w-full text-sm">
            <thead className="bg-muted/40 text-left text-xs uppercase text-muted-foreground">
              <tr>
                <th className="px-3 py-2">Seq</th>
                <th className="px-3 py-2">Time</th>
                <th className="px-3 py-2">Actor</th>
                <th className="px-3 py-2">Action</th>
                <th className="px-3 py-2">Resource</th>
              </tr>
            </thead>
            <tbody>
              {entries.map((e) => (
                <tr key={e.seq} className="border-t border-border">
                  <td className="px-3 py-2 font-mono">{e.seq}</td>
                  <td className="px-3 py-2 text-muted-foreground">{e.timestamp}</td>
                  <td className="px-3 py-2">{e.actor}</td>
                  <td className="px-3 py-2 font-mono">{e.action}</td>
                  <td className="px-3 py-2">{e.resource}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
