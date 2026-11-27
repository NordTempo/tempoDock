# 0111: Shells in running containers (`dock exec`)

Status: accepted (asked by Saile, 2026-11-27). Builds on
[0048](0048-one-off-commands.md) (its framing and streams) and
[0080](0080-permissions-roles-groups-and-agents.md) (a new permission).

## Context

`dock run` (ADR 0048) starts a new container from the active release's
image, on purpose: it never touches what serves traffic and works with
the service down. Some questions are only answered inside the serving
container itself: what is in its `/tmp`, what its process sees, why its
memory grows, what a file it wrote says. That meant SSH to the server
and `docker exec`, without token scopes, the audit log or remote
servers, which is what ADR 0048 set out to end for one-off commands.

## Decision

| Topic | Decision |
|---|---|
| Command | `dock exec [service] [-i] [-t] [-- <command...>]`. Without a command, a shell: `bash` where the image has it, `sh` otherwise, with stdin and a terminal. A command after `--` runs as it is, without a shell (as `docker exec` does: `-- sh -c '...'` for shell syntax), and gets neither stdin nor a terminal unless `-i` or `-t` asks. The exit code is the command's |
| Where | The container of the service's active release (`releases.container_id`): a service runs one container per release, so there is nothing to pick. Not a sidecar (Dock's own, ADR 0093), not the old release's container during a deploy's overlap. If services ever run replicas, a `--replica` flag picks one. A service without a running container (never deployed, stopped, restarting) is a `conflict` naming `dock run`, which works without it. An asleep environment is woken first, as for `dock run`, so a shell asks for what is asleep like any other use; one whose container isn't running yet when the session starts is that conflict, and opening it again finds it awake. A deploy between opening a session and starting it is a conflict too: the session would start in the container on its way out |
| Permission | `shell`, a new permission (ADR 0080) at the environment level, marked as reaching secrets (the process's environment holds them). It is a fourth level of the Deploy area role, above edit, so Developers (`deploy:edit`) don't hold it and Admins (Full access) do; today's check is scope admin on the environment. A shell is the serving container: it can change what users get without a release, so it sits above deploy. Its output is not masked: whoever holds it reads the variables with `printenv` anyway, and masking would break the terminal |
| Locked environments | The lock's one check (ADR 0044) through `serviceFor` refuses tokens with `environment_locked`, `dock login`'s included. A UI session goes through, except in an environment locked for people too, where only those who may approve there do. Unlike `dock run`, no access request is filed: a session can't wait for an approval, and approving it would have nothing to run. The answer is an approver opening the environment for a while. A session open when the environment gets locked ends: a lock change asks what is held open in that environment again (`accessChange.envID`), and the session's check reads the environment's lock as it is then; it also runs once right after the session's watch starts, for a lock written in between. A window opened for a while ends with no write, so a session in one has a timer at the window's end that checks again; every check passes on the environment it read, so a window opened, extended or shortened while the session runs moves the timer. An attached `dock run` is not ended that way: its one command was allowed when it started, and cutting a migration short does more harm |
| Public demo | Off (`demoOff`): the demo is view-only and this is a GET that changes everything |
| Audit | `service.exec` when the session opens, before it runs: who, the container (short ID) and release, and the command (`shell` for the default) with the release's secret values masked before it is cut to 300 characters, as `service.run` now does too: a value typed into a command never reaches an Activity reader who can't reveal it. Nothing of what was typed or shown is kept, as for `dock run -it`. No history table: the audit log is the record |
| Revocation | The session is registered with the access watch (`c.watched`): a revoked token, a disabled account, a removed member or a changed role ends it, with code `forbidden`. It is one of its actor's live streams (`maxActorStreams`), so a loop of sessions is bounded per actor, and holds the environment awake. A token that expires while a session is open ends it then: the session's deadline is the token's expiry when that comes before 24 hours |
| Ending | The session ends when the command exits, the caller goes away, access changes, the control plane stops or it has been open 24 hours. Docker keeps an exec running after its caller left, so the runtime stops what the session started: every exec carries `DOCK_EXEC_SESSION=<random>` in its environment, which its children inherit, and at the end a second exec (as root, with `sh`, `tr` and `grep`) sends SIGHUP to each process that has it, then SIGKILL to those left after 2 seconds. Best effort: an image without those tools keeps them, as `docker exec` would, and so does a process that rewrites its environment or switches to a user the stop can't reach. A session whose caller stopped reading isn't held by its output: when it ends early, core cuts the caller's connection (`JobStream.Cut`, a write deadline set to now), which frees a write blocked on it, over the node link too, and the runtime waits at most 5 seconds for one in progress. An attached `dock run` gets the same |
| Terminal | Raw mode, the terminal's size at the start and on every resize, stdin until it ends. Docker ends a terminal session's output when its input is closed, so the end of input is sent as Ctrl-D instead |
| Streaming | `GET .../services/{service}/exec` upgrades to `dock-exec` (HTTP/1.1, bearer tokens only, like `dock run`) and carries the same `runframe` frames. Query parameters: `cmd` (repeated, the command and its arguments), `tty`, `stdin`, `cols`, `rows`. The Exit frame is `ExecEnd`: the exit code, or the error and its code, and the container and release |
| Remote servers | Node protocol 39 adds `exec`: one full-duplex call per session, framed like `run-job-stream` (one shared implementation). Canceling resets the call, which ends the session on the node and stops its processes there. An older node's error says to upgrade dock on it |
| Agents | No MCP tool. An agent's commands go through `run_command`, which leaves the serving container alone; a shell is for a person at a terminal |
| Browsers | A browser can't send a bearer token with a WebSocket, and browsers don't apply CORS to WebSockets, so a socket authenticated by the session cookie could be opened from any page (cross-site WebSocket hijacking). The UI asks for a ticket instead: `POST .../exec-tickets`, an ordinary call behind the CSRF guard, checks what the session will (admin, the lock, a running container; the demo refuses it as any POST) and returns a random ticket, good once and for 30 seconds, for that person and service. `GET .../exec/ws` presents it as its second subprotocol (after `dock-exec`), not in the URL, so it never lands in an access log, and must come from the console's own origin (`Origin` equal to the host); the session then opens as that person, checked again and audited. Tickets live in memory, at most 256 waiting, expired ones dropped when the next is made, no timer. The socket's binary messages carry the same runframe frames, so the session is the one `dock exec` runs |
| WebSocket library | `github.com/coder/websocket` (ISC): 120 KB of `bin/dock` (66,023,687 to 66,146,567 bytes, stripped), no dependencies of its own, tested against the Autobahn suite. A server of our own would save that and take frames, masking, fragmentation and close handshakes to get right; not worth 0.2% of the binary. Compression is off: terminal traffic is small and deflate costs CPU and memory per socket |
| UI | Mock A of `docs/design/container-terminal.html` (Saile's pick, 2026-11-27): a Shell tab between Jobs and Data (`g x`), shown to those with admin in the environment. One line of what it opens in (container, release), Connect (focused, so Enter connects; nothing connects before), the terminal to the bottom of the window, a line of how the session is or how it ended. xterm.js (MIT, 67 KB gzipped) and its stylesheet load when the tab opens, in chunks of their own; the cursor doesn't blink (no repainting animations). Leaving the tab closes the session |

## Consequences

- The last reason to SSH into a server for a container is gone, and
  every session is in the audit log with who opened it.
- A shell can change a running release in ways no release records.
  That is why it takes more than deploy, and why the audit names the
  release it ran in.
- A process a session started that rewrites its own environment, or runs
  as another user the stop can't reach, survives the session's end, as
  it would with `docker exec`.
