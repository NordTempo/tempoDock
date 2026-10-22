# Install

The first server runs everything: the `dock` daemon (API, UI, deploys),
Caddy (public traffic and TLS), and BuildKit (builds). Apps run as Docker
containers and keep running when the daemon is stopped or upgraded. More
servers can [join as nodes](#add-a-server) later; the first one stays the
control plane.

## Requirements

| What | Needed |
|---|---|
| OS | Linux with systemd, `amd64` or `arm64` |
| Docker | Docker Engine 28 or newer, installed and running (`systemctl enable --now docker`), with its default iptables firewall backend. Older versions expose ports published on 127.0.0.1 to the LAN; the script warns |
| Tools | `curl` (downloads Caddy, detects the public IP), `iptables` (the app firewall) |
| Access | root (`sudo`) |
| Size | 2 vCPU / 4 GB is comfortable. Idle platform is about 490 MB including Docker, Caddy and BuildKit ([measured](../experience.md#measured-m0)). Builds are capped at 2 GB by default |
| Ports | 80/tcp, 443/tcp and 443/udp public. The API port (7070) and node port (7443) private. None on a [Cloudflare Tunnel](#cloudflare-tunnel) server |
| cgroups | cgroup v2 (the default on current distributions) for per-service statistics. Docker's systemd and cgroupfs drivers both work |
| Optional | Tailscale, so the API listens on the tailnet by default. Recommended for every server once there's more than one |

## Install

A server needs two files from a [release](https://github.com/NordTempo/tempoDock/releases):
the `dock-linux-<arch>` binary and `dock-install-<version>.tar.gz` (the
`deploy/` directory: `install.sh` and the systemd units). The repository is
private, so download with `gh` (logged in to an account that can read it),
check them against `SHA256SUMS`, copy them over and run the script as root:

```sh
v=v0.1.0
gh release download $v -R NordTempo/tempoDock -D dock-$v \
  -p dock-linux-amd64 -p "dock-install-$v.tar.gz" -p SHA256SUMS
(cd dock-$v && grep -E " (dock-linux-amd64|dock-install-$v.tar.gz)\$" SHA256SUMS | shasum -a 256 -c)
scp -r dock-$v server:/tmp/
ssh server "cd /tmp/dock-$v && tar -xzf dock-install-$v.tar.gz && sudo DOCK_BINARY=./dock-linux-amd64 ./deploy/install.sh"
```

Without `DOCK_BINARY`, `install.sh` downloads the binary itself: the
version the tarball came from (or `DOCK_VERSION`), checked against the
release's `SHA256SUMS`. While the repository is private it needs a
`GITHUB_TOKEN` that can read it, e.g. a fine-grained token with read access
to its contents, or `gh auth token`:

```sh
tar -xzf dock-install-v0.1.0.tar.gz
export GITHUB_TOKEN=github_pat_...
sudo --preserve-env=GITHUB_TOKEN ./deploy/install.sh
```

From source instead: `make web && GOOS=linux GOARCH=amd64 make build`
builds `bin/dock`; copy it and `deploy/` over and pass
`DOCK_BINARY=./dock`. The examples below say `DOCK_BINARY=./dock`; use
whichever binary you have.

The last lines of output print the API address and the first admin token
(shown once). `install.sh` also creates an admin user `admin@localhost` for
the UI and prints its generated password to stderr, once. Log in from your
laptop, then sign in as that user in the browser it opens, type the code
from the terminal and approve:

```sh
dock login http://100.64.0.10:7070
```

Keep the admin token for scripts and for creating agent tokens
(`dock login <url> --token-stdin` works too).

### The CLI

Laptops and CI only need the client, `dock-cli-<os>-<arch>` (a quarter of
the size; see [CLI-only build](../cli.md#cli-only-build)). Install the one
matching the server's version: `install-cli.sh` from the release picks the
right build, checks it against `SHA256SUMS` and installs it as `dock` in
`~/.local/bin` (`/usr/local/bin` as root, or `DOCK_INSTALL_DIR`). It
downloads with `gh` when that is logged in, otherwise with curl and
`GITHUB_TOKEN`.

```sh
gh release download v0.1.0 -R NordTempo/tempoDock -p install-cli.sh -O - | DOCK_VERSION=v0.1.0 sh
```

Or by hand:

```sh
gh release download v0.1.0 -R NordTempo/tempoDock -p dock-cli-darwin-arm64 -p SHA256SUMS
grep ' dock-cli-darwin-arm64$' SHA256SUMS | shasum -a 256 -c
install -m 0755 dock-cli-darwin-arm64 ~/.local/bin/dock
```

`dock version` shows the CLI's version and the server's, and the command
that installs the matching CLI when they differ. `dock login` warns too.

### install.sh options

`--node` installs a [node](#add-a-server) instead of a control plane.
`--restore ARCHIVE` (with `--old-server-becomes-node`) installs a control
plane from a backup: see [Moving the control plane](#moving-the-control-plane).
The rest are environment variables. `PUBLIC_IP`, `LISTEN`, `BASE_DOMAIN` and
`DISABLE_TLS` are only used when `/etc/dock/config.toml` doesn't exist yet;
the others also change an existing config, touching only their own keys.

| Variable | Default | Meaning |
|---|---|---|
| `DOCK_BINARY` | download | Path to a `dock` binary: a release's `dock-linux-<arch>`, or `bin/dock` from `make build` |
| `DOCK_VERSION` | the tarball's version, else the latest release | Without `DOCK_BINARY`: the release to download `dock-linux-<arch>` from, checked against its `SHA256SUMS` |
| `GITHUB_TOKEN` | empty | For that download: a token that can read the repository's releases (pass it with `sudo --preserve-env=GITHUB_TOKEN`) |
| `CADDY_VERSION` | `2.11.7` | Caddy release to download from GitHub |
| `PUBLIC_IP` | auto (`api.ipify.org`), empty on tunnel servers | Address users point DNS at → `public_ip` |
| `LISTEN` | Tailscale IPv4`:7070`, else `127.0.0.1:7070` | API and UI address → `listen`. Control plane only |
| `BASE_DOMAIN` | empty | Base for generated hostnames → `base_domain`. Needs a wildcard DNS record |
| `DISABLE_TLS` | `false` | `true` serves plain HTTP only → `[caddy] disable_tls`. For LAN/Tailscale-only setups. Ignored on tunnel servers |
| `CLOUDFLARE_TUNNEL_TOKEN` | empty | Serve public traffic through a [Cloudflare Tunnel](#cloudflare-tunnel): installs `cloudflared` and `dock-cloudflared.service`, sets `[ingress]` |
| `CLOUDFLARED_VERSION`, `CLOUDFLARED_SHA256` | `2026.10.3`, pinned | Another `cloudflared` release and the SHA-256 of `cloudflared-linux-<arch>` from its release notes |
| `NODE_LISTEN` | empty | Accept nodes on this address, e.g. `100.64.0.10:7443` → `[node] listen`. Control plane only |
| `REGISTRY` | empty | `1` runs the [image registry](#registry) → `[registry] enabled`. Control plane only |
| `REGISTRY_NAME` | `dock-registry` with `REGISTRY=1` | The registry's name in its tokens → `[registry] public_name` |
| `REGISTRY_LISTEN` | `127.0.0.1:5000` | Host address the registry container is published on → `[registry] listen` |
| `NODE_JOIN_TOKEN` | empty | From `dock server add`; implies `--node`. Needed on the first run, and to re-join |

### What it installs

| Thing | Where |
|---|---|
| `dock` binary | `/usr/local/bin/dock` |
| Caddy binary | `/usr/local/bin/caddy`, checked against the release's published SHA-512 checksums (skipped if the right version is already there) |
| System users | `dock` runs the daemon, `dock-caddy` runs Caddy. Neither has a login shell or is in the `docker` group itself |
| Daemon config | `/etc/dock/config.toml` (root:dock, 0640). Written once; later runs only change the keys their options set, and only after the new binary accepts the result |
| Caddy bootstrap config | `/etc/dock/caddy.json` (regenerated on every run) |
| Daemon unit | `dock.service`: `dock serve --config /etc/dock/config.toml` as `dock`, with the `docker` and `dock-caddy` groups for this process only. `MemoryMax=768M` (sized for builds), `GOMEMLIMIT=64MiB` |
| Node unit (nodes only, instead of `dock.service`) | `dock-node.service`: `dock node --config /etc/dock/config.toml`, same user, groups and limits. No API, UI or SQLite |
| Tunnel unit (tunnel servers only) | `dock-cloudflared.service`: `cloudflared tunnel run` as a throwaway user with no capabilities; the token is in `/etc/dock/cloudflared.env` (root, 0600). `cloudflared` binary in `/usr/local/bin`, checked against a pinned SHA-256 |
| Registry (control plane, `REGISTRY=1`) | `dock-registry` container, `registry:3.1.2` pinned by digest, Docker restart policy `unless-stopped`. See [Registry](#registry) |
| Proxy unit | `dock-caddy.service`: `caddy run --resume` as `dock-caddy`, only binds 80/443. No Docker access |
| Firewall unit | `dock-firewall.service`: runs `dock admin firewall apply` as root whenever Docker starts. See [App firewall](#app-firewall) |
| Daemon data | `/var/lib/dock` (0700): `dock.db`, `secret.key`, `link.key` (what nodes trust), `registry.key`, `builds/`, `uploads/` |
| Caddy data | `/var/lib/dock-caddy`: certificates and the last loaded config |
| Caddy admin socket | `/run/dock-caddy/admin.sock` (dock-caddy:dock-caddy, 0660: only the daemon can change routing) |
| Kernel setting | `/etc/sysctl.d/90-dock.conf`: `net.ipv4.tcp_migrate_req = 1`, so Caddy reloads hand queued connections over instead of resetting them |
| BuildKit image | `moby/buildkit:v0.34.1`, pinned by digest, pulled now; the `dock-buildkit` container is created before the first build |

Re-running the script is safe: config and data are kept, binaries and units
are replaced, and no second admin is created. A server installed with
`--node` is upgraded as a node without the flag; the script refuses to turn
a control plane into a node or back.

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
| `[buildkit] timeout_minutes` | `30` | A build running longer fails, on any builder (read by the control plane). A release's build log stops at 16 MB |
| `[backup] restic_image` | `restic/restic:0.18.1@sha256:39d9…` | Where backup jobs get the restic binary, pinned by digest. The repository and retention are set with `dock backup configure` |
| `[network] pool` | `10.213.0.0/16` | IPv4 range app networks get their addresses from, a /24 per environment. Keep it clear of your LAN and VPNs |
| `[network] allow_private` | `[]` | Private CIDRs apps and builds may reach, e.g. `["192.168.10.20/32"]` for a NAS. See [App firewall](#app-firewall) |
| `[defaults] memory_mb` | `512` | Memory limit for services that don't set one |
| `[defaults] cpus` | `1` | CPU limit for services that don't set one |
| `[defaults] health_check_timeout_seconds` | `60` | Health check timeout for services that don't set one |
| `[defaults] keep_releases` | `5` | Releases kept for rollback per service: their images and history are never pruned |
| `[ingress] mode` | `direct` | `direct`: public IP, Caddy issues certificates. `cloudflare_tunnel`: Caddy serves plain HTTP on `127.0.0.1:<http_port>` only, for the local `cloudflared`; `disable_tls` must stay `false` (apps are `https://` at Cloudflare) |
| `[ingress] tunnel_hostname` | empty | `<tunnel-id>.cfargotunnel.com`, the CNAME target for hostnames on a tunnel server. Set by `install.sh` from the tunnel token |
| `[node] listen` | empty | Control plane: where nodes connect, e.g. `100.64.0.10:7443` on Tailscale. Join tokens carry it, so it must be an address nodes can dial, not `0.0.0.0`. Empty: no nodes |
| `[node] control_plane` | empty | Node: overrides the control plane address in `node.toml` (`host:port`). Its key stays pinned. When the control plane moves, `dock node set-control-plane` is the usual way |
| `[registry] enabled` | `false` | Control plane: run the image registry |
| `[registry] listen` | `127.0.0.1:5000` | Control plane: host address the `dock-registry` container is published on. Node: where `dock node` serves the registry over the link. Image references use it on every server |
| `[registry] storage_dir` | `/var/lib/dock-registry` | Registry blobs and manifests |
| `[registry] public_name` | empty | The registry's name: the audience of its tokens. Required when enabled |
| `[hooks] public_host` | empty | Control plane: the public hostname GitHub delivers pushes to, e.g. `hooks.example.com`, with DNS pointing at this server like an app domain. Caddy routes only `/hooks/github/` on it, to `[hooks] listen`. Empty: [push-to-deploy](deploying.md#deploy-on-git-push) can be connected but receives nothing |
| `[hooks] listen` | `127.0.0.1:7072` | Loopback listener for webhook deliveries; serves nothing else. Must be loopback |
| `[hooks] ui_url` | empty | The UI's URL, e.g. `https://dock.example.com`, for the Details link of GitHub commit statuses. Empty: statuses have no link |
| `[retention] build_log_days` | `30` | Build logs, saved output and progress lines of releases outside the rollback window are deleted after this many days |
| `[retention] audit_days` | `365` | Audit events older than this are deleted |
| `[retention] audit_max_events` | `0` | Also cap the audit log at this many events; `0` no cap |
| `[retention] alert_days` | `90` | Resolved alerts older than this are deleted |
| `[retention] prune_disk_percent` | `80` | When Docker's disk on a server is this full, dangling images dock made (built, or pulled from its registry; not ones a rollback needs) and dock's own BuildKit cache are pruned, at most hourly. Other images and build caches on the server are never touched. `0` never |
| `[retention] build_cache_keep_mb` | `2048` | BuildKit cache left after such a prune |
| `[alerts] disk_warning_percent`, `disk_critical_percent` | `85`, `95` | Disk use that raises a warning or critical alert |
| `[alerts] server_offline_minutes` | `5` | How long a server may be unreachable before an alert |
| `[alerts] crash_loop_restarts`, `crash_loop_minutes` | `3`, `10` | This many restarts within this many minutes is a crash loop |

Where alerts are sent (webhook, Slack, email) is set with `dock alerts
channel add`, not in this file; see [ADR 0020](../decisions/0020-retention-and-alerts.md).

Changing `[buildkit]` settings recreates the `dock-buildkit` container
before the next build. So does finding its memory cap loosened: before each
build the daemon checks the container's cgroup, because with Docker's
systemd cgroup driver a `systemctl daemon-reload` resets its swap limit to
unlimited.

**One dock runtime per Docker host.** A server runs `dock serve` or `dock
node`, not both, and not two of either against the same Docker: they would
share the `dock-buildkit` container (and Caddy's ports). The container
records the data dir of the runtime that created it; a second runtime's
builds fail saying so, instead of recreating it under the first one's
builds. If the runtime that made it is gone for good (reinstalled with
another data dir), remove it with `docker rm -f dock-buildkit`; the next
build creates it again and its cache volume is reused. Docker shared with
other work is fine: dock only ever prunes images it built or pulled from
its registry, and only its own BuildKit's cache.

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
| **Cloudflare Tunnel** | Tailscale or loopback | (n/a) | `cloudflared` → Caddy on `127.0.0.1:80`; Cloudflare terminates TLS |

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
([ADR 0013](../decisions/0013-homelab-ingress-cloudflare-tunnel.md)).
`cloudflared` dials out to Cloudflare and hands every request to Caddy, which
listens in plain HTTP on `127.0.0.1:80` only. Cloudflare's edge terminates
TLS, so apps are still `https://`. Nothing listens publicly and the home IP
never appears in DNS.

1. **Zone.** Add a dedicated domain for app hostnames to Cloudflare, e.g.
   `example-apps.dev`, separate from your product's domain (cookies and
   reputation stay apart).
2. **Tunnel.** Zero Trust → Networks → Tunnels → Create a tunnel →
   Cloudflared, name it after the server. Copy the token from the install
   command it shows (the long `eyJ...` string). Skip its install steps.
3. **Ingress.** Send everything to Caddy: a public hostname `*.example-apps.dev`
   with service `HTTP` `127.0.0.1:80`. Use `127.0.0.1`, not `localhost`,
   which can resolve to `::1` where Caddy doesn't listen. For custom domains
   on other zones too, make it a true catch-all through the API instead:

   ```sh
   curl -X PUT "https://api.cloudflare.com/client/v4/accounts/$ACCOUNT_ID/cfd_tunnel/$TUNNEL_ID/configurations" \
     -H "Authorization: Bearer $CF_API_TOKEN" -H "Content-Type: application/json" \
     --data '{"config":{"ingress":[{"service":"http://127.0.0.1:80"}]}}'
   ```

4. **DNS.** In the zone: `*` CNAME `<tunnel-id>.cfargotunnel.com`, proxied.
   One level only (`web-shop-staging.example-apps.dev`): Cloudflare's free
   certificate covers `*.example-apps.dev`, not deeper names.
5. **Install** with the token, and the zone as the base domain:

   ```sh
   sudo DOCK_BINARY=./dock CLOUDFLARE_TUNNEL_TOKEN=eyJ... BASE_DOMAIN=example-apps.dev \
     ./deploy/install.sh            # add --node and NODE_JOIN_TOKEN for a node
   ```

The script installs `cloudflared` (checked against a pinned SHA-256), runs
it as `dock-cloudflared.service`, and sets `[ingress] mode =
"cloudflare_tunnel"` and `tunnel_hostname` in `config.toml`. Re-runs without
the token keep the tunnel; a new token replaces the old one. Already running
your own `cloudflared` for this server? Disable it first.

Apps see the visitor's address in `CF-Connecting-IP` (and
`X-Forwarded-For`); Caddy only trusts those headers from loopback. Custom
domains need their DNS on the same Cloudflare account: CNAME them to the
tunnel hostname. Domain status is checked with an HTTPS request through
Cloudflare that looks for this server's `Dock-Server` header, since a
tunnelled hostname resolves to Cloudflare, not to the server.

## Servers and placement

The server `install.sh` sets up is the first server. It accepts both kinds
of environment, production and non-production, so a single-server install
needs no setup. More servers [join as nodes](#add-a-server); then an admin
decides which kinds go where:

```sh
dock server list                                       # status, version, accepted kinds, builds, memory reserved
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

### Add a server

A node runs apps and builds for the control plane: Docker, Caddy, BuildKit
and `dock node`, but no API, UI or database. It dials out to the control
plane over TLS with pinned keys ([ADR 0014](../decisions/0014-node-protocol.md)),
so it works behind NAT and needs no inbound ports. Put every server on the
same Tailscale tailnet: the control plane's node port is only meant to be
reachable there. Images travel over the same link, so nodes need nothing
else.

1. **Control plane, once:** open the node port on its Tailscale address,
   and run the [registry](#registry) so images can move between servers.
   `NODE_LISTEN` must be an address nodes can dial, not `0.0.0.0`: join
   tokens carry it.

   ```sh
   sudo NODE_LISTEN="$(tailscale ip -4):7443" REGISTRY=1 DOCK_BINARY=./dock ./deploy/install.sh
   ```

2. **Anywhere you're logged in as an admin:** add the server. Choose the
   environment kinds it takes (`--production`, `--non-production` or both)
   and `--builder` if it builds images. It prints a join token, valid for an
   hour and usable once, inside the exact command for step 3. The token is
   not shown again; `dock server list` shows the server as `joining` until
   it connects.

   ```sh
   dock server add homelab --non-production --builder
   ```

3. **The new server:** install Docker, join the tailnet, copy the `dock`
   binary and `deploy/` over (the control plane's release, as in
   [Install](#install)), then run the printed command. Add
   `CLOUDFLARE_TUNNEL_TOKEN` (and `BASE_DOMAIN`) for a
   [tunnel](#cloudflare-tunnel) server.

   ```sh
   sudo DOCK_BINARY=./dock NODE_JOIN_TOKEN=dockjoin1_... ./deploy/install.sh --node
   ```

   The script installs the same binaries, Caddy and app firewall as on the
   control plane, runs `dock node join` once (it creates the node's key in
   `/var/lib/dock/node.key`, pins the control plane's in
   `/var/lib/dock/node.toml` and prints the server's name), then starts
   `dock-node.service`. The node reports its ingress mode, public IP or
   tunnel hostname, Docker version and memory every time it connects.

4. **Check** that it's online. A `BASE_DOMAIN` given to the node's
   install becomes its base domain for generated hostnames when it joins;
   otherwise set one (this also changes it later):

   ```sh
   dock server list
   dock server update homelab --base-domain example-apps.dev
   ```

The node daemon serves the control plane's registry on its own
`127.0.0.1:5000` (over the link) and answers Caddy's certificate checks
from the routes it last applied, so certificates keep issuing while the
control plane is unreachable. Stopping or upgrading it never stops apps.

| Command | What it does |
|---|---|
| `dock server revoke homelab` | Forgets its key and drops the link. It stays out until it joins again; its apps keep running, but nothing deploys there. Archiving a project there leaves its containers running; remove them on that machine |
| `dock server rotate-key homelab` | The online node switches to a new key |
| `dock server remove homelab` | Only once no live project uses it; archive projects first. Archived projects' environments move to the control plane's server. Containers a revoked or offline server still runs stay there; remove them by hand |

Upgrade a node like the control plane: re-run `install.sh` with the new
binary; no token needed. Upgrade the control plane first. To re-join (after
`dock server revoke`, or once the control plane's key changed), get a new
token with `dock server add` and re-run with it as `NODE_JOIN_TOKEN`. Node
logs: `journalctl -u dock-node`.

What to expect, measured on two LAN machines (control plane building,
node non-production only, plain HTTP): joining takes about 20 ms; a source
deploy to the node (build on the control plane, push, pull by digest over
the link, health check) 5 s with a warm build cache; promoting it to
production 6 s. Upgrading the control plane drops node links for about a
second. A stopped node turns `offline` at once, deploys to it fail
immediately with `server_offline`, and its apps keep serving.

### Registry

When images move between servers (built on one, deployed on another, or
promoted), they go through a registry on the control plane: CNCF
Distribution in the `dock-registry` container. It is never on a network:
the control plane reaches it on `127.0.0.1:5000`, and every node serves it
on its own `127.0.0.1:5000` through the node link, so image references are
the same on every server. Nodes hold no registry credentials; the control
plane hands them a short-lived token per pull or push.

```sh
sudo REGISTRY=1 DOCK_BINARY=./dock ./deploy/install.sh
```

| What | Where |
|---|---|
| Image | `registry:3.1.2`, pinned by digest |
| Container | `dock-registry`, restart policy `unless-stopped`, as the `dock-registry` user, read-only root, no capabilities, 256 MB |
| Published on | `[registry] listen`, `127.0.0.1:5000` by default |
| Config | `/etc/dock/registry/config.yml`, generated by `dock admin registry-config` on every run |
| Storage | `[registry] storage_dir`, `/var/lib/dock-registry`. Back it up only if you want to keep images; releases can be rebuilt |

Re-runs recreate the container only when its image, config or mounts
changed. `REGISTRY_NAME` only names the registry in its tokens. The daemon
serves the registry's token endpoint on the internal listener
(`/internal/registry/token`). To turn the registry off, set `[registry]
enabled = false` and `docker rm -f dock-registry`.

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
the API is down for a second. Download the new release as in
[Install](#install) and re-run the script from its tarball, so the units
match the binary:

```sh
sudo DOCK_BINARY=./dock-linux-amd64 ./deploy/install.sh     # also refreshes units and caddy.json
# or let it download the tarball's version:
sudo --preserve-env=GITHUB_TOKEN ./deploy/install.sh
# or by hand:
sudo install -m 0755 ./dock-linux-amd64 /usr/local/bin/dock && sudo systemctl restart dock
```

Then install the matching CLI on laptops and in CI ([The CLI](#the-cli));
`dock version` shows whether they are in step.

- Database migrations run on start and only go forward. Back up first:
  `sudo dock admin backup` ([Backups](#backups)).
- A deploy running during the restart is marked `failed`; the previous
  release keeps serving. Deploy again afterwards.
- To upgrade Caddy, re-run with `CADDY_VERSION=x.y.z`, then
  `sudo systemctl restart dock-caddy` (the script doesn't restart a running
  Caddy). Traffic blips briefly; `--resume` restores the routes.
- To upgrade BuildKit, change `[buildkit] image` in `config.toml`.
- With several servers, upgrade the control plane first, then each node the
  same way (re-run `install.sh` there; it stays a node).
- To upgrade `cloudflared`, re-run with `CLOUDFLARED_VERSION` and
  `CLOUDFLARED_SHA256` (from the release notes); the tunnel restarts.
- Upgrading an install from before the app firewall: the script moves Caddy
  to the `dock-caddy` user (one Caddy restart, `--resume` restores routes),
  takes `dock` out of the `docker` group, and enables `dock-firewall`.
  Existing app networks keep their Docker-assigned addresses; the firewall
  covers them too. Your `config.toml` is not rewritten, so pin
  `[buildkit] image` by digest yourself if it still says only
  `moby/buildkit:v0.34.1`.

## Backups

The control plane backs itself up every day at the install's backup hour
(default 03:00), into the same restic repository as the databases
([ADR 0021](../decisions/0021-control-plane-backup-and-move.md)). Out of
the box that repository is a Docker volume on this server, not off-box:
configure an S3 repository ([databases and backups](databases.md#where-backups-go))
and keep its password outside the platform.

```sh
dock backup status                   # control plane: succeeded 5h ago (snapshot 4cf53263, 2.1 MB), next Thu 03:00
dock backup run --control-plane      # now (admin)
```

For a file in hand (before an upgrade, or to [move](#moving-the-control-plane)),
on the control plane, as root or the `dock` user. It is safe while dock
runs: the database is copied with SQLite's `VACUUM INTO`, never the live
file.

```sh
sudo dock admin backup                               # ./dock-backup-<host>-<time>.tar.gz
sudo dock admin backup -o /root/dock.tar.gz --include-registry
ssh server 'sudo dock admin backup -o -' > dock.tar.gz
```

| In the archive | Holds |
|---|---|
| `dock.db` | Projects, services, releases, domains, users, tokens, audit log, sealed variable values and credentials |
| `secret.key` | The AES-256 key that opens every sealed value. Without it the database's secrets are lost for good |
| `link.key` | The control plane's identity: what nodes pin |
| `registry.key` | Signs registry tokens |
| `builds/` | Build logs and saved container output |
| `config.toml` | `/etc/dock/config.toml` |
| `registry/` | Only with `--include-registry` (root): the image registry's storage, so rollbacks and promotions of old releases work right after a move. Otherwise their images are built or pushed again |

**The archive holds every secret of the platform.** It is created 0600 and
never overwrites a file; keep it that way, store it encrypted, and delete
copies you no longer need.

Not in it:

- App volumes: backed up per service ([databases and backups](databases.md)).
- `/var/lib/dock-caddy`: certificates, issued again on demand (slow, and it
  counts against CA rate limits with many domains).
- The Cloudflare Tunnel token: in `/etc/dock/cloudflared.env` and the
  Cloudflare dashboard.

To get the latest control plane backup out of the repository, for example
after losing the server (the S3 keys and the repository password in the
environment; `read -rs RESTIC_PASSWORD` keeps them out of shell history):

```sh
docker run --rm -e RESTIC_REPOSITORY -e RESTIC_PASSWORD -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY \
  restic/restic:0.18.1 dump --tag dock-control-plane latest /dock-control-plane.tar > dock.tar
chmod 600 dock.tar
```

From a local repository, on the server holding it: `-v dock_backups_<id>:/repo -e
RESTIC_REPOSITORY=/repo` instead (`docker volume ls --filter name=dock_backups_`).
Restore it as in [Moving the control plane](#moving-the-control-plane).

## Moving the control plane

A move takes the control plane's state to another server with the same
identity: same link key, so nodes keep trusting it and never re-join; same
install ID, users, tokens, secrets and history. There are two shapes:

| | The old server becomes a node | The new server replaces the old one |
|---|---|---|
| When | Only the control plane moves; apps stay (the homelab hands over to a VPS and keeps running staging) | The old server is gone, or everything moves to a bigger server |
| Apps on the old server | Keep running there, untouched; it joins as a node under its name | Are expected on the new server: redeploy them, restore databases from their backups |
| `install.sh` | `--restore ARCHIVE --old-server-becomes-node` | `--restore ARCHIVE` |

The new server runs the same dock release or a newer one. The restore keeps
the old `config.toml` except what describes the machine (API and node
addresses, public IP, ingress, BuildKit size, app network, registry
address and storage), which is set as for a new install from the same
variables (`LISTEN`, `NODE_LISTEN`, `PUBLIC_IP`, `CLOUDFLARE_TUNNEL_TOKEN`,
...). The base domain and `DISABLE_TLS` carry over only when replacing,
unless given. The old file stays in `/etc/dock/config.from-backup.toml` for
reference. Without `NODE_LISTEN`, nodes are taken on this server's
Tailscale address with the old port, if the old control plane took any.

### The old server becomes a node

1. **New server:** install Docker, join the tailnet and copy the release
   over, as in [Install](#install). Don't run `install.sh` yet.
2. **Old server:** stop the daemon and take the last backup. `--move` needs
   the daemon stopped, so nothing changes after the backup, then retires
   this control plane: `dock serve` refuses to start here again. Apps,
   Caddy and the tunnel keep serving; nodes keep their apps running while
   they have no control plane.

   ```sh
   sudo systemctl stop dock
   sudo dock admin backup --move --include-registry -o /root/dock-move.tar.gz
   sudo systemctl disable dock
   ```

   Leave out `--include-registry` without a registry.
3. **Copy the archive** over SSH only, keeping it owner-only:

   ```sh
   ssh old 'sudo cat /root/dock-move.tar.gz' | ssh new 'sudo sh -c "umask 077; cat > /root/dock-move.tar.gz"'
   ```

4. **New server:** install from it. Nodes need an address here, and the old
   server joins as one:

   ```sh
   sudo DOCK_BINARY=./dock NODE_LISTEN="$(tailscale ip -4):7443" \
     ./deploy/install.sh --restore /root/dock-move.tar.gz --old-server-becomes-node
   ```

   Add `PUBLIC_IP`, `BASE_DOMAIN` or `CLOUDFLARE_TUNNEL_TOKEN` as for a new
   install. The old server's environments stay placed on it; this server
   registers as a new server that accepts both kinds of environment.
5. **Laptops and CI:** the API moved, the tokens didn't. Log in again at
   the new address and check:

   ```sh
   dock login http://100.64.0.20:7070
   dock server list            # the old server: revoked (waiting to join); this one: online
   ```

6. **The old server joins.** Add it under its old name (the command wants
   a kind, but a server joining again keeps the kinds and builder setting
   it had), then run the printed command on it:

   ```sh
   dock server add dock-lab --non-production
   # on the old server:
   sudo DOCK_BINARY=./dock NODE_JOIN_TOKEN=dockjoin1_... ./deploy/install.sh --node
   ```

   `install.sh` sees the retired control plane: it removes `dock.service`
   and the `dock-registry` container, moves the old data dir to
   `/var/lib/dock-retired-<time>`, and joins. The containers already there
   are taken over as they are: no app restarts. Its Cloudflare Tunnel, if
   it has one, stays as it is.
7. **Other nodes:** point each at the new address; they reconnect within
   30 seconds, no restart or new join:

   ```sh
   sudo dock node set-control-plane 100.64.0.20:7443
   ```

8. **Settle:** decide which kinds go where (`dock server update`), move
   DNS for anything that pointed at the old server itself (the
   `[hooks] public_host`, the UI's name), and check `dock backup status`.
   Then delete `/var/lib/dock-retired-*` on the old server and every copy
   of the archive: they hold every secret.

### The new server replaces the old one

As above, with these differences:

- Step 2 only if the old server still runs; otherwise take the latest
  control plane backup from the repository ([Backups](#backups)). Never
  start the old server's dock again: wipe it, or run `sudo dock admin
  backup --move` on it first thing.
- Step 4 without `--old-server-becomes-node` (and `NODE_LISTEN` only if
  there are nodes).
- Step 6 is replaced by bringing the apps back. `dock status` shows the
  services of the old server stopped, with a drift note. Redeploy them
  (`dock deploy`, or Deploy in the UI). Don't redeploy a database to get
  its data back: its volume here is new and empty. Restore its latest
  backup into a new service (`dock backup restore`, [databases](databases.md#restore))
  and point references at it.
- DNS: point app domains, the wildcard of the base domain and the hooks
  host at the new public IP. Certificates are issued again on demand.

### With a Cloudflare Tunnel

The node link never goes through Cloudflare: nodes dial the control
plane's node port over Tailscale wherever its apps are served
([ADR 0013](../decisions/0013-homelab-ingress-cloudflare-tunnel.md)).

| Old server | New server | What to do |
|---|---|---|
| Tunnel, becomes a node | Anything | Nothing for the tunnel: the old server keeps it and serves its apps as before. The new server gets its own ingress and base domain |
| Tunnel, replaced | Tunnel | Use the same tunnel: pass its token as `CLOUDFLARE_TUNNEL_TOKEN` in step 4 (from the old `/etc/dock/cloudflared.env` or the dashboard). Stop the old connector first (`sudo systemctl disable --now dock-cloudflared`), or Cloudflare splits traffic between both. DNS stays as it is |
| Tunnel, replaced | Direct | `install.sh` warns. Replace the tunnel CNAMEs (`*.<zone>` and custom domains) with A records for the new public IP, and delete the tunnel |
| Direct, replaced | Tunnel | Set up the tunnel as in [Cloudflare Tunnel](#cloudflare-tunnel) and pass its token; point DNS at it |

### If something goes wrong

- `install.sh --restore` fails before dock starts: fix the cause and run
  the same command again; a restore cut short leaves no database behind.
- To call off a move before restoring anywhere: on the old server, delete
  `/var/lib/dock/MOVED` and `sudo systemctl enable --now dock`. Nodes and
  apps pick up where they were.
- A node logs `control plane key ... does not match the pinned`: the
  address reaches a different control plane (a fresh install rather than a
  restore). Point it at the restored one.
