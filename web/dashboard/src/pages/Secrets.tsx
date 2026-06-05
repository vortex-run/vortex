import { useState } from "react";
import { useQuery } from "@tanstack/react-query";

interface SecretStatus {
  name: string;
  set: boolean;
}

// fetchSecretStatus calls the secrets-status API. Values are never returned —
// only names and set/unset state. 404 yields an empty list (not yet wired).
async function fetchSecretStatus(): Promise<SecretStatus[]> {
  const res = await fetch("/api/secrets/status", { headers: { Accept: "application/json" } });
  if (res.status === 404) return [];
  if (!res.ok) throw new Error(`/api/secrets/status returned ${res.status}`);
  const body = (await res.json()) as { secrets?: SecretStatus[] };
  return body.secrets ?? [];
}

export function Secrets() {
  const { data: secrets = [] } = useQuery({
    queryKey: ["secrets-status"],
    queryFn: fetchSecretStatus,
  });
  const [modalFor, setModalFor] = useState<string | null>(null);
  const [msg, setMsg] = useState("");

  const onSet = () => {
    setMsg("Secret management API coming soon.");
    setModalFor(null);
  };

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-3 text-sm text-muted-foreground">
        <span>Backend:</span>
        <span className="rounded border border-border px-2 py-0.5 font-mono text-foreground">local</span>
      </div>

      {msg && <div className="rounded-md border border-border bg-card px-3 py-2 text-sm">{msg}</div>}

      {secrets.length === 0 ? (
        <div className="rounded-lg border border-border bg-card p-8 text-center text-muted-foreground">
          No secrets declared in config.
        </div>
      ) : (
        <div className="overflow-hidden rounded-lg border border-border">
          <table className="w-full text-sm">
            <thead className="bg-muted/40 text-left text-xs uppercase text-muted-foreground">
              <tr>
                <th className="px-4 py-2">Name</th>
                <th className="px-4 py-2">Status</th>
                <th className="px-4 py-2 text-right">Actions</th>
              </tr>
            </thead>
            <tbody>
              {secrets.map((s) => (
                <tr key={s.name} className="border-t border-border">
                  <td className="px-4 py-2 font-mono">{s.name}</td>
                  <td className="px-4 py-2">
                    {s.set ? (
                      <span className="rounded bg-green-500/15 px-2 py-0.5 text-xs text-green-400">[set]</span>
                    ) : (
                      <span className="rounded bg-amber-500/15 px-2 py-0.5 text-xs text-amber-400">
                        [not set]
                      </span>
                    )}
                  </td>
                  <td className="px-4 py-2 text-right">
                    <button
                      onClick={() => setModalFor(s.name)}
                      className="text-xs text-primary hover:underline"
                    >
                      Set
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {modalFor && (
        <div className="fixed inset-0 z-40 grid place-items-center bg-black/50">
          <div className="w-96 rounded-lg border border-border bg-card p-5">
            <div className="mb-3 font-semibold">Set secret: {modalFor}</div>
            <input
              type="password"
              placeholder="value"
              className="w-full rounded-md border border-border bg-background px-3 py-1.5 text-sm"
            />
            <div className="mt-4 flex justify-end gap-2">
              <button
                onClick={() => setModalFor(null)}
                className="rounded-md border border-border px-3 py-1.5 text-sm hover:bg-accent"
              >
                Cancel
              </button>
              <button
                onClick={onSet}
                className="rounded-md bg-primary px-3 py-1.5 text-sm text-primary-foreground"
              >
                Set
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
