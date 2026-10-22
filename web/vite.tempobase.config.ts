import tailwindcss from "@tailwindcss/vite";
import { tanstackRouter } from "@tanstack/router-plugin/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// tempoBase's UI (ADR 0024 section 4): a second entry in this Vite project
// that shares web/src's data panel. Run from web/ (the pnpm scripts do):
// paths are relative to it. Builds into web/tempobase/dist, which the
// tempobase binary embeds (web/tempobase/embed.go). Its public/ holds only
// the .gitkeep that keeps the tracked dist/.gitkeep through emptyOutDir:
// style G uses system fonts, so Dock's font files stay out. Plugin paths
// are relative to the root (tempobase/).
export default defineConfig({
  root: "tempobase",
  plugins: [
    // Must come before react(): generates the route tree from the routes.
    tanstackRouter({
      target: "react",
      autoCodeSplitting: true,
      routesDirectory: "src/routes",
      generatedRouteTree: "src/routeTree.gen.ts",
    }),
    react(),
    tailwindcss(),
  ],
  server: {
    // tempobase serve's default listen address.
    proxy: { "/api": "http://127.0.0.1:7171" },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
});
