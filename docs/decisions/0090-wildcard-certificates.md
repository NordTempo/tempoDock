# 0090: Wildcard certificates for generated hostnames

Status: accepted (approved by Saile through the coordinator, 2026-11-17).
Amends [0009](0009-dynamic-domains-and-tls.md) (generated hostnames
share one certificate) and [0036](0036-cloudflare-api.md) (Dock writes
TXT records for DNS-01). Follows [0087](0087-unguessable-generated-hostnames.md).

## Context

[0087](0087-unguessable-generated-hostnames.md) put a random part in generated
hostnames so they can't be guessed. On a direct server they could still
be listed:

- **Certificate transparency.** Caddy issued one on-demand certificate
  per hostname ([0009](0009-dynamic-domains-and-tls.md)), and every
  certificate a public CA issues is published in CT logs. Searching
  crt.sh for the base domain lists every generated address, random part
  included.
- **The handshake as an oracle.** The ask endpoint refuses names without
  a route, so a TLS handshake for an unknown name failed while one for a
  deployed name succeeded.

0009 already expected that at scale Dock "would switch to a wildcard
certificate via DNS challenge". Caddy is the stock release binary
(install.sh), with no DNS provider plugins, so Caddy can't get a wildcard
itself. Dock already holds Cloudflare tokens with DNS edit for its
domains ([0036](0036-cloudflare-api.md)). On tunnel servers this was
never an issue: Cloudflare's edge serves its own certificate for the
zone.

## Decision

**One certificate per base domain, from Dock.** Every base domain of a
direct server with TLS gets a certificate for `*.<base>` and `<base>`,
when the base is in a zone of one of the platform's Cloudflare
connections (the oldest connection, when several hold the zone). Base
domains are platform settings, so org connections aren't used.
`internal/acmedns` obtains it: RFC 8555 over `golang.org/x/crypto/acme`,
which was already a dependency. lego and certmagic would pull in far
more than the few calls needed (bin/dock grew by 196 KB). The
steps:

1. Register an ECDSA P-256 account key, made once per CA directory and
   kept sealed (`acme_accounts`).
2. Order both names, and add both DNS-01 values as TXT records at
   `_acme-challenge.<base>` with the usual `dock:<install id>` comment.
3. Wait until every one of the zone's authoritative name servers serves
   them: asked directly every 2 s, for at most 2 minutes.
4. Answer the challenges, then finalize with a fresh P-256 key.

The TXT records are deleted afterwards, whether the order worked or not,
and before an order Dock deletes any of its own that a crash left
behind. It never touches a record it didn't make.

**Stored sealed, sent with the routes.**
- `wildcard_certificates` (migration 0121) holds the chain, the key
  sealed (bound to its base domain), the validity, and the last error,
  attempts and retry time.
- Valid certificates are held in memory by base domain. Every route apply
  to a server carries its base's certificate:
  `Proxy.ApplyRoutes(ctx, routes, certs)`. There is no separate push, so
  a restarted node gets it with its first routes.
- Protocol 31 adds `certificates` to apply-routes. The control plane
  sends none to an older node, which keeps issuing on demand, so a key
  never goes to a node that wouldn't use it.

**Caddy serves it for the whole base.** The rendered config:
- loads the certificate with `tls.certificates.load_pem`, tagged
  `dock:*.<base>`;
- puts a TLS connection policy ahead of the catch-all that matches the
  SNI `*.<base>` and `<base>` and selects the certificate by that tag.
  Selecting by tag matters: an on-demand certificate Caddy still holds
  for an exact name from before would otherwise win, and keep renewing;
- makes the ask endpoint refuse names a loaded certificate covers, so
  nothing under the base is issued on demand.

Custom domains, and names more than one label under the base (Dock
generates none), work as before. Plain-HTTP servers ignore certificates.

**The oracle is gone.** A name one label under the base, deployed or
not, gets the wildcard in the handshake, then the routed service or the
same 404 page as any unrouted host.

**Renewal: one timer.** One worker (`runWildcards`) obtains and renews
certificates, one at a time. Its single timer fires at the earliest of:
- a renewal, a third of a certificate's lifetime before it ends (30 days
  for a 90-day certificate, and still sensible if Let's Encrypt shortens
  lifetimes);
- a retry;
- an expiry.

While issued it fires weeks apart. Without a base domain in a zone it
doesn't exist. It is kicked when a server's base domain or ingress
changes, when the Cloudflare zones change, and by `dock cloudflare
retry`. A kicked pass also tries again what was waiting for a change.

**Failures.**
- A failed attempt is retried after 5 minutes, doubling to 6 hours. That
  keeps Dock well inside Let's Encrypt's 5 failed validations per
  hostname an hour.
- A token Cloudflare refuses, a missing Zone › DNS › Edit permission, or
  a base domain whose zone left the connections waits for a change,
  without retries.
- The platform alert `certificate_failed` (key `wildcard:<base>`) opens
  on such a wait, or after three failures in a row. It says whether the
  certificate can't be issued or isn't renewing, and when it expires.
  The next success resolves it.
- A certificate that expires unrenewed stops being sent, and its names go
  back to on-demand certificates.
- A restart never orders again: the certificate is in SQLite. So
  restarts can't use up the 5 duplicate certificates a week.

**Fallback, and where it shows.** Without a platform zone for the base,
or until the first certificate is issued, generated hostnames get a
certificate each, as before. Console › Address and `dock address` (`GET
/address`, `wildcards`) list each base domain with its servers, and one
of these statuses:
- `active` (with expiry and renewal, and the reason when renewal fails);
- `issuing`;
- `failed`, with the reason;
- `unavailable`, with the reason.

**Config.** `[caddy] acme_directory` picks another CA (Let's Encrypt
staging, or Pebble in tests); `acme_email` is the account's contact.

## Not now

ACME renewal information (ARI), a fallback CA, wildcards for custom
domains, turning the wildcard off for one base domain, and wildcards on
tunnel servers (Cloudflare's edge already covers them).

## Consequences

- Generated hostnames under a base in a connected zone never reach CT
  logs, and a handshake doesn't say whether one exists. Hostnames issued
  before the wildcard stay in the logs they already reached.
- The certificate's key is in the control plane's database and its
  backups (sealed). It is sent over the node link to each server with
  that base, and Caddy keeps it in its autosaved config, readable only by
  its own user, like its on-demand keys.
- A platform token without DNS edit on the base's zone now raises an
  alert; until it gets the permission, hostnames are issued one by one,
  as before.
- Tests: `internal/acmedns` runs against Pebble and pebble-challtestsrv
  in Docker (`-tags integration`). Caddy's integration test loads a
  wildcard into a real Caddy. Core's tests use a fake CA, which every
  core test gets, so none reaches a real one.
