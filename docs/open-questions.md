# Open questions

Each question has a current leaning. When answered, record it as a decision in
[decisions](decisions/README.md) and remove it here.

## Homelab ingress

How does public traffic reach staging and dev domains on the homelab?

- **Port forward 80/443** to the homelab's Caddy. Simple, exposes the home IP,
  needs dynamic DNS if the IP changes.
- **Cloudflare Tunnel.** No open ports, hides the home IP, adds a Cloudflare
  dependency and complicates automatic TLS.
- **Through production's Caddy** over Tailscale. One public entry point, but
  staging traffic and availability then depend on production.
- **Private only** (Tailscale). Safest; staging isn't publicly reachable.

Depends on how the homelab is exposed today.

## Base domain for generated subdomains

Which domain do we use for generated hostnames (like Railway's
`*.up.railway.app`)? Needs a wildcard DNS record pointing at the server. See
[ADR 0009](decisions/0009-dynamic-domains-and-tls.md).

## Name

`platform` is a placeholder. Needed before the binary name, CLI commands and
config file name (`platform.toml`) get baked into docs and agent prompts.
Renaming is cheap until M1 ships.

## Homelab VM for M0-M2

M0 to M2 run on the homelab. A small VM (2 vCPU, 2-4 GB RAM) with Docker makes
footprint problems show up early. Which host, and is Tailscale already there?

## UI access

Leaning Tailscale-only for the API and UI, plus a login. Public access with
proper auth is needed before open source or hosting, so the auth code should
not assume Tailscale.

## License

Not needed while the repo is private. To keep options open, only take
dependencies with permissive licenses (MIT, Apache-2.0, BSD). See
[ADR 0008](decisions/0008-keep-doors-open.md).
