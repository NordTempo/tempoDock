# Tempo Dock guide

How to install, use and automate Tempo Dock (`dock`). For the design and the
reasons behind it, see the [docs index](../../README.md#docs).

| Guide | For |
|---|---|
| [Install](install.md) | Putting `dock` on a server: requirements, `install.sh`, `config.toml`, upgrades, backups |
| [Getting started](getting-started.md) | From `dock login` to a live app with a domain |
| [Deploying](deploying.md) | Source vs image deploys, builds, health checks, cutover, rollback |
| [Variables](variables.md) | Shared vs service variables, references, staged changes, secrets |
| [Domains](domains.md) | Custom and generated domains, DNS, TLS, redirects |
| [Previews](previews.md) | An environment per pull request, cloned from staging, archived when it closes |
| [Data](data.md) | Browsing, querying and changing database data; the data access policy |
| [Databases and backups](databases.md) | Postgres and Redis templates, daily backups, off-box repository, restore into a new service and switch to it |
| [Two-factor sign-in](two-factor.md) | Authenticator apps, recovery codes, confirming sensitive actions, requiring it, getting back in |
| [Single sign-on](sso.md) | Continue with Google, GitHub or any OpenID Connect provider, set up by operators; who gets an account, invite only, turning passwords off |
| [Email](email.md) | The SMTP server Dock sends invites through, set up by operators; what is sent and what happens without it |
| [Tracing](tracing.md) | Apps' traces and metrics with no setup: OpenTelemetry SDKs send to Dock, `dock traces` and the Metrics tab read them |
| [Watching](watching.md) | Logs, errors, alerts and the Inbox, Analytics, image vulnerabilities |
| [Organizations](organizations.md) | Switching, members and roles, tokens, the organization's settings |
| [Status pages](status-pages.md) | A public page of your services' status and 90 days of uptime, from Dock's uptime checks |
| [Agents](agents.md) | Letting Claude Code, CI and other bots deploy safely |
| [API](api.md) | Auth, OpenAPI spec, event stream, errors, curl examples |

## Words

| Term | Means |
|---|---|
| Project | A group of environments, e.g. `shop` |
| Environment | `production`, `staging`, ...: a set of services on one server with a private network. Its kind, production or non-production, decides which servers it may go to |
| Server | A machine running apps. It accepts production and/or non-production environments; a project keeps each kind on one server |
| Service | One container, built from source or a prebuilt image |
| Release | An immutable image + resolved config. Every deploy, rollback, restart and config change creates one |
| Variable | Shared (environment-level) or service-level, staged before it goes live |
| Domain | A public hostname routed to a service |
| Volume | A named Docker volume mounted into a service. Makes the service stateful |
| Token | An API credential with a scope (`read`, `deploy`, `admin`), optionally limited to a project or environment |

## Commands at a glance

Every command takes `--json`. Exit codes are in [CLI conventions](../cli.md#exit-codes).
`dock env clone`, `lock`, `unlock`, `move` and `previews` are the `dock environment` commands under a shorter name.

| Area | Commands |
|---|---|
| Auth and servers | `dock login`, `dock logout`, `dock whoami`, `dock version`, `dock me name\|password\|two-factor`, `dock context add\|list\|use\|current\|remove`, `dock mcp` |
| Organizations | `dock org list\|use\|create\|rename\|delete\|join\|leave`, `dock org members\|member role\|member remove`, `dock org invite\|invites\|invites revoke`, `dock org env list\|set\|unset\|redeploy`, `dock org locks`, `dock org two-factor` |
| Where you deploy | `dock link`, `dock unlink`, `dock project list\|create\|rename\|export\|archive\|pin`, `dock export`, `dock apply FILE`, `dock import compose`, `dock environment list\|create\|clone\|lock\|unlock\|move\|previews`, `dock service list\|create\|show\|update\|start\|stop\|archive\|switch-references` |
| Deploys | `dock deploy`, `dock status`, `dock logs`, `dock releases`, `dock release list\|show\|wait\|rollback`, `dock rollback`, `dock restart`, `dock promote`, `dock git connect\|status\|deploy\|disconnect`, `dock github status\|installations\|app\|install\|assign\|unassign\|repos` |
| Run commands | `dock run`, `dock runs`, `dock runs show`, `dock job list\|set\|run\|history\|enable\|disable\|remove`, `dock connect` |
| Config | `dock env set\|unset\|list\|import`, `dock changes`, `dock apply`, `dock discard`, `dock volume list\|add`, `dock registry login\|logout\|credentials` |
| Domains and access | `dock domain add\|list\|check\|remove\|generate`, `dock access public\|password\|login`, `dock uptime list\|add\|update\|remove\|domains`, `dock cloudflare zones\|records\|redirects` |
| Databases and backups | `dock service create NAME --template postgres\|redis`, `dock backup list\|run\|log\|restore\|schedule\|status\|configure\|move`, `dock volume snapshot [list\|restore\|delete]` |
| Data | `dock db status\|setup\|settings\|tables\|schema\|rows\|query\|history\|saved\|changes\|apply\|policy\|sensitive` |
| Watch | `dock alerts`, `dock alerts resolve`, `dock alerts channel add\|list\|test\|remove`, `dock errors`, `dock errors show\|resolve`, `dock traffic`, `dock analytics sites\|stats\|errors\|vitals\|replays\|goals\|funnels\|site\|enable\|disable\|defaults\|server`, `dock logs drain add\|list\|test\|remove`, `dock audit`, `dock request list\|show\|wait` |
| Admin | `dock token list\|create\|update\|revoke`, `dock user list\|create\|disable\|enable\|delete\|reset-password`, `dock server list\|show\|add\|update\|rename\|disk\|prune\|drain\|tunnel\|revoke\|rotate-key\|remove`, `dock address set\|remove`, `dock email set\|test\|log\|off`, `dock cloudflare add\|set\|refresh\|retry\|remove`, `dock storage add\|set\|rename\|remove`, `dock registry`, `dock registry prune\|migrate`, `dock update`, `dock update apply`, `dock security operators-two-factor`, `dock sso list\|add\|set\|remove\|invite-only\|password-sign-in` |
| On the server | `dock serve`, `dock admin bootstrap\|caddy-config\|registry-config\|registry-storage\|firewall`, `dock admin backup\|restore\|update`, `dock admin reset-2fa`, `dock admin password-sign-in`, `dock node join\|set-control-plane` |
