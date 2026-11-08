# site

Tempo Dock's landing page ([ADR 0067](../docs/decisions/0067-landing-site-and-console-address.md)):
Astro, Tailwind, built to static HTML with no client JavaScript, served by
Caddy (`Dockerfile`, `Caddyfile`). Its own pnpm project, separate from `web/`.

```sh
pnpm dev     # hot reload on :4321
pnpm check   # astro check (types) and Biome
pnpm build   # dist/
docker build -f site/Dockerfile .   # from the repo root, as Dock builds it
```

- Keep it static. Interactions are CSS (the stack's hover is `:has()`),
  illustrations are computed in the component frontmatter (`src/iso.ts`).
- Numbers on the page come from `docs/performance.md`; change them together.
- Terminal examples use real CLI commands and messages.
- Copy: short sentences, no em dashes. True black, white text, the
  feature colours from `src/styles/global.css`.
- Addresses (console, early access email) live in `src/site.ts`. The
  Caddyfile redirects every path the site doesn't serve to the console.
