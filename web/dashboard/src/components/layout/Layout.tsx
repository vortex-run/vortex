import { Outlet, useLocation } from "react-router-dom";
import { Sidebar } from "./Sidebar";
import { TopBar } from "./TopBar";

// titleFor maps a pathname to the page title shown in the TopBar.
function titleFor(pathname: string): string {
  const map: Record<string, string> = {
    "/dashboard": "Overview",
    "/dashboard/nodes": "Nodes",
    "/dashboard/routes": "Routes",
    "/dashboard/traffic": "Traffic",
    "/dashboard/security": "Security",
    "/dashboard/metrics": "Metrics",
    "/dashboard/plugins": "Plugins",
    "/dashboard/audit": "Audit Log",
    "/dashboard/secrets": "Secrets",
    "/dashboard/settings": "Settings",
  };
  // Longest matching prefix wins (so /dashboard/routes/new → Routes).
  const match = Object.keys(map)
    .filter((p) => pathname === p || pathname.startsWith(p + "/"))
    .sort((a, b) => b.length - a.length)[0];
  return match ? map[match] : "VORTEX";
}

// Layout is the persistent shell: sidebar, top bar, and the routed page content.
export function Layout() {
  const { pathname } = useLocation();
  return (
    <div className="flex h-screen overflow-hidden">
      <Sidebar />
      <div className="flex flex-1 flex-col overflow-hidden">
        <TopBar title={titleFor(pathname)} />
        <main className="flex-1 overflow-auto p-6">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
