# tempoBase guide

tempoBase is Tempo Dock's database panel as a standalone, self-hosted
server: browse, query and safely change Postgres data, with users, a data
access policy, an audit log and an MCP endpoint for agents. One small
binary or image, one data directory, no Docker access needed. It is built
from this repository and released with Dock
([ADR 0024](../decisions/0024-tempobase-standalone.md)).

| Page | For |
|---|---|
| [Install](install.md) | The binary, the Docker image or systemd; configuration |
| [First run](first-run.md) | Creating the first admin, signing in, a lost admin |
| [Connections](connections.md) | Adding a Postgres database, TLS, setting up access, browsing and changing data |
| [People and the policy](people.md) | Viewers, editors and admins, invites, who may do what, sensitive columns, audit |
| [Agents and MCP](agents.md) | Tokens, connecting Claude Code and other agents, the tools |
| [Backups](backups.md) | The data directory and the key file, backing up and restoring |

## Words

| Term | Means |
|---|---|
| Connection | A saved Postgres database: its URL (stored encrypted), a name used in URLs, and a production flag |
| Viewer, editor, admin | A person's role. Viewers look, editors change data, admins run everything |
| Policy | Rules that decide each role's and token's level on production and non-production connections |
| Changeset | Row inserts, updates and deletes committed in one transaction, after a check that nobody changed the rows meanwhile |
| Token | An API credential (`tbt_...`) a person creates for an agent or script |
