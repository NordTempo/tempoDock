# 0112: Uploaded certificates

Status: accepted (decided by the custom certificates thread, 2026-11-27;
Saile prefers these decided and recorded). Amends
[0009](0009-dynamic-domains-and-tls.md) (a domain's certificate can be
the org's own) and [0090](0090-wildcard-certificates.md) (its worker and
`Proxy.ApplyRoutes`' certificates carry uploads too).

## Context

Every domain gets its certificate from Let's Encrypt, on demand
([0009](0009-dynamic-domains-and-tls.md)), and generated hostnames a
wildcard ([0090](0090-wildcard-certificates.md)). Some setups can't use
that:

- **Cloudflare origin certificates.** A zone in SSL mode Full (strict)
  with Authenticated Origin Pulls, or a team that wants the 15-year origin
  certificate Cloudflare hands out instead of a public one at the origin.
- **Names no public CA issues for.** LAN and Tailscale-only hostnames
  (`intranet.corp.lan`, `*.tail1234.ts.net` behind a split DNS) that an
  internal or corporate CA signs, and which the org's machines trust.
- **A required CA.** A company that must serve certificates from its own
  or a contracted CA.

The plumbing exists: `Proxy.ApplyRoutes` already takes certificates and
Caddy serves them by name, ahead of on-demand issuance (0090).

## Decision

**Certificates are the org's, matched to domains by name.** An org
uploads a chain and its key (`dock certificate add --cert --key
[--domain]`, `POST /org/certificates`). It serves every verified domain of
that org one of its DNS names covers: a name itself, or `*.<rest>` for one
label under `<rest>`, as TLS clients match them. Never another org's: each
server gets the certificate with the org's own hostnames as its names,
not the wildcard, so it can't answer for a name it doesn't own on a shared
server. A wildcard covers the domains added later, with no step per
domain. `--domain` only checks that the right file was picked.

Of several that cover a domain, the valid one that **expires last**
serves it (the newest upload on a tie). So a renewal takes over the moment
it is uploaded, and the old one can be removed at leisure. A domain served
to a Cloudflare tunnel only gets none: Cloudflare's edge holds its
certificate.

**What an upload must be.**
- PEM certificates in the chain, a PEM key (PKCS#8, PKCS#1 or SEC 1;
  an encrypted key is refused with how to decrypt it). The key is stored
  as PKCS#8.
- The key belongs to one of the certificates: that is the leaf. Dock puts
  the rest in order (each one issued the one before) and refuses a
  certificate that isn't part of the leaf's chain. A root at the end is
  kept as given.
- The leaf names DNS hostnames (subject alternative names); a CN is not
  enough, as for browsers. A name with whitespace in it is left out: no
  client matches it.
- Valid now: not expired, and not before its start. So is every
  intermediate: a client that needs one refuses it expired, trusted CA
  or not. Only a last certificate that really signed itself counts as a
  root and isn't checked.
- For servers: the leaf's extended key usage, if it has one, includes
  serverAuth (or any), and so does every certificate above it that has
  one. A client-authentication-only certificate is refused: browsers
  would refuse it for a website.
- The same leaf twice in one org is refused.
- Whether it chains to a CA the control plane trusts is recorded and
  shown (`public`), never required: origin and internal CAs usually
  aren't, unless that store holds their CA. It is the control plane's
  system trust store, usually but not always what browsers trust.
- The issuer's name is kept without control characters, since lists
  print it to terminals.

**Its end is the chain's.** A certificate stops working when its leaf
or a needed intermediate ends, so `not_after` is the earlier of the two,
and serving, the list and the alerts all go by it.

**Stored sealed.** `custom_certificates` (migration 0142): the names, the
ordered chain, the key sealed with `c.box` and bound to the org and the
leaf's SHA-256 ([0041](0041-sealed-values-bound-to-their-place.md)), issuer,
validity, the last expiry alert, who uploaded it. Keys are never returned.
Deleting an org deletes its certificates and drops their keys from
memory. IDs are never reused (`AUTOINCREMENT`), so a stale
`dock certificate remove 3` or a worker pass can't reach a later upload.

**Served like the wildcard.** Valid uploads are held in memory with their
keys open. Every route apply sends a server, for its routed hostnames, the
uploads that cover them, ahead of its base domain's wildcard: Caddy picks
the first connection policy whose names match, and an exact upload must
win over the wildcard where both match. The ask endpoints refuse on-demand
certificates for covered names. Without any upload it is one map check.
A domain counts as served by an upload (in the list, on the domain, for
its alerts) only where it really gets it: on a direct server with TLS
whose node takes uploads (only such a server is sent the keys at all),
and not routed through a Cloudflare tunnel (as
`tunnelOnlyHosts` decides: a name under the base domain still reaches
the IP until its record is ready). The domain itself shows what its
server's last routes carried for it.

**Node protocol 40: the domain check.** A node's domain check handshakes
with its Caddy and verifies against the system roots, which an origin or
internal CA fails. Apply-routes' certificates gain `uploaded`; a node
accepts the uploaded leaf it loaded for that hostname as it is (still
checking it is current), and verifies anything else as before. A node
older than 40 gets no uploads (its domains keep Let's Encrypt), since it
would report them as failing. (Reserved as 39, and this ADR as 0111;
protocol, migration and ADR numbers grow in merge order, so each took
main's next.)

**Uploads don't renew; expiry is the work.**
- Settings › Certificates (Saile's pick of three mocks, B) owns them: one
  table with each one's names, issuer, expiry and the domains it serves,
  a line over it while one that serves domains is close to its end, and
  the upload form (paste or choose the PEM files). A domain's row says
  "own certificate until ..." and links there (API `certificate` on the
  domain). `dock certificate list` shows the same. An org keeps at most
  100, all held in memory with their keys. Statuses: `active`,
  `expiring` (30 days or less), `expired`, `unused` (nothing matches, or a
  later one covers its domains).
- Alerts use the existing `certificate_failed` kind, as org alerts on the
  org's channels: a warning 30 days before the end, critical 7 days
  before (an escalation, so a second notification), and at the end the
  same alert says it expired, without a third. Calm by the rules of
  [0097](0097-outside-heartbeat-and-calmer-alerts.md): one alert per
  certificate, and only for one that serves domains. A renewal that covers
  its domains, or removing it, resolves it. The stage reached is stored,
  so restarts and a hand-resolved alert don't send it again.
- No new timer. The wildcard worker (`runWildcards`) has the one timer for
  certificates; its pass also looks at uploads and its wake is the earliest
  of its own and the uploads' next stage (30 days, 7 days, the end). A
  15-year origin certificate wakes it once in 15 years. Uploading or
  removing kicks it.

**After expiry: back to Let's Encrypt.** An expired upload stops being
sent and its domains go back to on-demand certificates, as an expired
wildcard does (0090). Keeping the expired one would fail every handshake
for every strict client (browsers refuse it outright on HSTS sites;
Cloudflare Full (strict) refuses it too); a public certificate never does
worse. Where Let's Encrypt can issue (a public name, Cloudflare in Full
strict accepts public certificates), the site keeps working; where it
can't (a LAN name), it was broken either way, and the alerts said so a
month ahead. A company that requires its CA gets a Let's Encrypt
certificate for the gap, which the expired alert names. The expired row
stays, shown as `expired`, until it is removed.

**Permissions.** A new org permission, `certificates.manage`, in the
Domains area's edit level (ADR 0080): uploading, listing and removing
take it. Being an org permission, only a grant on the whole org holds it,
so domains edit on one project doesn't let anyone serve every project's
domains (`domains.write`, an environment permission, would pass at the
org for a grant anywhere). Tokens need `deploy` and no project limit, as
for domains. Everyone who sees a domain sees its certificate on the
domain. Not stopped by a locked environment: like registry credentials,
the certificate is the org's and nothing in an environment changes by
itself.

## Not now

Keys in an HSM or a passphrase, uploads for Dock's own address or status
pages, a per-domain choice between an upload and Let's Encrypt (the name
decides), ACME from another CA for custom domains, MCP tools (an agent
handing over private keys is not a flow to encourage), and OCSP stapling
settings (Caddy's defaults).

## Consequences

- Cloudflare origin certificates and internal CAs work for custom domains
  with no Caddy config by hand.
- An upload's key is in the control plane's database and backups
  (sealed), on every server serving its org's covered domains, and in
  their Caddy's autosaved config, like the wildcard's.
- A domain served by an upload can show `active` while browsers outside
  the org don't trust it: the check accepts the uploaded leaf by design.
  The certificate's `public` says so.
- Tests: core's tests upload certificates from a test CA; Caddy's
  integration test serves an upload next to a wildcard in a real Caddy.
