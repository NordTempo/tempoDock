# e2e

Scenarios against a live Tempo Dock, the way a user drives it: the API,
the CLI and real traffic through the proxy.

| Script | What it covers |
| --- | --- |
| `run.sh` | apps from source, Postgres, zero-downtime deploys, failures, scoped tokens, daemon crashes |
| `features.sh` | pre-deploy, stop/start, `dock run`, locks, logs and drains, snapshots, moves, apply, health, nodes going down, backups, Slack alerts against a mock |
| `release.sh` | the release walk: traces, status pages, vulnerability scans, MySQL/Redis data, diagnose, sleeping environments |
| `templates.sh` | every app template deploys and serves |
| `traces.sh`, `metrics.sh` | OTLP traces and metrics, on their own servers |
| `analytics-*.sh` | Dock Analytics with a headless browser, on their own containers |
| `upgrade/run.sh` | one published release updated to another, with data checks |
| `fresh-install.sh` | the README's install on bare Debian 12/13 and Ubuntu 22.04/24.04: piped install with Docker, CLI login, a template, re-run, uninstall |
| `nightly.sh` | all of the above that fits one cycle, every night (below) |
| `repeat.sh` | one scenario N times, a line per run, for failures too rare for one run |

Each scenario prints `ok:` and `FAIL:` lines and exits non-zero when one
failed. They never run against production: `lib.sh` refuses its address,
and the CLI always runs with `DOCK_URL`, never a context from your config.

## Servers: e2e/vm

`e2e/vm/up.sh [NODES]` starts a control plane and NODES nodes as
privileged systemd containers with their own Docker, installed with
`deploy/install.sh`; `source /tmp/e2e2/env` points the scenarios at them;
`e2e/vm/down.sh` removes them.

Servers live in a **slot**, `E2E_SLOT` (default `e2e2`): containers
`$SLOT-cp` and `$SLOT-node<N>`, network `$SLOT-net`, state `/tmp/$SLOT`.
Two slots never touch each other's servers, so give each its own subnet:

| Slot | Subnet | Used by |
| --- | --- | --- |
| `e2e2` | `10.250.250.0/24` | interactive work (bug hunts, release walks) |
| `e2e-nightly` | `10.250.251.0/24` | `nightly.sh` only |
| `e2e-nightly-fresh` | `10.250.252.0/24` | `nightly.sh`'s `fresh-install.sh` |
| `e2e-fresh` | `10.250.255.0/24` | `fresh-install.sh` by hand (`E2E_FRESH_SLOT`, `E2E_FRESH_SUBNET`) |

A new scenario names containers through `$SLOT` and addresses through
`$SUBNET_PREFIX` (both from `lib.sh`), never `e2e2-*` or `10.250.250.*`,
so it runs in any slot. Apps are reached with `http_get`/`http_code`,
which go through `E2E_PROXY`; after creating a project, `use_proxy_of`
points it at the server the project was placed on.

Several slots at once need more inotify instances than the default 128
(each server runs systemd and Docker as root): the dev VM has
`fs.inotify.max_user_instances = 1024` in `/etc/sysctl.d/90-dock-e2e.conf`.

`E2E_RELEASE=v0.54.2 e2e/vm/up.sh` installs a published release instead
of `bin/dock`; `E2E_VM_MEMORY=5g` caps each server's memory.

## The nightly run

`e2e/nightly.sh` runs every night at 03:00 Europe/Oslo from a systemd
user timer on the dev VM (the servers live there, out of reach of
GitHub's runners):

1. checks out main's newest commit whose `ci` run passed, in
   `~/.cache/dock-nightly/src`, and builds it into a directory release
   (`v<next patch>-nightly.<date>.g<sha>`);
2. starts the `e2e-nightly` slot (a control plane and two nodes) with the
   latest published release;
3. points the control plane's `[updates] source` at the build and runs
   `dock update apply`, while a hammer sends requests to an app on each
   server: one failed request fails the night;
4. runs each scenario of `SUITE` in `nightly.sh` in its own process and
   log, with a 45 minute cap each, no retries, then `fresh-install.sh`
   on bare servers of its own (slot `e2e-nightly-fresh`);
5. removes the servers.

It holds `/tmp/dock-heavy.lock` from the build to the last scenario
(waiting up to 90 minutes for it), skips the night when less than 8 GB of
RAM is free, and caps the whole cycle at 4 hours.

**Results.** Everything goes to `~/.cache/dock-nightly/<date>/`:
`nightly.log`, one log per step (`build`, `up`, `update`, ...) and per
scenario (`run-apps.log`, `features-lock.log`, ...), `results.tsv`, and
`soak-<server>.out` from the update's hammers. Each night adds one line to
`~/.cache/dock-nightly/history.log`. A failure opens one issue labelled
`nightly` (or comments on the open one) with the failed scenarios and
their last log lines, redacted (when the repository has issues turned on), and POSTs a summary to the webhook in
`~/.config/dock-nightly/webhook-url`, which tells the coordinator thread.
The first night that passes closes the issue.

```sh
e2e/nightly.sh install       # clone, write and start the timer (once)
e2e/nightly.sh status        # next run, last results
e2e/nightly.sh pause         # stop the timer; resume starts it again
e2e/nightly.sh --now         # a full cycle now, as the timer runs it
e2e/nightly.sh --now --here --keep --no-report run/apps features/lock
                             # this checkout, two scenarios, servers kept,
                             # nothing reported
```

**Flaky scenarios** are fixed, or quarantined: a line in
`e2e/nightly.quarantine` (`suite/scenario reason`) skips it, and the log
and the issue list it. Nothing is retried: a scenario that passes only on
a second try is a bug in the scenario or in Dock.
