import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
  // The admin app is served from the root of the dedicated admin hostname.
  base: "/",
  plugins: [react()],
  server: {
    proxy: {
      "/api": "http://127.0.0.1:3000"
    }
  },
  build: {
    outDir: "dist",
    emptyOutDir: true
  }
});
