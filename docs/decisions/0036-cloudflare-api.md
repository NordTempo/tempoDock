# 0036: Cloudflare tunnels, and domains through them, from Dock

Status: accepted (planned with Saile, 2026-10-30; narrowed the same day
to tunnels only). Amends [0013](0013-homelab-ingress-cloudflare-tunnel.md),
[0009](0009-dynamic-domains-and-tls.md) and [0014](0014-node-protocol.md).
Superseded in part by [0062](0062-org-alerts-and-operators.md) (connections per org, zones on several connections, failure alerts), [0068](0068-redirects-on-cloudflare-zones.md) (redirect placeholders are IP records). Amended by [0090](0090-wildcard-certificates.md) (DNS-01 TXT records).

## Context

A server behind a Cloudflare Tunnel ([0013](0013-homelab-ingress-cloudflare-tunnel.md))
took manual steps:

1. Create the tunnel in the dashboard.
2. Paste its token into install.sh.
3. Route its traffic to `http://127.0.0.1:80`.
4. Add `*.<zone>`.
5. CNAME every custom domain to the tunnel.

0013 named automating this "a later option". The homelab joins the
Hetzner control plane as a builder and non-production node behind a
tunnel, and Dock will be open-sourced, so the integration must stay
optional and clean.

Saile's direction: tunnels only. Dock never points DNS at an IP, uses no
wildcard, and leaves hostnames typed by hand alone. A domain is picked
"from Cloudflare", from the zones connected to the platform, and Dock
sets up everything the tunnel needs for it, reusing the server's tunnel
when there is one.

| Force | Means here |
|---|---|
| Optional | Without a Cloudflare connection nothing changes; the token-based install keeps working |
| Safe on production | The control plane is production: it must never overwrite or delete a record it didn't make |
| Idle means idle | API calls only on changes; no sync loop ([performance](../performance.md)) |
| Failure-proof | Cloudflare being down delays, never breaks; failures are retried |
| One shape | Storage connections ([0034](0034-registry-object-storage.md)) are named, sealed and operator-managed; this matches |

## Decision

