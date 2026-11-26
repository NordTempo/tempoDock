# 0101: Features on and off

Status: accepted (Saile picked mock A on 2026-11-26, Postplan
`j712y3gquqao`). Amends [0037](0037-analytics.md) (analytics can be
turned off), [0074](0074-image-vulnerability-scanning.md) (scanning off
can remove its database) and [0075](0075-otlp-tracing.md) (tracing's
switch moves to the Features page too).

## Context

Prod is a 4 GB, 37 GB server. On 2026-11-26 its disk filled
([0035 amendment 2](0035-server-disk-breakdown.md)), and Saile asked to
turn the heavy optional parts off and on easily. They were switched in
three different ways:

| Feature | Takes | Switch before |
|---|---|---|
| Vulnerability scanning | a 3.4 GB database on each builder; jobs up to 2 GB of memory | `dock scanning on\|off`, no UI; off kept the database |
| Dock Analytics | its store, up to 512 MB of memory, and its data | could be turned on (a server, or the first site), never off |
| Tracing | its trace store, up to 512 MB, and its spans | on and off on Console › Analytics |

## Decision

| Topic | Decision |
|---|---|
| Place | A console page, **Features** (`/console/features`, its own sidebar entry), lists each feature with the servers it runs on, its memory now (last minute's sample) or at most, its data on disk (each server's last disk reading, read in the background if there is none), and a switch. `GET /api/v1/features`, `dock features` |
| On | Switches at once: scanning starts its worker (and downloads a database a builder doesn't have); analytics runs its store on the analytics server again, on the data it kept, and the servers' ingests get their salts back; tracing starts its trace store |
| Off | Asks once. Scanning: keep or remove every builder's database (`PUT /scanning {remove_database}`, `dock scanning off --remove-database`). Analytics: keep or remove the store's data (`PUT /analytics/enabled {remove_data}`, `dock analytics off [--remove-data]`). Tracing: its spans always go with the trace store, as before |
| Analytics off | The store's container stops (node op `stop-analytics-store`, protocol 35; its volume and binary stay) or is removed with its data. The servers' ingests get no salts, so they keep nothing: the sites' script still loads and counts nothing. A site's first domain no longer turns analytics on (refused, naming the switch) until an operator does; `analytics_settings.turned_off` (migration 0130) tells that apart from never on, since tracing makes the settings row too |
| Scanning off | Stops a running scan and starts none, as before; with the database removed (`pruneScanDatabase`), turning it on downloads it again before the next scan |
| Builds | Not a feature here: whether a server builds is its own setting (`dock server update --no-builder`), and BuildKit already stops when idle ([#498](https://github.com/NordTempo/tempoDock/pull/498)) |

## Consequences

- A small server can drop scanning (3.4 GB disk, 2 GB memory peaks) and
  analytics (its store's memory) in two clicks, and get them back later.
- Analytics off is a platform decision: org admins can't turn it on by
  adding a site, and their dashboards show no new data meanwhile.
- A node older than protocol 35 can't stop its store and keep the data:
  turning analytics off there asks to upgrade dock on it, or to remove
  the data.
- Numbers on the page are as fresh as their samples: memory a minute old,
  disk as old as the server's last disk reading.
