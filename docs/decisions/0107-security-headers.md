# 0107: Security headers

Status: accepted (2026-11-27)

## Context

Dock's own UI sets security headers (`internal/httpkit/guard.go`), but
apps behind Dock's Caddy got whatever they sent. Most apps send none, and
the common ones (HSTS, `nosniff`, a referrer policy, a framing rule) are
the same for nearly every app. Adding them at the proxy is one place for
everyone, as long as it can't break an app.

## Decision

| Topic | Decision |
|---|---|
| The headers | `Strict-Transport-Security: max-age=31536000`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: strict-origin-when-cross-origin`, `X-Frame-Options: SAMEORIGIN` (`node.SecurityHeaders`). HSTS has no `includeSubDomains` or `preload`: other hosts under the name may not serve HTTPS, and preload can't be taken back quickly. `X-Frame-Options` rather than CSP `frame-ancestors`, so Dock never sends a `Content-Security-Policy` beside an app's own |
| No CSP | A content policy depends on what the app's pages load; a generic one breaks apps. It stays the app's |
| The app wins | Each header is its own Caddy `headers` handler, deferred, with `require` matching only a response without that header (Caddy's `?Header` in a Caddyfile, but per header: Caddy's own `?` groups them, so one header the app set would stop all of them). The handlers go first in the route, so Dock's own answers (a login, the stopped page) get them too. Basic auth's `401` is a handler error, which Caddy writes outside the route, so the headers are set on the response before the check and deleted once it passes: the `401` carries them, and a signed-in request reaches the app with none set, so the app's own still win. Server-wide `errors` routes would also reach the `401`, but with any of them Caddy logs every handled error, a `502` too, at debug level. Redirects don't get them |
| HSTS and plain HTTP | Only where visitors are on HTTPS: a direct server's HTTPS listener and a tunnel's (Cloudflare serves HTTPS). A `disable_tls` server sends the rest without HSTS |
| A setting, per service | `security_headers`, a staged service setting like the port: `dock service update --security-headers=false`, the Settings tab's Security headers row under Domains, `[deploy] security_headers` in dock.toml, `security_headers` in project files and the API and MCP's settings. An app framed by another site, or one moving off HTTPS, turns it off |
| Per release | A release seals the value it was deployed with (`releases.security_headers`), and routes follow the active release, so a rollback serves the old setting again and a staged change routes nothing until it is live |
| Defaults | On for new services (`CreateService`, templates, files, copies keep their source's). Off for services that existed before (migration 0137): HSTS can't be taken back for a year once a browser saw it, and an app framed elsewhere would break under its owner with no change of theirs. Owners turn it on |
| Project files | Written only when off. Left out, a new service gets them and an existing one keeps its setting, so a file from before this turns nothing on |
| Older servers | The route's field is new in node protocol 37; an older node ignores it and serves without the headers until it is updated |
| Custom headers | Not in this change: arbitrary response headers need validation, ordering against these and the app's, and a UI of their own. A later ADR if people ask |

## Consequences

- New apps get a baseline that scanners and browsers expect, with nothing
  to set up, and no app's own header is ever replaced.
- Changing the setting redeploys the service, like any setting. A setting
  applied without a release would have been cheaper, but then a rollback
  and dock.toml couldn't carry it.
- Existing services stay as they were until their owners opt in.
