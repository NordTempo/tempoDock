# Domains

Any hostname, any number per service, added or removed at any time. Nobody
edits proxy config: the daemon renders Caddy's whole config from the
database and loads it atomically.

## Custom domains

```
$ dock domain add shop.example.com
Added shop.example.com → web (production)
Prove control:  TXT  _dock-challenge.shop.example.com  →  dock-verify=…
Point DNS:  A  shop.example.com  →  203.0.113.10
Waiting for the TXT record... ok. Waiting for DNS... ok. Issuing certificate... ok.
https://shop.example.com is live.
```

DNS can be set before or after adding. The command waits up to
`--timeout` (default 5 minutes; `--detach` returns at once); still waiting
after that is not an error, the server keeps checking. Status updates live.

### Proving control

A custom hostname is routed and gets a certificate only once a TXT record
proves your organization controls it
([ADR 0042](../decisions/0042-domain-verification.md)): create
`_dock-challenge.<hostname>` with the value `dock domain add` printed (also in
`verification` in the API, and in the UI). Until it is there the domain is
`waiting_for_dns` and the reason says which record is missing. Hostnames
under the server's base domain, generated ones, domains from Cloudflare and
domains an operator adds need no proof (`verified` is `true` at once).

Several organizations may claim a hostname nobody proved yet; the first to
verify holds it, and the others' domains turn `error`. A hostname another
organization proved is refused. `dock domain check shop.example.com` checks
the TXT record, DNS and certificate now instead of at the next retry.

### DNS records

`dns_records` lists the records that route the hostname; any one of them
is enough. The TXT record above comes on top.

| Hostname | Record |
|---|---|
| Apex (`example.com`) | `A` → the server's `public_ip` |
| Subdomain (`shop.example.com`) | `A` → `public_ip`, or `CNAME` → the service's generated domain (offered when it has one) |
| Generated | None; the wildcard covers it |

Only IPv4 is offered. The check flags any other address the hostname
resolves to (an old `A` or an `AAAA`) as "not this server; remove that
record", because a CA may validate against it. Without `public_ip` in the
server config, no `A` record is shown and DNS can't be checked.

Records proxied by Cloudflare (orange cloud) or another CDN resolve to the
proxy, not the server. When the addresses don't match, the check requests
`https://<hostname>/` like a visitor and looks for the server's
`Dock-Server` header; if it comes back, the domain is `active`. Set
Cloudflare's SSL/TLS mode to Full (strict): in Flexible mode Cloudflare
talks plain HTTP to the server, which redirects back to HTTPS in a loop,
and the check reports that as an `error`.

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
minutes to show (`dock domain check` checks at once). With `disable_tls`, a domain is `active` as soon as DNS is
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

Gives the service `<service>-<project>[-<environment>]-<random>.<base_domain>`
instantly, with no DNS work. The environment part is left out for
`production`. The random part (7 characters) keeps others from finding
your services by guessing names ([ADR 0087](../decisions/0087-unguessable-generated-hostnames.md));
it is made once, so redeploys, renames and moves between servers with the
same base domain keep the hostname. Hostnames generated before it keep
working unchanged; to swap one for an unguessable one, generate a new
domain and remove the old. Examples with `base_domain = "apps.example.com"`:

| Service | Generated |
|---|---|
| `web` in `shop/production` | `web-shop-k7m2q9x.apps.example.com` |
| `web` in `shop/staging` | `web-shop-staging-p4fz2ma.apps.example.com` |

Needs `base_domain` in the server's `config.toml` and one wildcard record,
`*.apps.example.com A <public_ip>`. Without it the command fails with a hint
to add your own domain.

## Redirects

```sh
dock domain add www.example.com --redirect-to example.com
```

Permanent `308` redirect to the other hostname, keeping path and query.
Typical: `www` → apex. A redirect domain gets its own certificate.

A hostname in a connected Cloudflare zone can also redirect to any URL at
Cloudflare's edge, with no service and no server involved
([ADR 0068](../decisions/0068-redirects-on-cloudflare-zones.md)). Dock
makes a Single Redirect rule and a proxied placeholder record, both marked
as its own; the token needs Zone › Single Redirect › Edit.

