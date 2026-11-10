import tailwindcss from "@tailwindcss/vite";
import { tanstackRouter } from "@tanstack/router-plugin/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";
import { gzipDist } from "./gzipDist";

// Builds into web/dist, which the Go binary embeds (web/embed.go).
// public/.gitkeep is copied into dist on every build, so the tracked
// web/dist/.gitkeep survives emptyOutDir and `go build` works in a clean
// checkout without running pnpm.
export default defineConfig({
  plugins: [
    gzipDist(),
    // Must come before react(): generates src/routeTree.gen.ts from src/routes.
    tanstackRouter({ target: "react", autoCodeSplitting: true }),
    react(),
    tailwindcss(),
  ],
  server: {
    // The daemon's default listen address (`platform serve`).
    proxy: { "/api": "http://127.0.0.1:7070" },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
});
