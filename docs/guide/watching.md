# Watching

What Dock shows about running services, and where it sends word when
something breaks. Requests, traces and metrics have their own page,
[Tracing](tracing.md); status pages theirs, [Status pages](status-pages.md).

## Logs

A service's **Logs** tab follows its output live: stdout and stderr, with
error lines marked. Pick a release to read an older one: when Dock
replaces or removes a container it keeps its log, up to 64 MB per
service (compressed) and 30 days.

```sh
dock logs -s web --follow                  # live, on to each new release
dock logs -s web --grep timeout --since 1h # search
dock logs -s web --release 41              # a release that no longer runs
dock logs -s web --build                   # the newest build's log
```

A **log drain** sends every service's output in an environment somewhere
else as it is written: `http` (JSON lines), `syslog` or `loki`. Admins
add them in the project's Settings, or with `dock logs drain add` (see
[log drains](deploying.md#log-drains)). A slow drain never holds an app
up: its lines are dropped and counted instead.

## Errors

**Errors** (the organization's view, and each service's tab) groups
errors from three places into issues:

| Source | Counted |
|---|---|
| Logs | A line with an error level, a panic or a stack trace; one error each, however many lines |
| Browsers | The site's JavaScript errors, from [Analytics](#analytics) on the service's domain |
| Traffic | A minute in which at least 5 requests, and 5% of them, answered 5xx |

Errors that differ only in numbers, IDs or line numbers are one issue,
so an issue survives a deploy. An issue is **new** in its first release
or day, **regressed** when it comes back after being resolved, and
**resolved** by a person or by itself after 7 days without an error.
A deploy that fails opens issues for the errors its app logged, like a
live release. Two things are left out: lines about Dock's own health
check getting a 404, and what the app wrote while Dock stopped it.
`dock diagnose` shows the failed deploy's output with the likely cause;
in the UI the service's Overview says it under its status, with the
environment where the same image runs, the settings that differ, and the
fix that fits (for a port, "Use staging's port 8080" saves it and deploys
the image again).

```sh
dock errors                     # the organization's open issues
dock errors -p shop -s web      # one service's
dock errors show 12             # where, how often, since which release, a sample
dock errors resolve 12          # seen again, it regresses and alerts
```

## Alerts

An alert opens when something needs someone: a crash loop, a failed
deploy, backup or scheduled job, a failing uptime check or health check,
a domain that stopped working, a new or regressed error, a drop in
traffic, an access request. Most resolve themselves once the cause goes away. The
**Inbox** in the top bar lists open alerts and the access requests that
wait for an admin ([locked environments](agents.md#locked-environments-and-access-requests)).

Alert channels send them on: a webhook, Slack, Discord, Pushover or
email, each with a minimum severity, when an alert opens, while it stays
open (`--renotify`) and when it resolves. Admins add them under Settings
› Alerts. Slack, Discord and Pushover messages link to the alert's
release or service in the UI once Dock has a public address or
`[hooks] ui_url`. A delivery that fails is retried
for about 20 minutes, waiting longer when Slack asks (`Retry-After`); an
answer that can't change, like Slack's 404 for a removed webhook, isn't
retried and marks the channel broken: its error says what Slack said,
and an alert stays open until a test or a delivery works again, or the
channel is removed. Alerts are kept calm ([ADR
0097](../decisions/0097-outside-heartbeat-and-calmer-alerts.md)): an
unhealthy, uptime down, crash loop or server offline alert resolves only
once its condition has stayed clear for `[alerts] settle_minutes`
(3 by default), so a service that flaps sends one message when it opens
and one when it has been quiet, saying how many times it happened. A
deploy or backup that fails again while its alert is open sends
`repeated` with the count. A webhook, Slack, Discord or Pushover channel
waits 10 seconds and sends what arrived together: Slack, Discord and
Pushover get one message listing each alert, a webhook one POST per
notification. Pushover sends critical alerts at high priority, which
gets past quiet hours. A Dock that restarts or
updates meanwhile sends what a channel still holds before it stops, once
and without retries. A backed-up channel drops
new alerts past 32 waiting, never a resolution. Email channels aren't
grouped yet: one mail per notification.

```sh
dock alerts                                                      # open ones
dock alerts channel add team --slack https://hooks.slack.com/... # where they go
dock alerts channel add chat --discord https://discord.com/api/webhooks/...
dock alerts channel add phone --pushover <user key> --pushover-token -  # token on stdin
dock alerts channel add pager --webhook https://hooks.example.com/dock --min-severity critical
dock alerts channel test team
dock alerts resolve 31                                           # by hand
```

### Outside heartbeat

Every alert is sent by the control plane, so the one failure that takes
the control plane down sends nothing. The heartbeat covers it: while the
control plane runs, it sends a GET to one URL every few minutes. Point it
at a dead man's switch, a monitor outside this server that raises the
alarm when the pings stop. Any service or endpoint that expects a
regular GET works.

```sh
dock alerts heartbeat set https://monitor.example.com/ping/3f2a   # every 5 minutes
dock alerts heartbeat set - --every 2 < heartbeat-url             # the URL from stdin
dock alerts heartbeat                                             # how the last pings went
dock alerts heartbeat test                                        # send one now
dock alerts heartbeat remove
```

Operators set it. The URL names the check, so it is stored encrypted and
only its scheme and host are shown. Set the monitor's grace period to a
few intervals: a ping that fails isn't retried, the next interval is the
retry, and a control plane restart skips at most one. The address may be
private (a monitor on your own network); redirects aren't followed.
Three failed pings in a row open a platform alert, which resolves with
the next ping that works. A control plane whose database is stuck stops
pinging too.

A server other than the control plane can't send alerts itself. When it
has lost the control plane for five minutes it says so once in its log
(`journalctl -u dock`), at error level, and once more when the link is
back, with how long it was gone. Its apps keep running meanwhile. A
server that dies is what the server offline alert is for.

## Analytics

Dock Analytics counts visits to your sites first party and without
cookies: nothing is stored in the visitor's browser. A visitor is a hash
of a salt that changes daily, the site, their address and browser, so one
person counts once a day and nothing names them. A site also gets browser
errors, Web Vitals, goals, funnels and, where turned on, session replays
of a sampled share of page loads.

**Consent.** Session replay needs the visitor's consent, so by default it
waits for it: a page records only once your consent banner calls
`dock.consent(true)`. Most sites run the rest without a banner, but
Norwegian law has no explicit exemption for statistics; `--consent all`
holds everything back until consent, and `--consent none` runs
everything at load (for sites that add the script only after consent). A
site's settings page shows what each setting means under Privacy, with
privacy-policy text to copy. This is not legal advice.

```html
<script src="/_dock/a/s.js" defer></script>
<script>
  // in your consent banner's callbacks
  dock.consent(true);  // accepted
  dock.consent(false); // declined or withdrawn: replay stops
</script>
```

An operator picks the server the analytics store runs on first
(`dock analytics server`). Then any domain can join a site:

```sh
dock analytics enable shop.example.com   # count its visits
dock analytics stats shop                # visitors, pages, sources
dock analytics errors shop               # browser errors, grouped
dock analytics vitals shop               # LCP, INP, CLS at p75
dock analytics replays shop              # sessions, with --errors for the ones that failed
dock analytics goals add shop signup page:/welcome
dock analytics funnels add shop checkout page:/cart page:/pay page:/done
dock analytics site set shop --replay on # a site's own settings
dock analytics site set shop --consent all # nothing before consent
```

The top bar's **Analytics** has every site; a service's Requests tab
shows its own site's visitors.

## Images

Dock scans the image of every live release for known vulnerabilities,
after it went live and only while no build or deploy runs, so a scan
never slows a deploy. **Images** lists them worst first; an image's page
groups what it found by fix: rebuild on a newer base image, upgrade the
app's dependencies, or no fix yet.

```sh
dock images                       # every live image, worst first
dock vulns -s web                 # one service's
dock vulns --fixable              # only what a fix exists for
dock vulns ignore CVE-2026-1234 --reason "not reachable" --days 30
dock alerts vulns critical        # alert when a live image has a critical one
```
