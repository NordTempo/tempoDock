# 0058: Single sign-on with OpenID Connect

Status: accepted (asked by Saile, 2026-11-04; designed by the agent).
Amended by [0065](0065-sign-in-is-the-platforms.md): providers are the
platform's, with a button each on the sign-in page; no org providers,
domains, just-in-time roles or sso-only.

## Context

Teams already have an identity provider (Google Workspace, Microsoft
Entra ID, Okta, Keycloak, Authentik) that enforces their MFA and turns
people off when they leave. ADR 0057 left SSO to its own ADR. Saile asked
for org-level SSO: an org admin connects the org's provider, members sign
in through it, and an org can require it.

| Force | Means here |
|---|---|
| Orgs own their provider | A provider is an org's setting, managed by its admins, not the platform's. One Dock serves orgs with different providers |
| A provider vouches only for its org | An org admin chooses the issuer and the email domains. Dock doesn't verify that the org owns those domains, so a provider must never reach another org or the account as a whole |
| Invite only by default | Signing in through a provider must not make someone a member the org didn't ask for |
| Never locked out | A provider that's down, or misconfigured, must not lock an org out for good |
| Standard and small | One protocol covers every provider. Two small, permissively licensed libraries, no SAML |
| Idle means idle | No timers or caches: a sign-in in flight lives in memory and expires when read |

## Decision

### Protocol

Generic OpenID Connect: the authorization code flow with PKCE (S256),
`state` and `nonce`, scopes `openid email profile`. Libraries:
`github.com/coreos/go-oidc/v3` (Apache-2.0; discovery, JWKS, ID token
checks; it brings `github.com/go-jose/go-jose/v4`, Apache-2.0) and
`golang.org/x/oauth2` (BSD-3-Clause; the code exchange). The daemon grows
by about 0.85 MB (stripped); the client-only CLI build doesn't link
them.

Google, Entra ID, Okta, Keycloak and Authentik speak OIDC directly.
GitHub doesn't (its OAuth has no ID token): use an OIDC proxy in front of
it, Dex or Authentik. SAML is out of scope.

### Providers

| Topic | Decision |
|---|---|
| Who manages | Unlimited org admins, with a recent step-up (`requireRecent`, ADR 0057). API: `/org/sso-providers` (list, add, change, remove); CLI: `dock org sso list\|add\|set\|remove` |
| Fields | `name` (unique in the org), `issuer`, `client_id`, `client_secret`, `allowed_domains` (one or more email domains, lowercase), `jit_role` (`''`, `read` or `deploy`), `created_at`, `created_by` |
| Client secret | Write-only. Sealed in `sso_providers.client_secret`, bound to `(org_id, name)` (ADR 0041, `sealedColumns`) |
| Issuer | `https`, or `http` on loopback for trying a provider out. Kept exactly as given: OIDC compares issuers byte for byte. Adding or changing it reads its discovery document then, so a typo fails at once |
| Domains | A separate table, `sso_provider_domains (domain, provider_id)`, so signing in finds providers by an email's domain with one indexed lookup. Several orgs may list the same domain |
| Redirect URL | `<Dock URL>/api/v1/auth/sso/callback`, shown with each provider for registering it at the provider |
| Removing | Removes the provider's identities with it; people keep their accounts and memberships. The last provider of an sso-only org stays until that's off |

### Signing in

1. The sign-in page asks for an email. `POST /auth/sso/start {email}` is
   public and rate limited per client IP (`loginLimit.AllowIP`). It finds
   the providers whose domains include the email's. One: it answers the
   provider's authorization URL. Several orgs: it answers the choices,
   and the page starts again with `{org, provider}`. None: it says to
   use the password.
