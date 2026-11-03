# Decisions

Architecture decision records. Each one is short: context, decision,
consequences. When a decision changes, add a new record that supersedes the old
one instead of rewriting history.

Status is one of **proposed**, **accepted**, **superseded**.

| # | Decision | Status |
|---|---|---|
| [0001](0001-go-for-daemon-and-cli.md) | Go for the daemon and CLI | accepted |
| [0002](0002-single-binary-embedded-ui.md) | Single binary with embedded UI | accepted |
| [0003](0003-sqlite-for-platform-state.md) | SQLite for platform state | accepted |
| [0004](0004-caddy-as-separate-proxy.md) | Caddy as a separate proxy process | accepted |
| [0005](0005-openapi-api-with-generated-clients.md) | OpenAPI API with generated clients | accepted |
| [0006](0006-every-way-to-build.md) | Every way to build and deploy: source, git push, CI image, promotion | accepted |
| [0007](0007-config-db-plus-repo-file.md) | Config in the platform DB plus an optional repo file | accepted |
| [0008](0008-keep-doors-open.md) | Keep open source and hosting possible, cheaply | accepted |
| [0009](0009-dynamic-domains-and-tls.md) | Dynamic domains and automatic TLS | accepted |
| [0010](0010-environments-on-servers.md) | Environments are placed on servers (production vs homelab) | accepted |
| [0011](0011-stateful-services-and-databases.md) | Stateful services and databases | accepted |
| [0012](0012-name-tempo-dock.md) | Name: Tempo Dock, CLI `dock` | accepted |
| [0013](0013-homelab-ingress-cloudflare-tunnel.md) | Homelab ingress through Cloudflare Tunnel | accepted |
| [0014](0014-node-protocol.md) | Node protocol: reverse HTTP/2 over pinned TLS | accepted |
| [0015](0015-server-pools-and-placement.md) | Server pools, placement and the admin dashboard | accepted |
| [0016](0016-database-panel.md) | Database panel: native data browsing, querying and editing | accepted |
| [0017](0017-mcp-endpoint.md) | One MCP endpoint for the whole platform, data included | accepted |
| [0018](0018-git-push-deploy.md) | GitHub push-to-deploy: a webhook per service, tarballs over the API | accepted |
| [0019](0019-templates-and-backups.md) | Database templates and backups | accepted |
| [0020](0020-retention-and-alerts.md) | Retention, disk pruning and alerts | accepted |
| [0021](0021-control-plane-backup-and-move.md) | Control plane backup, restore and move | accepted |
| [0022](0022-clone-and-export.md) | Clone an environment, export a project's config | accepted |
| [0023](0023-connect-tunnels.md) | `dock connect`: tunnels to private services through the API | accepted |
| [0024](0024-tempobase-standalone.md) | tempoBase: a standalone build of the Data panel from this codebase | accepted |
| [0025](0025-platform-console.md) | The platform console is Dock's admin panel; the registry gets an API | accepted; operators by [0027](0027-organizations.md) |
| [0026](0026-ui-cli-parity.md) | Everything the CLI does, the UI does | accepted |
| [0027](0027-organizations.md) | Organizations and the platform operator | accepted |
| [0028](0028-updates-from-the-console.md) | Updating Dock from the console | accepted |
| [0029](0029-public-address.md) | Dock's public address | accepted |
| [0030](0030-access-protection.md) | Access protection for a service's domains | accepted |
| [0031](0031-github-app.md) | Dock's GitHub App | accepted |
| [0032](0032-server-memory-breakdown.md) | Where a server's memory goes | accepted |
| [0033](0033-traffic-statistics.md) | Traffic statistics for a service's domains | accepted |
| [0034](0034-registry-object-storage.md) | The registry's images in S3-compatible object storage | accepted |
| [0035](0035-server-disk-breakdown.md) | Where a server's disk goes | accepted |
| [0036](0036-cloudflare-api.md) | Cloudflare tunnels, and domains through them, from Dock | accepted |
| [0037](0037-analytics.md) | Dock Analytics: DuckDB in its own container, sites, spools | accepted |
| [0038](0038-own-tokens.md) | Tokens in organization settings, your own on your account | accepted |
| [0039](0039-backups-on-storage-connections.md) | Backups on storage connections, and moving them | accepted |
| [0040](0040-traffic-counts-requests.md) | Traffic counts requests; visitors are Analytics' | accepted |
| [0041](0041-sealed-values-bound-to-their-place.md) | Sealed values are bound to where they are stored | accepted |

## Template

```md
# NNNN: Title

Status: proposed | accepted | superseded by NNNN

## Context
What forces are at play.

## Decision
What we do.

## Consequences
What gets easier, what gets harder.
```
