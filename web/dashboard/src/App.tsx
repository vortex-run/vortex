import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { fetchHealth } from "./lib/api";

const queryClient = new QueryClient();

// Landing shows the live cluster health to prove the embedded app talks to the
// VORTEX management API. Full routed pages and layout arrive in File 2.
function Landing() {
  const { data, isLoading, isError } = useQuery({
    queryKey: ["health"],
    queryFn: fetchHealth,
    refetchInterval: 5000,
  });

  return (
    <div className="min-h-screen flex flex-col items-center justify-center gap-4 p-8">
      <h1 className="text-3xl font-bold tracking-tight text-primary">VORTEX</h1>
      <p className="text-muted-foreground">Management Dashboard</p>
      <div className="mt-4 rounded-lg border border-border bg-card p-6 text-sm">
        {isLoading && <span className="text-muted-foreground">Loading health…</span>}
        {isError && <span className="text-red-400">management API unreachable</span>}
        {data && (
          <div className="space-y-1">
            <div>
              Cluster: <span className="font-mono">{data.cluster_name}</span>
            </div>
            <div>
              Status: <span className="font-mono">{data.status}</span>
            </div>
            <div>
              Version: <span className="font-mono">{data.version}</span>
            </div>
            <div>
              Uptime: <span className="font-mono">{data.uptime}</span>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

export default function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <Landing />
    </QueryClientProvider>
  );
}
