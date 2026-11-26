# 0105: Dock adds the analytics script

Status: accepted (Saile asked for it on 2026-11-26). Amends
[0037](0037-analytics.md) (how pages load the script) and
[0028](0028-updates-from-the-console.md) (an update carries Caddy).

## Context

A site counted nothing until its app put
`<script src="/_dock/a/s.js" defer></script>` in its pages. Caddy
already routes `/_dock/a/*` on every analytics hostname, so the one
step left to the app was the tag. Stock Caddy can't change a response's
body: `templates` evaluates the whole page (any `{{` in a Vue or
Handlebars page breaks), and `replace-response` is a plugin. So Dock
needs a Caddy of its own build.

## Decision

| Topic | Decision |
|---|---|
| Dock's Caddy | `cmd/dock-caddy`, its own Go module (Caddy's dependencies stay out of the main `go.mod`): Caddy's standard modules, as in Caddy's release build (same tags), plus `http.handlers.dock_inject`. The release ships `dock-caddy-linux-{amd64,arm64}` in `SHA256SUMS`, built without VCS stamps, so it changes only when Caddy or the handler does |
| The handler | In a route's chain right before the upstream (after access, wake and trace), for the hostnames of analytics groups with `Script` set. It changes a page load only: a `GET` without `Range` or `Upgrade` whose `Sec-Fetch-Dest` is `document`, `iframe` or missing (htmx and fetch() fragments send `empty`), answered `200 text/html`, not compressed, not `no-transform`, and allowed by its `Content-Security-Policy`. The tag goes right after the opening `<head>` tag, if that is in the first 16 KB; otherwise the page goes out unchanged |
| Streaming | Nothing is held but the bytes before `<head>`. A flush before it gives up on the page. The upstream is asked for an uncompressed page (its `Accept-Encoding` is dropped), and the page is gzipped here, level 5, when the visitor takes gzip. Changed pages lose `Content-Length` and `Accept-Ranges`, get a weak `ETag` and `Vary: Accept-Encoding`. About 1 µs and 744 B per page, 21 µs with gzip (a 33 KB page) |
| CSP | Per policy, `script-src-elem`, else `script-src`, else `default-src`: none allows it; `'self'`, `*`, `https:` or `http:` allow it unless `'strict-dynamic'` is there; else only a nonce, which the tag then carries (`nonce="..."`). A policy that allows none of these leaves the page alone |
| The tag | `<script src="/_dock/a/s.js" defer data-dock-auto></script>`. `s.js` removes an auto tag from the page when it runs, before a framework hydrating the whole document (React 18 on `document`) sees it, and gives way to a tag the page has itself (`script[src*="/_dock/a/s.js"]` without `data-dock-auto`), whose `data-consent` counts |
| The setting | Per site, `auto_script`, on by default: off and the app loads the script itself, as before |
| A stock Caddy | Refuses a config with `dock_inject` ("unknown module"). The node then loads the routes without it and leaves it out until the daemon restarts, which an update that installs Dock's Caddy does. Pages then load the script themselves |
| Installing | `install.sh` installs `DOCK_CADDY` as `/usr/local/bin/caddy` when given, else the release's, downloaded and checked with its binary. A dock binary given without its Caddy, by a release tarball (the bootstrap, an older updater, by hand), gets its release's (`fetch_caddy`); a failed download only warns. With neither (a dock binary from `make build`), Caddy's own release as before. A changed binary restarts `dock-caddy.service`, before the daemon starts |
| Updates | The control plane stages each arch's Caddy when the release lists it, sends it to nodes from protocol 36 (`CaddyUpdateProtocol`; older ones refuse extra files and fetch it themselves), and the updater checks it against the signed sums and passes it to install.sh as `DOCK_CADDY` |
| CI | The integration job builds Dock's Caddy and runs its tests, and the Caddy integration tests drive it. The lint job lints the module. `install.sh`'s stock Caddy version matches the one Dock's builds on (a test), so the fallback never swaps Dock's for an older stock one |

## Consequences

- An app gets analytics by turning them on: no code change, no deploy.
- Dock ships and updates a 51 MB Caddy of its own (Caddy's own release
  is 52 MB), and a Caddy upgrade is a `go.mod` bump in `cmd/dock-caddy`
  and a release.
- Every update carries Caddy (51 MB a server), though install.sh only
  replaces and restarts it when it changed.
- Analytics pages are gzipped by Caddy instead of the app, and lose
  Brotli or zstd the app may have used.
- A stock Caddy doesn't start on a saved config with `dock_inject`.
  install.sh putting one back (`CADDY_VERSION`) drops the saved config
  first, so Caddy starts on `caddy.json` and the daemon loads the routes.
  An install.sh from before this release that pins another Caddy version
  doesn't know to: the server serves nothing until `autosave.json` in
  `/var/lib/dock-caddy/caddy` is removed.
