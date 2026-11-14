# Decisions

Architecture decision records: context, decision, consequences. Records are
history and are not rewritten. A detail settled later in the same area is a
dated "Amendment" section at the end of the record. A decision made
differently later gets a new record, and the old record's status line points
at it.

Status is **proposed**, **accepted** or **superseded**. A record whose
decision changed in part stays accepted and reads **superseded in part by
NNNN**; its own status line says which part. "Amended by NNNN" in a status
line means a later record added to it without changing what it decided.

| # | Decision | Status |
|---|---|---|
| [0001](0001-go-for-daemon-and-cli.md) | Go for the daemon and CLI | accepted |
| [0002](0002-single-binary-embedded-ui.md) | Single binary with embedded UI | accepted |
| [0003](0003-sqlite-for-platform-state.md) | SQLite for platform state | accepted |
| [0004](0004-caddy-as-separate-proxy.md) | Caddy as a separate proxy process | accepted |
| [0005](0005-openapi-api-with-generated-clients.md) | OpenAPI API with generated clients | accepted |
| [0006](0006-every-way-to-build.md) | Every way to build and deploy | accepted |
| [0007](0007-config-db-plus-repo-file.md) | Config in the platform DB plus an optional repo file | accepted |
| [0008](0008-keep-doors-open.md) | Keep open source and hosting possible, cheaply | superseded in part by [0027](0027-organizations.md), [0082](0082-open-source-license.md) |
| [0009](0009-dynamic-domains-and-tls.md) | Dynamic domains and automatic TLS | superseded in part by [0042](0042-domain-verification.md) |
| [0010](0010-environments-on-servers.md) | Environments are placed on servers | superseded in part by [0015](0015-server-pools-and-placement.md) |
| [0011](0011-stateful-services-and-databases.md) | Stateful services and databases | superseded in part by [0047](0047-volume-snapshots.md) |
| [0012](0012-name-tempo-dock.md) | Name: Tempo Dock, CLI `dock` | accepted |
| [0013](0013-homelab-ingress-cloudflare-tunnel.md) | Homelab ingress through Cloudflare Tunnel | superseded in part by [0036](0036-cloudflare-api.md) |
| [0014](0014-node-protocol.md) | Node protocol | accepted |
| [0015](0015-server-pools-and-placement.md) | Server pools, placement and the admin dashboard | superseded in part by [0025](0025-platform-console.md), [0053](0053-moving-environments.md) |
| [0016](0016-database-panel.md) | Database panel | superseded in part by [0023](0023-connect-tunnels.md), [0024](0024-tempobase-standalone.md) |
| [0017](0017-mcp-endpoint.md) | MCP endpoint | accepted |
| [0018](0018-git-push-deploy.md) | GitHub push-to-deploy | superseded in part by [0031](0031-github-app.md) |
| [0019](0019-templates-and-backups.md) | Database templates and backups | superseded in part by [0039](0039-backups-on-storage-connections.md), [0061](0061-one-restore-model.md) |
| [0020](0020-retention-and-alerts.md) | Retention, disk pruning and alerts | superseded in part by [0062](0062-org-alerts-and-operators.md) |
| [0021](0021-control-plane-backup-and-move.md) | Control plane backup, restore and move | accepted |
| [0022](0022-clone-and-export.md) | Clone an environment, export a project's config | superseded in part by [0049](0049-apply-and-compose-import.md) |
| [0023](0023-connect-tunnels.md) | `dock connect` tunnels | accepted |
| [0024](0024-tempobase-standalone.md) | tempoBase, a standalone build of the Data panel | accepted |
| [0025](0025-platform-console.md) | The platform console is Dock's admin panel | superseded in part by [0027](0027-organizations.md), [0038](0038-own-tokens.md), [0062](0062-org-alerts-and-operators.md) |
| [0026](0026-ui-cli-parity.md) | Everything the CLI does, the UI does | accepted |
| [0027](0027-organizations.md) | Organizations and the platform operator | superseded in part by [0062](0062-org-alerts-and-operators.md), [0064](0064-changing-an-org-slug.md), [0065](0065-sign-in-is-the-platforms.md), [0080](0080-permissions-roles-groups-and-agents.md) |
| [0028](0028-updates-from-the-console.md) | Updating Dock from the console | accepted |
| [0029](0029-public-address.md) | Dock's public address | accepted |
| [0030](0030-access-protection.md) | Access protection for a service's domains | accepted |
| [0031](0031-github-app.md) | Dock's GitHub App | superseded in part by [0062](0062-org-alerts-and-operators.md), [0066](0066-github-apps-for-many-orgs.md) |
| [0032](0032-server-memory-breakdown.md) | Where a server's memory goes | accepted |
| [0033](0033-traffic-statistics.md) | Traffic statistics for a service's domains | superseded in part by [0040](0040-traffic-counts-requests.md) |
| [0034](0034-registry-object-storage.md) | The registry's images in S3-compatible object storage | accepted |
| [0035](0035-server-disk-breakdown.md) | Where a server's disk goes | accepted |
| [0036](0036-cloudflare-api.md) | Cloudflare tunnels, and domains through them, from Dock | superseded in part by [0062](0062-org-alerts-and-operators.md), [0068](0068-redirects-on-cloudflare-zones.md) |
| [0037](0037-analytics.md) | Dock Analytics | superseded in part by [0059](0059-analytics-area-and-slimmer-service-page.md) |
| [0038](0038-own-tokens.md) | Tokens in organization settings, your own on your account | accepted |
| [0039](0039-backups-on-storage-connections.md) | Backups on storage connections, and moving them | accepted |
| [0040](0040-traffic-counts-requests.md) | Traffic counts requests; visitors are Analytics' | accepted |
| [0041](0041-sealed-values-bound-to-their-place.md) | Sealed values are bound to where they are stored | accepted |
| [0042](0042-domain-verification.md) | Custom domains are verified with a TXT record | accepted |
| [0043](0043-ongoing-health-checks.md) | Ongoing health checks | accepted |
| [0044](0044-environment-lock.md) | Locked environments, and agents asking for access | accepted |
| [0045](0045-pre-deploy-command.md) | Pre-deploy command | accepted |
| [0046](0046-log-history-and-drains.md) | Log history past the container, and log drains | superseded in part by [0062](0062-org-alerts-and-operators.md) |
| [0047](0047-volume-snapshots.md) | Volume snapshots | superseded in part by [0061](0061-one-restore-model.md), [0063](0063-moves-without-downtime-and-server-drains.md) |
| [0048](0048-one-off-commands.md) | One-off commands (`dock run`) | accepted |
| [0049](0049-apply-and-compose-import.md) | Config as code with `dock apply FILE`, and importing Compose files | accepted |
| [0050](0050-stop-start-service.md) | Stopping and starting a service | accepted |
| [0051](0051-registry-credentials.md) | Pull credentials for private registries | accepted |
| [0052](0052-anonymous-volumes-of-served-containers.md) | Keeping the anonymous volumes of containers that served | accepted |
| [0053](0053-moving-environments.md) | Moving an environment to another server | superseded in part by [0063](0063-moves-without-downtime-and-server-drains.md) |
| [0054](0054-scheduled-jobs.md) | Scheduled jobs (cron) | accepted |
| [0055](0055-uptime-checks.md) | Uptime checks from another server | accepted; amended by [0070](0070-sleeping-environments.md) (paused while asleep) and [0073](0073-public-status-pages.md) (day rows for 90 days) |
| [0056](0056-preview-environments.md) | Preview environments per pull request | accepted; amended by [0070](0070-sleeping-environments.md) (previews sleep) |
| [0057](0057-two-factor-sign-in.md) | Two-factor sign-in, step-ups and break-glass | accepted |
| [0058](0058-sso-oidc.md) | Single sign-on with OpenID Connect | superseded in part by [0065](0065-sign-in-is-the-platforms.md) |
| [0059](0059-analytics-area-and-slimmer-service-page.md) | An Analytics area, a slimmer service page, project settings and uptime checks that set themselves up | accepted |
| [0060](0060-invite-links-signed-out.md) | Invite links work signed out, with a sign-up step | accepted |
| [0061](0061-one-restore-model.md) | One restore model: restore points, a restored service, and switching to it | accepted |
| [0062](0062-org-alerts-and-operators.md) | Alerts belong to the org, operators run the platform | superseded in part by [0065](0065-sign-in-is-the-platforms.md), [0066](0066-github-apps-for-many-orgs.md), [0080](0080-permissions-roles-groups-and-agents.md) |
| [0063](0063-moves-without-downtime-and-server-drains.md) | Moves without downtime, and draining a server | accepted |
| [0064](0064-changing-an-org-slug.md) | Changing an organization's slug | accepted |
| [0065](0065-sign-in-is-the-platforms.md) | Sign-in is the platform's | accepted; amended by [0081](0081-named-identity-providers.md) (named providers) |
| [0066](0066-github-apps-for-many-orgs.md) | GitHub Apps for many orgs | accepted |
| [0067](0067-landing-site-and-console-address.md) | The landing site, and the console's address | accepted |
| [0068](0068-redirects-on-cloudflare-zones.md) | Redirects on Cloudflare zones | accepted |
| [0069](0069-database-insights.md) | Database insights | accepted |
| [0070](0070-sleeping-environments.md) | Sleeping environments (scale to zero) | accepted |
| [0071](0071-deploy-diagnosis.md) | Deploy diagnosis | accepted |
| [0072](0072-mysql-and-redis-in-the-data-panel.md) | MySQL/MariaDB and Redis/Valkey in the Data panel | accepted |
| [0073](0073-public-status-pages.md) | Public status pages | accepted; phases 1 to 5 |
| [0074](0074-image-vulnerability-scanning.md) | Image vulnerability scanning | accepted |
| [0075](0075-tracing-over-otlp.md) | Tracing over OTLP | accepted; phases 1 and 2 |
| [0076](0076-service-page-in-nine-tabs.md) | The service page in nine tabs, one save model, a failure said once | accepted; Requests built |
| [0077](0077-previews-belong-to-services.md) | Previews belong to services | accepted |
| [0078](0078-deleting-archived-projects.md) | Renaming and deleting archived projects | accepted |
| [0079](0079-dock-sends-its-own-email.md) | Dock sends its own email: the platform's SMTP server, an outbox, invites first | accepted; invites built |
| [0080](0080-permissions-roles-groups-and-agents.md) | Permissions, roles, groups and agents: area roles, groups as job functions, personal agents, protected environments, the platform apart | accepted; phases 1 to 3 built |
| [0081](0081-named-identity-providers.md) | Named identity providers: Microsoft, GitLab, Okta, Auth0, Keycloak and Authentik by a tenant, domain or realm | accepted |
| [0082](0082-open-source-license.md) | Open source under the AGPL, with a trademark policy and a CLA; CI safe for a public repo; going public is v1.0.0 | accepted; not yet public |

## Template

```md
# NNNN: Title

Status: proposed | accepted | superseded by [NNNN](NNNN-slug.md)

Later changes go on the same line: `accepted; superseded in part by
[NNNN](NNNN-slug.md) (which part)`.

## Context
What forces are at play.

## Decision
What we do.

## Consequences
What gets easier, what gets harder.
```
