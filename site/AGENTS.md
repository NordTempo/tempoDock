# site

Tempo Dock's landing page ([ADR 0067](../docs/decisions/0067-landing-site-and-console-address.md)):
Astro, Tailwind, built to static HTML, served by Caddy (`Dockerfile`,
`Caddyfile`). Its own pnpm project, separate from `web/`. The home page
ships no client JavaScript; the docs pages ship one script, their heading
search (`DocsSearch.astro`).

```sh
pnpm dev     # hot reload on :4321
pnpm check   # astro check (types) and Biome
pnpm build   # dist/
docker build site   # the image, built from site/ as Dock builds it
```

- Keep it static. Interactions are CSS (the stack's hover is `:has()`),
  illustrations are computed in the component frontmatter (`src/iso.ts`).
- Numbers on the page come from `docs/performance.md`; change them together.
- Terminal examples use real CLI commands and messages.
- Copy: short sentences, no em dashes. True black, white text, the
  feature colours from `src/styles/global.css`.
- The docs (`/docs`, `/docs/<guide>`) are `docs/guide/*.md`, rendered at
  build time from where they are (`src/content.config.ts`): never copy
  guide text into `site/`. `src/guide.ts` has the sections and their
  order, and a guide missing from it fails the build. `src/markdown.ts`
  turns the guide's links into site or GitHub links and its shell blocks
  into commands to copy. The image build fetches `docs/guide` from `main`
  (`Dockerfile`), and Dock deploys the site on pushes that touch `site/`:
  a change to the guide alone goes live with the next site deploy.
- Addresses (repository, early access email) live in `src/site.ts`. The
  site never links to NordTempo's own console; example hostnames in the
  guide are fine. The Caddyfile still redirects paths the site doesn't
  serve there, so old invite links work.
