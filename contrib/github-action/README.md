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
        uses: NordTempo/deploy-action@v0
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

This repository is published from
[NordTempo/tempoDock](https://github.com/NordTempo/tempoDock)'s
`contrib/github-action` on each Dock release: changes and issues go there.

`deploy.sh` is the action; `action.yml` passes it the inputs as
`INPUT_*` variables. `install-cli.sh` is a copy of the release's
installer, with Dock's release keys, kept equal by a test in tempoDock.

Test locally against a throwaway Dock on the fake runtime (127.0.0.1, no
Docker needed), with a `dock` binary from a release or a tempoDock build:

```sh
test/local.sh path/to/dock
```

The `test` workflow runs the same cases, and the action through
`action.yml`, on a GitHub-hosted runner.
