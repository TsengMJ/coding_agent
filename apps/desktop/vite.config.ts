import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import { readAgentConfig } from "./src/main/agent.ts";

// `npm run dev:renderer` serves the renderer with hot reload; start Electron
// with QUILL_RENDERER_URL set to the Vite URL. The proxy forwards /api to the
// running `quill serve` with its token, as the main process does.
function apiProxy() {
  const agent = readAgentConfig();
  if ("error" in agent) return undefined;
  return {
    "/api": {
      target: agent.url,
      changeOrigin: true,
      rewrite: (path: string) => path.replace(/^\/api/, ""),
      headers: { "X-Quill-Token": agent.token },
      configure: (proxy: { on: (event: "proxyReq", fn: (req: { removeHeader: (h: string) => void }) => void) => void }) => {
        proxy.on("proxyReq", (req) => req.removeHeader("origin"));
      },
    },
  };
}

export default defineConfig({
  plugins: [react()],
  base: "./",
  build: {
    outDir: "out/renderer",
    emptyOutDir: true,
    chunkSizeWarningLimit: 1500,
  },
  server: {
    proxy: apiProxy(),
  },
  test: {
    environment: "node",
    include: ["src/**/*.test.ts"],
  },
});
