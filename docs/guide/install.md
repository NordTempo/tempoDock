# Install

One server runs everything: the `dock` daemon (API, UI, deploys), Caddy
(public traffic and TLS), and BuildKit (builds). Apps run as Docker
containers and keep running when the daemon is stopped or upgraded.

## Requirements

| What | Needed |
|---|---|
| OS | Linux with systemd, `amd64` or `arm64` |
| Docker | Docker Engine 28 or newer, installed and running (`systemctl enable --now docker`), with its default iptables firewall backend. Older versions expose ports published on 127.0.0.1 to the LAN; the script warns |
| Tools | `curl` (downloads Caddy, detects the public IP), `iptables` (the app firewall) |
| Access | root (`sudo`) |
| Size | 2 vCPU / 4 GB is comfortable. Idle platform is about 490 MB including Docker, Caddy and BuildKit ([measured](../experience.md#measured-m0)). Builds are capped at 2 GB by default |
| Ports | 80/tcp, 443/tcp and 443/udp public. The API port (7070) private |
| cgroups | cgroup v2 (the default on current distributions) for per-service statistics. Docker's systemd and cgroupfs drivers both work |
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
(shown once). `install.sh` also creates an admin user `admin@localhost` for
the UI and prints its generated password to stderr, once. Log in from your
laptop, then sign in as that user in the browser it opens and approve:

```sh
dock login http://100.64.0.10:7070
```

Keep the admin token for scripts and for creating agent tokens
(`dock login <url> --token-stdin` works too).

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
| Caddy binary | `/usr/local/bin/caddy`, checked against the release's published SHA-512 checksums (skipped if the right version is already there) |
| System users | `dock` runs the daemon, `dock-caddy` runs Caddy. Neither has a login shell or is in the `docker` group itself |
| Daemon config | `/etc/dock/config.toml` (root:dock, 0640). Written once, never overwritten |
| Caddy bootstrap config | `/etc/dock/caddy.json` (regenerated on every run) |
| Daemon unit | `dock.service`: `dock serve --config /etc/dock/config.toml` as `dock`, with the `docker` and `dock-caddy` groups for this process only. `MemoryHigh=96M`, `MemoryMax=256M`, `GOMEMLIMIT=64MiB` |
| Proxy unit | `dock-caddy.service`: `caddy run --resume` as `dock-caddy`, only binds 80/443. No Docker access |
| Firewall unit | `dock-firewall.service`: runs `dock admin firewall apply` as root whenever Docker starts. See [App firewall](#app-firewall) |
| Daemon data | `/var/lib/dock` (0700): `dock.db`, `secret.key`, `builds/`, `uploads/` |
| Caddy data | `/var/lib/dock-caddy`: certificates and the last loaded config |
| Caddy admin socket | `/run/dock-caddy/admin.sock` (dock-caddy:dock-caddy, 0660: only the daemon can change routing) |
| Kernel setting | `/etc/sysctl.d/90-dock.conf`: `net.ipv4.tcp_migrate_req = 1`, so Caddy reloads hand queued connections over instead of resetting them |
| BuildKit image | `moby/buildkit:v0.34.1`, pinned by digest, pulled now; the `dock-buildkit` container is created before the first build |

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
| `[buildkit] image` | `moby/buildkit:v0.34.1@sha256:9936…` | BuildKit image, pinned by digest |
| `[buildkit] memory_mb` | `2048` | Hard memory cap for builds (no swap). Builds over it fail; apps are unaffected |
| `[buildkit] cpus` | `1` | CPU cap for builds. Builds also get a quarter of the normal CPU weight |
| `[network] pool` | `10.213.0.0/16` | IPv4 range app networks get their addresses from, a /24 per environment. Keep it clear of your LAN and VPNs |
| `[network] allow_private` | `[]` | Private CIDRs apps and builds may reach, e.g. `["192.168.10.20/32"]` for a NAS. See [App firewall](#app-firewall) |
| `[defaults] memory_mb` | `512` | Memory limit for services that don't set one |
| `[defaults] cpus` | `1` | CPU limit for services that don't set one |
| `[defaults] health_check_timeout_seconds` | `60` | Health check timeout for services that don't set one |
| `[defaults] keep_releases` | `5` | Release images kept for rollback. Not enforced yet: nothing prunes old images |

Changing `[buildkit]` settings recreates the `dock-buildkit` container
before the next build. So does finding its memory cap loosened: before each
build the daemon checks the container's cgroup, because with Docker's
systemd cgroup driver a `systemctl daemon-reload` resets its swap limit to
unlimited.

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

### App firewall

Apps and builds reach the public internet, but not your private networks:
`dock-firewall.service` drops their traffic to

- private IPv4 (`10/8`, `172.16/12`, `192.168/16`): the LAN,
- `100.64.0.0/10`: the Tailscale tailnet,
- link-local (`169.254/16`, `fe80::/10`): cloud metadata endpoints,
- IPv6 unique local (`fc00::/7`), which includes Tailscale's `fd7a:115c:a1e0::/48`,
- the server itself on any address, so the API, SSH and anything else
  listening on it.

Exceptions: containers in one environment reach each other (different
environments are isolated by Docker), they reach Caddy's ports 80/443 on the
server so apps can call each other by domain, and port 53 on the server's DNS
servers (Docker forwards container DNS to them). If the server's resolver is
Tailscale's MagicDNS (`100.100.100.100`), apps can resolve tailnet names but
still can't connect to them.

To let apps reach a LAN host, such as a NAS or a database, add it to the
allowlist and re-apply:

```toml
# /etc/dock/config.toml
[network]
allow_private = ["192.168.10.20/32"]
```

```sh
sudo systemctl restart dock-firewall
sudo dock admin firewall apply --dry-run   # show the rules without applying them
```

How it works: iptables rules in the `mangle` table, in chains named
`DOCK-*` (`sudo iptables -t mangle -S`). They run before Docker's,
Tailscale's and ufw's filter rules, so those can't let traffic around them.
The service re-applies them whenever Docker starts. If the server's DNS
servers change, restart it, or apps lose DNS. Containers that Docker
restarts at boot run unfiltered for the moment before the service starts.

The rules cover dock's networks and Docker's default bridge (`docker0`),
where BuildKit runs, so they also apply to any container you start yourself
with plain `docker run`. Docker's `nftables` firewall backend isn't
supported; the service fails with an error instead. To switch the firewall
off: `sudo systemctl disable --now dock-firewall && sudo dock admin firewall remove`.

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

## Servers and placement

The server `install.sh` sets up is the first server. It accepts both kinds
of environment, production and non-production, so a single-server install
needs no setup. More servers join as nodes (M3); then an admin decides which
kinds go where:

```sh
dock server list                                       # status, version, accepted kinds, memory reserved
dock server update vps --no-non-production              # production only from now on
dock server update homelab --non-production --no-production
dock server update vps-2 --disable-placement            # nothing new lands here
dock server show homelab                                # what's placed there, current CPU, memory, disk
```

An environment's kind comes from its name (`production` is production,
everything else non-production) unless `dock environment create --kind`
says otherwise, and never changes. A project keeps all its environments of
one kind on one server: the first one picks the online server accepting the
kind with the most free memory (the memory limits of the services already
placed there against the server's total; ties go to the server with fewer
projects), and later ones follow it. Turning a kind off on a server only
stops new placements; environments already there stay.

To choose the server yourself, pin the project before its first
environment of that kind exists:

```sh
dock project pin shop --kind production --server vps-2
```

When no server can take an environment, creating it fails and names the
servers that accept the kind and why each is out (offline or disabled).
Moving a placed project to another server is a later feature.

Upgrading from a version with server roles keeps every environment where it
is. A production-role server accepts production, a non-production one
non-production; a lone server accepts both. Each project's placement per
kind is taken from its environments; a project whose environments of one
kind were already spread over servers keeps them there, and new ones follow
the most recently created.

### Statistics

The daemon samples CPU, memory, disk and load of every server, and CPU,
memory and IO of every running service, once a minute from `/proc` and the
containers' cgroup files (no Docker API calls). Points are kept in SQLite:
one per minute for 24 hours, one per 10 minutes for 7 days, pruned
automatically. While someone watches a server or service live in the UI (or
the `/stats/live` streams), it samples every 3 seconds, and stops when they
leave. Service memory is the working set, the same number `docker stats`
shows.

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
- Upgrading an install from before the app firewall: the script moves Caddy
  to the `dock-caddy` user (one Caddy restart, `--resume` restores routes),
  takes `dock` out of the `docker` group, and enables `dock-firewall`.
  Existing app networks keep their Docker-assigned addresses; the firewall
  covers them too. Your `config.toml` is not rewritten, so pin
  `[buildkit] image` by digest yourself if it still says only
  `moby/buildkit:v0.34.1`.

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
