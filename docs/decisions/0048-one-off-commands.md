# 0048: One-off commands (`dock run`)

Status: accepted (asked by Saile, 2026-11-03).

## Context

Seeding data, fixing a stuck queue or opening a Rails console meant SSH
to the server and `docker exec`. That skips token scopes, the audit log
and the environment's limits, and it needs a shell account on every
server, remote ones included. Backups already run one-off job containers
(`node.Containers.RunJob`, ADR 0019), and the pre-deploy command (ADR
0043) runs one from the new release's image. A command a person or an
agent runs is the same job, connected to its caller.

## Decision

| Topic | Decision |
|---|---|
| What runs | A job container from the service's active release: its image (by ID), resolved variables, CPU and memory limits, on the environment's private network without aliases (it isn't reachable as the service), no published port, no routes. The command runs as `/bin/sh -c <command>` like a start command, so the image needs `sh`. Not `docker exec` into the running container: a run never touches what serves traffic, and works with the service stopped or crash looping |
| Command | Up to 16 KB of UTF-8. `dock run -- a b c` sends one argument as it is (so `dock run -- 'rake a && rake b'` works) and quotes each of several for the shell, so `dock run -- psql -c 'select 1'` means what it says |
| Volumes | Never two writers on one volume by accident. A database template's volume is never mounted: it belongs to its server process, and clients reach the database over the network (its variables are set). Another stateful service's volumes mount read-only by default; `--volumes write` mounts them read-write next to the running service, for apps whose storage takes a second writer (SQLite with its locks, plain files), at most one such run per service at a time. A stateless service has none |
| Permission | Scope deploy on the environment, no new scope. Deploy can already ship any image with any start command and the service's variables, so a run grants nothing new; a fourth level in a linear read < deploy < admin ladder would not fit. Environment-limited tokens run in their environment. Reading a run's output (`GET .../runs/{id}`) also takes deploy, since output can show variables; the list (commands, exit codes) is read, like the audit log |
| Locked environments | A run is a change in the environment: it takes deploy through `serviceFor`, so the lock's one check (ADR 0044) refuses tokens in a locked environment with `environment_locked`, the UI session runs freely. A background run (`POST .../runs`, MCP `run_command`) is an ordinary operation, so its access request replays it on approval. A streamed run (`dock run`) holds a terminal, which can't wait for an approval: it is refused with the same error and files its request as the background run, so approving it runs the command without a terminal. A console needs the environment opened for a while. Scheduled jobs' own runs (ADR 0049) are the daemon's and keep running |
| Audit | `service.run` when a run ends: who, the command (first 300 characters; the run keeps all of it), and the exit code with the duration, or how it ended otherwise. The run is recorded before its container starts, so a run a crash cut short is still audited, as interrupted, on the next start |
| Limits | The service's CPU and memory limits. A timeout, default 1 hour, at most 24 hours; past it the job is killed (`timed_out`). At most 4 runs per service at once |
| Streaming | `GET .../run` upgrades to `dock-run` (HTTP/1.1, bearer tokens only, like `dock connect`) and carries `runframe` frames: stdin, close-stdin and terminal resizes up; stdout, stderr and one exit frame (the run, JSON) down. Query parameters: `command`, `timeout_seconds`, `volumes`, `tty`, `stdin`, `cols`, `rows`. The client going away cancels the run (`canceled`) and removes its container. `dock run -it` gives a terminal: raw mode, resizes, stdin |
| Background | `POST .../runs` (the UI, scripts) runs without a stream and answers with the run, running; `run` events follow it (without output, which needs deploy to read) |
| History | `service_runs`, the newest 50 per service, trimmed as runs end: command, release, status, exit code, who, when, and the last 16 KB of output. Terminal sessions keep no output: it holds what was typed, passwords included |
| Statuses | running, succeeded, failed (non-zero exit, or it couldn't start: the error says why), timed_out, canceled, interrupted (the control plane stopped) |
| Remote servers | Node protocol 16 adds `run-job-stream`: one full-duplex call per run, like Dial, with the same frames. It has no call timeout, the run's own bounds it, and canceling resets the call, which removes the job on the node. Background runs use it too (their output streams into the tail), so their length isn't bound by a control call's timeout. A node on an older protocol answers with an error saying to upgrade dock on it |
| Agents | MCP `run_command` (non-interactive): waits for the end, up to its timeout (default 300 s, at most 900), and returns the status, exit code and the output's tail (16 KB) |
| UI | The service's Run tab: a command box (with the volume choice for stateful services), its CLI equivalent, the history, and the selected run's output. Non-interactive |

## Consequences

- People and agents stop needing SSH for maintenance, and every command
  is in the audit log with its outcome.
- A run uses the active release's variables, not staged changes: what
  runs is what serves.
- `--volumes write` is a footgun on purpose: the flag names the risk, the
  default doesn't take it, and databases refuse it.
- Orphans: a job container whose control plane crashed keeps running
  until its command ends (or, for a console, its input closes). It is
  labelled as a job, so it is never mistaken for a release.