```sh
dock cloudflare redirects add instagram.example.com https://instagram.com/acme   # 302 by default
dock cloudflare redirects add docs.example.com https://example.dev/docs --code 301 --keep-path
dock cloudflare redirects set instagram.example.com --code 301
dock cloudflare redirects                       # each one's state
dock cloudflare redirects remove instagram.example.com
```

A redirect belongs to the level its connection is on: an organization's
(its admins) or the platform's (`--platform`, operators).

## Domains from Cloudflare

With a Cloudflare connection (`dock cloudflare add`, Organization settings
or the console, [ADR 0036](../decisions/0036-cloudflare-api.md)), a
hostname in one of its zones needs no DNS work:

```sh
dock cloudflare zones                              # the zones domains can come from
dock domain add shop.example-apps.dev -s web --cloudflare
```

Dock routes the hostname through the server's Cloudflare tunnel (creating
or adopting one, also on a server with a public IP) and creates its
proxied CNAME; both go when the domain is removed. A record Dock didn't
make at that name is refused. Setup and tokens: [Tunnels managed by
Dock](install.md#tunnels-managed-by-dock).

## Access

By default anyone can open a service's domains. To keep one off the
public internet:

```sh
dock access password --username team   # prompts for the password twice
dock access login                      # members signed in to Dock
dock access public                     # anyone again
dock access                            # which one is set
```

In the UI it is the line above the domain list on Settings, Domains ("Open
to anyone", "Password for team", "Dock login") with Change. It covers
every domain of the service, generated ones included, and takes effect at
once. Each environment's service has its own setting, so staging
can be private while production is public.

| Mode | Visitors | The app gets |
|---|---|---|
| `public` | Anyone | |
| `password` | Type the username and password (HTTP basic auth; `curl -u team:...` works) | `X-Dock-User: team` |
| `login` | Are sent to Dock to sign in, then back. Members who can read the service's environment get in; the sign-in lasts 12 hours | `X-Dock-User: <their email>` |

Notes:

- `login` sends visitors to Dock's [public address](install.md#public-address) if one
  is set, else to its private one, so people off the tailnet need the
  public address.
- Requests from scripts to a `login` service get `401`; use `password` for
  machine traffic and webhooks.
- Turning `login` off and on again signs everyone out. A disabled user can
  keep a sign-in for up to 12 hours.
- `X-Dock-User` is always set by Dock; a value the client sends is
  replaced.

See [ADR 0030](../decisions/0030-access-protection.md).

## Uptime checks

An uptime check fetches a URL on one of the service's domains from outside
the server that serves it, so "production is down" reaches you within a
minute or two, even when the container still runs ([ADR
0055](../decisions/0055-uptime-checks.md)).

```sh
dock uptime add https://shop.example.com/health          # GET every minute, expects 200
dock uptime add https://shop.example.com/ --status 3xx --interval 5m --timeout 5s
dock uptime list                                         # state, 24h/7d/30d uptime, last probe
dock uptime domains                                      # a check of every verified domain without one
dock uptime remove 3
```

Dock adds a check by itself ([ADR 0059](../decisions/0059-analytics-area-and-slimmer-service-page.md)):
`GET https://<domain>/` every minute, expecting 200, marked `auto`, when a
domain that serves the service (not a redirect) is verified, or at the
service's first live release for domains verified before it. A domain
whose DNS names only private or tailnet addresses gets none, since no
probe could reach it. Removing the domain removes its check; you can
remove an auto check like any other (Dock doesn't add it again) or replace
it with your own. Uptime checks aren't part of `dock.toml`, so `dock apply`
and `dock project export` leave them alone. For domains verified before
this, **Check all domains** on its Metrics tab (or `dock uptime domains`)
adds the missing checks.

- A probe is one `GET`, redirects not followed. Up means the expected
  status (a code, or `2xx`/`3xx` for any of the class) within the timeout.
- Another online server probes, picked by the check so checks spread
  over servers; with no other server, the control plane probes its own
  apps, which sees the app and Caddy failing but not the network or DNS.
  The UI and `dock uptime list` say which server probes.
- Two failed probes in a row open an `uptime_down` alert (critical) on the
  alert channels; the next successful probe resolves it.
- Interval 30 seconds to 1 hour (default 1 minute); timeout 1 to 30 seconds
  and below the interval (default 10 s); at most 10 checks per service.
- Checks go on verified domains only (the TXT record above), and removing
  a domain removes its checks. Probes reach public addresses on ports 80
  and 443 only: a domain that resolves to a private, tailnet, Docker or
  cloud metadata address reads as down ("the domain resolves to an
  address that isn't public"). A site only on your LAN can't be checked.
- An organization has at most 50 checks; a server runs at most 8 probes
  at once and 2 per organization, and skips a probe rather than queue it.
- Project admins add and remove checks; everyone who can read the service
  sees them: the service's Metrics tab (checks, a 24-hour strip, uptime
  over 24 hours, 7 and 30 days, the last incidents, and next to them
  Dock's own health check of the container, the view from inside), the
  service's Overview (up or down and the last incident) and `dock status`.

History is kept per minute for a day and per hour for 30 days.

## TLS

- Caddy gets a certificate (ACME, Let's Encrypt) on the first HTTPS request
  for a hostname, after asking the daemon whether the hostname belongs to a
  service. Unknown hostnames are refused, so pointing a random domain at the
  server can't trigger issuance.
- On a direct server, every plain-HTTP request redirects to HTTPS, known
  hostname or not, so port 80 doesn't tell which hostnames exist
  ([ADR 0087](../decisions/0087-unguessable-generated-hostnames.md)).
  Through a tunnel, unknown hosts get a small 404 page.
- Set `[caddy] acme_email` to get CA notices.
- If the daemon is down, existing certificates keep working; new domains
  wait until it's back.
- `[caddy] disable_tls = true` serves everything over plain HTTP (LAN,
  Tailscale, or behind a Cloudflare Tunnel; see [install](install.md#network-access)).

### Wildcard certificates for generated hostnames

A certificate for each generated hostname would publish every one of them
in [certificate-transparency logs](https://crt.sh), where anyone can list
them. So when a direct server's base domain is in a zone of one of the
platform's Cloudflare connections (Console › Cloudflare), Dock gets one
certificate for `*.<base domain>` and `<base domain>` instead
([ADR 0090](../decisions/0090-wildcard-certificates.md)):

- It asks Let's Encrypt over ACME DNS-01: Dock adds two TXT records at
  `_acme-challenge.<base domain>`, marked as its own, and deletes them
  once the CA has checked them. The connection's token needs
  Zone › DNS › Edit on the zone.
- Every server with that base domain serves it for every name one label
  under the base, deployed or not: an address that isn't deployed gets the
  same 404 page as any other, so the TLS handshake doesn't tell you
  whether a service exists. Custom domains keep a certificate each, as
  above.
- Dock renews it a third of its lifetime before it expires (30 days for
  Let's Encrypt's 90-day certificates). A failed attempt is tried again
  after 5 minutes, doubling to 6 hours; after three failures, or at once
  when the token lacks the permission, the platform gets a
  `certificate_failed` alert. `dock cloudflare retry` tries again now.
- Without such a zone, or until the first certificate is issued, generated
  hostnames get a certificate each, as before. A certificate that expires
  without a renewal stops being served, and its names go back to that.
- Console › Address and `dock address` show each base domain's
  certificate: active (until when, and when it renews), issuing, failing
  with the reason, or unavailable and why.
- `[caddy] acme_directory` points it at another ACME CA (Let's Encrypt's
  staging, for instance); empty is Let's Encrypt.

Remote servers get the certificate with their routes; one older than
protocol 31 keeps a certificate per hostname until it is upgraded.

## Rules

- A hostname belongs to at most one service. Adding a taken one fails with
  `hostname_taken` (exit code 5).
- Adding and removing domains needs the `deploy` scope.
- Adding the first domain to a running service whose port isn't published
  yet starts a `config` release that publishes it (returned as `release`).
- `dock domain remove shop.example.com` stops routing it immediately.
- The service receives `DOCK_PUBLIC_DOMAIN` on its next release.
