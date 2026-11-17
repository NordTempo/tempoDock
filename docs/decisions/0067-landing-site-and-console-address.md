# 0067: The landing site, and the console's address

Status: accepted (asked by Saile, 2026-11-08; decided with the agent)

## Context

Dock needs a page marketing can point at. Saile wants it at
`dock.nordtempo.com`, which is where the console (Dock's UI and API,
[0029](0029-public-address.md)) is today. Both can't have that hostname:
the public address hands the whole host to the daemon.

| Force | Means here |
|---|---|
| One subdomain level | DNS goes through Cloudflare, whose free certificate covers `*.nordtempo.com` but not `app.dock.nordtempo.com` |
| Not next to hosted apps | `nordtempo.dev` is the base domain for apps and previews. Any app there can set cookies for the whole domain, which a console on `dock.nordtempo.dev` would receive (cookie tossing) |
| Ship copy on its own | A landing page inside the binary would need a Dock release for every copy change |
| Old links | Invite links, CLI contexts and bookmarks point at `dock.nordtempo.com` |

## Decision

| Topic | Decision |
|---|---|
| Hostnames | The landing site at `dock.nordtempo.com`; the console moves to `console.nordtempo.com` (`dock address set`) |
| Where it lives | `site/` in this repo: Astro, static, no client JavaScript. Illustrations render at build time |
| Serving | `site/Dockerfile` builds it (context `site/`) and serves it with Caddy on :8080. Deployed on Dock as its own project with `dock git connect NordTempo/tempoDock --dir site`, so only pushes that change `site/` deploy it |
| Old links | Caddy serves only the site's own paths; every other path gets a 308 to the console with the same path and query, so invites and deep links keep working |
| Early access | A mailto link for now (`site/src/site.ts`). A form is its own change if the volume asks for one |
| Numbers on the page | Only measured ones, from [performance](../performance.md); they change with the budgets |

## Order of the switch

The address and an app domain can't share a hostname (`hostname_taken`),
so the console moves first:

1. Deploy the site on a generated domain and review it there.
2. Add DNS for `console.nordtempo.com`, then `dock address set console.nordtempo.com`.
3. Update the SSO providers' redirect URIs, the GitHub App's callback and
   webhook URLs, and CLI contexts (`dock context add`).
4. Add `dock.nordtempo.com` as the site's domain.

## Consequences

- Passkeys are bound to the address's host ([0057](0057-two-factor-sign-in.md)),
  so every passkey stops working at step 2. Users sign in with an
  authenticator app or a recovery code and add their passkey again; anyone
  with only a passkey and no codes needs `dock admin reset-2fa`. Cheapest
  now, while there are few users.
- The CLI drops its token on a redirect to another host, so a context on the
  old address fails until it is re-added; the redirect only helps browsers.
- The site is one more container on the server: about 15 MB of memory idle.

## The docs on the site (2026-11-17)

The site also serves the user guide, at `/docs` and `/docs/<guide>`:
people read it before they install. The pages are `docs/guide/*.md`
rendered at build time, so the guide has one source for the repository,
the binary (in-app help, MCP) and the site. Two consequences:

- The image's build context is `site/`, which doesn't hold `docs/guide`,
  so the Dockerfile fetches the directory from `main`. Dock deploys the
  site on pushes that touch `site/`: a change to the guide alone is live
  with the next site deploy.
- The docs pages ship the site's only script, a search over the guides'
  headings. The home page still ships none.

The site no longer links to the console: it is NordTempo's own install,
not a public one. The redirect of unknown paths to it is gone too; they
get the site's 404 page.
