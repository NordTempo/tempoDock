# Security policy

## Reporting a vulnerability

Please report security issues privately, not in a public issue or pull
request. Use GitHub's private reporting: the repository's **Security** tab,
then **Report a vulnerability**.

Include what you found, how to reproduce it, and the version (`dock version`).
We aim to acknowledge a report within 3 working days and to ship a fix for a
confirmed issue in a patch release, crediting you in the release notes unless
you ask us not to.

## Supported versions

Fixes go into the latest release. Dock updates in place
(`dock admin update`, or Updates in the console), so upgrade to the newest
version before reporting, and stay on it.

## Scope

In scope: the `dock` binary (daemon, CLI, node runtime), the embedded web UI,
the MCP endpoint, the helper binaries in `cmd/`, the install script and the
update path, and `tempobase`.

What Dock promises, and what it doesn't, is in the
[safety model](docs/safety-model.md). Apps you deploy on Dock, and the
servers you run it on, are yours to secure.
