import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The dashboard is served by VORTEX under /dashboard/, so the app must be built
// with that base path. In dev, API calls are proxied to the running VORTEX
// management server on :9090.
export default defineConfig({
  base: "/dashboard/",
  plugins: [react()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
    rollupOptions: {
      output: {
        // React and the router are needed by every page and change rarely, so
        // they get their own long-cached chunk. Charts (recharts) are left to
        // the bundler: only lazily loaded pages use them, so they split off.
        manualChunks(id) {
          if (!id.includes("node_modules")) return undefined;
          if (/\/node_modules\/(react|react-dom|react-router|scheduler)\//.test(id)) return "react";
          return undefined;
        },
      },
    },
  },
  server: {
    proxy: {
      "/health": "http://localhost:9090",
      "/metrics": "http://localhost:9090",
      "/api": "http://localhost:9090",
      "/internal": "http://localhost:9090",
    },
  },
});
