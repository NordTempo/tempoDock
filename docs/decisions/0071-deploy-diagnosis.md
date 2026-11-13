# 0071: Deploy diagnosis

Status: accepted (decided by the agent; Saile picked the release page's
layout, mock A with a "ruled out" line, 2026-11-10).
Builds on [0017](0017-mcp-endpoint.md), [0020](0020-retention-and-alerts.md),
[0043](0043-ongoing-health-checks.md), [0045](0045-pre-deploy-command.md) and
[0046](0046-log-history-and-drains.md).

## Context

A failed release left its evidence in five places: the release's error,
the build log (with the pre-deploy command's output), the container's
output, the service status (exit code, OOM kill) and the Errors tab. An
agent made five calls and still had to work out the cause; the release
page guessed two causes from the error text in the browser, with a regex
that no longer matched what the node says. Much of the evidence is gone
soon: the container is removed seconds after the failure, with its exit
state, and the health check's answer only ever lived in the deploy.

## Decision

| Topic | Decision |
|---|---|
| What | When a release fails, Dock keeps one bundle that explains it, and names the likely cause with a small set of deterministic rules. No LLM |
| When | Captured in the deploy's failure path (`deployment.fail`), before the container is removed: one inspect and a 60-line log tail, within 5 s, then the rules and one write. Not computed later: the container, its exit and the probe's answer would be gone |
| Which failures | Every failed release: build, image pull, pre-deploy command, start, health check (the gate refusing cutover), routes, the server going offline. Not interrupted deploys (the daemon's own restart), nor stopped or sleeping services, which aren't failures. Runs and jobs keep their own record (exit code and output, ADR 0048) |
| The bundle | `apitypes.Diagnosis`: the cause (rule, one-line summary, what matched, the fix as a setting and a CLI command), every rule that applies with its verdict (`checked`: what was ruled out and why), the failure (step, error, exit code, OOM kill), the health check and its answer, what changed since the last release that served (image, variable names, settings), the pre-deploy command's run, the last lines of the container (matched ones marked) and the build, error issues and 1-minute resource samples around it. Shapes only gain fields; `trace` is kept as the name for the trace of the failing request once Dock records traces |
| Rules | Ordered, the first match is the cause: `server_offline`, `push_failed`, `build_failed`, `pull_failed`, `pre_deploy_failed`, `oom_killed`, `missing_binary`, `wrong_arch`, `missing_variable`, `port_mismatch`, `loopback_only`, `health_path`, `not_listening`, `exited`. A rule that reads the app's output needs a fact from the platform too (an exit code, the health check's answer, a variable the release doesn't set), never a log line alone. No match is an answer: "No rule matched", with the evidence. They run in core (`diagnose.go`), so the UI, CLI and MCP say the same |
| The health check's answer | Read back from its error with `node.ParseHealthFailure`. The messages are the node's own (`node.ExitedError`, the Docker runtime's probes) and are tested together with the parser. No node protocol change: the error already crosses the link as text, and inspect and logs exist since protocol 1 |
| Storage | `diagnoses` (migration 0082): the bundle as JSON, at most 48 KB (older log lines go first), the rule and summary beside it. A service keeps its newest 20, for `[retention] build_log_days` (30) |
| Secrets | Log lines are stored as written and masked when read, with the release's values, exactly as logs are (`logRedactor`): read and deploy tokens see `*****`, admins what logs show them. Variables are names only; values are compared in the sealed configs and never leave them. The summary is made from the rule's facts (a port, a limit, a variable name), never from log text |
| Permissions | Scope read: everything in a bundle is readable with read already |
| API | `GET .../services/{s}/diagnoses` (summaries, `?release=N`) and `.../diagnoses/{id}`; a failed release carries `diagnosis: {id, kind, rule, summary}` (`ListReleases` loads them in one query) |
| CLI | `dock diagnose [-r N] [--json]`; a failed `dock deploy` or `release wait` says the likely cause and points at it; `release show` has a "likely cause" field |
| MCP | `diagnose` (read-only): a summary line with the cause and the fix, then the bundle. Deploy-like tools' summaries name the likely cause and say to call it |
| UI | The release page: the cause with a ruled edge, what matched and what was ruled out, the fix as the primary button (port and health check path edited in place, other settings and variables through their editors), then the evidence as rows, the matched ones marked. Replaces the page's regex guess |

## Running services (phase 2)

| Topic | Decision |
|---|---|
| When | A crash loop is diagnosed when its alert is about to open: the capture runs in its own goroutine (it reads the container over the node link, within 5 s) and the alert opens after it, so the first notification already says the cause; restarts meanwhile wait for it. An unhealthy service is diagnosed when its alert turns critical (a restart didn't help), in the health check's own goroutine, before the next restart. Once per alert: the alert engine keeps the cause while the alert is open (`alertEngine.causes`, bounded by services) and drops it when the alert resolves, so the next occurrence is diagnosed again |
| What | The same bundle, kind `crash_loop` or `unhealthy`, kept with the active release (migration 0083 widens `kind`). The exit code and OOM kill come from the service status, since Docker resets them on each restart; an unhealthy service's probe output (dock-probe's words, which `node.ParseHealthFailure` knows) feeds the health rules. `failure.duration_ms` is how long it had been live. Rules that read a deploy's steps skip it |
| Alerts | Crash loop, unhealthy and failed deploy alerts say "Likely cause: ..." (the rule's sentence, never log text), their command is `dock diagnose`, their target the release, so the UI's "See why" opens its page. A failed deploy's alert reads the cause from the release's diagnosis, and after a restart from the reconcile query |
| UI | A release's page shows its diagnosis when it failed, and when it crash looped or turned unhealthy while live ("Crash looped 4m ago, while live"). The service's headline says the likely cause while it crash loops or is unhealthy, and the failed-deploy strip after the failure. The project overview carries each service's newest diagnosis with its release (one more query, one row per service); a crash loop or unhealthy alert refetches it |
| Not republished | Capturing a running service's diagnosis publishes no release event: the alert engine reads an active release's event as a release going live and would reset the crash count |

## Runs, jobs and the release list (phase 3)

- A failed one-off command or job run gets a cause line: the same rules
  over its exit code and output's tail, when it ends, kept in
  `service_runs.cause` (migration 0084) so lists read no output. Only
  rules that can apply to a command (out of memory, program not in the
  image, wrong architecture, missing variable); that it exited is not a
  cause. A terminal session keeps no output, so it gets none. The run's
  page, `dock runs show`, `dock job history`, MCP `run_command` and a
  job's `job_failed` alert say it.
- The release list shows a failed release's likely cause instead of its
  error when a rule matched (the error stays in its title).

## Not now

- Database signals (connections, locks) as evidence, from the Data
  panel's insights.

## Consequences

- An agent gets one answer instead of five calls, and a person sees the
  cause where the release page used to guess.
- Nothing runs while nothing fails: no timer, goroutine or cache. A
  failure costs two node calls, a few indexed reads, the rules
  (`BenchmarkDiagnoseRules`, about 20 µs) and one write; the hourly
  retention pass one indexed delete.
- A rule that misfires in practice is removed, not tuned until it
  guesses. Changing the node's health check wording means changing the
  parser in the same package, which its test enforces.
- A release that failed before this has no diagnosis; its page reads as
  before, without a cause.
