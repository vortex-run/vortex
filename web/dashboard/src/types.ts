// TypeScript types mirroring VORTEX's Go management API responses.

// RouteHealth mirrors api.RouteHealth (GET /health routes[]).
export interface RouteHealth {
  name: string;
  protocol: string;
  listen: string;
  active: number;
}

// HealthResponse mirrors api.healthResponse (GET /health).
export interface HealthResponse {
  status: string;
  version: string;
  config_hash: string;
  cluster_name: string;
  uptime: string;
  routes?: RouteHealth[];
}

// ClusterInfo is a derived view of cluster state for the UI.
export interface ClusterInfo {
  name: string;
  nodes: NodeInfo[];
}

// NodeInfo describes a single cluster node for the Nodes page.
export interface NodeInfo {
  id: string;
  status: "running" | "stopped";
  leader: boolean;
  protocols: string[];
  uptime: string;
}
