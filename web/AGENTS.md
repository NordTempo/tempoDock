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
src/recorder/   Dock Analytics' session recorder (ADR 0037), not part of
                the UI: `pnpm build:recorder` writes it to
                internal/analytics/ingest/recorder/r.js, which is committed
tests/          dev-only browser fixtures (`*.html`, with `*.test.ts(x)`
                under src/); open them under `pnpm dev`, see tests/README.md.
                Pure functions are tested in `*.unit.test.ts` next to them
                instead (node:test, imports with `.ts`): `pnpm check` runs those
```

- Types come from `src/api/schema.d.ts` (`components["schemas"][...]`)
  or `src/data/types.ts`. Never redeclare an API shape. After an API
  change, `make generate` from the repo root.
- Nothing polls. `src/data/live.ts` patches or invalidates the query cache
  from `/api/v1/events`; a new event type is handled there (or in its
  feature's data.ts). Every call that changes something is a
  `useAction` (`src/data/action.ts`; Biome refuses `useMutation`
  outside it), naming the cache subtrees it touches: only what the
  action changes, and the same subtrees its audit event invalidates in
  `live.ts`, since other pages refetch from that event. While a call is
  in flight, live.ts leaves its subtrees to it, so the page that made
  the change fetches once; an `invalidateQueries` of your own after a
  call fetches twice. A call that changes nothing cached touches
  `() => []`, one whose answer replaces a query sets it in mutationFn,
  and a call that isn't a mutation (a streamed query run) goes through
  `settling`.
  `src/data` is in the initial bundle, so a mutation
  only one screen uses lives in that screen's feature.
- A route's `beforeLoad` also runs when a hovered link preloads it
  (`defaultPreload: "intent"`). Change nothing global there (the org,
  the console flag, the query cache) while `preload` is true: see
  `routes/_app.$org.tsx`, whose org switch on a hover once dropped the
  queries of the page being opened (`tests/org-switch.html`).
- Every server action the CLI has is in the UI too (ADR 0026), and the
  button shows its CLI equivalent (`Act` with `cli`).
- A new audit action gets its words in `features/audit/AuditTable.tsx`
  (`words`) in the same change; without them Activity shows the raw key.
- Heavy code loads with what opens it: dialogs and editors are `lazy()`
  (see `features/create/open.tsx`). `make perf` holds the initial JS, CSS
  and fonts to a budget.
- A route's search is its `validateSearch` result laid over the raw
  parameters, so a key it leaves out keeps its raw value (`?request=abc`
  reaches the page as a string typed as a number). Name every key and
  set a dropped one to `undefined`, never `cond ? { key } : {}` or
  `...(cond && { key })`.
- Only a route's components are code-split. `beforeLoad`, `loader` and
  `validateSearch` go into the route tree, in the initial bundle, with
  everything they import: a check that needs a big module belongs in the
  component (see `routes/_app.$org.help.$topic.tsx`, whose topic check in
  `beforeLoad` once put every help topic in the initial JS).
- An input for a secret (a key, a token, a variable's value) sets
  `autoComplete`: `new-password` on a `type="password"` input, since
  browsers ignore `off` there, and `off` on a text input or textarea.
  `src/ui/secretInputs.unit.test.ts` checks the password inputs.
- Help (`features/help`): a new page gets its topic in `where.ts` if its
  parent's isn't right, and a new feature a topic in `topics.ts`, short:
  the rows' `dock` commands are run against the real CLI in
  `internal/cli`'s tests and the guide links against `docs/guide`.
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
fails if either generated API client or the committed recorder is stale.