2. The flow in flight (state, nonce, PKCE verifier, provider, the browser
   binding's hash and where to land) waits in memory, in its own bounded
   `ceremonies` map (1000, 10 minutes, taken once; no timer). A restart
   forgets it; the person starts again.
3. The browser binding is a random value in a cookie, `dock_sso`:
   HttpOnly, `SameSite=Lax` (the provider sends the browser back with a
   cross-site top-level GET, which a Strict cookie would miss), scoped to
   `/api/v1/auth/sso`, lasting as long as the flow. A state stolen from a
   URL doesn't finish the sign-in in another browser.
4. `GET /api/v1/auth/sso/callback` takes the flow by its state, checks
   the cookie, exchanges the code with the verifier, and verifies the ID
   token with go-oidc: signature against the provider's JWKS, issuer,
   audience (the client ID), expiry. Then the nonce, `email_verified`
   true (a boolean, or the string some providers send), and the email's
   domain in the provider's list.
5. It redirects (303) to where the sign-in started (same-origin paths
   only; `/` by default) with the session cookie, to `/login?second=1`
   when the user has a Dock second factor, or to `/login?error=<reason>`.
   Reasons are short and human; what the provider answered goes to the
   daemon log only.

Every refusal after the flow is found is audited in the provider's org
(`org.sso_refused`), as are sign-ins (`user.login`, "with single
sign-on"), links (`user.sso_link`), accounts created (`user.create`) and
joins (`org.join`).

### Who the person is

1. **Identity.** `sso_identities (provider_id, subject) → user_id`. A
   linked subject finds its user whatever email it sends now.
2. **Email.** Else a user with that verified email. They are linked only
   if they can be a member of the provider's org: one already, an open
   invite of their email (accepted now, with its role, through the same
   code as `AcceptInvite`), or the provider's just-in-time role.
3. **New account.** Else, with an open invite or a just-in-time role, a
   new user with that email and an unusable random password (an
   operator can give them a password), a member with that role.
4. Else refused: "you aren't a member of <org>; ask an admin of <org>
   for an invite".

Just-in-time membership is off by default and gives at most `deploy`:
admins are made by admins. Disabled users are refused.

### Sessions

| Topic | Decision |
|---|---|
| What a session remembers | `sessions.sso_org_id`: the org whose provider vouched. `factor` is `sso` |
| Second factor | A user with a Dock second factor still gives it: SSO leaves a pending sign-in (ADR 0057's `dock_login` cookie) and the page asks for the code. The finished session keeps `sso_org_id` |
| Counts as two-factor | **An SSO session counts as two-factor for an org that requires it.** Dock can't see what the provider checked: an org that turns on both trusts its provider to enforce MFA. Enforce MFA at the provider |
| Acts only in its org | An SSO session acts only in its org (with no org named, it acts there), never operates the platform, and can't change account-wide things: second factors and recovery codes need a password sign-in. Its step-ups use the Dock second factor if the user has one, else signing in through the provider again |
| `dock login` | The token remembers the approving session's org (`tokens.sso_org_id`, like `tokens.two_factor`) and is bound the same way |

The org bound is what makes a provider safe to hand an org admin: their
provider can say anything about any email in its domains, but what it
gets is a session in their own org, which they already control.

### SSO only

`orgs.sso_only`, set with `PUT /org/sso-only` (`dock org sso-only
on|off`) by an unlimited admin with a step-up. While on, a member acts in
the org only with a session (or `dock login` token) that came through one
of its providers; anything else stays signed in, acts in no org there,
and every org operation says why (`sso_required`, 403). API tokens are
not affected. Turning it on needs a provider and the admin's own session
to have come through the org's SSO, so they know it works.

Break-glass, on the server:

```sh
sudo dock admin sso-only <org> off
```

It opens the database directly (like `dock admin reset-2fa`) and audits
`org.sso_only`. Members sign in with a password again; the providers
stay.

### tempoBase

Unchanged: it has its own login. Giving it SSO would need its own
provider table and callback in `tbstore`/`tbhttp`, with the same core of
discovery, verification and mapping lifted into a shared package (like
`datapanel`), and no orgs to bind sessions to: a tempoBase provider
would vouch for the whole instance.

## Consequences

- Teams sign in with the provider they already run, and leaving the
  company at the provider ends future sign-ins. Existing Dock sessions
  run out on their own (30 days); an admin removes the member to cut
  them off at once.
- Dock doesn't verify domain ownership. The org bound above is the
  answer; a provider listing `gmail.com` reaches only people the org
  invited or lets in just in time, and only inside that org.
- Each sign-in reads the provider's discovery document and keys: two or
  three requests per sign-in, none at rest.
- A provider outage blocks SSO-only orgs until it's back or an operator
  turns the requirement off on the server.
- The sign-in page has a "Sign in with SSO" path (an email, a choice
  when several orgs match, the provider's errors and the Dock second
  factor after it), and org settings a "Single sign-on" page for admins
  (providers, the redirect URL, sso-only). A session an SSO rule keeps
  out of its org gets the account page with the way back in, and an SSO
  session's step-up without a Dock second factor signs in through the
  provider again.
