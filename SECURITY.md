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

## Verifying a release

From v0.56.0 on, every release's `SHA256SUMS` is signed with Dock's
release key ([ADR 0088](docs/decisions/0088-signed-releases.md)), in
`SHA256SUMS.sig`, and every file has a GitHub build-provenance
attestation. Dock checks the signature itself: `dock update apply`, the
console's Updates page, the root updater and `dock self-update` refuse a
release without a valid one, and `install.sh` and `install-cli.sh` check
it when OpenSSL 3 is installed. A release from before v0.56.0 has no
signature: `dock self-update` and the scripts install one only when you
name it yourself (`--to`, `DOCK_VERSION`), never because a server runs it
or it is the latest.

The release signing keys (raw Ed25519, base64, with their key id):

```
bc6c45a08a65c136 8uQjZq1kO37ezVv7EzN8eyrqOYXRGK2nsCRL/d3RSdU=
```

To check a release by hand, with OpenSSL 3 (on macOS: `brew install
openssl@3`), before running anything from it:

```sh
v=v0.56.0
for f in SHA256SUMS SHA256SUMS.sig; do
  curl -fsSLO "https://github.com/NordTempo/tempoDock/releases/download/$v/$f"
done
printf -- '-----BEGIN PUBLIC KEY-----\nMCowBQYDK2VwAyEA%s\n-----END PUBLIC KEY-----\n' \
  8uQjZq1kO37ezVv7EzN8eyrqOYXRGK2nsCRL/d3RSdU= > dock-release-key.pem
grep '^bc6c45a08a65c136 ' SHA256SUMS.sig | cut -d' ' -f2 | openssl base64 -d -A > SHA256SUMS.sig.bin
openssl pkeyutl -verify -pubin -inkey dock-release-key.pem -rawin -in SHA256SUMS -sigfile SHA256SUMS.sig.bin
grep -q "  dock-install-$v.tar.gz\$" SHA256SUMS && echo "SHA256SUMS is $v's"
```

Then check the files you downloaded against `SHA256SUMS` (`sha256sum
--check --ignore-missing SHA256SUMS`). Or check where a file was built,
with the GitHub CLI:

```sh
gh attestation verify dock-linux-amd64 --repo NordTempo/tempoDock
```

Releases before v0.56.0 have neither; they can only be checked against
their own `SHA256SUMS`. A release whose signature doesn't verify is
worth reporting, as above.

## Scope

In scope: the `dock` binary (daemon, CLI, node runtime), the embedded web UI,
the MCP endpoint, the helper binaries in `cmd/`, the install script and the
update path, and `tempobase`.

What Dock promises, and what it doesn't, is in the
[safety model](docs/safety-model.md). Apps you deploy on Dock, and the
servers you run it on, are yours to secure.
