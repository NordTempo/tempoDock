# 0087: Generated hostnames nobody can guess

Status: accepted (asked by Saile through the coordinator, 2026-11-17).
Amends [0009](0009-dynamic-domains-and-tls.md) (generated domains),
[0037](0037-analytics.md) (public dashboard addresses) and
[0073](0073-public-status-pages.md) (status page names and addresses).
Amended by [0090](0090-wildcard-certificates.md): the wildcard
follow-up below was built.

## Context

Every hostname Dock generated under a base domain came from names alone:
`<service>-<project>[-<env>].<base>` for services and previews,
`status-<page>.<base>` for status pages, `stats-<site>.<base>` for public
dashboards. Anyone could find another organization's services by trying
names, and a status page's name had to be unique in the whole install
because it was its address: Saile couldn't call a page `status` because
another organization had one.

## Decision

| Topic | Decision |
|---|---|
| Shape | A readable prefix, a dash and 7 random lowercase base32 characters (a-z, 2-7: 35 bits, `crypto/rand`): `web-shop-staging-k7m2q9x.<base>`, `status-acme-k7m2q9x.<base>`, `stats-shop-com-k7m2q9x.<base>`. The prefix is cut so the label stays within 63 characters (`hostLabel`). On the rare clash Dock draws again, at most 8 times |
| Fixed once made | A generated domain is a stored row: redeploys, renames and moves between servers with the same base keep it. A move to a server with another base makes a new one there, as before, with its own random part. A site's dashboard address (`sites.public_slug`) is made with the site and no longer follows renames, which broke the old link. A status page keeps its random part (`status_pages.address_key`) through renames: renamed, the readable part changes and the old address redirects as before |
| Names | A status page's name is unique in its organization (`status_pages (org_id, slug)`), and so are its old names (`status_page_old_slugs` keyed by organization). Service, project and site names keep their scopes. Custom hostnames people type stay unique across the install |
| Existing hostnames | None change: live URLs keep working. A status page from before has no random part (`address_key` '') until its first rename. To swap a service's old hostname for an unguessable one, generate a new domain and remove the old (`dock domain generate`); no separate "regenerate" action |
| Plain HTTP | A direct server's port 80 redirects every hostname to HTTPS, known or not, so plain HTTP no longer tells which hostnames exist. Caddy answers ACME HTTP-01 challenges before any route (`Server.ServeHTTP`), so certificates are still issued; checked against a local Pebble |
| Migration | 0120: `status_pages.address_key`, the per-organization indexes, `status_page_old_slugs` rebuilt with `org_id` and `address_key` |

## What can still be found, by ingress mode

| Mode | Certificates | What is enumerable |
|---|---|---|
| `direct` | Caddy's on-demand ACME, one certificate per hostname, logged in Certificate Transparency | Every hostname that ever got a certificate, from CT logs (crt.sh), generated ones included. The random part stops guessing, not this. HTTPS also tells existence apart: an unknown name fails the TLS handshake (the `ask` check refuses it), a known one gets a certificate; with the random part that takes a guess per name |
| `cloudflare_tunnel` | Cloudflare's edge certificate, which covers `*.<zone>` | Nothing per hostname reaches CT. With the wildcard record every name resolves and an unknown one gets Dock's 404 page; with Dock's per-hostname records an unknown name doesn't resolve. Either way only guessing finds a name, and the random part stops that |

Unpublished status pages and private dashboards aren't routed and get no
certificate, the same as unknown names. A service behind access
protection ([0030](0030-access-protection.md)) shows Dock's sign-in, so
its hostname is known to exist once found; the random part keeps it from
being found. Dock's 404 page names nothing.

## Not done

- **A wildcard certificate per base domain** is what takes generated
  names out of CT logs on direct servers. Caddy is a stock release
  (install.sh) without DNS provider plugins, so Dock would get `*.<base>`
  itself by ACME DNS-01 through the Cloudflare connection where the
  base's zone is on one, keep it sealed, load it into Caddy (`load_pem`)
  and leave generated names off the on-demand policy, renewing on one
  timer 30 days before expiry. Remote nodes need a protocol bump to
  receive it. About 1000 lines, a migration and a Pebble integration
  test. A follow-up for Saile to decide.
- Renaming existing hostnames, or a "regenerate address" action.
- Making the sign-in page of an access-protected service look like a 404.

## Consequences

- Nobody finds another organization's services, previews, status pages
  or dashboards by trying names; on direct servers CT logs still list
  them until the wildcard follow-up.
- Two organizations may name status pages the same.
- Generated hostnames are longer by 8 characters and no longer say a
  service's name exactly; the readable prefix still does.
