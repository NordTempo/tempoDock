# Roadmap

Each milestone ended with something usable on a real server. M0 to M5 are
done; what they held is in the ADRs and the git history. What is left is
M6 and the backlog below.

## Status (2026-11-10, v0.41.0)

| Milestone | State |
|---|---|
| M0 Spike and skeleton | Done. Measurements in [experience](experience.md); budgets enforced by `make perf` ([performance](performance.md)). |
| M1 Deploy loop | Done: source and image deploys, health-gated cutover, rollback, domains, staged variables, `dock.toml`, browser `dock login`. |
| M2 Railway model and web UI | Done: projects, environments, services, references, private networking, users and scoped tokens, audit, the web UI with CLI parity ([0026](decisions/0026-ui-cli-parity.md)). |
| M3 Production and homelab | Done: placement and server pools ([0015](decisions/0015-server-pools-and-placement.md)), the node link, registry, builder servers and `dock promote` ([0014](decisions/0014-node-protocol.md)), control plane backup, restore and move ([0021](decisions/0021-control-plane-backup-and-move.md)). The control plane runs on the Hetzner server; the homelab has not joined it yet. |
| M4 Day-to-day comfort | Done: push-to-deploy ([0018](decisions/0018-git-push-deploy.md)) and GitHub Apps ([0031](decisions/0031-github-app.md), [0066](decisions/0066-github-apps-for-many-orgs.md)), retention and alerts ([0020](decisions/0020-retention-and-alerts.md), [0062](decisions/0062-org-alerts-and-operators.md)), clone and export ([0022](decisions/0022-clone-and-export.md)), preview environments ([0056](decisions/0056-preview-environments.md)). |
| M5 Data | Done: the Data panel ([0016](decisions/0016-database-panel.md)) and tempoBase ([0024](decisions/0024-tempobase-standalone.md)), templates and backups ([0019](decisions/0019-templates-and-backups.md), [0039](decisions/0039-backups-on-storage-connections.md)), `dock connect` ([0023](decisions/0023-connect-tunnels.md)), volume snapshots ([0047](decisions/0047-volume-snapshots.md)), one restore model ([0061](decisions/0061-one-restore-model.md)). |
| M6 Ready for others | In progress, see below. |

Shipped beyond the milestones, by area:

- **Access:** organizations and the platform operator ([0027](decisions/0027-organizations.md)), the public address ([0029](decisions/0029-public-address.md)), access protection ([0030](decisions/0030-access-protection.md)), locked environments and access requests ([0044](decisions/0044-environment-lock.md)), two-factor and passkeys ([0057](decisions/0057-two-factor-sign-in.md)), SSO with OIDC ([0058](decisions/0058-sso-oidc.md), [0065](decisions/0065-sign-in-is-the-platforms.md)), invite links ([0060](decisions/0060-invite-links-signed-out.md)), org slug renames ([0064](decisions/0064-changing-an-org-slug.md)).
- **Deploys and services:** ongoing health checks ([0043](decisions/0043-ongoing-health-checks.md)), pre-deploy commands ([0045](decisions/0045-pre-deploy-command.md)), `dock run` ([0048](decisions/0048-one-off-commands.md)), `dock apply` and Compose import ([0049](decisions/0049-apply-and-compose-import.md)), stop and start ([0050](decisions/0050-stop-start-service.md)), private registry credentials ([0051](decisions/0051-registry-credentials.md)), scheduled jobs ([0054](decisions/0054-scheduled-jobs.md)).
- **Servers:** updates from the console ([0028](decisions/0028-updates-from-the-console.md)), memory and disk breakdowns ([0032](decisions/0032-server-memory-breakdown.md), [0035](decisions/0035-server-disk-breakdown.md)), the registry on object storage ([0034](decisions/0034-registry-object-storage.md)), moving environments and draining servers without downtime ([0053](decisions/0053-moving-environments.md), [0063](decisions/0063-moves-without-downtime-and-server-drains.md)).
- **Domains:** Cloudflare tunnels, DNS and redirects ([0036](decisions/0036-cloudflare-api.md), [0068](decisions/0068-redirects-on-cloudflare-zones.md)), TXT verification ([0042](decisions/0042-domain-verification.md)).
- **Observing:** traffic statistics ([0033](decisions/0033-traffic-statistics.md), [0040](decisions/0040-traffic-counts-requests.md)), Dock Analytics with errors, vitals, replays and funnels ([0037](decisions/0037-analytics.md), [0059](decisions/0059-analytics-area-and-slimmer-service-page.md)), log history and drains ([0046](decisions/0046-log-history-and-drains.md)), uptime checks ([0055](decisions/0055-uptime-checks.md)), one Errors tab per service ([0062](decisions/0062-org-alerts-and-operators.md)).
- **Agents:** one MCP endpoint for the platform and its data ([0017](decisions/0017-mcp-endpoint.md)).
- **Site:** the landing page and the console at its own address ([0067](decisions/0067-landing-site-and-console-address.md)).

## M6: Ready for others (if we go open source)

- Done: install script and in-place updates ([0028](decisions/0028-updates-from-the-console.md)), the [user guide](guide/README.md), roles in the UI, organizations with members, invites, org variables and org alert channels, no telemetry.
- Left: a license ([open questions](open-questions.md)). Open signup, email verification and payments come with paying customers; until then orgs are invite-only. Servers stay the platform's: orgs only say production or non-production.
- Hosting customers' apps on our servers: sandboxing, quotas, metering, billing, abuse handling. Its own ADR first.

## Backlog

### Being planned now (wave 1)

- Sleeping previews: preview environments scale to zero when idle and wake on a request.
- Deploy diagnosis: a deterministic failure bundle for a failed deploy, in the UI and over MCP.
- Database insights in the Data panel.

### Planned next (wave 2)

- OTLP tracing and app metrics.
- Public status pages, with incidents and maintenance windows.
- Image vulnerability scanning that never slows deploys: it runs after the release is live, and only when the builder is idle.
- MySQL and Redis in the Data panel.

### Later

- More service templates and a catalog: MySQL/MariaDB, Valkey, MongoDB, an S3-compatible bucket, Meilisearch, NATS/RabbitMQ.
- Shareable project templates.
- Object storage buckets for apps.
- Postgres point-in-time recovery.
- Replicas on one server.
- `dock run --local` with an environment's variables.
- Agent sandboxes: an environment per branch or task, with a TTL.
- Cost per project.
- Links across errors, traffic, logs and releases.

### Dropped

- Mail catcher for previews: few apps need it, and mail providers cover it.
- Heartbeat monitors: not needed for now.
- Domain purchasing.
- HA Postgres.
- Autoscaling across servers.
- A built-in AI chat panel: the MCP endpoint covers it.
