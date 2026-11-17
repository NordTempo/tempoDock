# Deploy from GitHub Actions

Deploy from your own CI when tests should pass first. The **Deploy to
Tempo Dock** action installs the `dock` CLI your server runs, deploys the
checkout, an image or a project file, follows the release and fails the
job when the release fails. The log shows the build as it happens, and the
job summary links the release in the console.

Without CI, [git push deploys](deploying.md#deploy-on-git-push) through
Dock's GitHub App or a webhook do the same on every push, without a test
step.

## 1. A token for the workflow

The workflow deploys as an organization agent: a token limited to the one
environment it deploys to. Create it as an admin:

```sh
dock token create github-shop-staging \
  --scope deploy --project shop --environment staging --json
```

That is the least it needs: deploy on `shop/staging` and nothing else
([ADR 0080](../decisions/0080-permissions-roles-groups-and-agents.md)). It
can't touch production, other projects, tokens or members. The secret in
the output is shown once.

In the repository's **Settings › Secrets and variables › Actions**, add:

| Name | Kind | Value |
|---|---|---|
| `DOCK_TOKEN` | Secret | The token's secret, `dock_...` |
| `DOCK_URL` | Variable | Your Dock's address, like `https://dock.example.com` |

The runner must reach that address: Dock's [public address](install.md#public-address),
or a tailnet the runner joins first (for example with
`tailscale/github-action`). Never expose the API port itself.

For production, make a second token limited to production and keep it as
a secret of a GitHub environment with required reviewers, so a person
approves each production deploy on GitHub. If the organization's
[agent rules](agents.md#the-organizations-agent-rules) make agents'
production deploys wait for an approver, the token's deploy files an
access request and the job fails with exit 5 (`environment_locked`); an
admin exempts a trusted CI token with `dock token bypass
github-shop-production on`.

## 2. The workflow

Test, then deploy the checkout's source to staging on every push to
`main`. Dock builds it on the server, like `dock deploy`, with the
repository's `dock.toml` if it has one:

```yaml
# .github/workflows/deploy.yml
name: deploy
on:
  push:
    branches: [main]

# One deploy of staging at a time; a newer push waits rather than cancels.
concurrency:
  group: deploy-staging
  cancel-in-progress: false

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v5
      - run: npm ci && npm test

  deploy:
    needs: test
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v5
      - id: dock
        uses: NordTempo/deploy-action@v0
        with:
          server: ${{ vars.DOCK_URL }}
          token: ${{ secrets.DOCK_TOKEN }}
          project: shop
          environment: staging
          service: web
      - run: echo "Release $RELEASE is live at $URL"
        env:
          RELEASE: ${{ steps.dock.outputs.release }}
          URL: ${{ steps.dock.outputs.url }}
```

`@v0` gets the action's fixes and Dock's release keys as they change; its
inputs stay compatible within v0, which lasts while Dock is 0.x. If your
policy pins third-party actions to a commit SHA, pin
[NordTempo/deploy-action](https://github.com/NordTempo/deploy-action)'s
newest `v0.x.y` tag's commit.

### A prebuilt image

Build and push the image in the workflow, then deploy it; the server only
pulls it. For a private registry an admin adds the organization's
credentials for its host once ([private registries](deploying.md#private-registries)):

```yaml
      - uses: docker/build-push-action@v6
        with:
          push: true
          tags: ghcr.io/acme/web:${{ github.sha }}
      - uses: NordTempo/deploy-action@v0
        with:
          server: ${{ vars.DOCK_URL }}
          token: ${{ secrets.DOCK_TOKEN }}
          project: shop
          environment: staging
          service: web
          mode: image
          image: ghcr.io/acme/web:${{ github.sha }}
```

### A project file

Keep the project's config in git ([config as code](deploying.md#config-as-code-dock-apply-file))
and apply it from CI: the action stages the file in the environment, then
applies it, following every release that starts.

```yaml
      - uses: NordTempo/deploy-action@v0
        with:
          server: ${{ vars.DOCK_URL }}
          token: ${{ secrets.DOCK_TOKEN }}
          environment: staging
          mode: apply
          path: dock.project.toml
```

Nothing is removed by an apply from the action; removals stay a person's
`dock apply --prune`.

## Inputs and outputs

| Input | Default | Means |
|---|---|---|
| `server` | | Your Dock's address. Required |
| `token` | | A Dock token, from a secret. Required |
| `project`, `environment`, `service` | | Where to deploy. `environment` may be left out when the project has one environment; `service` is created if it doesn't exist; `apply` reads the project from the file |
| `mode` | `source` | `source` builds the directory at `path` on the server; `image` deploys `image`; `apply` stages and applies the project file at `path` |
| `path` | `.`, or `dock.project.toml` for `apply` | The directory or the project file |
| `image` | | The image, for `mode: image` |
| `wait` | `true` | Follow the release until it is live or failed. `false` returns once it is queued |
| `version` | the server's | The `dock` release to install, like `v0.56.0` |
| `cli` | | A `dock` binary to use instead of installing one |

| Output | Means |
|---|---|
| `release` | The release's number |
| `status` | `active`, `failed`, `canceled`, or `queued` with `wait: false` |
| `url` | The release's page in the console |
| `releases` | Every release started, a JSON array; an `apply` may start several, and sets only this |
| `exit-code` | `dock`'s exit code |

## When it fails

The step exits with `dock`'s [exit code](../cli.md#exit-codes), which
fails the job, and the job summary says why:

| Exit | The job failed because |
|---|---|
| 3 | The token may not do this: the message says which grant is missing |
| 4 | The project or environment doesn't exist |
| 5 | The environment is locked, or the deploy waits for an approver |
| 6 | The build or health check failed. Traffic was not switched; the previous release still serves |
| 7 | The runner couldn't reach the server |
| 10 | Someone canceled the deploy |

A deploy that starts while another runs in the service (exit 5,
`deploy_in_progress`) follows that one to its end and tries again, up to
three times in all.

Canceling the workflow stops following the release, not the deploy. To
cancel the deploy too, add a step that runs when the job is canceled;
`dock` is on the job's `PATH` after the action installed it:

```yaml
      - if: cancelled()
        run: dock release cancel -p shop -e staging -s web
        env:
          DOCK_URL: ${{ vars.DOCK_URL }}
          DOCK_TOKEN: ${{ secrets.DOCK_TOKEN }}
```

## The CLI it installs

The action asks the server its version (`/api/v1/health`) and installs
that release's `dock-cli` for the runner, Linux or macOS. It checks the
download against the release's `SHA256SUMS`, and that file's signature
against Dock's release key, which the action carries itself
([ADR 0088](../decisions/0088-signed-releases.md)): a release swapped on
GitHub doesn't install. A server on a development build gets the latest
release. `version` pins one.

The action needs `jq`, which GitHub's runners have. On macOS the
signature check uses Homebrew's OpenSSL 3; on a runner without it, run
`brew install openssl@3` in an earlier step.

## Without a long-lived token

A token in a secret lives until it expires or is revoked. Trusting
GitHub's OpenID Connect tokens instead, so a workflow of one repository
and branch deploys with no stored secret at all, is designed in
[ADR 0094](../decisions/0094-deploying-from-github-actions.md) and not
built yet.
