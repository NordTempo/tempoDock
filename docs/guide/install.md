# Install

The first server runs everything: the `dock` daemon (API, UI, deploys),
Caddy (public traffic and TLS), and BuildKit (builds). Apps run as Docker
containers and keep running when the daemon is stopped or upgraded. More
servers can [join as nodes](#add-a-server) later; the first one stays the
control plane.

## Requirements

| What | Needed |
|---|---|
| OS | Linux with systemd, `amd64` or `arm64`. Tested on every change: Debian 12 and 13, Ubuntu 22.04 and 24.04 (`e2e/fresh-install.sh`) |
| Docker | Docker Engine 28 or newer with its default iptables firewall backend. On Debian and Ubuntu, `install.sh` installs it when missing and starts it when stopped. Older versions expose ports published on 127.0.0.1 to the LAN; the script warns |
| Tools | `curl` (downloads, detects the public IP), `iptables` (the app firewall; installed with Docker on Debian and Ubuntu), OpenSSL 3 for the signature check |
| Access | root (`sudo`) |
| Size | 2 vCPU / 4 GB is comfortable. Idle platform is about 490 MB including Docker, Caddy and BuildKit ([measured](../experience.md#measured-in-the-m0-spike-2026-10)). Builds are capped at 2 GB by default |
| Ports | 80/tcp, 443/tcp and 443/udp public. The API port (7070) and node port (7443) private: Dock goes public only through Caddy, at its [public address](#public-address). None on a [Cloudflare Tunnel](#cloudflare-tunnel) server |
| cgroups | cgroup v2 (the default on current distributions) for per-service statistics. Docker's systemd and cgroupfs drivers both work |
| Optional | Tailscale, so the API listens on the tailnet by default. Recommended for every server once there's more than one |

## Install

On the server, as a user who can `sudo`:

```sh
curl -fsSL https://dock.nordtempo.com/install.sh | sudo bash
```

That address serves a small script, the same on every release
([`site/public/install.sh`](../../site/public/install.sh)). It finds the
latest release, refuses one older than the release it was written for,
checks the release's `SHA256SUMS` against its signature, and runs the
release's `install.sh` only when the script matches its line in
`SHA256SUMS`, with your options and variables as they are. `cli.sh` below
does the same for `install-cli.sh`.

`install.sh` then downloads the release's
install tarball (`deploy/`: the script and the systemd units) and the
`dock-linux-<arch>` binary, checks both against the release's
`SHA256SUMS` and that file's signature
([ADR 0088](../decisions/0088-signed-releases.md)), and runs. Piped like
this the signature check is required: it needs OpenSSL 3, which Debian 12,
Ubuntu 22.04 and later have (`DOCK_REQUIRE_SIGNATURE=0` accepts
`SHA256SUMS` alone). A download cut short runs nothing: the whole script is
one function, called on its last line.

On Debian and Ubuntu it installs what's missing: Docker Engine (from
Docker's own apt repository, as [docs.docker.com](https://docs.docker.com/engine/install/)
does it) and `iptables`. It asks first; `--yes` doesn't. Elsewhere it
says what to install. On a new install with a terminal it also asks for
your email, the console login (`DOCK_ADMIN_EMAIL`, default
`admin@localhost`), and an optional domain for apps' generated hostnames
(`BASE_DOMAIN`). Options go after `bash -s --`, variables after `sudo`:

```sh
curl -fsSL https://dock.nordtempo.com/install.sh | sudo DOCK_ADMIN_EMAIL=you@example.com bash -s -- --yes
```

It ends with what to do next:

```
  Console   http://localhost:7070
            The console listens on this server only. On your laptop, open a tunnel first:
            ssh -L 7070:127.0.0.1:7070 you@203.0.113.7
  Sign in   you@example.com with password … (shown once)
  CLI       curl -fsSL https://dock.nordtempo.com/cli.sh | sh -s -- http://localhost:7070
            installs dock on your laptop (macOS, Linux or WSL) and signs it in
  Apps      get hostnames under apps.example.com; it needs DNS: *.apps.example.com  A  203.0.113.7
```

The console and API listen on loopback, or on the Tailscale address when
the server has one ([Network access](#network-access)); to reach Dock
from anywhere without a tunnel, give it a [public address](#public-address).
The user you sign in as is the platform's operator and the admin of its
first organization. The output also has an operator token, `admin token:
dock_…`, shown once: keep it for scripts and for creating agent tokens.
It is both the first organization's admin token and the platform's; split
it in Console › Tokens (`dock platform token split`) once you have other
credentials. Operators run the platform: servers, backups and user
accounts, with a platform login (`dock login --platform`) beside their org
login; an organization's admins manage its projects, members' roles and
tokens ([ADR 0027](../decisions/0027-organizations.md),
[ADR 0080](../decisions/0080-permissions-roles-groups-and-agents.md)).

A given release: `DOCK_VERSION=<version>` after `sudo` installs that
version, and so does its own `releases/download/<version>/install.sh`. Running the script again is safe ([What it installs](#what-it-installs)).

### By hand

The same from files you copy over yourself: a release's
`dock-linux-<arch>` and `dock-install-<version>.tar.gz`, checked against
its `SHA256SUMS` (and that file's signature, see
[Verifying a release](../../SECURITY.md#verifying-a-release)):

```sh
v=v0.56.0
mkdir dock-$v && cd dock-$v
for f in dock-linux-amd64 "dock-install-$v.tar.gz" SHA256SUMS; do
  curl -fsSLO "https://github.com/NordTempo/tempoDock/releases/download/$v/$f"
done
grep -E " (dock-linux-amd64|dock-install-$v.tar.gz)\$" SHA256SUMS | sha256sum -c   # shasum -a 256 -c on a Mac
chmod +x dock-linux-amd64
scp -r ../dock-$v server:/tmp/
ssh server "cd /tmp/dock-$v && tar -xzf dock-install-$v.tar.gz && sudo DOCK_BINARY=./dock-linux-amd64 ./deploy/install.sh"
```

Without `DOCK_BINARY`, the tarball's `install.sh` downloads the binary of
its own version (or `DOCK_VERSION`), checked as above. From source
instead: `make web && GOOS=linux GOARCH=amd64 make build` builds
`bin/dock`; copy it and `deploy/` over and pass `DOCK_BINARY=./dock`. The
examples below say `DOCK_BINARY=./dock`; use whichever binary you have.

### The CLI

Laptops and CI only need the client, `dock-cli-<os>-<arch>` (a quarter of
the size; see [CLI-only build](../cli.md#cli-only-build)). With your
server's address, the installer picks the version that server runs and
signs you in (`dock login`, a code you approve in the browser):

```sh
curl -fsSL https://dock.nordtempo.com/cli.sh | sh -s -- https://dock.example.com
```

The console shows this line with its own address, on the empty Overview
and the sign-in page. Without an address it installs the latest release
(`DOCK_VERSION=v0.56.0` for another). It checks the build against
`SHA256SUMS` and its signature and installs it as `dock` in
`~/.local/bin` (`/usr/local/bin` as root, or `DOCK_INSTALL_DIR`), and
prints the `PATH` line to add when that directory isn't on it. The
signature check needs OpenSSL 3; macOS ships LibreSSL, so there the script
warns and checks `SHA256SUMS` only, unless you `brew install openssl@3`
and put it first in `PATH`.

A version is a release version (`v0.56.0`, `v0.56.0-rc.1`) or nothing is
downloaded, whoever names it. The latest release and a server's version
must be signed: a release from before v0.56.0, which has no signature,
installs only when you name it yourself with `DOCK_VERSION`, with a
warning. `DOCK_REQUIRE_SIGNATURE=1` installs nothing without a signature
checked on this machine: no such older release, and none where OpenSSL 3
is missing.

With [Homebrew](https://brew.sh), on macOS or Linux:

```sh
brew install nordtempo/tap/dock
dock login https://dock.example.com
```

The tap's formula ([NordTempo/homebrew-tap](https://github.com/NordTempo/homebrew-tap))
is written by the release workflow, with each build's SHA-256 from the
signed `SHA256SUMS`; `brew upgrade` follows releases.

**Windows:** in WSL (`wsl --install`), with the Linux line above. There is
no native Windows build.

Or by hand:

```sh
for f in dock-cli-darwin-arm64 SHA256SUMS; do
  curl -fsSLO "https://github.com/NordTempo/tempoDock/releases/download/v0.56.0/$f"
done
grep ' dock-cli-darwin-arm64$' SHA256SUMS | shasum -a 256 -c
install -m 0755 dock-cli-darwin-arm64 ~/.local/bin/dock
```

Checking by hand, check `SHA256SUMS` itself too: with its signature
([Verifying a release](../../SECURITY.md#verifying-a-release)), or with
`gh attestation verify dock-cli-darwin-arm64 --repo NordTempo/tempoDock`.

Once it is installed, `dock self-update` keeps it in step: it installs the
release the current context's server runs (or the latest, with
`--latest`, or a given one, with `--to v0.54.1`), checked against the
release's signed `SHA256SUMS`, in place of the running binary. It only
goes forward, to a signed release, on its own: for a server on an older
release than the CLI, or on one from before v0.56.0 (no signature, so a
warning), it stops and prints the `dock self-update --to <version>` that
installs it anyway. In a directory
only root can write to, such as `/usr/local/bin`, it prints the `sudo`
command to run, with the version pinned. Installed with Homebrew, use
`brew upgrade dock` instead.

```sh
dock self-update             # the server's version
dock self-update --dry-run   # what it would install, and where
```

`dock version` shows the CLI's version and the server's, and says to run
`dock self-update` when they differ. `dock login` warns too.

### install.sh options

`--yes` installs missing prerequisites without asking and asks nothing
else (no terminal is a no). `--uninstall` and `--purge`: see
[Uninstalling](#uninstalling). `--node` installs a [node](#add-a-server) instead of a control plane.
`--restore ARCHIVE` (with `--old-server-becomes-node`) installs a control
plane from a backup: see [Moving the control plane](#moving-the-control-plane).
The rest are environment variables. `PUBLIC_IP`, `LISTEN`, `BASE_DOMAIN` and
`DISABLE_TLS` are only used when `/etc/dock/config.toml` doesn't exist yet;
the others also change an existing config, touching only their own keys.

| Variable | Default | Meaning |
|---|---|---|
| `DOCK_BINARY` | download | Path to a `dock` binary: a release's `dock-linux-<arch>`, or `bin/dock` from `make build` |
| `DOCK_VERSION` | the tarball's version, else the latest release | Without `DOCK_BINARY`: the release to download `dock-linux-<arch>` from, checked against its signed `SHA256SUMS`. A release from before v0.56.0, which has no signature, is taken on its `SHA256SUMS` alone only when named here |
| `DOCK_REQUIRE_SIGNATURE` | empty; `1` when piped | `1`: fail that download without a signature checked on this machine: a release from before v0.56.0, or no OpenSSL 3 here. `0` accepts `SHA256SUMS` alone when piped (for a release before v0.56.0, with `DOCK_VERSION`) |
| `DOCK_ADMIN_EMAIL` | asked, else `admin@localhost` | The first admin's email, the console login. Only when the first admin is created |
| `DOCK_YES` | empty | `1` is `--yes` |
| `DOCK_RELEASE_DIR` | empty | Take the release from this directory instead of GitHub: its `dock-install-<version>.tar.gz`, `dock-linux-<arch>`, `SHA256SUMS` and `SHA256SUMS.sig`, as `make release-local` builds them |
| `DOCK_TRUSTED_KEYS` | empty | More release keys to trust, base64 Ed25519, space separated, as `[updates] trusted_keys`: for builds you sign yourself |
| `GITHUB_TOKEN` | empty | For that download, only from a private repository: a token that can read its releases (pass it with `sudo --preserve-env=GITHUB_TOKEN`) |
| `CADDY_VERSION`, `CADDY_SHA256` | `2.11.7`, pinned | Caddy release to download from GitHub, checked against a SHA-256 the script holds; another release needs the SHA-256 of its `caddy_<version>_linux_<arch>.tar.gz` |
| `PUBLIC_IP` | auto (`api.ipify.org`), empty on tunnel servers | Address users point DNS at → `public_ip`. Only an address is taken, from you or from the lookup |
| `LISTEN` | Tailscale IPv4`:7070`, else `127.0.0.1:7070` | API and UI address → `listen`. Control plane only |
| `BASE_DOMAIN` | asked, else empty | Base for generated hostnames → `base_domain`. Needs a wildcard DNS record. Later: `dock server update <server> --base-domain` |
| `DISABLE_TLS` | `false` | `true` serves plain HTTP only → `[caddy] disable_tls`. For LAN/Tailscale-only setups. Ignored on tunnel servers |
| `CLOUDFLARE_TUNNEL_TOKEN` | empty | Serve public traffic through a [Cloudflare Tunnel](#cloudflare-tunnel): installs `cloudflared` and `dock-cloudflared.service`, sets `[ingress]` |
| `CLOUDFLARED_VERSION`, `CLOUDFLARED_SHA256` | `2026.10.3`, pinned | Another `cloudflared` release and the SHA-256 of `cloudflared-linux-<arch>` from its release notes |
| `INGRESS` | `direct` | `cloudflare_tunnel` without a token: tunnel mode, with the tunnel made and run by Dock ([Tunnels managed by Dock](#tunnels-managed-by-dock)); `dock server add --tunnel` prints it. Once Dock runs a tunnel this script installed, a re-run removes `dock-cloudflared.service` |
| `NODE_LISTEN` | empty | Accept nodes on this address, e.g. `100.64.0.10:7443` → `[node] listen`. Control plane only |
| `REGISTRY` | empty | `1` runs the [image registry](#registry) → `[registry] enabled`. Control plane only |
| `REGISTRY_NAME` | `dock-registry` with `REGISTRY=1` | The registry's name in its tokens → `[registry] public_name` |
| `REGISTRY_LISTEN` | `127.0.0.1:5000` | Host address the registry container is published on → `[registry] listen` |
| `REGISTRY_STORAGE` | asked, else `local` | Where a new registry keeps its images: `local` (this server's disk) or `s3` (a bucket, see [Images in object storage](#images-in-object-storage)). Asked when install.sh turns the registry on and there is a terminal |
| `REGISTRY_S3_ENDPOINT`, `REGISTRY_S3_REGION`, `REGISTRY_S3_BUCKET` | empty | With `s3`: the store's base URL (`https://fsn1.your-objectstorage.com`, no bucket), its region (empty signs as `us-east-1`) and the bucket |
| `REGISTRY_S3_ACCESS_KEY_ID`, `REGISTRY_S3_SECRET_ACCESS_KEY` | empty | With `s3`: the keys, passed to dock on stdin |
| `REGISTRY_S3_PATH_STYLE` | `true` | `false` puts the bucket in the host name instead of the path |
| `REGISTRY_S3_NAME` | the bucket's name | The storage connection's name in dock |
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
| Updater units | `dock-update.path` starts `dock-update.service` (`dock admin update`, root) when the daemon asks for an update in `/var/lib/dock/update`. See [Upgrading](#upgrading) |
| Daemon data | `/var/lib/dock` (0700): `dock.db`, `secret.key`, `link.key` (what nodes trust), `registry.key`, `builds/`, `uploads/`, `update/` (staged releases) |
| Updater data | `/var/lib/dock-update` (root, 0700): the release being applied, the binary and units it replaced, and the backup taken before the last update |
| Caddy data | `/var/lib/dock-caddy`: certificates and the last loaded config |
| Upstream files | `/var/lib/dock-upstreams` (dock:dock-caddy, 2750): one file per service with its container's address, which Caddy reads per request ([ADR 0091](../decisions/0091-switches-without-reloads.md)); dock writes it again on start |
| Caddy admin socket | `/run/dock-caddy/admin.sock` (dock-caddy:dock-caddy, 0660: only the daemon can change routing) |
| Kernel setting | `/etc/sysctl.d/90-dock.conf`: `net.ipv4.tcp_migrate_req = 1`, so Caddy reloads hand queued connections over instead of resetting them |
| BuildKit image | `moby/buildkit:v0.34.1`, pinned by digest, pulled now; the `dock-buildkit` container is created before the first build |

Re-running the script is safe: config and data are kept, binaries and units
are replaced, and no second admin is created. A server installed with
`--node` is upgraded as a node without the flag; the script refuses to turn
a control plane into a node or back.

### Uninstalling

```sh
curl -fsSL https://dock.nordtempo.com/install.sh | sudo bash -s -- --uninstall
```

removes Dock's services, binaries (`dock`, Caddy, `cloudflared`), kernel
setting and app firewall, and every container Dock made, the apps'
included: nothing would route to them without Caddy. Its data stays:
`/etc/dock`, `/var/lib/dock` (the database and secrets) and the apps'
volumes, so installing again brings the projects back, ready to
redeploy. `--uninstall --purge` removes those too, with Dock's networks
and system users. Docker stays either way. On a node, remove the server
on the control plane first (`dock server remove`).

## config.toml

`/etc/dock/config.toml`. Every field has a default; a missing file means all
defaults. Restart after editing: `sudo systemctl restart dock` (apps keep
running).

| Field | Default | Meaning |
|---|---|---|
| `data_dir` | `/var/lib/dock` | SQLite DB, secrets key, build logs, uploads |
| `listen` | `127.0.0.1:7070` | API and UI. Tailscale IP or loopback; not meant to be public |
| `internal_listen` | `127.0.0.1:7071` | Loopback-only endpoints (Caddy's TLS `ask`). Must be loopback or the daemon refuses to start |
| `pprof` | `false` | Serve Go's runtime profiles on `internal_listen` at `/debug/pprof/`, for debugging. Any local user can reach them |
| `public_listen` | `127.0.0.1:7073` | Where Caddy proxies Dock's [public address](#public-address): the API and UI again, trusting the client address Caddy sends. Must be loopback |
| `public_ip` | empty | Address shown in DNS instructions and used to check domains |
| `base_domain` | empty | Enables generated hostnames under it. Needs `*.<base>` pointing at `public_ip` |
| `max_upload_mb` | `512` | Largest source upload for `dock deploy` |
| `[host_ports] min`, `max` | `20000`, `29999` | Loopback ports published app containers bind to, one per release |
| `[caddy] admin_socket` | `/run/dock-caddy/admin.sock` | Caddy admin API socket |
| `[caddy] http_port` | `80` | |
| `[caddy] https_port` | `443` | |
| `[caddy] acme_email` | empty | Contact email for the certificate authority |
| `[caddy] acme_directory` | empty (Let's Encrypt) | ACME directory for base domains' wildcard certificates ([domains](domains.md#wildcard-certificates-for-generated-hostnames)) |
| `[caddy] disable_tls` | `false` | Serve every route over plain HTTP |
| `[caddy] tunnel_port` | `7075` | Where a direct server's Caddy also serves its Cloudflare tunnel, on `127.0.0.1`, when Dock runs one next to the public IP ([Tunnels managed by Dock](#tunnels-managed-by-dock)). Only domains added from Cloudflare go that way, and Caddy listens there only while there are some. `0` turns it off. Changing it after a tunnel exists also needs the tunnel's route in Cloudflare changed |
| `[caddy] traffic_listen` | `127.0.0.1:7074` | Loopback UDP address where Caddy sends the access log of service domains, counted for [traffic statistics](../decisions/0033-traffic-statistics.md). Must be a loopback IP; empty turns counting off |
| `[caddy] upstreams_dir` | `/var/lib/dock-upstreams` | Where Dock keeps each service's container address for Caddy, which reads it per request, so switching a service to a new release reloads no config and drops no connection ([ADR 0091](../decisions/0091-switches-without-reloads.md)). install.sh makes it, writable by `dock` and readable by Caddy. Empty, or a directory Dock can't write, puts the addresses in Caddy's config, and every switch reloads it |
| `[buildkit] enabled` | `true` | This server builds images. `false`: source deploys fail; deploy images instead |
| `[buildkit] image` | `moby/buildkit:v0.34.1@sha256:9936…` | BuildKit image, pinned by digest |
| `[buildkit] memory_mb` | `2048` | Hard memory cap for builds (no swap). Builds over it fail; apps are unaffected |
| `[buildkit] cpus` | `0` | CPU cap for builds; 0 is half the server's cores, at least two (all of a smaller server). Builds also get a quarter of the normal CPU weight, so apps win any contention |
| `[buildkit] timeout_minutes` | `30` | A build running longer fails, on any builder (read by the control plane). A release's build log stops at 16 MB |
| `[backup] restic_image` | `restic/restic:0.18.1@sha256:39d9…` | Where backup jobs get the restic binary, pinned by digest. Retention is set with `dock backup configure`, the repository with `dock backup move` |
| `[backup] snapshot_keep`, `snapshot_days` | `5`, `7` | [Volume snapshots](../decisions/0047-volume-snapshots.md) kept per service, and for how many days at most |
| `[network] pool` | `10.213.0.0/16` | IPv4 range app networks get their addresses from, a /24 per environment. Keep it clear of your LAN and VPNs |
| `[network] allow_private` | `[]` | Private CIDRs apps and builds may reach and servers may pull images from, e.g. `["192.168.10.20/32"]` for a NAS. See [App firewall](#app-firewall) |
| `[defaults] memory_mb` | `512` | Memory limit for services that don't set one |
| `[defaults] cpus` | `1` | CPU limit for services that don't set one |
| `[defaults] health_check_timeout_seconds` | `60` | Health check timeout for services that don't set one |
| `[defaults] keep_releases` | `5` | Releases kept for rollback per service: their images and history are never pruned |
| `[ingress] mode` | `direct` | `direct`: public IP, Caddy issues certificates. `cloudflare_tunnel`: Caddy serves plain HTTP on `127.0.0.1:<http_port>` only, for the local `cloudflared`; `disable_tls` must stay `false` (apps are `https://` at Cloudflare) |
| `[ingress] tunnel_hostname` | empty | `<tunnel-id>.cfargotunnel.com`, the CNAME target for hostnames on a tunnel server. Set by `install.sh` from the tunnel token |
| `[ingress] cloudflared_image` | `cloudflare/cloudflared:2026.10.3@sha256:…` | The `cloudflared` image of the `dock-cloudflared` container, when Dock runs the server's tunnel ([Tunnels managed by Dock](#tunnels-managed-by-dock)). Pin it by digest |
| `[node] listen` | empty | Control plane: where nodes connect, e.g. `100.64.0.10:7443` on Tailscale. Join tokens carry it, so it must be an address nodes can dial, not `0.0.0.0`. Empty: no nodes |
| `[node] control_plane` | empty | Node: overrides the control plane address in `node.toml` (`host:port`). Its key stays pinned. When the control plane moves, `dock node set-control-plane` is the usual way |
| `[registry] enabled` | `false` | Control plane: run the image registry |
| `[registry] listen` | `127.0.0.1:5000` | Control plane: where the `dock-registry` container listens, on the host's network. Node: where `dock node` serves the registry over the link. Image references use it on every server |
| `[registry] gateway_listen` | `127.0.0.1:5001` | Control plane: the storage gateway the registry reaches a bucket through ([Slow or degraded buckets](#slow-or-degraded-buckets)). Must be loopback |
| `[registry] spool_dir` | `registry-spool` in `data_dir` | Where pushes land before they upload to the bucket. `dock.service` may write `data_dir`; another dir needs a `ReadWritePaths=` drop-in |
| `[registry] spool_max_gb` | `0` | The spool's cap; 0 is the smaller of 20 GB and a quarter of its disk. Past it, or with less than 10% of the disk free, pushes are refused |
| `[registry] storage_dir` | `/var/lib/dock-registry` | Registry blobs and manifests, unless they are in a bucket ([Images in object storage](#images-in-object-storage)) |
| `[registry] config_dir` | `/etc/dock/registry` | The registry's `config.yml` and `jwks.json`, written by dock (owned by `dock`, setgid `dock-registry`). `dock.service` may write only there: another dir needs a `ReadWritePaths=` drop-in |
| `[registry] public_name` | empty | The registry's name: the audience of its tokens. Required when enabled |
| `[hooks] public_host` | empty | Control plane: a public hostname GitHub delivers pushes to, e.g. `hooks.example.com`, with DNS pointing at this server like an app domain. Not needed when Dock has a public address, which serves `/hooks/github/` too; set it to keep the API off the internet. Caddy routes only `/hooks/github/` on it, to `[hooks] listen`. Empty, and no public address: [push-to-deploy](deploying.md#deploy-on-git-push) can be connected but receives nothing |
| `[hooks] listen` | `127.0.0.1:7072` | Loopback listener for webhook deliveries; serves nothing else. Must be loopback |
| `[hooks] ui_url` | empty | The UI's URL, e.g. `https://dock.example.com`, for the Details link of GitHub commit statuses and the link in Slack alerts. Empty: Dock's public address, if it has one |
| `[retention] build_log_days` | `30` | Build logs, saved output and progress lines of releases outside the rollback window are deleted after this many days |
| `[retention] audit_days` | `365` | Audit events older than this are deleted |
| `[retention] audit_max_events` | `0` | Also cap the audit log at this many events; `0` no cap |
| `[retention] alert_days` | `90` | Resolved alerts older than this are deleted |
| `[retention] prune_disk_percent` | `80` | When Docker's disk on a server is this full, dangling images dock made (built, or pulled from its registry; not ones a rollback needs) and dock's own BuildKit cache are pruned, at most hourly. Other images and build caches on the server are never touched. `0` never |
| `[retention] build_cache_keep_mb` | `2048` | BuildKit cache left after such a prune |
| `[logs] keep_mb_per_service` | `64` | When dock removes a container, its output is kept on its server (`<data dir>/logs/`) so `dock logs --release N` can read it. Each service keeps at most this much, compressed; the oldest releases' logs go first. Applies to every server. At least `1` |
| `[logs] keep_mb_per_server` | `2048` | All services' kept output on one server together, compressed; the oldest go first, whatever their service. Nothing is kept while the disk has less than 1 GB or 5% free. At least `1` |
| `[logs] keep_days` | `30` | A release's kept output is deleted this many days after its container was removed. At least `1` |
| `[alerts] disk_warning_percent`, `disk_critical_percent` | `85`, `95` | Disk use that raises a warning or critical alert |
| `[alerts] server_offline_minutes` | `5` | How long a server may be unreachable before an alert |
| `[alerts] crash_loop_restarts`, `crash_loop_minutes` | `3`, `10` | This many restarts within this many minutes is a crash loop |
| `[alerts] memory_warning_percent`, `memory_critical_percent`, `memory_minutes` | `90`, `97`, `5` | A server's memory use above this for this many minutes raises a warning or critical alert |
| `[alerts] cpu_percent`, `cpu_minutes` | `90`, `15` | A server's CPUs busier than this for this many minutes raise a warning; `0` never |
| `[analytics] ingest_listen` | `127.0.0.1:7077` | Loopback address Caddy sends [Dock Analytics](../decisions/0037-analytics.md) events to. Must be loopback; empty turns the ingest off |
| `[analytics] stats_listen` | `127.0.0.1:7078` | Loopback address of sites' public dashboards. Empty turns them off |
| `[status] listen` | `127.0.0.1:7079` | Loopback address of [status pages](status-pages.md). Empty turns them off. Control plane only |
| `[analytics] spool_max_mb` | `256` | Events waiting for the store, at most; past it the oldest go. At least `16` |
| `[analytics] site_day_events`, `site_day_replay_mb`, `max_gb` | `0`, `0`, `0` | Control plane: what a site keeps a UTC day of events and of replay recordings (anyone can send a site more; the rest of that day's is dropped), and the size of the analytics store's database and replay files together; past it the oldest days of replays go early, then the oldest days of raw events, whose daily totals stay. `0` is the store's default: `1000000` events, `1024` MB, and the smaller of 10 GB and a tenth of the store's disk |
| `[analytics] store_port`, `store_memory_mb` | `7076`, `512` | On the analytics server: the store's loopback port and memory limit (at least `256`) |
| `[traces] store_port`, `store_memory_mb`, `retention_days` | `7080`, `512`, `7` | The trace store, beside the analytics store on the analytics server (`dock tracing on`): its loopback port and memory limit (at least `256`), and how many days of spans it keeps (1 to 30; control plane only) |
| `[traces] ingest_listen`, `spool_max_mb`, `metrics_spool_max_mb` | `127.0.0.1:4318`, `128`, `64` | This server's trace ingest (OTLP/HTTP) on loopback, which takes spans and app metrics while tracing is on (empty turns it off), and the caps on spans and on metric points waiting for the trace store; past either the oldest go |
| `[traces] service_day_spans`, `org_day_spans`, `error_day_spans`, `max_gb` | `2000000`, `5000000`, `200000`, `4` | Control plane: spans a service and an org keep a day; past either only spans with an error are kept, up to `error_day_spans` a service. `max_gb` caps the trace store's data; past it the oldest day goes early |
| `[traces] metric_series`, `metric_names` | `2000`, `200` | Control plane: the app metrics' series and metric names a service keeps a day; past either its new series are cut (the series it already sent go on) |
| `[scan] syft_image`, `grype_image` | `anchore/syft:v1.57.1@sha256:0321…`, `anchore/grype:v0.121.3@sha256:1311…` | The image scanners, pinned by digest ([ADR 0074](../decisions/0074-image-vulnerability-scanning.md)). Builders pull them the first time they scan |
| `[scan] memory_mb`, `quiet_seconds` | `1024`, `60` | Each scan job's memory limit, and how long no build, deploy, wake or move may have run before a scan starts. `dock scanning off` turns scanning off |
| `[scan] offline` | `false` | For installs without internet access: builders never download a vulnerability database or check base images; import one with `dock scanning import FILE` (a Grype database archive) |
| `[previews] copy_max_mb` | `2048` | The largest database a preview copies from its base ([ADR 0092](../decisions/0092-preview-database-copies.md)); a larger one starts empty |
| `[updates] source` | `github.com/NordTempo/tempoDock` | Where [updates](#upgrading) come from: a GitHub repository's releases, or an absolute path to a directory of releases (`<dir>/<version>/` with the release's assets and an optional `notes.md`), for a mirror or an air-gapped install. Control plane only |
| `[updates] trusted_keys` | none | Ed25519 public keys (base64) whose signatures count as Dock's, for releases you build and sign yourself ([ADR 0088](../decisions/0088-signed-releases.md)). Only with a directory `source` on the control plane; on a node, for what its control plane sends. Set it in this file only |
| `[updates] token_file` | `/etc/dock/github-token` | A GitHub token that can read the repository's releases, used when the file exists. Only releases in a private repository need one |

Where alerts are sent (webhook, Slack, email) is set with `dock alerts
channel add`, not in this file: an organization's channels get its
alerts, platform channels (`--platform`) the platform's ([ADR
0062](../decisions/0062-org-alerts-and-operators.md)); see [ADR 0020](../decisions/0020-retention-and-alerts.md).
Alerts also come from failing health checks (`unhealthy`, [ADR
0043](../decisions/0043-ongoing-health-checks.md)) and uptime checks
(`uptime_down`, two failed probes in a row, [ADR
0055](../decisions/0055-uptime-checks.md)); those have no settings here.

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
port to the internet: tokens travel over plain HTTP to it. To reach Dock
from anywhere, give it a [public address](#public-address) instead.

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

### Public address

Dock itself can be served at a hostname, over HTTPS, by the control
plane's Caddy, so people open invite links and CI deploys without joining
the tailnet ([ADR 0029](../decisions/0029-public-address.md)). Set it in
the console (System → Address) or as an operator:

```sh
dock address set dock.example.com    # prints the DNS record to create
dock address                         # status: waiting for DNS, issuing certificate, active
dock login https://dock.example.com  # once it's active, from anywhere
```

Create the record it prints (`A` to the server's public IP, or the CNAME to
the tunnel on a tunnel server), without a CDN proxy in direct mode. The
certificate is issued once it resolves. The private address (`listen`)
keeps working beside it; `dock address remove` stops serving the public
one. The hostname can't also be an app domain or `[hooks] public_host`.
Invite links and GitHub commit status links use it.

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

To let apps reach a LAN host, such as a NAS, a database or a registry to
pull images from, add it to the allowlist and re-apply:

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

With a Cloudflare API token, Dock does the setup for you: see [Tunnels
managed by Dock](#tunnels-managed-by-dock). By hand:

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
   read -rs CLOUDFLARE_TUNNEL_TOKEN && export CLOUDFLARE_TUNNEL_TOKEN   # paste the token, Enter
   sudo --preserve-env=CLOUDFLARE_TUNNEL_TOKEN DOCK_BINARY=./dock BASE_DOMAIN=example-apps.dev \
     ./deploy/install.sh            # add --node and NODE_JOIN_TOKEN for a node
   ```

   `read` takes the token from the terminal without showing it, so it is
   in neither the shell's history nor the process list, where a variable
   written after `sudo` would be for as long as the install runs.

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

### Tunnels managed by Dock

With a Cloudflare connection, Dock creates (or takes over) a tunnel
server's tunnel and routes domains you add from Cloudflare through it
([ADR 0036](../decisions/0036-cloudflare-api.md)). It is optional:
without one, everything above still applies. Dock only uses tunnels: it
never points DNS at an IP and makes no wildcard record.

1. **Zone.** As above: a zone for app hostnames, on Cloudflare.
2. **Token.** In the Cloudflare dashboard, create an API token (an
   account-owned one survives you leaving the account) with:
   - Account › Cloudflare Tunnel › Edit
   - Zone › DNS › Edit, on the zones domains may come from
   - Zone › Zone › Read, on the same zones

   Dock only edits records it made (comment `dock:<install id>`) and
   never overwrites one it didn't.
3. **Connect** it on the control plane (or Console › Cloudflare):

   ```sh
   printf %s "$CLOUDFLARE_API_TOKEN" | dock cloudflare add home --token-stdin --platform --shared
   ```

   Dock checks the token and lists the zones domains can come from
   (`dock cloudflare zones`).
4. **A new server:** `dock server add homelab --non-production --tunnel`,
   then run the command it prints on the server (it has
   `INGRESS=cloudflare_tunnel` and no tunnel token). Once the server
   joins, Dock creates the tunnel `dock-<install id>-homelab`, routes it
   to the server's Caddy and runs `cloudflared` there in its
   `dock-cloudflared` container.
5. **Domains:** `dock domain add shop.example-apps.dev -s web --cloudflare`
   (in the UI, Add domain → from Cloudflare). Dock routes the hostname
   through the server's tunnel and creates its proxied CNAME; both go
   when the domain is removed or the service archived. A name that
   already has a record Dock didn't make is refused. Generated domains
   under a base domain in a connected zone, on a tunnel server, get the
   same.

**A direct server can have both.** On a server with a public IP (like a
VPS), adding a domain from Cloudflare has Dock create a tunnel there too:
`cloudflared` runs next to Caddy and reaches it on `127.0.0.1:7075`
(`[caddy] tunnel_port`). Domains you type keep their A records and
certificates on the public IP; domains from Cloudflare are served only
through the tunnel, never on the IP. Nothing changes for existing
domains, and no port opens.

A server that already runs a tunnel set up by hand (above) needs no step
of its own: the first domain from Cloudflare (or `dock server tunnel
adopt homelab`) has Dock take it over. The tunnel must be in the token's
account and configured in the dashboard (a locally configured one needs
its Migrate step first). Its routes stay; Dock adds a hostname route for
each of its domains when the tunnel's catch-all doesn't go to Caddy.
Dock's container joins the old `cloudflared` as a second connector, so
nothing drops; then run install.sh on the server again to remove the old
service. CNAMEs you made that already point at the tunnel stay yours,
untouched, and so does the wildcard.

If Cloudflare is down, tunnels and domains wait and are retried. If the
token is revoked or expires, the connection shows `invalid` and its work
waits for `dock cloudflare set home --platform --token-stdin`. Dock asks Cloudflare
nothing while idle. Removing a server deletes the tunnel Dock created,
never one it adopted.

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

### Moving environments and draining a server

An environment moves to another server with its volumes, snapshots and
domains, without downtime: see [moving an
environment](deploying.md#move-an-environment-to-another-server). To
empty a server for maintenance or before removing it, drain it
([ADR 0063](../decisions/0063-moves-without-downtime-and-server-drains.md)):

```sh
dock server drain hetzner-1 --dry-run                 # the plan: each environment and its target
dock server drain hetzner-1 --to shop=hetzner-2 --yes
dock server drain hetzner-1 --status                  # progress and the server's forwards
```

A drain moves the server's environments (or one kind's, `--kind`) one at a
time, to the server placement would pick or the one `--to` names, and
stops the server taking new ones. A failed move pauses it: `--resume`
tries again, `--resume --skip` leaves that environment where it is,
`--cancel` stops after the move that runs. Drains are for operators.

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
   [Install](#install)), then install with the printed token. Add
   `CLOUDFLARE_TUNNEL_TOKEN` (and `BASE_DOMAIN`) for a
   [tunnel](#cloudflare-tunnel) server.

   ```sh
   read -rs NODE_JOIN_TOKEN && export NODE_JOIN_TOKEN   # paste the token, Enter
   sudo --preserve-env=NODE_JOIN_TOKEN DOCK_BINARY=./dock ./deploy/install.sh --node
   ```

   Read like this, the token is in neither the shell's history nor the
   process list. The printed command has it after `sudo` instead, where
   other users of the server can see it while the install runs.

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

Nodes are updated with the control plane, over their link
([Upgrading](#upgrading)). By hand, upgrade a node like the control
plane: re-run `install.sh` with the new binary; no token needed. Upgrade
the control plane first. To re-join (after
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
| Listens on | `[registry] listen`, `127.0.0.1:5000` by default, on the host's network |
| Config | `/etc/dock/registry/config.yml` (0640), generated by `dock admin registry-config` on every run and by the daemon when the storage changes |
| Storage | `[registry] storage_dir`, `/var/lib/dock-registry`, or a bucket. Back it up only if you want to keep images; releases can be rebuilt |

Re-runs recreate the container only when its image, config or mounts
changed. `REGISTRY_NAME` only names the registry in its tokens. The daemon
serves the registry's token endpoint on the internal listener
(`/internal/registry/token`). To turn the registry off, set `[registry]
enabled = false` and `docker rm -f dock-registry`.

#### Images in object storage

The registry can keep its images in any S3-compatible bucket instead of
the control plane's disk: AWS S3, Hetzner Object Storage, Cloudflare R2,
Backblaze B2, MinIO or Garage
([ADR 0034](../decisions/0034-registry-object-storage.md)). The disk stays
small, and a control plane restored on a new server rolls back at once,
with no `--include-registry`. Pulls still go through the registry, so
nodes need nothing new. Use a bucket of its own (or one no other Dock
uses): the registry's garbage collection deletes what it doesn't know.

A bucket is a storage connection, defined once by name; Dock lists the
bucket with the keys before saving it:

```sh
printf %s "$SECRET" | dock storage add hetzner --endpoint https://fsn1.your-objectstorage.com \
    --region fsn1 --bucket dock-images --access-key-id "$KEY_ID" --secret-access-key-stdin
dock registry migrate --to s3 --storage hetzner
dock registry                                   # storage, size, how the move went
```

The console has the same on its Storage page and the Registry page's
**Move images**. A migration runs in the background: the registry serves
the images read-only while they are copied and builds and pulls wait, as
during a prune; then it switches over and the old copy is deleted. A
failure before the switch leaves everything as it was. `dock registry
migrate --to local` moves them back. Images land under `dock-registry/`
in the bucket.

On a new install, install.sh asks where the images go (this server's disk
is the default); without a terminal, set `REGISTRY_STORAGE=s3` and the
`REGISTRY_S3_*` variables:

```sh
sudo REGISTRY=1 REGISTRY_STORAGE=s3 REGISTRY_S3_ENDPOINT=https://fsn1.your-objectstorage.com \
  REGISTRY_S3_REGION=fsn1 REGISTRY_S3_BUCKET=dock-images \
  REGISTRY_S3_ACCESS_KEY_ID=... REGISTRY_S3_SECRET_ACCESS_KEY=... \
  DOCK_BINARY=./dock ./deploy/install.sh
```

Rotate keys with `dock storage set hetzner --access-key-id ... --secret-access-key-stdin`;
the daemon uses them from the next request. Hetzner's endpoints are
`https://<location>.your-objectstorage.com` with the location as the
region; R2's is `https://<account>.r2.cloudflarestorage.com` with region
`auto`.

##### Slow or degraded buckets

The registry reaches its bucket through the daemon's storage gateway
([ADR 0084](../decisions/0084-registry-storage-gateway.md)). A push lands
on the control plane's disk first, in the spool, and uploads to the
bucket in the background with retries, so a slow or partly failing
bucket doesn't fail or hang deploys. Pulls read what was just pushed from
the spool and the rest from the bucket, with deadlines.

`dock registry` (and the Registry page's Storage section) shows the
bucket's health from the gateway's recent calls, what waits to upload,
the spool and the disk:

```
bucket             degraded since 3m ago: 9 of the last 64 calls failed or took over 10 s, the slowest 31.0 s
waiting to upload  3 objects, 412 MB, the oldest since 6m ago
spool              412 MB of 20 GB
pushes             accepted; they land in the spool and upload in the background
```

A degraded or failing bucket raises a platform alert, which resolves
by itself. What to know:

- What waits in the spool is only on this server's disk until it
  uploads. If that disk dies first, those images are built again by the
  next deploy.
- The spool takes at most `[registry] spool_max_gb` and leaves 10% of the
  disk free. Past either, pushes are refused (the alert turns critical,
  and the deploy says why) until uploads free space.
- A blob the bucket keeps timing out on is stuck. The next push of an
  image with it uploads it again, which repairs it; no prune needed.
- `dock registry migrate` away from a bucket waits for the spool to
  upload first.

### Statistics

The daemon samples CPU, memory, disk and load of every server, and CPU,
memory and IO of every running service, once a minute from `/proc` and the
containers' cgroup files (no Docker API calls). Points are kept in SQLite:
one per minute for 24 hours, one per 10 minutes for 7 days, pruned
automatically. While someone watches a server or service live in the UI (or
the `/stats/live` streams), it samples every 3 seconds, and stops when they
leave. Service memory is the working set, the same number `docker stats`
shows.

A server's page in the console also shows where its disk goes
([ADR 0035](../decisions/0035-server-disk-breakdown.md)): services'
volumes, images (running, kept for rollbacks, shared layers, not Dock's),
images a prune would remove, BuildKit's cache, Dock's own data and the
rest. Docker sizes every image and volume for it, which takes a few
seconds, so it is only read when someone asks: the page shows the last
reading at once and, when it is over a minute old, reads it again in the
background and updates in place.
**Prune** frees what is reclaimable now instead of waiting for
`[retention] prune_disk_percent`; running apps, rollback images and
volumes are never touched.

```sh
dock server disk ubuntu-4gb-nbg1-1     # the same breakdown
dock server prune ubuntu-4gb-nbg1-1    # unused images, build cache above build_cache_keep_mb
```

## Upgrading

The platform console's Updates page (by the account, at the foot of its menu) shows when a newer
release is out, with its notes, and updates the platform: the control
plane first, then every node. The overview says when one is out. The CLI
does the same:

```sh
dock update             # versions, the newer releases' notes, each server's version
dock update apply       # update to the newest release and wait until every server runs it
dock update apply v0.4.0 --detach
```

Apps and routing keep running; the API is down for a few seconds while
the control plane restarts. What happens
([ADR 0028](../decisions/0028-updates-from-the-console.md)):

1. The control plane downloads the release (its binary for every server's
   architecture, the install tarball, `SHA256SUMS` and its signature) and
   refuses a release whose signature isn't Dock's and any file that
   doesn't match `SHA256SUMS`
   ([ADR 0088](../decisions/0088-signed-releases.md)).
2. It asks its updater: `dock-update.service`, which runs as root,
   started by `dock-update.path`. The updater copies the release out of
   the daemon's reach and checks it again, signature included, stops `dock`, writes a control
   plane backup to `/var/lib/dock-update/`, and runs the new release's
   `install.sh`, which swaps the binary and refreshes the units, as below.
3. If `install.sh` fails, or the new version doesn't answer on the API
   within 90 seconds, the updater puts the old binary, units and database
   (from the backup) back and starts it. Changes made in those seconds are
   lost. The console and `dock update` show the result and the end of the
   log; `journalctl -u dock-update` has all of it.
4. The control plane, now on the new version, updates each node behind
   it over its link, one at a time: the node gets the files, checks them,
   and its own updater applies them the same way (no backup: a node has
   no database). Offline nodes are skipped; `dock update apply` again
   takes them once they are back.

Everything is audited as platform events (`dock audit --platform`).

Coming from v0.14 or v0.23? Read
[Upgrading from 0.14 or 0.23](../upgrading/0.14-and-0.23-to-latest.md)
first: services that keep data in an anonymous Docker volume lose it on
their first restart after the update.

**The first update to a version with updates in the console is by hand**,
because the updater units come with it, and so is a node's first update
to protocol 4 (`dock update` lists such nodes with the command). By hand
means downloading the new release as in [Install](#install) and re-running
the script from its tarball, so the units match the binary:

```sh
sudo DOCK_BINARY=./dock-linux-amd64 ./deploy/install.sh     # also refreshes units and caddy.json
# or let it download the tarball's version:
sudo ./deploy/install.sh
```

With several servers, upgrade the control plane first, then each node the
same way (re-run `install.sh` there; it stays a node). Then install the
matching CLI on laptops and in CI with `dock self-update` ([The CLI](#the-cli));
`dock version` shows whether they are in step.

- Database migrations run on start and only go forward. Updates from the
  console back up first; by hand, run `sudo dock admin backup`
  ([Backups](#backups)).
- A deploy running during the restart is marked `failed`; the previous
  release keeps serving. Deploy again afterwards.
- Rolling back by hand (the old binary and `dock.db` from the backup the
  update took) leaves the containers of releases deployed since then
  running, unknown to the restored database: drift lists them. The
  database hands out their release numbers again, so the next deploy of
  such a service fails before changing anything, naming the container to
  remove. Run `docker rm -f` on the containers drift lists, then restart
  each service (`dock restart`).
- To upgrade Caddy, re-run with `CADDY_VERSION=x.y.z` and `CADDY_SHA256`
  (of that release's tarball for this server's architecture), then
  `sudo systemctl restart dock-caddy` (the script doesn't restart a running
  Caddy). Traffic blips briefly; `--resume` restores the routes. A
  rollback leaves an upgraded Caddy, cloudflared or BuildKit image as it
  is.
- BuildKit follows Dock: an update sets `[buildkit] image` to the image
  that release pins by digest, and the next build starts it. An image you
  set yourself stays when it comes from another repository or is a newer
  `moby/buildkit` than the release's.
- To upgrade `cloudflared`, re-run with `CLOUDFLARED_VERSION` and
  `CLOUDFLARED_SHA256` (from the release notes); the tunnel restarts.
- Upgrading an install from before the app firewall: the script moves Caddy
  to the `dock-caddy` user (one Caddy restart, `--resume` restores routes),
  takes `dock` out of the `docker` group, and enables `dock-firewall`.
  Existing app networks keep their Docker-assigned addresses; the firewall
  covers them too.
- Air-gapped: copy a release's assets to `<dir>/<version>/` on the
  control plane and set `[updates] source = "<dir>"`.

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
| `registry/` | Only with `--include-registry` (root): the image registry's storage, so rollbacks and promotions of old releases work right after a move. Otherwise their images are built or pushed again. Not needed when the images are [in a bucket](#images-in-object-storage): the restored database points the new registry at it |

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

   Leave out `--include-registry` without a registry, or with its images
   [in a bucket](#images-in-object-storage).
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
   # on the old server (paste the token, Enter):
   read -rs NODE_JOIN_TOKEN && export NODE_JOIN_TOKEN
   sudo --preserve-env=NODE_JOIN_TOKEN DOCK_BINARY=./dock ./deploy/install.sh --node
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
