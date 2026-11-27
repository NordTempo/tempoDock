# dock.toml

An optional file at the root of a repo that holds the settings belonging to
the code: how to build it and how to run it
([ADR 0007](decisions/0007-config-db-plus-repo-file.md)). It is read on every
source deploy (`dock deploy` or a git push); an image deploy has none. A field set here wins over the platform's stored value and shows as
locked in the UI; a field left out falls back to the platform.

Variables, secrets, domains, resource limits and volumes are not
configured here, and there are no per-environment sections. Anything that
differs between environments lives in the platform. A whole project's
config, all environments included, is a [project file](project-file.md)
instead.

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
# The ongoing check while the release runs (ADR 0043): it probes what the
# deploy's check does, and a service that keeps failing is restarted.
# Time between probes, or "off". Default 30s.
health_check_interval = "30s"
# How long one probe may take. Default 5s.
health_check_probe_timeout = "5s"
# Failed probes in a row before the service counts as unhealthy. Default 3.
health_check_retries = 3
# Failures this soon after a (re)start don't count. Default: health_check_timeout.
health_check_start_period = "60s"
# Runs (with `sh -c`) in a one-off container of the new image before it gets
# traffic, e.g. database migrations. A non-zero exit fails the release.
pre_deploy_command = "npx prisma migrate deploy"
# How long the pre-deploy command may run (default 10m).
pre_deploy_timeout = "15m"
# Snapshot the environment's stateful services before the pre-deploy command.
pre_deploy_snapshot = true
# Dock's security headers on responses that lack them (on for new services).
security_headers = true

# Scheduled jobs (ADR 0054), one [[jobs]] table each: a command from the
# release's image on a cron schedule. See "Jobs" below.
[[jobs]]
name = "nightly-cleanup"
schedule = "0 3 * * *"
timezone = "Europe/Oslo"
command = "bin/rails cleanup"
timeout = "30m"
overlap = "skip"
```

| Key | Type | Rules |
|---|---|---|
| `build.dockerfile` | string | Relative path inside the repo |
| `build.command` | string | Not allowed together with `build.dockerfile` |
| `deploy.start_command` | string | |
| `deploy.port` | integer | 1-65535 |
| `deploy.health_check_path` | string | Starts with `/` |
| `deploy.health_check_timeout` | duration string | 1s to 1h |
| `deploy.health_check_interval` | duration string or `"off"` | 1s to 1h |
| `deploy.health_check_probe_timeout` | duration string | 1s to 5m |
| `deploy.health_check_retries` | integer | 1-100 |
| `deploy.health_check_start_period` | duration string | 1s to 1h |
| `deploy.pre_deploy_command` | string | See [pre-deploy command](guide/deploying.md#pre-deploy-command) |
| `deploy.pre_deploy_timeout` | duration string | 1s to 1h |
| `deploy.pre_deploy_snapshot` | boolean | See [pre-deploy command](guide/deploying.md#pre-deploy-command); `false` turns the service's setting off |
| `deploy.security_headers` | boolean | See [security headers](guide/domains.md#security-headers); `false` turns them off for releases from this source |
| `jobs[].name` | string | Required. Lower-case letters, digits and dashes, at most 40; unique in the file |
| `jobs[].schedule` | string | Required. Five cron fields, or `@hourly`, `@daily`, `@weekly`, `@monthly`, `@yearly` |
| `jobs[].timezone` | string | IANA name, e.g. `"Europe/Oslo"`. Default UTC |
| `jobs[].command` | string | Required. Run with `sh -c` |
| `jobs[].timeout` | duration string | 1s to 24h. Default 1h |
| `jobs[].overlap` | `"skip"` or `"allow"` | Default `skip` |

Parsing is strict: unknown keys, wrong types and empty strings are errors
(reported with line numbers where possible), so a typo never silently does
nothing. To use the platform's value, leave the key out.

## Jobs

`[[jobs]]` are the service's scheduled jobs
([ADR 0054](decisions/0054-scheduled-jobs.md)), at most 50. Each runs its
command like [`dock run`](cli.md#dock-run): in a new container from the
service's active image, with its variables and private network, without
volumes. The schedule is five fields (minute, hour, day of month, month,
day of week) with ranges, lists, steps and names, read in `timezone`. A run
that goes past `timeout` is stopped (`timed_out`). With `overlap = "skip"`
a job still running when it comes due again records that run as
`skipped`; `"allow"` runs them side by side.

The file's jobs take effect when a release from it goes active: jobs are
added or changed to match, and removed once the file drops them. A file
with no `[[jobs]]` removes them all; a release without a dock.toml (an
image deploy) leaves them. A file job replaces a job of the same name set
in the UI or CLI. File jobs can't be changed or removed with `dock job
set` or `remove`, only in the file, but `dock job disable`, `enable` and
`run` work on them, and a job turned off stays off across deploys.
