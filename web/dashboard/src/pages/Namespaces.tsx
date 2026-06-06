import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, Trash2 } from "lucide-react";

interface Quotas {
  max_routes: number;
  max_secrets: number;
  max_connections: number;
  bandwidth_mbps: number;
}

interface Namespace {
  id: string;
  name: string;
  org_id: string;
  quotas: Quotas;
}

async function fetchNamespaces(): Promise<Namespace[]> {
  const res = await fetch("/api/namespaces", { headers: { Accept: "application/json" } });
  if (res.status === 404 || res.status === 503) return [];
  if (!res.ok) throw new Error(`/api/namespaces returned ${res.status}`);
  const body = (await res.json()) as { namespaces?: Namespace[] };
  return body.namespaces ?? [];
}

export function Namespaces() {
  const qc = useQueryClient();
  const { data: namespaces = [] } = useQuery({ queryKey: ["namespaces"], queryFn: fetchNamespaces });
  const [showCreate, setShowCreate] = useState(false);
  const [form, setForm] = useState({ id: "", name: "", org_id: "", max_conns: "1000" });
  const [msg, setMsg] = useState("");

  const create = async () => {
    const res = await fetch("/api/namespaces", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        id: form.id,
        name: form.name || form.id,
        org_id: form.org_id,
        quotas: { max_connections: Number(form.max_conns) || 0 },
      }),
    });
    if (res.ok) {
      setShowCreate(false);
      setForm({ id: "", name: "", org_id: "", max_conns: "1000" });
      qc.invalidateQueries({ queryKey: ["namespaces"] });
    } else {
      const body = (await res.json()) as { error?: string };
      setMsg(body.error ?? `create failed (${res.status})`);
    }
  };

  const remove = async (id: string) => {
    await fetch(`/api/namespaces/${id}`, { method: "DELETE" });
    qc.invalidateQueries({ queryKey: ["namespaces"] });
  };

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        {msg && <span className="text-sm text-red-400">{msg}</span>}
        <button
          onClick={() => setShowCreate(true)}
          className="ml-auto inline-flex items-center gap-1 rounded-md bg-primary px-3 py-1.5 text-sm text-primary-foreground"
        >
          <Plus size={16} /> New namespace
        </button>
      </div>

      {namespaces.length === 0 ? (
        <div className="rounded-lg border border-border bg-card p-8 text-center text-muted-foreground">
          No namespaces. Create one to isolate routes, secrets, and quotas per tenant.
        </div>
      ) : (
        <div className="overflow-hidden rounded-lg border border-border">
          <table className="w-full text-sm">
            <thead className="bg-muted/40 text-left text-xs uppercase text-muted-foreground">
              <tr>
                <th className="px-4 py-2">ID</th>
                <th className="px-4 py-2">Name</th>
                <th className="px-4 py-2">Org</th>
                <th className="px-4 py-2">Max conns</th>
                <th className="px-4 py-2 text-right">Actions</th>
              </tr>
            </thead>
            <tbody>
              {namespaces.map((n) => (
                <tr key={n.id} className="border-t border-border">
                  <td className="px-4 py-2 font-mono">{n.id}</td>
                  <td className="px-4 py-2">{n.name}</td>
                  <td className="px-4 py-2 text-muted-foreground">{n.org_id}</td>
                  <td className="px-4 py-2">{n.quotas.max_connections}</td>
                  <td className="px-4 py-2 text-right">
                    <button onClick={() => remove(n.id)} aria-label="delete" className="text-red-400 hover:text-red-300">
                      <Trash2 size={16} />
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {showCreate && (
        <div className="fixed inset-0 z-40 grid place-items-center bg-black/50">
          <div className="w-96 space-y-3 rounded-lg border border-border bg-card p-5">
            <div className="font-semibold">New namespace</div>
            {(["id", "name", "org_id", "max_conns"] as const).map((f) => (
              <input
                key={f}
                value={form[f]}
                onChange={(e) => setForm({ ...form, [f]: e.target.value })}
                placeholder={f}
                className="w-full rounded-md border border-border bg-background px-3 py-1.5 text-sm"
              />
            ))}
            <div className="flex justify-end gap-2 pt-1">
              <button
                onClick={() => setShowCreate(false)}
                className="rounded-md border border-border px-3 py-1.5 text-sm hover:bg-accent"
              >
                Cancel
              </button>
              <button onClick={create} className="rounded-md bg-primary px-3 py-1.5 text-sm text-primary-foreground">
                Create
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
