# 0013: Homelab ingress through Cloudflare Tunnel

Status: accepted; superseded in part by [0036](0036-cloudflare-api.md) (Dock makes the tunnel and runs cloudflared in a container)

## Context

Staging, dev and preview environments live on the homelab
([0010](0010-environments-on-servers.md)). They need public HTTPS, but the
home IP must never be exposed: no port forwarding, no DNS record pointing at
home.

## Decision

**Servers have an ingress mode.**

| Mode | Used by | Public traffic | TLS |
|---|---|---|---|
| `direct` | Hetzner (production) | Public IP, ports 80/443 to Caddy | Caddy, on-demand ([0009](0009-dynamic-domains-and-tls.md)) |
| `cloudflare_tunnel` | Homelab | `cloudflared` dials out to Cloudflare, forwards to Caddy on `localhost:80` | Cloudflare's edge |

- On a tunnel server, `cloudflared` runs as its own systemd service with one
  catch-all ingress rule to the local Caddy. Caddy routes by hostname exactly
  as on a direct server, in plain-HTTP mode. Nothing else changes.
- Everything on the homelab connects outward: the tunnel, the node connection
  to the control plane, registry pushes. Nothing dials into the home network.

**Staging uses hostnames on a domain we own.** Each server has its own base
domain for generated hostnames. The homelab's is a dedicated zone on
Cloudflare with one wildcard record, `*.<zone>` CNAME to the tunnel, so new
services get working HTTPS hostnames with no DNS work. The wildcard is first
level only (`web-shop-staging.<zone>`, not `web.staging.<zone>`), because
Cloudflare's free certificates cover one level.

App hostnames use a separate zone from the product's own domain, to keep
app cookies and reputation apart.

**Custom domains on tunnel servers** work when the domain's DNS is on our
Cloudflare account: the platform shows the CNAME to the tunnel to create.
Automating that record through the Cloudflare API is a later option.

**Domain status on tunnel servers** can't compare IPs, so the check makes an
HTTPS request to the hostname and looks for a marker header that only this
server's Caddy adds.

## Consequences

- The home IP stays private, and the homelab needs no open ports at all.
- Staging depends on Cloudflare being up. Production doesn't: it stays
  `direct` on Hetzner.
- Apps on tunnel servers see the client IP in `CF-Connecting-IP`; Caddy
  trusts forwarding headers from the local `cloudflared` only.
- Replacing the homelab with a rented server later is just another node
  joining, in either mode.
- Lands in M3 with the node runtime. M1 and M2 run on a direct server.

## Amendment (2026-10-30): tunnels and records from Dock

[0036](0036-cloudflare-api.md) automates this: with a Cloudflare
connection (an API token), Dock creates or adopts a tunnel server's
tunnel, runs `cloudflared` in its own `dock-cloudflared` container
instead of the systemd service, and routes each domain added from
Cloudflare through it with a proxied CNAME (and a hostname route on an
adopted tunnel that needs one). Dock makes no wildcard record; the one
made by hand keeps serving generated hostnames where it exists.
Installing with `CLOUDFLARE_TUNNEL_TOKEN` and the systemd service stays
the way without a connection.
