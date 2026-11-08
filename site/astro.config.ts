import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "astro/config";

// A static site: every page and illustration renders at build time and the
// output in dist/ ships no JavaScript. The Dockerfile serves it with Caddy.
export default defineConfig({
  site: "https://dock.nordtempo.com",
  vite: { plugins: [tailwindcss()] },
});
