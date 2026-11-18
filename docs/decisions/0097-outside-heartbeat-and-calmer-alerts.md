# 0097: An outside heartbeat and calmer alerts

Status: accepted (product review 2026-11-18, proposals 3 and 5; Saile
approved the top 5). Built in parts; see "Built so far" at the end.

## Context

Two gaps in alerting, both about the bad day.

**A dead server tells nobody.** Every alert is sent by the control
plane, and on a single-server install uptime checks run from that same
server ([0055](0055-uptime-checks.md)). The one failure that takes
everything down sends nothing.

**Alerts are noisy when it matters.** A service that flaps between
healthy and unhealthy sends a message pair each time. A server that goes
offline makes every check on it report separately. A deploy that fails
again while its alert is open sends nothing. A resolution dropped while
a channel is backed up leaves Slack showing the alert open. A team that
mutes the channel has no alerts.

## Decision

### 1. Outside heartbeat

Dock sends outward; it doesn't receive pings from apps (that idea stays
dropped).

- One platform setting, the **heartbeat**: a URL and an interval in
  minutes (1 to 60, default 5). While the control plane runs, it sends a
  GET to the URL every interval. Any dead man's switch works
  (healthchecks-style services, an endpoint the team runs elsewhere):
  it raises the alarm when the pings stop.
- It rides the statistics tick. No new timer. The setting is kept in
  memory, so a tick without a ping due costs a time comparison, and a
  due ping is one small request in its own goroutine, never held by the
  tick.
- The ping is sent only by a tick that got as far as listing servers
  from the database, so a daemon whose database is stuck stops pinging.
  There is no separate "fail" variant: every dead man's switch treats
  silence as failure, and a second URL convention would tie Dock to one
  vendor.
- The URL is a secret (it holds the check's ID): sealed at rest, never
  returned. Lists show scheme and host only.
- It uses the platform's alert egress guard: private addresses are
  allowed (a homelab's own monitor), Dock's own addresses and link-local
  are not. No redirects, 15 s timeout, no retries: the next interval is
  the retry.
- After three failed pings in a row a platform alert opens (`heartbeat`,
  a connection alert); the next ping that works resolves it. The last
  outcome shows in `dock alerts heartbeat` and the console.
- Operators with `platform.alerts` set it: `dock alerts heartbeat set
  <url> [--every 5]`, `dock alerts heartbeat remove`, `dock alerts
  heartbeat test`.

**Nodes.** A node can't send to the platform's channels without their
settings, and pushing secrets to every node for this isn't worth a
protocol bump. A node that has lost the control plane for five minutes
says so once at error level in its log (`journalctl -u dock`), and once
when the link is back, with how long it was gone. The control plane's
own death is what the heartbeat covers; a node's death is what the
server offline alert covers.

### 2. Calmer alerts

Four rules, all inside the alert engine and delivery. No new timers
except one one-shot per channel while a group is being collected.

**Settling (damping).** Alerts of kinds that flap (unhealthy, uptime
down, crash loop, server offline) don't resolve the moment their
condition clears. The alert is marked cleared (`alerts.cleared_at`) and
stays open; the statistics tick resolves it once it has been clear for
`[alerts] settle_minutes` (default 3; 0 turns settling off). If the
condition comes back first, it is the same alert: `cleared_at` is
reset, its count goes up, and nothing is sent. So a flapping service
sends one "opened" and, after it has been quiet, one "resolved" that
says how many times it happened. Resolving by hand, archiving a service
and removing a server resolve at once.

**Count.** `alerts.count` (default 1) is how many times the condition
occurred while the alert was open. The API returns it, notifications
say it ("3 times"), and the Inbox shows it.

**Repeats.** A deploy or backup that fails again while its alert is
open (the alert's target, the release or backup, changed) raises the
count and sends a `repeated` notification, instead of silently
rewriting the alert.

**Grouping.** A channel's sender waits 10 seconds after the first
notification arrives, then sends everything queued for that channel as
one message: Slack and email get one message listing each alert; a
webhook gets one POST per notification as before, since receivers parse
one alert per body. A server going offline with its checks is then one
Slack message. The wait is a one-shot timer that exists only while a
channel has something queued.

**Resolutions aren't dropped.** A backed-up channel's queue cap
(`maxQueued`) drops opened, escalated, repeated and reminder
notifications, never a resolution: those are bounded by the alerts the
channel was told about.

## Consequences

- An alert about a flapping kind resolves up to `settle_minutes` plus a
  tick later than before. The Inbox shows it as open until then.
- Every notification is up to 10 seconds later.
- Webhook receivers see a new event, `repeated`, and a new field,
  `count`.
- Migration 0127: table `heartbeat`, columns `alerts.count` and
  `alerts.cleared_at`.
- Not done: per-channel grouping windows, a node sending alerts itself,
  grouping by cause across channels (a group is whatever arrives within
  the window).

## Built so far

See the handoff note in the pull request; this section is updated as
parts land.
