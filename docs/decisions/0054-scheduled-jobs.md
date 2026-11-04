# 0052: Scheduled jobs (cron)

Status: accepted (asked by Saile, 2026-11-03).

## Context

Apps need recurring work: a nightly cleanup, an hourly digest, a queue
sweep. Without it in Dock people run a second, always-on "worker" with a
sleep loop, or a cron on the host, which skips scopes, the audit log,
the service's variables and the environment's network. `dock run` (ADR
0048) already runs a command in a service's active image as a job and
keeps its history; a scheduled job is the same run, started by the clock.

## Decision

| Topic | Decision |
|---|---|
| Shape | A schedule on a service, not a kind of service. A service has any number of named jobs, each a cron expression, a timezone and a command that runs from the service's active image exactly like `dock run`: its variables, limits and private network, without volumes. A "job service" would need its own builds, a deploy flow without a container that stays up, and its own place in placement, routes and the overview; a schedule needs none of that, and a worker-only image can still be a service whose start command idles |
| Expression | Five fields, ranges, lists, steps, names and the @hourly/@daily/@weekly/@monthly/@yearly macros (`internal/cron`). Timezone an IANA name, default UTC, from the host's zoneinfo (checked when a job is set; embedding tzdata would cost the binary 450 KB of a budget it is at). On a daylight saving change, a time that doesn't exist that day is skipped and one that happens twice runs once |
| Scheduler | One goroutine and one timer for every job of every service, like backups (ADR 0019): a pass lists the enabled jobs in one query, starts the due ones and sleeps until the earliest next one. Kicked when a job changes, a run of one ends, a server reconnects or a release goes active. With nothing to wait for (no enabled job) the goroutine exits, so an install without jobs has no goroutine and no timer; the next kick starts it |
| Missed runs | After the control plane was down (or the server offline), a job whose time passed runs once on recovery, for its latest missed time, never once per missed time. Each job records the time it last ran for (`last_due_at`), so a restart neither repeats nor loses a run |
| Overlap | Skip by default: a job still running when it is due again gets a `skipped` run in its history, and the next time is tried as usual. `overlap = "allow"` runs them side by side, within the service's 4 concurrent runs |
| Timeout | Per job, default 1 hour, at most 24 hours; past it the run is killed (`timed_out`) |
| No release, stopped service | A service with no active release, or stopped (`dock service stop`, ADR 0050), records the due run as `skipped` with the reason: stopped means "don't run this", its schedule included. Running a job now still works on a stopped service, as `dock run` does: it is asked for explicitly. Starting the service runs what comes due after |
| Locked environments | Changing jobs (set, remove, enable) and running one now are changes in the environment: deploy through `serviceFor`, so a locked environment refuses tokens (ADR 0044). The scheduled runs themselves keep running: the daemon starts them on a schedule someone was allowed to set, which is not an agent acting in the environment |
| Permissions | Read lists jobs and their history (commands, statuses, exit codes); deploy sets, removes and runs them now, and reads a run's output |
| History | In `service_runs` with the job (ADR 0048): the newest 20 runs per job, trimmed as each run ends, each with its exit code, duration and the last 16 KB of output. Removing a job removes its runs |
| Alerts | A scheduled or run-now run that fails or times out raises `job_failed` (warning) through the alert channels, one open alert per job, updated in place; the job's next successful run resolves it. Skips and interrupted runs don't alert |
| Audit | `job.set`, `job.remove` and `job.run` (run now) name who and what. Scheduled runs aren't audited one by one, their history is the record: a job every minute would bury the log |
| dock.toml | `[[jobs]]` with `name`, `schedule`, `timezone`, `command`, `timeout`, `overlap`. A release whose source has a dock.toml makes its jobs the service's file jobs when it goes active: added, changed, or removed when no longer in the file. A release without a file leaves them. File jobs are shown as such and changed in the file; the UI and CLI can disable them and run them now. A file job takes over a UI job of the same name |
| CLI | `dock job list`, `dock job set <name> --schedule ... [--timezone] [--timeout] [--overlap] -- <command>` (the command after `--`, as for `dock run`), `dock job remove`, `dock job enable/disable`, `dock job run <name>` (run now), `dock job history <name>` |
| UI | The service's Jobs tab: the jobs with their schedule in words and next time, a schedule editor, each job's history with its output, and Run now |
| Agents | MCP `list_jobs`, `set_job`, `remove_job`, `run_job` (waits like `run_command`), `job_history` |

## Consequences

- Recurring work needs no always-on worker or host cron, and shows up in
  the service it belongs to, with its output and failures alerted.
- A daemon that is down misses nothing it can't catch up on, but a job
  that must run at every single minute it was due can't rely on that.
- The timer is a new periodic wake-up, listed in
  [performance](../performance.md): one per due job, and nothing at all
  without jobs.
