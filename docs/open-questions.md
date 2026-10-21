# Open questions

Each question has a current leaning. When answered, record it as a decision in
[decisions](decisions/README.md) and remove it here.

## Base domain for generated subdomains

Which zones do we buy for generated hostnames (like Railway's
`*.up.railway.app`)? Each server gets its own base domain; the homelab's is a
Cloudflare zone with a wildcard to the tunnel
([ADR 0013](decisions/0013-homelab-ingress-cloudflare-tunnel.md)). Until then
a test server uses `<ip>.sslip.io`.

## Homelab VM for M0-M2

Leaning: a fresh, disposable Hetzner VPS (2 vCPU, 4 GB) for M1 and M2 instead
of the homelab. It has a public IP, so real domains and certificates can be
tested before the tunnel setup ([ADR 0013](decisions/0013-homelab-ingress-cloudflare-tunnel.md))
exists, and it can become the production server in M3.

## UI access

Leaning Tailscale-only for the API and UI, plus a login. Public access with
proper auth is needed before open source or hosting, so the auth code should
not assume Tailscale.

## License

Not needed while the repo is private. To keep options open, only take
dependencies with permissive licenses (MIT, Apache-2.0, BSD). See
[ADR 0008](decisions/0008-keep-doors-open.md).
