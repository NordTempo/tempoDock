# dock.toml

An optional file at the root of a repo that holds the settings belonging to
the code: how to build it and how to run it
([ADR 0007](decisions/0007-config-db-plus-repo-file.md)). It is read on every
deploy. A field set here wins over the platform's stored value and shows as
locked in the UI; a field left out falls back to the platform.

Variables, secrets, domains, resource limits, volumes and replicas are not
configured here, and there are no per-environment sections. Anything that
differs between environments lives in the platform.

```toml
[build]
# Dockerfile path relative to the repo root. Without it, a root Dockerfile is
# used if present, otherwise a zero-config Railpack build.
dockerfile = "docker/Dockerfile.prod"
# Railpack build command. Only for builds without a Dockerfile.
# command = "npm run build"

[deploy]
# Overrides the image's command (run with `sh -c`).
start_command = "node server.js"
# Container port that receives HTTP traffic.
port = 3000
# Must return 2xx/3xx before the new release gets traffic.
health_check_path = "/healthz"
# How long a new release may take to pass. Go duration: "30s", "2m".
health_check_timeout = "60s"
```

| Key | Type | Rules |
|---|---|---|
| `build.dockerfile` | string | Relative path inside the repo |
| `build.command` | string | Not allowed together with `build.dockerfile` |
| `deploy.start_command` | string | |
| `deploy.port` | integer | 1-65535 |
| `deploy.health_check_path` | string | Starts with `/` |
| `deploy.health_check_timeout` | duration string | 1s to 1h |

Parsing is strict: unknown keys, wrong types and empty strings are errors
(reported with line numbers where possible), so a typo never silently does
nothing. To use the platform's value, leave the key out.
