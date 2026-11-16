# 0007: Config in the platform DB plus an optional repo file

Status: accepted

## Context

Service configuration can live in the platform (edited in UI/CLI) or in the
app's repository (edited in code).

**Platform DB**
- Pros: the UI and CLI can edit everything; changes are instant; secrets must
  live outside the repo anyway; works for services with no repo (prebuilt
  images, database templates).
- Cons: changes aren't reviewed in PRs; harder to recreate a setup from scratch.

**Repo file (GitOps-style)**
- Pros: reviewed and versioned with the code; agents edit files naturally;
  reproducible.
- Cons: the UI becomes read-only or must commit to git; per-environment
  overrides in files get complex; secrets still need a second home; needs repo
  access to change config.

## Decision

Split by the kind of setting.

| Setting | Lives in |
|---|---|
| Dockerfile path, build command, start command, port, health check | Optional `dock.toml` in the repo, applied on deploy. Falls back to the DB if absent. |
| Variables, secrets, domains, resource limits, volumes, replicas | Platform DB, edited in UI/CLI |

- If the repo file sets a field, the file wins. Those fields show as locked in
  the UI ("from dock.toml").
- The file has no per-environment overrides. Anything that differs between
  environments belongs in the DB.
- Every config change creates a release, which gives history, diffs and
  rollback for DB-held config too.
- Later: `dock config export` dumps a project as a file for backup and
  reproducibility.

## Consequences

- Things that belong to the code travel with the code; things that differ per
  environment are easy to change in the UI.
- Two places to look, mitigated by the UI always showing the effective config
  and where each value came from.
