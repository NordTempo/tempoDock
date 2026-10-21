# Domains

Any hostname, any number per service, added or removed at any time. Nobody
edits proxy config: the daemon renders Caddy's whole config from the
database and loads it atomically.

## Custom domains

```
$ dock domain add shop.example.com
Added shop.example.com → web (production)
Point DNS:  A  shop.example.com  →  203.0.113.10
```

DNS can be set before or after adding. Status updates live.

### DNS records

`dns_records` lists the records to create; any one of them is enough.

| Hostname | Record |
|---|---|
| Apex (`example.com`) | `A` → the server's `public_ip` |
| Subdomain (`shop.example.com`) | `A` → `public_ip`, or `CNAME` → the service's generated domain (offered when it has one) |
| Generated | None; the wildcard covers it |

Only IPv4 is offered. The check flags any other address the hostname
resolves to (an old `A` or an `AAAA`) as "not this server; remove that
record", because a CA may validate against it. Without `public_ip` in the
server config, no `A` record is shown and DNS can't be checked.

### Status

```
added → waiting_for_dns → issuing_certificate → active
                                 └────────────→ error (with reason)
```

| Status | Means | Do |
|---|---|---|
| `added` | Not checked yet | Wait a few seconds |
| `waiting_for_dns` | DNS doesn't point here yet. `status_reason` says what it resolves to | Create the record; DNS caches may take a while |
| `issuing_certificate` | DNS is right, certificate on its way | Wait |
| `active` | Serving at `url` (`https://`, or `http://` with `disable_tls`) | |
| `error` | DNS is right but the certificate is invalid, or still missing after 10 minutes, or DNS also points somewhere else. `status_reason` says why | Fix and wait for the next check |

Checks back off from 5 s to 5 min per domain, so a fix can take up to five
minutes to show. With `disable_tls`, a domain is `active` as soon as DNS is
right. Until then each domain also carries `checked_at` (the last check)
and `resolved_ips` (what the hostname resolved to then), shown in the UI as
"checked 20s ago, now resolves to ...". They are kept in memory, so they
are empty for a moment after the daemon restarts, and active domains are not
checked again.

```sh
dock domain list            # status, reason, DNS records
dock domain list --wide     # plus URL, last check and what it resolved to
dock domain list --json     # everything, machine-readable
```

## Generated domains

```sh
dock domain generate
```

Gives the service `<service>-<project>[-<environment>].<base_domain>`
instantly, with no DNS work. The environment part is left out for
`production`; `-2`, `-3`, ... are added on collisions. Examples with
`base_domain = "apps.example.com"`:

| Service | Generated |
|---|---|
| `web` in `shop/production` | `web-shop.apps.example.com` |
| `web` in `shop/staging` | `web-shop-staging.apps.example.com` |

Needs `base_domain` in the server's `config.toml` and one wildcard record,
`*.apps.example.com A <public_ip>`. Without it the command fails with a hint
to add your own domain.

## Redirects

```sh
dock domain add www.example.com --redirect-to example.com
```

Permanent `308` redirect to the other hostname, keeping path and query.
Typical: `www` → apex. A redirect domain gets its own certificate.

## TLS

- Caddy gets a certificate (ACME, Let's Encrypt) on the first HTTPS request
  for a hostname, after asking the daemon whether the hostname belongs to a
  service. Unknown hostnames are refused, so pointing a random domain at the
  server can't trigger issuance.
- HTTP requests for known hostnames redirect to HTTPS. Unknown hosts get a
  small 404 page.
- Set `[caddy] acme_email` to get CA notices.
- If the daemon is down, existing certificates keep working; new domains
  wait until it's back.
- `[caddy] disable_tls = true` serves everything over plain HTTP (LAN,
  Tailscale, or behind a Cloudflare Tunnel; see [install](install.md#network-access)).

## Rules

- A hostname belongs to at most one service. Adding a taken one fails with
  `hostname_taken` (exit code 5).
- Adding and removing domains needs the `deploy` scope.
- Adding the first domain to a running service whose port isn't published
  yet starts a `config` release that publishes it (returned as `release`).
- `dock domain remove shop.example.com` stops routing it immediately.
- The service receives `DOCK_PUBLIC_DOMAIN` on its next release.
