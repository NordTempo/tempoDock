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
`dock diagnose` shows the failed deploy's output with the likely cause.

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

Alert channels send them on: a webhook, Slack or email, each with a
minimum severity, when an alert opens, while it stays open (`--renotify`)
and when it resolves. Admins add them under Organization settings, Alerts.
Slack messages link to the alert's release or service in the UI once Dock
has a public address or `[hooks] ui_url`. A delivery that fails is retried
for about 20 minutes, waiting longer when Slack asks (`Retry-After`); an
answer that can't change, like Slack's 404 for a removed webhook, isn't
retried and marks the channel broken: its error says what Slack said,
and an alert stays open until a test or a delivery works again, or the
channel is removed. Not done yet: alerts aren't batched, throttled or
damped when they flap, a resolution dropped while a channel is backed
up leaves Slack showing the alert open, and a service that fails to
deploy again while its alert is open sends nothing new.

```sh
dock alerts                                                      # open ones
dock alerts channel add team --slack https://hooks.slack.com/... # where they go
dock alerts channel add pager --webhook https://hooks.example.com/dock --min-severity critical
dock alerts channel test team
dock alerts resolve 31                                           # by hand
```

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
