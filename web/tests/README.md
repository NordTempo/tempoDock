# Layout checks

Run `pnpm dev` in `web`, then open `/tests/overflow.html`. No backend is
needed. The page reports `PASS: horizontal overflow` in its title or throws
with the failed assertion.

Check at 320, 390, 768, 1280 and 1920px. The fixture exercises the real Dock
and data-panel action buttons with long CLI hints at both viewport edges,
using hover, keyboard focus, Escape and scrolling. It also verifies both
audit-table variants keep wide rows scrollable without widening the page.

The fixture is a dev-only entry, not part of either production build.
