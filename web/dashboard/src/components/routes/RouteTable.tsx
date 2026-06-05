import type { RouteHealth } from "../../types";
import { ProtocolBadge } from "./ProtocolBadge";
import { StatusDot } from "../ui/StatusDot";

interface Props {
  routes: RouteHealth[];
  onRowClick: (route: RouteHealth) => void;
}

// RouteTable renders the routes list. Edit/Disable actions are stubs until the
// route management API exists.
export function RouteTable({ routes, onRowClick }: Props) {
  if (routes.length === 0) {
    return (
      <div className="rounded-lg border border-border bg-card p-8 text-center text-muted-foreground">
        No routes match the current filter.
      </div>
    );
  }
  return (
    <div className="overflow-hidden rounded-lg border border-border">
      <table className="w-full text-sm">
        <thead className="bg-muted/40 text-left text-xs uppercase tracking-wide text-muted-foreground">
          <tr>
            <th className="px-4 py-2">Name</th>
            <th className="px-4 py-2">Protocol</th>
            <th className="px-4 py-2">Listen</th>
            <th className="px-4 py-2">Active conns</th>
            <th className="px-4 py-2">Health</th>
            <th className="px-4 py-2 text-right">Actions</th>
          </tr>
        </thead>
        <tbody>
          {routes.map((r) => (
            <tr
              key={r.name}
              onClick={() => onRowClick(r)}
              className="cursor-pointer border-t border-border hover:bg-accent/40"
            >
              <td className="px-4 py-2 font-medium">{r.name}</td>
              <td className="px-4 py-2">
                <ProtocolBadge protocol={r.protocol} />
              </td>
              <td className="px-4 py-2 font-mono text-muted-foreground">{r.listen}</td>
              <td className="px-4 py-2">{r.active}</td>
              <td className="px-4 py-2">
                <StatusDot status="green" />
              </td>
              <td className="px-4 py-2 text-right" onClick={(e) => e.stopPropagation()}>
                <button className="text-xs text-muted-foreground hover:text-foreground" disabled>
                  Edit
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
