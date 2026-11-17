import { satteri } from "@astrojs/markdown-satteri";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "astro/config";
import { guideLinks, shellCommands } from "./src/markdown";

// A static site: every page and illustration renders at build time. The
// home page ships no JavaScript; the docs pages ship one small script,
// their heading search. The Dockerfile serves dist/ with Caddy.
//
// Pages are files (docs/install.html, served at /docs/install by the
// Caddyfile). The guide's Markdown keeps its punctuation as written:
// flags like --json and quotes in commands must not turn into dashes
// and curly quotes.
export default defineConfig({
  site: "https://dock.nordtempo.com",
  trailingSlash: "never",
  build: { format: "file" },
  markdown: {
    processor: satteri({
      mdastPlugins: [guideLinks, shellCommands],
      features: { smartPunctuation: false },
    }),
    shikiConfig: { theme: "vitesse-black" },
  },
  vite: { plugins: [tailwindcss()] },
});
