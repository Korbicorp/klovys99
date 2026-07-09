import { defineConfig } from "vite";

export default defineConfig({
  server: {
    port: 3001,
    strictPort: true,
    proxy: {
      "/api": "http://127.0.0.1:8080",
      "/dashboard": "http://127.0.0.1:8080",
      "/dashboard/assets": "http://127.0.0.1:8080",
    },
    fs: {
      allow: ["../.."],
    },
  },
});
