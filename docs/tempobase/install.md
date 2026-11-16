# Install

tempoBase is one static binary with its UI built in. It keeps its state in
one data directory: `tempobase.db` (SQLite) and `secrets.key` (the key that
encrypts saved connection URLs). It needs no Docker and no database of its
own, and idles at about 10 MB of memory with no background work.

| What | Needed |
|---|---|
| OS | Linux `amd64` or `arm64` (a macOS `arm64` build exists for trying it out) |
| Network | Outbound to your databases. Inbound on port 7171, behind TLS for anything public |
| Databases | Postgres 14 or newer, and a user that can create roles (`CREATEROLE`) to set up access |

Every release carries the binaries (`tempobase-linux-amd64`,
`tempobase-linux-arm64`, `tempobase-darwin-arm64`) and `SHA256SUMS` next
to Dock's.

## Binary

```sh
v=v0.2.0
for f in tempobase-linux-amd64 SHA256SUMS; do
  curl -fsSLO "https://github.com/NordTempo/tempoDock/releases/download/$v/$f"
done
grep ' tempobase-linux-amd64$' SHA256SUMS | sha256sum -c
sudo install -m 0755 tempobase-linux-amd64 /usr/local/bin/tempobase
TEMPOBASE_DATA_DIR=$HOME/.tempobase tempobase serve
```

It listens on `127.0.0.1:7171` and keeps its data in `/var/lib/tempobase`
unless told otherwise (see Configuration).

## systemd

With the binary in `/usr/local/bin/tempobase`, install
[deploy/tempobase/tempobase.service](../../deploy/tempobase/tempobase.service)
as `/etc/systemd/system/tempobase.service`:

```sh
sudo systemctl enable --now tempobase
journalctl -u tempobase    # the setup link
```

The unit runs tempoBase as a throwaway user with no other access, keeps its
data in `/var/lib/tempobase` and sets `GOMEMLIMIT=48MiB`. Put settings in
`Environment=` lines, or in a TOML file that its commented
`TEMPOBASE_CONFIG` line points at.

## Configuration

An optional TOML file (`--config FILE`, else `$TEMPOBASE_CONFIG`), then
environment variables, which win. Everything has a default. An example
with comments: [deploy/tempobase/tempobase.toml](../../deploy/tempobase/tempobase.toml).

| TOML | Environment | Default | |
|---|---|---|---|
| `listen` | `TEMPOBASE_LISTEN` | `127.0.0.1:7171` | HTTP address |
| `data_dir` | `TEMPOBASE_DATA_DIR` | `/var/lib/tempobase` | `tempobase.db` and, by default, the key file |
| `secrets_key_file` | `TEMPOBASE_SECRETS_KEY_FILE` | `<data_dir>/secrets.key` | The encryption key, created on first start. Point it at a mounted secret to keep it out of volume backups |
| `base_url` | `TEMPOBASE_BASE_URL` | from each request | The address people open: used in setup, invite and reset links and the MCP snippet, and to decide `Secure` cookies |
| `trusted_proxies` | `TEMPOBASE_TRUSTED_PROXIES` (comma separated) | none | CIDRs whose `X-Forwarded-For` and `X-Forwarded-Proto` are believed |
| `audit_retention_days` | `TEMPOBASE_AUDIT_RETENTION_DAYS` | `90` | Older audit events are deleted |

## Behind TLS

tempoBase speaks plain HTTP. For anything beyond your own machine, put a
TLS proxy in front, set `base_url` to the public `https://` address and
`trusted_proxies` to the proxy's address. With Caddy:

```
tempobase.example.com {
	reverse_proxy 127.0.0.1:7171
}
```

A Cloudflare or Tailscale tunnel works the same way. Sessions are cookies
marked `Secure` once `base_url` is `https://`.

## Upgrades

Replace the binary and restart (`systemctl restart tempobase`). The database is migrated on start.
Back up first ([Backups](backups.md)); there is no downgrade.

## Commands

| Command | Does |
|---|---|
| `tempobase serve [--config FILE]` | The server |
| `tempobase admin create-user --email E --name N --role R` | Creates a user; password from stdin, or a set-password link with `--link` |
| `tempobase admin reset-link --email E` | Prints a one-time password reset link |
| `tempobase admin backup FILE` | A consistent copy of the database, safe while it runs |
| `tempobase healthcheck [--url URL]` | Exits 0 when the server answers |
| `tempobase openapi -o FILE` | Writes the OpenAPI spec |
| `tempobase version` | Prints the version |

`admin` commands work on the data directory directly, so run them on the
server with the same configuration.
