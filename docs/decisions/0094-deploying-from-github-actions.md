# 0094: Deploying from GitHub Actions

Status: accepted (asked by the coordinator, Saile approved the action,
2026-11-17). Part 1, the action and `--json-file`, is built; part 2,
GitHub OIDC trust, is designed here and not built yet.

## Context

Teams that want tests to pass before a deploy can't use Dock's push to
deploy ([0018](0018-git-push-deploy.md), [0066](0066-github-apps-for-many-orgs.md)):
it deploys every push. They deploy from their own CI instead, and the
guide only had a hand-written excerpt: download a CLI of some version,
check its sums, run `dock deploy --json`. That left four problems:

| Problem | Why |
|---|---|
| A blank log while the server builds | `--json`, needed for the release's number, silences progress |
| A CLI that doesn't match the server | Every workflow pins or guesses a version |
| No signature check | The excerpt predates [0088](0088-signed-releases.md) |
| A long-lived secret in every repository | Tokens are the only credential CI can use |

## Decision

### 1. An official action (built)

**Deploy to Tempo Dock**, a composite action: `action.yml` plus a bash
script, no Node, no build step, nothing to keep patched. It is
developed in `contrib/github-action/` and published as
[NordTempo/deploy-action](https://github.com/NordTempo/deploy-action),
since GitHub's Marketplace needs `action.yml` at a repository's root.
Workflows use `NordTempo/deploy-action@v0`.

- **Inputs**: `server`, `token`, `project`, `environment`, `service`,
  `mode` (`source`, `image`, `apply`), `path`, `image`, `wait`,
  `version`, `cli`. **Outputs**: `release`, `status`, `url` (the
  release's History page), `releases` (JSON, for an apply), `exit-code`.
  Inputs reach the script as environment variables, never interpolated
  into it.
- **The CLI the server runs.** The action reads the version from
  `/api/v1/health` and runs its own copy of `install-cli.sh` with
  `DOCK_REQUIRE_SIGNATURE=1`, so the signature is checked against the key
  the action was pinned with, not one a tampered release brings along. A
  test keeps the copy equal to `scripts/release/install-cli.sh`, whose
  keys are tested against `internal/update/sign.go`. A development build
  gets the latest release; `version` pins one, `cli` skips installing.
  *Amended 2026-11-17:* the version, from the server or from `version`,
  is a release version or the step fails before any download, and
  `DOCK_REQUIRE_SIGNATURE=1` installs no release without a signature
  ([0088](0088-signed-releases.md)'s amendment): only v0.56.0 and later
  install here, and a server older than that takes a binary through `cli`.
- **Exit codes are the job's.** The step exits with `dock`'s code
  ([cli](../cli.md#exit-codes)): 6 for a failed release, 10 for a
  canceled one, 3 for a refusal. An error becomes an annotation and the
  job summary lists the releases with links. A deploy refused as
  `deploy_in_progress` follows the running release (`dock release wait`)
  and tries again, three attempts in all; other conflicts (a lock, an
  approval) fail at once, since trying again would file more requests.
- **Not built**: canceling the deploy when the workflow is canceled (the
  guide shows the `if: cancelled()` step, one line), and commit metadata
  on CLI deploys: a CLI-claimed SHA would drive the git connection's
  commit statuses and queued pushes, which today only follow commits
  Dock fetched itself. Revisit with a separate "claimed" commit if teams
  ask for it.

**`--json-file FILE`**, a global CLI flag: the `--json` value written to a
file while output stays human and progress goes on, one compact JSON
value per line, the result and then, on failure, the error object. A
failed deploy writes both, so the job knows the release and why it
failed. Considered: progress on stderr under `--json` (breaks "stderr is
one error object"), and parsing human output (never). A CLI older than
the flag gets `--json` from the action, without progress, with a warning.

**Publishing.** The release workflow's last step runs
`scripts/release/sync-action.sh`: the directory as committed becomes the
repository's root (with the LICENSE), and when that changed it commits,
tags the next minor (`v0.2.0`, ...) and moves `v0` there, so `@v0` gets
fixes and rotated release keys. Not for pre-releases. The action's major
follows Dock's: v0 while Dock is 0.x (Saile, 2026-11-17: no 1.0.0 action
before Dock's own), with its inputs kept compatible; when Dock reaches
1.0, `v1.0.0` is tagged by hand once and the syncs follow it, as they
would a later breaking change's `v2.0.0`. It pushes with
`DEPLOY_ACTION_TOKEN`, a fine-grained token with Contents and Workflows
write on that repository only (the directory carries the repository's own
test workflow, `.github/workflows/test.yml`, which GitHub doesn't run
from a subdirectory here); unset, the step only says so. The Marketplace
listing is a release with "Publish this Action to the GitHub
Marketplace" checked, which only the web UI does.

**Tests** run on a GitHub-hosted runner with nothing outside it: here
the workflow builds `dock` from the checkout (in deploy-action, the
`test` workflow takes Dock's latest release), starts it on the fake runtime
on 127.0.0.1 (`test/fake-dock.sh`), and deploys to it with the script's
cases (`test/local.sh`, which also runs on a laptop) and through
`action.yml`, including the signed install of the latest release.

**Auth** is a token in a secret, documented with the least it needs:
`--scope deploy --project P --environment E`, one per environment, the
production one kept as a secret of a GitHub environment with reviewers.

### 2. GitHub OIDC trust (designed, not built)

A workflow asks GitHub for an ID token (`permissions: id-token: write`),
signed by `https://token.actions.githubusercontent.com` and naming the
repository, ref, workflow and run. Dock trusts it for an org agent, so a
repository deploys with no stored secret.

- **A trust belongs to an org agent.** `dock token trust NAME github
  --repo acme/shop (--ref refs/heads/main | --environment production)
  [--workflow .github/workflows/deploy.yml]`, in the token's menu in the
  UI, needing `agents.manage` like the token's grants. The agent's grants,
  agent rules, locks and audit stay as they are: a trust is another way
  to act as that agent, never more.
- **What must match.** `iss`; `aud`, the trust's audience, by default
  Dock's public address (the action requests the token with its `server`
  input as audience); `exp`, `iat` and `nbf` with a minute of skew; the
  repository by name and, once the first exchange has seen them, by
  `repository_id` and `repository_owner_id`, so a deleted and re-created
  or renamed-into name doesn't inherit the trust; the ref exactly, or the
  GitHub environment (whose protection rules then gate the deploy), or
  both; the workflow when set. A trust needs a ref or an environment: a
  trust of a whole repository would let every branch deploy.
  `pull_request` runs carry `refs/pull/N/merge`, so they match no branch.
- **The exchange.** `POST /api/v1/auth/github-oidc` with the ID token
  answers with a short-lived credential, `dock_s_...`, valid for an hour
  or the job's remaining time, whichever is shorter. It is a row in a new
  table (its hash, the token, the run, the expiry, the ID token's `jti`
  under a unique index, so an ID token is exchanged once) that the auth
  path reads like a token's, acting as the agent with the run in its
  audit name: `github-shop-staging (acme/shop@main, run 4211)`. Expired
  rows are deleted by the next exchange, not a timer. The exchange is
  rate-limited per address like sign-ins and audited
  (`token.exchanged`).
- **Keys.** GitHub's JWKS through `go-oidc` (already a dependency for
  SSO, [0058](0058-sso-oidc.md)): fetched on the first exchange, cached
  in memory, fetched again only for an unknown key id. No timer, no new
  dependency.
- **The action.** With no `token` input and the ID token endpoint in its
  environment, it requests an ID token for `server`, exchanges it, masks
  the result and deploys with it. `token` keeps working.
- **Size.** A migration (trusts, sessions), the exchange endpoint, the
  auth path's third credential kind, CLI and UI for trusts, and the
  action's half: one pull request of its own, after this one.

## Consequences

- A team deploys from GitHub Actions with five lines of YAML, sees the
  build in the job's log and a link to the release, and a failed release
  fails the job with the reason.
- The action carries Dock's release keys: a rotation (0088's steps)
  reaches it through the copy, which the test keeps equal, and the next
  release's sync moves `v0` to it. A workflow pinned to an older tag or
  SHA keeps trusting only the keys it was pinned with.
- `--json-file` is one more global flag, useful to any CI.
- Until part 2 ships, every repository that deploys holds a token; the
  guide limits each to one environment.
