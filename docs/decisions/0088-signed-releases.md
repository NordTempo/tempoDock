# 0088: Signed releases

Status: accepted (asked by the coordinator, 2026-11-17). Amends
[0028](0028-updates-from-the-console.md): an update also checks the
release's signature. Settles the release signing left open in
[0082](0082-open-source-license.md).

## Context

Every download path (`dock update apply` and the console, the root
updater, `dock self-update`, `install.sh`, `install-cli.sh`, the analytics
store's binary) checks files against the release's `SHA256SUMS`. That
file comes from the same GitHub release as the files, so whoever can
change a release (a compromised workflow, token or account) changes both
and passes the check. The root updater also took `SHA256SUMS` from the
daemon's staging directory, so the `dock` user could stage any binary
with matching sums and have root install it.

Releases are built on GitHub-hosted runners and are immutable since
v0.52.1, which stops a published release from changing, not a bad one
from being published.

## Decision

**An Ed25519 signature of `SHA256SUMS`, checked offline.** The release
workflow signs the exact bytes of `SHA256SUMS` and publishes
`SHA256SUMS.sig`: a line per signing key, `<key id> <base64 signature>`,
the key id being the first 8 bytes of the SHA-256 of the raw public key,
hex. The public keys are built into `dock` and `dock-cli`
(`internal/update/sign.go`) and checked with Go's `crypto/ed25519`: no
new dependency, no network, a few hundred bytes of binary. A release
passes when one line is a trusted key's valid signature and the signed
`SHA256SUMS` lists that version's `dock-install-<version>.tar.gz`, which
binds the signature to the version: an old release's signed sums can't
be served under a newer tag.

Considered and not taken: Sigstore (cosign keyless, sigstore-go), which
needs the network to verify and adds megabytes to a binary held to a size
budget ([performance](../performance.md)); and GitHub's artifact
attestations alone, which only `gh` can check. Attestations are added
anyway, as provenance: every asset gets a SLSA build-provenance
attestation (`actions/attest`), checkable with `gh attestation verify
<file> --repo NordTempo/tempoDock`. They say which workflow run on which
commit built a file; the signature says the release is ours.

**Where it's checked.**

| Path | Check |
|---|---|
| Staging (`dock update apply`, the console, the analytics store) | `SHA256SUMS` and `SHA256SUMS.sig` are downloaded first and checked before any other file; the signature is staged next to them |
| Root updater (`dock admin update`) | Copies `SHA256SUMS.sig` out of the daemon's reach with the rest and checks it with the keys built into the running binary, so the `dock` user can no longer stage a release of its own |
| Nodes | Node protocol 30 sends `SHA256SUMS.sig` with the update, after `SHA256SUMS`; the node's updater checks it like the control plane's. Older nodes, which would refuse a fourth file, get the three they expect |
| `dock self-update` | Refuses a bad or missing signature; `--json` says `unsigned` for an older release installed without one |
| `install.sh`, `install-cli.sh` | Check with `openssl pkeyutl -verify -rawin` (OpenSSL 3) against the same keys, written into the scripts (a test keeps them equal to `sign.go`) |

**The scripts on machines without OpenSSL 3.** A shell can't check
Ed25519 without a tool. OpenSSL 3 is on Debian 12, Ubuntu 22.04 and
later, which install.sh's servers are. macOS ships LibreSSL, which can't.
Where `openssl pkeyutl` has no `-rawin`, the scripts warn, print the `gh
attestation verify` command and go on with the `SHA256SUMS` check, so no
install breaks; `DOCK_REQUIRE_SIGNATURE=1` makes that an error. A
signature that is there and doesn't verify always fails. The scripts
come from the release themselves, so their check guards against swapped
assets, not a release rewritten whole: checking independently means
checking `SHA256SUMS.sig` with the key in [SECURITY.md](../../SECURITY.md)
before running anything from the release.

**Transition.** `FirstSignedRelease` is v0.56.0. A release from it on
(by its X.Y.Z, so its pre-releases too) must be signed. An older one has
no signature to check: it is accepted on its `SHA256SUMS` alone, with a
warning, so `dock self-update --to` an older server's version and the
scripts' `DOCK_VERSION` pins keep working. A release older than
v0.56.0 that does have a signature is checked. Docks before v0.56.0
don't know about signatures and update to signed releases as before; the
first check happens on the update after that.

**Keys.** The private key was generated on the maintainers' machine
(`openssl genpkey -algorithm ed25519`), is kept offline, and its only
other copy is the secret `RELEASE_SIGNING_KEY` of the protected GitHub
environment `release`, which only `v*` tags and `main` can deploy to.
Only the release job declares that environment, and only its signing
step gets the secret, as an environment variable: the key never touches
the runner's disk. The signer (`scripts/release/sign`) checks its
signature against the keys in the commit being released, so a key dock
doesn't trust stops the release before anything is published.

**Rotation.** `RELEASE_SIGNING_KEY` may hold several PEM keys; the signer
writes a line for each.

1. Generate the new key. Add its public key to `releaseKeys` in
   `internal/update/sign.go` and to `RELEASE_KEYS` / `release_keys` in
   both install scripts. Release that: it trusts both keys and is still
   signed with the old one.
2. Add the new private key to `RELEASE_SIGNING_KEY` beside the old one.
   From the next release on, both sign: docks that only know the old key
   still verify, newer ones verify either.
3. Once the releases that only know the old key are no longer supported
   (or at once, if the old key leaked), drop the old private key from the
   secret and its public key from the lists. A leaked key is also removed
   from the lists in a release of its own, as soon as possible.

**The workflow.** The release job signs `SHA256SUMS` once the analytics
store's sums are added to it, attests every file, then publishes. Run by
hand on `main` (`workflow_dispatch`), it is a dry run: it builds, signs
and attests `v0.0.0-dryrun.<run>` and keeps `dist/` as an artifact for a
day, publishing nothing.

## Consequences

- A release can't be changed without the signing key. A workflow can
  read the secret only when it runs from `main`, which takes a pull
  request with green checks, or from a `v*` tag, which only repository
  admins may create (the `release-tags` ruleset). A stolen token without
  those rights can't reach it.
- One more small download per update; nothing at start-up.
- Losing the private key means the next release can't be signed: the
  offline copy is the backup. Losing it with the secret means a rotation
  in which every server updates once, by hand (`install.sh` from a
  release signed by the new key).
- macOS and old Linux users of `install-cli.sh` get a warning instead
  of a check until they install OpenSSL 3 (`brew install openssl@3`);
  once installed, `dock self-update` checks every later release itself.
