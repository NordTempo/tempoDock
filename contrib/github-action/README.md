# Deploy to Tempo Dock

Deploy to your own [Tempo Dock](https://github.com/NordTempo/tempoDock)
server from a GitHub Actions job, after your tests pass. The action
installs the `dock` CLI your server runs (its release signature checked),
deploys the checkout's source, a prebuilt image or a project file,
follows the release until it is live, and fails the job when it fails.

```yaml
jobs:
  deploy:
    needs: test
    runs-on: ubuntu-latest
    concurrency: deploy-staging
    steps:
      - uses: actions/checkout@v5
      - id: dock
        uses: NordTempo/tempoDock/contrib/github-action@main  # pin a commit SHA
        with:
          server: ${{ vars.DOCK_URL }}
          token: ${{ secrets.DOCK_TOKEN }}
          project: shop
          environment: staging
          service: web
```

The token needs deploy on the one environment, and nothing else:

```sh
dock token create github-shop-staging --scope deploy --project shop --environment staging
```

Image deploys (`mode: image`), project files (`mode: apply`), every input
and output, exit codes and production tokens:
[Deploy from GitHub Actions](https://github.com/NordTempo/tempoDock/blob/main/docs/guide/github-actions.md).

## Developing

`deploy.sh` is the action; `action.yml` passes it the inputs as
`INPUT_*` variables. `install-cli.sh` is a copy of the release's
installer, kept equal by a test in `internal/update`: after changing the
original, `cp scripts/release/install-cli.sh contrib/github-action/`.

Test locally against a throwaway Dock on the fake runtime (127.0.0.1, no
Docker needed):

```sh
go build -o /tmp/dock ./cmd/dock
contrib/github-action/test/local.sh /tmp/dock
```

The `github-action` workflow runs the same cases, and the action through
`action.yml`, on a GitHub-hosted runner.
