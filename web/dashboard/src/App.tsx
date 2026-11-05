import { lazy } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { BrowserRouter, Routes, Route } from "react-router";
import { Layout } from "./components/layout/Layout";

// Every page is its own chunk, fetched the first time it is opened, so the
// app shell (sidebar, top bar) renders before page code and the charts
// library arrive (it was a single ~700 kB bundle).
const Overview = lazy(() => import("./pages/Overview").then((m) => ({ default: m.Overview })));
const RoutesPage = lazy(() => import("./pages/Routes").then((m) => ({ default: m.RoutesPage })));
const RouteBuilder = lazy(() => import("./pages/RouteBuilder").then((m) => ({ default: m.RouteBuilder })));
const Namespaces = lazy(() => import("./pages/Namespaces").then((m) => ({ default: m.Namespaces })));
const Metrics = lazy(() => import("./pages/Metrics").then((m) => ({ default: m.Metrics })));
const Security = lazy(() => import("./pages/Security").then((m) => ({ default: m.Security })));
const AuditLog = lazy(() => import("./pages/AuditLog").then((m) => ({ default: m.AuditLog })));
const Secrets = lazy(() => import("./pages/Secrets").then((m) => ({ default: m.Secrets })));
const Nodes = lazy(() => import("./pages/Nodes").then((m) => ({ default: m.Nodes })));
const Plugins = lazy(() => import("./pages/Plugins").then((m) => ({ default: m.Plugins })));
const Settings = lazy(() => import("./pages/Settings").then((m) => ({ default: m.Settings })));
const Healing = lazy(() => import("./pages/Healing").then((m) => ({ default: m.Healing })));
const Traffic = lazy(() => import("./pages/Traffic").then((m) => ({ default: m.Traffic })));
const Agents = lazy(() => import("./pages/Agents").then((m) => ({ default: m.Agents })));

const queryClient = new QueryClient();

// Served under /dashboard/ by the VORTEX management API (embedded dist).
export default function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <BrowserRouter basename="/dashboard">
        <Routes>
          <Route element={<Layout />}>
            <Route index element={<Overview />} />
            <Route path="nodes" element={<Nodes />} />
            <Route path="routes" element={<RoutesPage />} />
            <Route path="routes/new" element={<RouteBuilder />} />
            <Route path="namespaces" element={<Namespaces />} />
            <Route path="traffic" element={<Traffic />} />
            <Route path="security" element={<Security />} />
            <Route path="metrics" element={<Metrics />} />
            <Route path="plugins" element={<Plugins />} />
            <Route path="audit" element={<AuditLog />} />
            <Route path="secrets" element={<Secrets />} />
            <Route path="healing" element={<Healing />} />
            <Route path="agents" element={<Agents />} />
            <Route path="settings" element={<Settings />} />
          </Route>
        </Routes>
      </BrowserRouter>
    </QueryClientProvider>
  );
}
