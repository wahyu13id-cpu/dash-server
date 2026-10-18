import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// During development the SPA runs on Vite (5173) and proxies API + WebSocket
// traffic to the Go backend. In production the Go server serves ../dist.
export default defineConfig({
  plugins: [react()],
  server: {
    host: "127.0.0.1",
    port: 5173,
    proxy: {
      "/api": {
        target: "http://127.0.0.1:8080",
        changeOrigin: true,
        ws: true,
      },
    },
  },
  build: {
    outDir: "dist",
    sourcemap: false,
    chunkSizeWarningLimit: 900,
  },
});
