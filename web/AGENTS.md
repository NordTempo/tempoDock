# web

One pnpm project, two apps: Dock's UI (`index.html`, `src/`,
`vite.config.ts`, built into `dist/`) and tempoBase's (`tempobase/`,
`vite.tempobase.config.ts`, built into `tempobase/dist/`). Both are
embedded into their Go binaries. `pnpm dev` / `pnpm dev:tempobase` run
them with hot reload; `pnpm dev:tempobase:memory` runs tempoBase on an
in-memory backend with no server.

## Dock's UI

```
src/routes/     file-based routes (TanStack Router, auto code splitting);
                routeTree.gen.ts is generated, never edit it
src/api/        openapi-fetch client over schema.d.ts (generated), SSE
src/data/       shared query keys and queries, mutations, live events
src/features/   one folder per area; a feature's own queries go in its
                data.ts (see features/org/data.ts)
src/shell/      sidebar, command palette, keyboard shortcuts, org context
src/ui/         the small kit (kit.tsx), Dialog, formatting
```

- Types come from `src/api/schema.d.ts` (`components["schemas"][...]`)
  or `src/data/types.ts`. Never redeclare an API shape. After an API
  change, `make generate` from the repo root.
- Nothing polls. `src/data/live.ts` patches or invalidates the query cache
  from `/api/v1/events`; a new event type is handled there (or in its
  feature's data.ts). Mutations use `useAction`, naming the cache
  subtrees they touch. `src/data` is in the initial bundle, so a mutation
  only one screen uses lives in that screen's feature.
- Every server action the CLI has is in the UI too (ADR 0026), and the
  button shows its CLI equivalent (`Act` with `cli`).
- Heavy code loads with what opens it: dialogs and editors are `lazy()`
  (see `features/create/open.tsx`). `make perf` holds the initial JS, CSS
  and fonts to a budget.
- Styling per `docs/design/README.md`: style A, Tailwind theme tokens
  instead of literal colors, no em dashes in copy, no repainting
  animations.

## The data panel, shared with tempoBase

`src/features/data/` is used by both apps (ADR 0024). Its components reach
their server only through a `DataSource` (`api.ts`) given by
`DataSourceContext`: Dock's is `source.ts`, tempoBase's is
`tempobase/src/data/source.ts`. Keep it that way:

- tempoBase may import anything in `features/data` except Dock's
  bindings (`source.ts`, `dockPolicy.ts`, `PolicyPage.tsx`).
- Words that differ between the hosts ("service" vs "connection", role
  names) come from the DataSource, not from literals in the panel.
- The panel is style G (`g.css`); tempoBase pulls the classes in with
  `@source` in `tempobase/src/tempobase.css`.

`pnpm check` typechecks both apps (`tsc -p tempobase` too), runs Biome and
fails if either generated API client is stale.
