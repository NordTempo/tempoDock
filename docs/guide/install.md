# Install

One server runs everything: the `dock` daemon (API, UI, deploys), Caddy
(public traffic and TLS), and BuildKit (builds). Apps run as Docker
containers and keep running when the daemon is stopped or upgraded.

## Requirements

| What | Needed |
|---|---|
| OS | Linux with systemd, `amd64` or `arm64` |
| Docker | Docker Engine installed and running (`systemctl enable --now docker`) |
| Tools | `curl` (downloads Caddy, detects the public IP) |
| Access | root (`sudo`) |
| Size | 2 vCPU / 4 GB is comfortable. Idle platform is about 490 MB including Docker, Caddy and BuildKit ([measured](../experience.md#measured-m0)). Builds are capped at 2 GB by default |
| Ports | 80/tcp, 443/tcp and 443/udp public. The API port (7070) private |
| Optional | Tailscale, so the API listens on the tailnet by default |

## Install

Build the binary (on any machine; it's static), copy it and the `deploy/`
directory to the server, then run the script as root.

```sh
make web && GOOS=linux GOARCH=amd64 make build     # bin/dock
scp -r bin/dock deploy server:/tmp/dock-install/
ssh server 'cd /tmp/dock-install && sudo DOCK_BINARY=./dock ./deploy/install.sh'
```

The last lines of output print the API address and the first admin token
(shown once). Log in from your laptop:

```sh
dock login http://100.64.0.10:7070 --token dock_...
```

`install.sh` also creates an admin user `admin@localhost` for the UI and
prints its generated password to stderr, once.

### install.sh options

Environment variables. Only `DOCK_BINARY` and `CADDY_VERSION` matter on a
re-run; the rest are only used when `/etc/dock/config.toml` doesn't exist yet.

| Variable | Default | Meaning |
|---|---|---|
| `DOCK_BINARY` | (required) | Path to the built `dock` binary |
| `CADDY_VERSION` | `2.11.7` | Caddy release to download from GitHub |
| `PUBLIC_IP` | auto (`api.ipify.org`) | Address users point DNS at → `public_ip` |
| `LISTEN` | Tailscale IPv4`:7070`, else `127.0.0.1:7070` | API and UI address → `listen` |
| `BASE_DOMAIN` | empty | Base for generated hostnames → `base_domain`. Needs a wildcard DNS record |
| `DISABLE_TLS` | `false` | `true` serves plain HTTP only → `[caddy] disable_tls`. For LAN/Tailscale-only setups or behind a Cloudflare Tunnel |

### What it installs

| Thing | Where |
|---|---|
| `dock` binary | `/usr/local/bin/dock` |
| Caddy binary | `/usr/local/bin/caddy` (skipped if the right version is already there) |
| System user | `dock` (no login shell, home `/var/lib/dock`), added to the `docker` group |
| Daemon config | `/etc/dock/config.toml` (root:dock, 0640). Written once, never overwritten |
| Caddy bootstrap config | `/etc/dock/caddy.json` (regenerated on every run) |
| Daemon unit | `dock.service`: `dock serve --config /etc/dock/config.toml`, `MemoryHigh=96M`, `MemoryMax=256M` |
| Proxy unit | `dock-caddy.service`: `caddy run --resume`, only binds 80/443 |
| Daemon data | `/var/lib/dock` (0700): `dock.db`, `secret.key`, `builds/`, `uploads/` |
| Caddy data | `/var/lib/dock-caddy`: certificates and the last loaded config |
| Caddy admin socket | `/run/dock-caddy/admin.sock` (only the daemon can change routing) |
| Kernel setting | `/etc/sysctl.d/90-dock.conf`: `net.ipv4.tcp_migrate_req = 1`, so Caddy reloads hand queued connections over instead of resetting them |
| BuildKit image | `moby/buildkit:v0.34.1`, pulled now; the `dock-buildkit` container is created before the first build |

Re-running the script is safe: config and data are kept, binaries and units
are replaced, and no second admin is created.

## config.toml

`/etc/dock/config.toml`. Every field has a default; a missing file means all
defaults. Restart after editing: `sudo systemctl restart dock` (apps keep
running).

| Field | Default | Meaning |
|---|---|---|
| `data_dir` | `/var/lib/dock` | SQLite DB, secrets key, build logs, uploads |
| `listen` | `127.0.0.1:7070` | API and UI. Tailscale IP or loopback; not meant to be public |
| `internal_listen` | `127.0.0.1:7071` | Loopback-only endpoints (Caddy's TLS `ask`). Must be loopback or the daemon refuses to start |
| `public_ip` | empty | Address shown in DNS instructions and used to check domains |
| `base_domain` | empty | Enables generated hostnames under it. Needs `*.<base>` pointing at `public_ip` |
| `max_upload_mb` | `512` | Largest source upload for `dock deploy` |
| `[host_ports] min`, `max` | `20000`, `29999` | Loopback ports published app containers bind to, one per release |
| `[caddy] admin_socket` | `/run/dock-caddy/admin.sock` | Caddy admin API socket |
| `[caddy] http_port` | `80` | |
| `[caddy] https_port` | `443` | |
| `[caddy] acme_email` | empty | Contact email for the certificate authority |
| `[caddy] disable_tls` | `false` | Serve every route over plain HTTP |
| `[buildkit] enabled` | `true` | This server builds images. `false`: source deploys fail; deploy images instead |
| `[buildkit] image` | `moby/buildkit:v0.34.1` | BuildKit image |
| `[buildkit] memory_mb` | `2048` | Hard memory cap for builds (no swap). Builds over it fail; apps are unaffected |
| `[buildkit] cpus` | `1` | CPU cap for builds. Builds also get a quarter of the normal CPU weight |
| `[defaults] memory_mb` | `512` | Memory limit for services that don't set one |
| `[defaults] cpus` | `1` | CPU limit for services that don't set one |
| `[defaults] health_check_timeout_seconds` | `60` | Health check timeout for services that don't set one |
| `[defaults] keep_releases` | `5` | Release images kept for rollback. Not enforced yet: nothing prunes old images |

Changing `[buildkit]` settings recreates the `dock-buildkit` container
before the next build.

## Network access

Public firewall: 22, 80/tcp, 443/tcp, 443/udp (HTTP/3). Never expose the API
port to the internet: tokens travel over plain HTTP to it.

```sh
ufw allow 22/tcp && ufw allow 80/tcp && ufw allow 443 && ufw enable
```

How you reach the API and how the public reaches apps are separate choices:

| Setup | `LISTEN` | `DISABLE_TLS` | Apps reachable via |
|---|---|---|---|
| **Tailscale** (default) | Tailscale IP:7070 (auto) | `false` | Public IP, Caddy issues certificates |
| **LAN only** | LAN IP:7070 | `true` | LAN, plain HTTP. Point local DNS at the server |
| **Cloudflare Tunnel** | Tailscale or loopback | `true` | `cloudflared` → Caddy on `localhost:80`; Cloudflare terminates TLS |

On LAN setups anyone on the network can sniff tokens sent to the API. Prefer
Tailscale.

### Cloudflare Tunnel

For servers without a public IP or open ports, like a homelab
([ADR 0013](../decisions/0013-homelab-ingress-cloudflare-tunnel.md)). Built-in
support (a per-server ingress mode) lands in M3; today you run `cloudflared`
yourself:

1. Install with `DISABLE_TLS=true`.
2. Create a tunnel with one catch-all ingress rule to `http://localhost:80`
   and run `cloudflared` as a systemd service.
3. In Cloudflare DNS, CNAME your hostnames (or `*.<base_domain>`, first level
   only) to the tunnel.

Routing works, but domain status stays `waiting_for_dns`: the check compares
DNS answers with `public_ip`, and a tunnelled hostname resolves to
Cloudflare. The marker-header check from ADR 0013 replaces this in M3.

## Upgrading

Swap the binary and restart the daemon. Apps and routing are unaffected; only
the API is down for a second.

```sh
sudo DOCK_BINARY=./dock ./deploy/install.sh     # also refreshes units and caddy.json
# or by hand:
sudo install -m 0755 ./dock /usr/local/bin/dock && sudo systemctl restart dock
```

- Database migrations run on start and only go forward. Back up first.
- A deploy running during the restart is marked `failed`; the previous
  release keeps serving. Deploy again afterwards.
- To upgrade Caddy, re-run with `CADDY_VERSION=x.y.z`, then
  `sudo systemctl restart dock-caddy` (the script doesn't restart a running
  Caddy). Traffic blips briefly; `--resume` restores the routes.
- To upgrade BuildKit, change `[buildkit] image` in `config.toml`.

## Backups

Back up `/var/lib/dock`. **You need both files**:

| File | Holds | Without it |
|---|---|---|
| `dock.db` | Projects, services, releases, domains, tokens, audit log, encrypted variable values | Nothing to restore |
| `secret.key` | The AES-256 key for variable values and release configs | Every secret and release config in `dock.db` is unreadable. No recovery |

Store them separately if you can: the key is kept outside the DB so a leaked
DB file alone reveals no secrets.

The DB is SQLite in WAL mode, so copy it while the daemon is stopped
(stopping it is always safe for apps):

```sh
sudo systemctl stop dock
sudo tar -czf /root/dock-$(date +%F).tgz -C /var/lib dock dock-caddy
sudo systemctl start dock
```

Or online, if `sqlite3` is installed:
`sqlite3 /var/lib/dock/dock.db ".backup /root/dock.db"` plus a copy of
`secret.key`.

Also worth keeping:

- `/var/lib/dock-caddy`: certificates. Without it they are re-issued on
  demand, which is slow and counts against CA rate limits.
- `/etc/dock/config.toml`.
- App volumes. These are Docker volumes, not in `/var/lib/dock`; find their
  names with `dock volume list --json` (`docker_name`). Back up databases with
  their own tools (`pg_dump`). Scheduled off-box backups land in M5.

Restore: install as usual, stop `dock`, put `dock.db` and `secret.key` back
in `/var/lib/dock` (owner `dock`, mode 0600), start `dock`.
