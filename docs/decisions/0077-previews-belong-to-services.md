# 0077: Previews belong to services

Status: accepted (asked by Saile, 2026-11-13, from using prod; the
coordinator relayed the pick of the plan's model, Postplan
`54pidqa7y3qp`). Amends [0056](0056-preview-environments.md)'s "Opt in"
and "Which pull requests".

## Context

Previews were a project setting naming a base environment. Saile's
question, from prod: "Why do we have previews settings on project level,
and not service level? Isn't it the services that would be connected to a
repo?" A service is what is connected to a repository, a branch and a
directory, and a monorepo has several services under different paths. The
project setting already named services everywhere (`web.KEY` copies,
`seed_from_snapshot: [db]`), and pull requests that changed nothing a
service builds still got a whole preview.

A preview is still one environment per pull request: a monorepo pull
request changes `web` and `api` together, and both need the database next
to them.

## Decision

| Topic | Decision |
|---|---|
| Who opts in | A service with a GitHub source (`service_previews.build`). Its environment is the base the preview is cloned from. `dock service previews on`, Settings › Source › Previews |
| Which pull requests | Into the branch such a service deploys, on its repository, and changing something in its directory: one compare call to GitHub (300 files; more, a failed call or a service at the root count as changed). An open preview follows every new commit, as before |
| What builds | Those services build the head commit; every other service of the base runs its current image, as before |
| Forks | Per service (`allow_forks`): a fork's pull request builds only the services that allow forks, and gets no preview if none does |
| Copied values | Each service lists its own keys; the base lists shared ones (`copy_shared`). Org references count as literals, as before |
| Seeding | Per service (`seed`): its volumes start from its newest snapshot |
| The environment's numbers | `environment_previews`: at most `max_previews` open from this base, `sleep_after_minutes`. No row means 3 and 30 minutes. `dock env previews set` |
| One base per branch | A project previews a repository and branch from one environment: turning previews on for a service is refused while another environment previews the same pair. A pull request has one preview |
| Disconnecting | Turns the service's builds off, so connecting again doesn't silently preview again |
| Permissions | Admin of the service's environment (was: of the whole project). The environment lock applies through `serviceFor` |
| API | `GET/PUT …/services/{s}/previews`, `GET/PUT …/environments/{e}/previews`. `GET /projects/{p}/previews` stays as the summary, with `services` and `environments`; its single-base fields describe the first base. `PUT` and `DELETE` on the project go |
| CLI | `dock service previews [on\|off] [--forks] [--copy KEY] [--seed]`, `dock env previews set ENV`. `enable` and `disable` go |
| Migration | 0102: every live service of a base with a git connection builds, with the project's forks setting; `web.KEY` to web, `shared.KEY` and the numbers to the base; seeded services keep seeding. The same pull request makes the same preview |

## Why the environment keeps two numbers

How many may be open and when they sleep are about a preview as a whole.
On the service, two services in one preview could disagree (web says 30
minutes, api says 2 hours), and Dock would need a rule for which wins.

## Consequences

- A monorepo reads as one thing: each service says whether pull requests
  build it, and a pull request touching only the docs gets no preview.
- The project's Previews page becomes a summary: who builds pull
  requests, each base's numbers, the open previews.
- Two environments of a project can preview different branches of the
  same repository (staging for `dev`, production for `main`).
- Idle cost is unchanged: nothing runs until a pull request event arrives.
  A new pull request costs one more GitHub call (the compare).
