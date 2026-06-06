import { NavLink } from "react-router-dom";
import {
  LayoutDashboard,
  Server,
  Route as RouteIcon,
  Layers,
  Activity,
  Shield,
  BarChart2,
  Puzzle,
  FileText,
  Key,
  Settings,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";

interface NavItem {
  label: string;
  to: string;
  icon: LucideIcon;
}

// Navigation items, in display order. Paths are absolute under /dashboard.
const NAV: NavItem[] = [
  { label: "Overview", to: "/dashboard", icon: LayoutDashboard },
  { label: "Nodes", to: "/dashboard/nodes", icon: Server },
  { label: "Routes", to: "/dashboard/routes", icon: RouteIcon },
  { label: "Namespaces", to: "/dashboard/namespaces", icon: Layers },
  { label: "Traffic", to: "/dashboard/traffic", icon: Activity },
  { label: "Security", to: "/dashboard/security", icon: Shield },
  { label: "Metrics", to: "/dashboard/metrics", icon: BarChart2 },
  { label: "Plugins", to: "/dashboard/plugins", icon: Puzzle },
  { label: "Audit Log", to: "/dashboard/audit", icon: FileText },
  { label: "Secrets", to: "/dashboard/secrets", icon: Key },
  { label: "Settings", to: "/dashboard/settings", icon: Settings },
];

// version is stamped at build time; falls back to "dev".
const VERSION = "dev";

export function Sidebar() {
  return (
    <aside className="flex h-screen w-60 flex-col border-r border-border bg-card">
      {/* Logo + name */}
      <div className="flex items-center gap-2 px-5 py-5">
        <div className="h-7 w-7 rounded bg-primary/20 grid place-items-center text-primary font-bold">
          V
        </div>
        <span className="text-lg font-semibold tracking-tight">VORTEX</span>
      </div>

      {/* Nav */}
      <nav className="flex-1 space-y-1 px-3">
        {NAV.map(({ label, to, icon: Icon }) => (
          <NavLink
            key={to}
            to={to}
            // Overview must match exactly so it isn't active on every subpage.
            end={to === "/dashboard"}
            className={({ isActive }) =>
              [
                "flex items-center gap-3 rounded-md px-3 py-2 text-sm transition-colors",
                isActive
                  ? "bg-primary/15 text-primary"
                  : "text-muted-foreground hover:bg-accent hover:text-foreground",
              ].join(" ")
            }
          >
            <Icon size={18} />
            {label}
          </NavLink>
        ))}
      </nav>

      {/* Footer: user + version */}
      <div className="border-t border-border px-5 py-4 text-xs text-muted-foreground">
        <div className="font-medium text-foreground">admin</div>
        <div>VORTEX {VERSION}</div>
      </div>
    </aside>
  );
}
