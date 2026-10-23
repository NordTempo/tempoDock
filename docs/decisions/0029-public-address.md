# 0029: Dock's public address

Status: accepted (decided by the agent, on Saile's direction)

## Context

The API and UI listen only on `listen`, the Tailscale address by default,
over plain HTTP. The docs said never to expose it. That was right while
everyone who used Dock was on the tailnet. Dock is now an invite-only tool
for a company ([0027](0027-organizations.md)): colleagues get an invite
link and should open it in a browser, and CI should deploy, without
joining a tailnet first. The owner wants Dock at `dock.nordtempo.com`,
set from the platform console.

| Force | Means here |
|---|---|
| TLS | Tokens and session cookies must only cross the internet over HTTPS |
| One proxy | Caddy already holds ports 80/443 and issues certificates for apps and the webhook host ([0018](0018-git-push-deploy.md)) |
| Runtime setting | Operators change it in the console and CLI ([0026](0026-ui-cli-parity.md)), with no config edit or restart |
| Rate limits | Login and CLI-login limits are per client IP; behind a proxy every request would come from it |
| Keep the way in | Changing the address must never lock operators out |

## Decision

| Topic | Decision |
|---|---|
| Setting | One hostname, stored in `platform_settings` (one row) and kept in memory. Set with `PUT /address` / `dock address set`, removed with `DELETE /address` / `dock address remove`. Operators only. Audited as `platform.address_set` / `platform.address_removed`, platform events with no org |
| Routing | The control plane's Caddy serves the hostname like an app domain: an on-demand certificate (the `ask` endpoint approves it), HTTP redirected to HTTPS, proxied to `public_listen` (`127.0.0.1:7073`). A tunnel control plane serves it through Cloudflare like its apps |
| Listener | `public_listen` serves the same API and UI handler as `listen`. It must be loopback, and it is served whether or not an address is set, so setting one needs no restart |
| Client IP | Caddy sets `X-Dock-Client-Ip` to the client's address as it determined it (`{http.vars.client_ip}`: the peer, or `CF-Connecting-IP` from the local cloudflared), replacing any value the client sent. Only `public_listen` reads it, as the request's remote address. So rate limits and tunnels see the visitor |
| Cookies | Session cookies are `Secure` there: Caddy sends `X-Forwarded-Proto: https`, which the API already honours |
| Private address | `listen` keeps working, unchanged. Removing or breaking the public address never locks anyone out |
| Conflicts | The hostname can't be an app domain or `[hooks] public_host`, either way round. Adding a domain with it fails with `hostname_taken` |
| Status | Reading the address checks its DNS and certificate now (`CheckDomain` on the local runtime, as for domains), and returns the records to create. The first check's TLS handshake is what makes Caddy issue the certificate. Nothing runs in the background |
| Links | Invite links (`CreatedInvite.url`) and GitHub commit status links use it; `[hooks] ui_url` still wins for the latter. CLI login links already use the address the CLI connected to |
| Console | System → Address (mock A, picked by the owner): the hostname with Save and Remove, the status as a domain shows it with Check again, the DNS record, and the private address |

## Why this shape

**Caddy, not TLS in the daemon.** Caddy already terminates TLS for every app
on the server, renews certificates and redirects HTTP. Serving Dock is one
more route. A second TLS stack in the daemon would need its own ACME client
and ports.

**A separate loopback listener.** The header that carries the client IP can
only be trusted from Caddy. A listener that only Caddy reaches makes that
true without IP allowlists. The Tailscale listener stays exactly as it
was. Apps can't reach the server's own addresses (the app firewall), so
they can't reach it either.

**A database setting, not config.toml.** Operators asked to change it from
the console. A config value would need root on the server and a restart.

## Consequences

- Dock is on the internet once an address is set. What protects it is what
  protected it on the tailnet: passwords (bcrypt, rate limited per IP and
  per email), tokens, and operator-only platform endpoints. There is no
  signup, so only invited people have accounts.
- `dock connect` tunnels go through Caddy as HTTP/1.1 upgrades. Cloudflare
  tunnels may not pass Dock's upgrade protocol on; use the private address
  for those there.
- A restored or moved control plane keeps the address (it's in the
  database); its DNS then has to point at the new server.
