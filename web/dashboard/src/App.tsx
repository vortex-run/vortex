import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { BrowserRouter, Routes, Route } from "react-router-dom";
import { Layout } from "./components/layout/Layout";
import { Overview } from "./pages/Overview";
import { RoutesPage } from "./pages/Routes";
import { RouteBuilder } from "./pages/RouteBuilder";
import { Metrics } from "./pages/Metrics";
import { Security } from "./pages/Security";
import { Stub } from "./pages/Stub";

const queryClient = new QueryClient();

// All routes live under the /dashboard base path (the app is served there by
// VORTEX). Pages are stubs here; real implementations land in later files.
export default function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <BrowserRouter basename="/dashboard">
        <Routes>
          <Route element={<Layout />}>
            <Route index element={<Overview />} />
            <Route path="nodes" element={<Stub name="Nodes" />} />
            <Route path="routes" element={<RoutesPage />} />
            <Route path="routes/new" element={<RouteBuilder />} />
            <Route path="traffic" element={<Stub name="Traffic" />} />
            <Route path="security" element={<Security />} />
            <Route path="metrics" element={<Metrics />} />
            <Route path="plugins" element={<Stub name="Plugins" />} />
            <Route path="audit" element={<Stub name="Audit Log" />} />
            <Route path="secrets" element={<Stub name="Secrets" />} />
            <Route path="settings" element={<Stub name="Settings" />} />
          </Route>
        </Routes>
      </BrowserRouter>
    </QueryClientProvider>
  );
}
