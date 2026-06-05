import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Plus, Trash2 } from "lucide-react";

interface Backend {
  host: string;
  port: string;
}

// RouteBuilder is the create/edit form for a route. Saving calls a route
// management API that does not exist yet (returns 501), so the form shows an
// informational message instead of persisting.
export function RouteBuilder() {
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const [host, setHost] = useState("");
  const [protocol, setProtocol] = useState("http");
  const [backends, setBackends] = useState<Backend[]>([{ host: "", port: "" }]);
  const [message, setMessage] = useState("");

  const addBackend = () => setBackends((b) => [...b, { host: "", port: "" }]);
  const removeBackend = (i: number) => setBackends((b) => b.filter((_, idx) => idx !== i));
  const updateBackend = (i: number, field: keyof Backend, value: string) =>
    setBackends((b) => b.map((bk, idx) => (idx === i ? { ...bk, [field]: value } : bk)));

  const save = async () => {
    // POST /api/routes is not implemented yet (returns 501).
    setMessage("Route management API coming in M8.");
  };

  return (
    <div className="max-w-2xl space-y-5">
      <div className="space-y-4 rounded-lg border border-border bg-card p-5">
        <Row label="Name">
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            className="w-full rounded-md border border-border bg-background px-3 py-1.5 text-sm outline-none focus:border-primary"
          />
        </Row>
        <Row label="Host">
          <input
            value={host}
            onChange={(e) => setHost(e.target.value)}
            className="w-full rounded-md border border-border bg-background px-3 py-1.5 text-sm outline-none focus:border-primary"
          />
        </Row>
        <Row label="Protocol">
          <select
            value={protocol}
            onChange={(e) => setProtocol(e.target.value)}
            className="w-full rounded-md border border-border bg-background px-2 py-1.5 text-sm outline-none focus:border-primary"
          >
            {["http", "https", "tcp", "udp", "h3"].map((p) => (
              <option key={p} value={p}>
                {p}
              </option>
            ))}
          </select>
        </Row>

        <div>
          <div className="mb-2 text-sm text-muted-foreground">Backends</div>
          <div className="space-y-2">
            {backends.map((b, i) => (
              <div key={i} className="flex items-center gap-2">
                <input
                  value={b.host}
                  onChange={(e) => updateBackend(i, "host", e.target.value)}
                  placeholder="host"
                  className="flex-1 rounded-md border border-border bg-background px-3 py-1.5 text-sm outline-none focus:border-primary"
                />
                <input
                  value={b.port}
                  onChange={(e) => updateBackend(i, "port", e.target.value)}
                  placeholder="port"
                  className="w-24 rounded-md border border-border bg-background px-3 py-1.5 text-sm outline-none focus:border-primary"
                />
                <button
                  onClick={() => removeBackend(i)}
                  aria-label="remove backend"
                  className="text-muted-foreground hover:text-red-400"
                >
                  <Trash2 size={16} />
                </button>
              </div>
            ))}
          </div>
          <button
            onClick={addBackend}
            className="mt-2 inline-flex items-center gap-1 text-sm text-primary hover:underline"
          >
            <Plus size={14} /> Add backend
          </button>
        </div>
      </div>

      {message && (
        <div className="rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-sm text-amber-300">
          {message}
        </div>
      )}

      <div className="flex gap-3">
        <button
          onClick={save}
          className="rounded-md bg-primary px-4 py-1.5 text-sm text-primary-foreground hover:opacity-90"
        >
          Save
        </button>
        <button
          onClick={() => navigate("/dashboard/routes")}
          className="rounded-md border border-border px-4 py-1.5 text-sm hover:bg-accent"
        >
          Cancel
        </button>
      </div>
    </div>
  );
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="block">
      <span className="mb-1 block text-sm text-muted-foreground">{label}</span>
      {children}
    </label>
  );
}
