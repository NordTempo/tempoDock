# Layout checks

Run `pnpm dev` in `web`, then open `/tests/overflow.html`. No backend is
needed. The page reports `PASS: horizontal overflow` in its title or throws
with the failed assertion.

Check at 320, 390, 768, 1280 and 1920px. The fixture exercises the real Dock
and data-panel action buttons with long CLI hints at both viewport edges,
using hover, keyboard focus, Escape and scrolling. It also verifies both
audit-table variants keep wide rows scrollable without widening the page.
Append `?scrollbar` to exercise classic scrollbars as well.

The fixture is a dev-only entry, not part of either production build.

# Org switch

Open `/tests/org-switch.html` under `pnpm dev`; no backend. It runs the
real route tree against a fetch fixture with two orgs: switching while a
link to the old org preloads must end in the new org with no failed page,
and a loader whose query was cancelled must load again instead of showing
"This page didn't load". The title says `PASS: org switch` or the console
has the failed assertion.
