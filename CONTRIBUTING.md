# Contributing

Thanks for helping. Issues and pull requests are welcome.

- **Security issues:** report them privately, never in an issue
  ([SECURITY.md](SECURITY.md)).
- **Setup and checks:** see [README](README.md#local-development) and
  [AGENTS.md](AGENTS.md). AGENTS.md holds the rules for people and coding
  agents alike: performance budgets, conventions, tests. Run `make lint`,
  `go vet ./...`, `make test` and, for UI changes, `cd web && pnpm check`
  before you open a pull request.
- **Bigger changes:** open an issue first, so we can agree on the approach
  before you build it. Design decisions are recorded in
  [docs/decisions](docs/decisions).
- **Pull requests:** one logical change, a conventional commit title
  (`fix(core): ...`), and a description of the problem and how you solved
  it.
- **CLA:** on your first pull request a bot asks you to accept the
  [Contributor License Agreement](CLA.md) with a comment. You keep your
  copyright; the agreement lets NordTempo offer Tempo Dock under other terms
  too, such as the hosted version.

By contributing you agree your work is published under the
[AGPL-3.0](LICENSE).
