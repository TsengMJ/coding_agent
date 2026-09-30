import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// `npm run dev` proxies the API to a running `dcode -no-open`; set
// DCODE_GUI_URL to the address it printed (without the #token part) and
// open the Vite URL with the same #token=... fragment.
const target = process.env.DCODE_GUI_URL ?? "http://127.0.0.1:8124";

export default defineConfig({
  plugins: [react()],
  base: "./",
  build: {
    outDir: "../dist",
    emptyOutDir: true,
    chunkSizeWarningLimit: 1500,
  },
  server: {
    proxy: {
      "/api": {
        target,
        changeOrigin: true,
        configure: (proxy) => {
          proxy.on("proxyReq", (req) => req.removeHeader("origin"));
        },
      },
    },
  },
  test: {
    environment: "node",
  },
});
