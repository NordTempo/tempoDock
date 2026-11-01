import { defineConfig } from "vite";

// The session recorder (src/recorder, ADR 0037 phase A3): one minified
// IIFE that the Dock ingest embeds and serves at /_dock/a/r.js. Not part of
// the UI's build; `pnpm build:recorder` writes it into the Go tree and
// `pnpm check:recorder` fails when the committed copy is stale.
export default defineConfig({
  build: {
    lib: {
      entry: "src/recorder/recorder.ts",
      formats: ["iife"],
      name: "dockRecorder",
      fileName: () => "r.js",
    },
    outDir: process.env.RECORDER_OUT ?? "../internal/analytics/ingest/recorder",
    emptyOutDir: false,
    minify: true,
    sourcemap: false,
    target: "es2020",
  },
  publicDir: false,
});
