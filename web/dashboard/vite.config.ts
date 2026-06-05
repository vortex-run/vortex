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
