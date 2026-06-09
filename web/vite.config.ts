import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import path from "node:path";

// vite + React build. Output goes into ../internal/assets/dashboard/ so
// `go:embed` picks the fresh bundle on next `go build`. emptyOutDir
// wipes the previous vanilla files cleanly.
//
// `/dashboard/` is set as the public base so the absolute asset URLs
// the browser sees match the nginx mount point (`<base href="/dashboard/">`
// then strips it). In dev the Vite server runs at /, but every fetch to
// /api/ /events /auth is proxied to the running dashboard daemon on
// :9080 so React-side `fetch("api/admin/...")` resolves the same way it
// will in production.
export default defineConfig({
  plugins: [react()],
  base: "/dashboard/",
  resolve: {
    alias: { "@": path.resolve(__dirname, "./src") },
  },
  build: {
    outDir: path.resolve(__dirname, "../internal/assets/dashboard"),
    emptyOutDir: true,
    sourcemap: false,
    target: "es2022",
    chunkSizeWarningLimit: 600,
    rollupOptions: {
      output: {
        manualChunks(id) {
          // Split the React + Radix runtime off the application bundle
          // so the first paint can stream JS while the bigger vendor
          // chunk arrives. ~80KB gz vendor vs ~40KB gz app.
          if (id.includes("node_modules")) {
            if (id.includes("react") || id.includes("scheduler")) return "react";
            if (id.includes("@radix-ui") || id.includes("@tanstack/react-query")) return "ui";
          }
          return null;
        },
      },
    },
  },
  server: {
    port: 5173,
    strictPort: true,
    // Dev proxy → running `apigw-dashboard` on :9080.
    // Run `cd /var/lib/apigw && sudo apigw dashboard serve` (or whatever
    // your local dev setup is) and the React dev server will hit it.
    proxy: {
      "/api":    { target: "http://localhost:9080", changeOrigin: true },
      "/events": { target: "http://localhost:9080", changeOrigin: true, ws: true },
      "/auth":   { target: "http://localhost:9080", changeOrigin: true },
    },
  },
});