| Topic | Decision |
|---|---|
| Connection | `cloudflare_connections`: a named API token (sealed), its account, and its status (`ok`, or `invalid` once Cloudflare refused it). Operators manage them: `dock cloudflare [add\|set\|refresh\|remove\|zones\|records\|retry]`, `/cloudflare` endpoints, the console's Cloudflare page. Same shape as storage connections, own table: their columns are S3's |
| Checked on save | Cloudflare doesn't tell a token its own permissions. Saving verifies the token, finds the account (`--account-id` when several, or for account-owned tokens), lists tunnels and zones, and reads one page of each zone's records, a few zones at a time. Zones it can't read are left out. Edit permissions show on first use, as an error naming the missing one |
| Token scopes | Account › Cloudflare Tunnel › Edit; Zone › DNS › Edit and Zone › Zone › Read on the zones domains may come from; Zone › Single Redirect › Edit for redirects ([0068](0068-redirects-on-cloudflare-zones.md)) |
| Tunnels | One remotely managed tunnel per tunnel server. Dock creates `dock-<install id>-<server>`, routed by a single catch-all to the server's Caddy (`http://127.0.0.1:<http_port>`), or adopts the one the server runs (made in the dashboard; it must be remotely managed, and its routes stay). It happens when a domain is added from Cloudflare and the server has no tunnel Dock runs, with `dock server add --tunnel` once the server joins (not before, so an unused join token leaves nothing behind), or with `dock server tunnel create\|adopt`. Dock deletes a tunnel it created when the server is removed, never one it adopted |
| Running it | Protocol 8's `ensure-tunnel` sends the token to the node, which runs `dock-cloudflared`: the pinned `cloudflare/cloudflared` image, host network (Caddy listens on 127.0.0.1 only and trusts loopback), read-only, no capabilities, 256 MB, metrics on `127.0.0.1:0` (the image's default is `0.0.0.0`), token in its environment, never argv. The token is sealed in SQLite, so a reconnect sends it again without asking Cloudflare; the node compares a hash label and does nothing when it matches. An adopted tunnel's old `cloudflared` is a second connector until install.sh removes it, so traffic doesn't drop |
| Domains from Cloudflare | `dock domain add HOST --cloudflare` (the UI's "from Cloudflare"), for anyone who can add domains: HOST must be in a connected zone and the service's server in tunnel mode (a direct server is refused: no IPs). Dock checks the name at once (a record it didn't make there is refused), gets the server's tunnel ready, then creates a proxied CNAME to it, and on an adopted tunnel whose catch-all doesn't go to Caddy a hostname route ahead of the others. A generated domain under a base domain in a connected zone, on a tunnel server, gets the same. Both go with the domain, the archived service or the server. Typed hostnames are as before: Dock shows the CNAME to create |
| Ownership | Records Dock makes carry the comment `dock:<install id>` (comments fit the Free plan; tags don't). Dock only edits or deletes those, and only the hostname routes it added. A proxied CNAME someone else made that already points at the tunnel is used as it is and never touched |
| Queue | `cloudflare_tunnels` and `cloudflare_records` hold what Dock wants (`want` present or absent) and how far it is. One worker works through pending rows, one call at a time, then sleeps until kicked or until the earliest retry; nothing is triggered by the lossy event bus alone. Rows carry a generation every other writer bumps, and the worker saves a result only if it is unchanged, so a removal while it works isn't lost |
| Failures | Cloudflare unreachable, too slow or 5xx: the row retries with backoff, 5 s doubling to 5 min. 429: the worker stops calling Cloudflare until `Retry-After` (5 min, Cloudflare's block, without one). A refused token: Dock verifies it; if Cloudflare refuses the token itself, the connection turns `invalid` and its rows wait, without retries, for `dock cloudflare set`; otherwise the row fails with the permission it needs, until `dock cloudflare retry` |
| Not now | A records to IPs and wildcards (by choice), switching a running server's ingress (re-run install.sh; direct to tunnel since the 2026-11-26 amendment), Access policies, load balancing, per-org connections or zones (an `org_id` like storage's when orgs need their own; today every org's members see the zones), an alert when a token turns invalid (the console shows it) |

## Why this shape

**Tunnels only.** A CNAME to a tunnel never exposes a server's address,
and every domain goes the same way. Not making A records keeps Dock out
of production's direct DNS entirely.

**Picked, not inferred.** Only a hostname added from Cloudflare (or one
Dock generates under a connected zone) is Dock's to set up. Typing a
hostname in a connected zone changes nothing in Cloudflare, so connecting
a token never touches records someone manages by hand.

**A queue in the database.** The add answers once the name is checked and
the tunnel is in hand; the CNAME and route follow. A crash, a restart or
Cloudflare being down leaves rows pending, and they resume. Idle, the
worker has no timer.

**A container, not the systemd unit.** The node daemon runs as `dock`
with Docker access but can't manage systemd units, and the token must
arrive over the link. A container is what Dock already runs for BuildKit,
survives reboots (`unless-stopped`), and is replaced (after pulling) when
the token or image changes.

**Routes on adopted tunnels, not a takeover.** Adding a hostname route
for Dock's own domains ahead of a tunnel's existing rules keeps what the
tunnel already serves working; replacing its configuration with a
catch-all would not.

## Consequences

- A new tunnel server is `dock server add NAME --tunnel` plus the printed
  install command; a domain on it is `dock domain add HOST --cloudflare`,
  with no DNS work.
- The Cloudflare token is in the control plane's database and its
  backups (sealed, like every secret). A restored control plane keeps its
  install ID and so manages the same records; a moved one's old copy stops
  (`MOVED`, [0021](0021-control-plane-backup-and-move.md)).
- Nodes report their tunnel hostname and public IPs, and these end up in
  DNS instructions and records, so hello refuses values that aren't a
  `<uuid>.cfargotunnel.com` or an address DNS can point at.
- A Cloudflare outage delays new tunnels and domains, never deploys or
  existing traffic; adding a domain from Cloudflare needs Cloudflare up
  for its name check.
- One migration (0030), protocol 8, `[ingress] cloudflared_image`, and an
  `INGRESS=cloudflare_tunnel` option in install.sh.

## Amendment (2026-10-30): tunnels next to a public IP

A direct server can run a tunnel too, so one server serves both ways:
typed domains on its public IP, as before, and domains added from
Cloudflare through the tunnel. Saile asked for both on the Hetzner
server.

- **Listener.** While it has domains through the tunnel, a direct
  server's Caddy also listens on `127.0.0.1:<[caddy] tunnel_port>` (7075
  by default, checked against Dock's other ports; 0 turns it off): plain
  HTTP, every route, forwarding headers trusted from loopback, redirects
  to `https://` on 443 as visitors at Cloudflare's edge use. The tunnel's
  catch-all goes there; changing `tunnel_port` later needs the tunnel's
  route changed too (or the tunnel removed with its server and made
  again). In direct mode, sending the tunnel to the
  public `:80` would loop on its redirect to HTTPS, and `:443` can't get a
  certificate for a name that resolves to Cloudflare.
- **Tunnel-only routes.** Domains Dock routes through the tunnel are
  served on that listener only: the public listeners don't route them
  (no bypassing Cloudflare through the IP), and on-demand TLS refuses
  them. Their domain check finds the server's marker through Cloudflare,
  as for any proxied record.
- **Protocol 9.** Hello reports the port the node's cloudflared forwards
  to (`tunnel_port`), and routes carry `tunnel_only`. A direct node that
  reports no port (older, or turned off) can't take a tunnel: adding a
  domain from Cloudflare there says to upgrade it. Tunnel-only routes are
  never sent to a node before protocol 9, which would serve them on its
  IP.
- "From Cloudflare" and `dock server tunnel create` work on any server
  that can take a tunnel; generated domains on a direct server stay on
  its IP.


## Amendment (2026-11-17): TXT records for DNS-01

For a direct server's base domain in a platform zone, Dock writes the
ACME DNS-01 challenge records (`TXT _acme-challenge.<base>`, with its
`dock:<install id>` comment) while it obtains the base's wildcard
certificate, and deletes them once the CA has checked them
([0090](0090-wildcard-certificates.md)). They point nothing anywhere;
records others made at that name are left alone.


## Amendment (2026-11-26): switching a direct server to its tunnel

A homelab joined direct (no `--tunnel`) behind a router: its "public IP"
was the router's, and a tunnel added later only carried domains from
Cloudflare. It shared the control plane's base domain, whose hand-made
wildcard points at the control plane, so every generated hostname of an
environment drained onto it went there and answered 404 unless the
control plane forwarded it. Saile wants both servers on tunnels, sharing
one base domain, without reinstalling.

- **Setting.** `servers.through_tunnel` (migration 0131), set with `dock
  server update --tunnel|--no-tunnel` or the server's Tunnel section. The
  control plane owns it; the node keeps its direct install.
- **Counts as a tunnel server** while Dock runs its tunnel, ready
  (`ingressOf`): no public IPs, DNS instructions name the tunnel, and its
  generated hostnames under a platform zone get their own proxied CNAME
  (`GenerateDomain`). The ones it already had get theirs when it switches,
  and an environment moved onto it gets them for the names it keeps
  (`routeGeneratedDomains`). Servers then share a base domain with no
  wildcard record. On the control plane, status pages' addresses under
  its base domain (a page's own, its old names', a custom hostname
  there) get theirs the same way (`syncStatusRecords`), when it switches,
  on start and whenever the pages' addresses change.
- **Its Caddy is still the direct one.** Hostnames with records are served
  on its tunnel listener only, as for domains from Cloudflare; others
  (typed domains whose DNS Dock doesn't manage, Dock's own address) stay
  on its public IP too, so switching breaks nothing that worked. A
  hostname under its base domain can reach the IP through the base's
  wildcard record, so it stays there too until its own record is ready
  (or for as long as that record fails); the worker applies the server's
  routes once per pass when one becomes ready (`routeReadyRecords`). This
  holds for any direct server's names under its base. Its base domain's
  wildcard certificate is kept for them (`installedIngress`).
- **Refused** without a tunnel Dock runs and has ready, or with a base
  domain outside the platform's zones or in another Cloudflare account
  than the tunnel: its generated hostnames could get no records. Going
  back is refused on a server installed in tunnel mode, which has no IP.
- **Back** (`--no-tunnel`): records stay, their hostnames on the tunnel,
  which still serves them; new generated ones rely on the wildcard again.
  Removing the records instead would break those names once the wildcard
  record is gone.
- **Retried by asking again.** `--tunnel` (or a base domain change while
  it is on) gives records to whatever generated hostnames still lack one,
  changed or not, so a request that failed halfway is finished by its
  retry.
