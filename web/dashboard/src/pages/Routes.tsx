import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { Plus } from "lucide-react";
import { useRoutes } from "../lib/hooks";
import type { RouteHealth } from "../types";
import { RouteTable } from "../components/routes/RouteTable";
import { RouteDetailDrawer } from "../components/routes/RouteDetailDrawer";

const PROTOCOLS = ["all", "http", "https", "tcp", "udp", "h3"];

export function RoutesPage() {
  const { routes } = useRoutes();
  const [search, setSearch] = useState("");
  const [protocol, setProtocol] = useState("all");
  const [selected, setSelected] = useState<RouteHealth | null>(null);

  const filtered = useMemo(
    () =>
      routes.filter(
        (r) =>
          (protocol === "all" || r.protocol === protocol) &&
          r.name.toLowerCase().includes(search.toLowerCase()),
      ),
    [routes, search, protocol],
  );

  return (
    <div className="space-y-4">
      {/* Filter bar */}
      <div className="flex flex-wrap items-center gap-3">
        <input
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder="Search routes…"
          className="rounded-md border border-border bg-card px-3 py-1.5 text-sm outline-none focus:border-primary"
        />
        <select
          value={protocol}
          onChange={(e) => setProtocol(e.target.value)}
          className="rounded-md border border-border bg-card px-2 py-1.5 text-sm outline-none focus:border-primary"
        >
          {PROTOCOLS.map((p) => (
            <option key={p} value={p}>
              {p}
            </option>
          ))}
        </select>
        <Link
          to="/dashboard/routes/new"
          className="ml-auto inline-flex items-center gap-1 rounded-md bg-primary px-3 py-1.5 text-sm text-primary-foreground hover:opacity-90"
        >
          <Plus size={16} /> New route
        </Link>
      </div>

      <RouteTable routes={filtered} onRowClick={setSelected} />
      <RouteDetailDrawer route={selected} onClose={() => setSelected(null)} />
    </div>
  );
}
