# Templates

A template creates an app in one step: its services, volumes, generated
secrets and a domain. Umami comes with its Postgres, Paperless-ngx with
Postgres and Redis, Twenty with a worker. Postgres and Redis are templates
too. Design: [ADR 0083](../decisions/0083-app-templates.md).

## Use one

```sh
dock template list                      # the catalog
dock template show umami                # its services and inputs
dock template use umami -e production   # create and deploy
```

In the UI: **New service › Template**, or **New project › From template**
for a project of its own. Pick the app, fill in its inputs, Create.

- The main service gets the name you give (`--name`, default the
  template's), the others `NAME-<part>`: `umami` and `umami-db`. Use a
  template twice in one environment with another name.
- A template with a domain gets the one you pass (`--domain`) or a
  generated one. A server without a base domain needs `--domain`.
- Inputs are `--set KEY=VALUE`, one per input. `show` lists them, with
  their defaults; secrets are sealed like any variable.
- It waits for the first deploys (`-d` doesn't). Other staged changes in
  the environment stay staged.

## Afterwards

The services are ordinary: change settings, variables and volumes, roll
back, archive, as with any service. Generated secrets are variables;
admins reveal them on the Variables tab. The service page says which
template made it (`dock service show`), and a newer template never changes
it. Each template's notes say what to do first, such as creating the
admin account.

## Edit before creating

```sh
dock template use n8n --file -e production > n8n.toml
# edit it, then:
dock apply n8n.toml --plan
```

`--file` prints the project file a template expands to. Its generated
secrets are names without values there: set them with `dock env set`
before deploying, or apply the file and set them on the staged services.

## Your organization's templates

Admins save an environment's services as the organization's own template,
which its members find first in the picker and use like Dock's:

```sh
dock template save shop-stack -p shop -e production --services api,worker,db
dock template show umami --toml > stats.toml   # start from one of Dock's
dock template save --file stats.toml           # after changing its name
dock template remove shop-stack                # services made from it stay
```

In the UI: the project's Settings › Config file, **Save as template…**. Settings, volumes,
jobs and the references between the services are kept, a service
deploys after those it references, parts named after the main service
(`shop-db`) lose its name (`db`), and database templates stay templates. Variables that aren't references become inputs
the form asks for: a template never carries a value someone set. Services
that build from source can't be in one; deploy them from an image first.

## For agents

MCP `list_templates` (with `name`: its inputs) and `use_template` (with
`plan` to only see what it creates).

## What isn't a template

Apps that need the Docker socket, host networking, UDP or other TCP ports
from outside, host folders or GPUs don't run on Dock, nor do pairs of
services sharing one volume. For anything else, `dock import compose`
converts its compose file.
