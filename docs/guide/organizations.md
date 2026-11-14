# Organizations

An organization owns projects, members, tokens and settings. One person
can belong to several and switch between them in the top bar's switcher
or with `dock org use`. Servers are the operators': an organization says
whether an environment is production or not, and Dock places it.

```sh
dock org list               # yours, the current one marked
dock org use acme           # act in acme from now on
dock org create acme --name "Acme"
dock org rename --slug acme-inc   # old links and CLI links keep working
```

## Members and roles

Members are people with one role in the organization:

| Role | May |
|---|---|
| `read` | See projects, logs, metrics and variable names |
| `deploy` | Also deploy, change settings and variables (values stay write-only), run commands, resolve errors and alerts |
| `admin` | Also read variable values, manage members, tokens, locks, previews and the organization's settings |

```sh
dock org invite ana@example.com --role deploy   # prints an invite link to send
dock org invites                                # pending ones
dock org member role ana@example.com admin
dock org member remove ana@example.com
```

Reading or changing database data has its own rules, the data access
policy ([Data](data.md#who-may-do-what)). An admin can require two-factor
sign-in for every member (`dock org two-factor on`, see
[Two-factor sign-in](two-factor.md)).

## Groups, roles and grants

A member's role puts them in one of the groups every organization starts
with: Admins, Developers and Viewers (Approvers is a fourth, for people
who decide access requests). A group gives built-in roles, an area at a
level like `deploy:edit` or `logs:view` (`dock role list`), on the whole
organization, one project or one environment, optionally only for some
environment kinds (production, other, previews) or a while. Make your own
groups for job functions, like contractors on one project's staging:

```sh
dock group create Contractors
dock group grant Contractors deploy:edit -p shop -e staging
dock group grant Contractors logs:view -p shop
dock group add Contractors ola@example.com
dock grant ola@example.com data:read -p shop --for 4h   # a one-off
dock can ola@example.com                                # what adds up
```

An organization's own roles bundle built-in roles and single permissions
under one name, granted like a built-in one:

```sh
dock role create "Release manager" --include deploy:edit,variables:edit --permission errors.manage
dock group grant Leads "Release manager"
```

The same is in Settings › Members, Groups and Roles. Until the platform's
operator switches the permission model to enforce (`dock platform
access-model enforce`), roles still decide and groups only show what
would change.

## Tokens

Tokens act for CI, scripts and agents. Each has a role of its own and can
be limited to one project or environment; production data access is set
apart (`--data-production`).

```sh
dock token create ci --scope deploy --project shop
dock token create agent --scope deploy --project shop --environment staging
dock token list
dock token revoke 7
```

Locking an environment (`dock env lock production`) makes tokens' changes
there wait as access requests for an admin; people signed in are not
stopped. See [Agents](agents.md).

## Organization settings

| Setting | What |
|---|---|
| Variables | Values every project's services can reference as `${{org.KEY}}` (`dock org env set`) |
| Alerts | Where the organization's alerts go (`dock alerts channel`) |
| GitHub | Dock's GitHub App installations, for deploys on push and previews (`dock github install`) |
| Cloudflare | Connections that create DNS records and redirects (`dock cloudflare add`) |
| Registries | Credentials for private images (`dock registry login`) |
| Data access | The data access policy |
| Activity | Every change, by whom (`dock audit`) |
