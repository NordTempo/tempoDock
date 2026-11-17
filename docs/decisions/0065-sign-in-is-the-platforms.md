# 0065: Sign-in is the platform's

Status: accepted (asked by Saile, 2026-11-07; designed by the agent).
Amends [0058](0058-sso-oidc.md); [0057](0057-two-factor-sign-in.md) and
[0060](0060-invite-links-signed-out.md) as noted. Amended by
[0081](0081-named-identity-providers.md): more kinds of provider.

## Context

ADR 0058 made single sign-on an org setting: each org's admins added
OpenID Connect providers for their email domains, an email-first page
found the provider, and a session that came through an org's provider
acted only in that org. Saile reversed it: "each org should not have its
own SSO, it should be platform level", and "orgs should not care about
that, it's not enterprise stuff like that". [ADR 0062](0062-org-alerts-and-operators.md)
already split the two: the platform runs the infrastructure and sets
defaults, orgs own their people and access. How someone proves who they
are is the platform's; what they may do is the org's.

| Force | Means here |
|---|---|
| One sign-in for the Dock | Operators set providers up once; there is no per-org provider, mapping or requirement |
| Familiar | A "Continue with Google" button per provider, next to the password form, like any other site |
| GitHub is common | GitHub has no ID token; support its OAuth directly instead of asking for a proxy |
| Small config | Google and GitHub are presets: a client ID and secret. A custom provider adds its issuer and a label |
| Accounts aren't memberships | A provider says who someone is. Membership still comes from invites or an admin adding them |
| Never locked out | Passwords can be turned off, so there is a way back on from the server |

## Decision

### Providers

| Topic | Decision |
|---|---|
| Who manages | Operators, recently confirmed (`requireRecent`). Console: Access, Sign-in. API: `/platform/sso-providers`; CLI: `dock sso list\|add\|set\|remove` |
| Kinds | `google` (OpenID Connect, issuer `https://accounts.google.com` built in), `github` (GitHub's OAuth 2.0), `oidc` (any other OpenID Connect provider, by its issuer) |
| Fields | `name` (unique), `kind`, `label` (the button's "Continue with <label>"; Google and GitHub by default), `issuer` (oidc only), `client_id`, `client_secret` (write-only, sealed, bound to the name), `created_at`, `created_by` |
| Redirect URL | One for every provider, `<Dock URL>/api/v1/auth/sso/callback`: the flow in flight names its provider |
| Email domains | Dropped. They decided which provider an email used and who could join; buttons and invite only replace both |
| Removing | Removes its identities and the sessions that came through it; people keep their accounts and memberships. The last provider stays while passwords are off |

### Signing in

The sign-in page asks `GET /auth/methods` (public) for the buttons and
whether passwords are on, and shows a button per provider, in the order
they were added, above the password form. `POST /auth/sso/start
{provider, to}` returns the provider's URL. The flow in memory, the
browser binding cookie, PKCE and `state` are as in 0058.

- **OpenID Connect** (`google`, `oidc`): as 0058, minus the domain
  check: a verified ID token with the flow's nonce and a verified email.
- **GitHub**: the authorization code flow with PKCE and the scopes
  `read:user user:email`, then `GET /user` (its numeric `id` is the
  subject; logins can be renamed) and `GET /user/emails`: the primary
  email, which must be verified.

### Who signs in

1. **Linked:** `sso_identities (provider_id, subject)` finds the account,
   whatever email the provider sends now.
2. **Existing account:** else the account with that verified email, linked
   now.
3. **New account:** else, when the email has open invites, an account
   (with an unusable random password) and every one of its invites
   accepted; or, when the platform isn't invite only, an account in no
   org.
4. Else, on an invite-only platform, no account: the browser lands on
   `/invite-only`, which says the person signed in with the provider but
   has no account and no invite, to ask an admin for one, with a way back
   to the sign-in page. Audited as `user.sso_refused`.

Disabled accounts are refused. A new account's membership comes only
from its invites; nothing adds it to an org by itself. A member-less
account sees the existing "not in an organization yet" page.

**No default org or role.** 0058's just-in-time role gave a provider's
org new members. With providers belonging to no org, that would be a
mapping from a provider to an org, which Saile ruled out. Invites, and an
admin adding people, cover it.

### Platform switches

`platform_settings` gains two columns, `GET|PATCH /platform/sign-in`,
`dock sso password-sign-in|invite-only [on|off]`, the console's Sign-in
page.

| Switch | Default | Means |
|---|---|---|
| `invite_only` | on | A provider creates an account only for an email with an open invite. Off: for anyone it signs in, in no org. On by default so an upgrade gives nobody an account they couldn't get before |
| `password_sign_in` | on | The sign-in page takes email and password. Off: password sign-in and password sign-up from an invite link are refused; providers and passkeys still sign in; sessions already signed in stay until they end |

Turning passwords off needs a provider and the operator's own session to
have come through one (an operator API token may, as for 0058's
sso-only), so they know it works.

**Break-glass,** on the server, opening the database directly (like
`dock admin reset-2fa`):

```sh
sudo dock admin password-sign-in on [--password-for ops@example.com]
```

It turns passwords back on and audits `platform.password_sign_in`.
`--password-for` also gives that account a new generated password,
printed once: an operator who only ever signed in through a provider has
none they know.

### Sessions

| Topic | Decision |
|---|---|
| What a session remembers | `sessions.sso_provider_id` (removing the provider ends it) in place of `sso_org_id`. `factor` stays `sso` |
| Org bound | Gone. A provider's session is a full session: every org the person belongs to, operating the platform, their own second factors |
| Counts as two-factor | Still: where an org or the platform requires a second factor, a provider's sign-in counts. Enforce MFA at the provider |
| Step-ups | A Dock second factor if the person has one, else signing in through the same provider again (`sso_required`; the provider is asked to sign them in again, see the amendment) |
| `dock login` | Tokens no longer carry an org (`tokens.sso_org_id`, `cli_logins.sso_org_id` dropped); `two_factor` as before |

### Migration (0078)

- Every org's provider becomes a platform provider with the same id, so
  its identities and sessions carry over. A name an older provider has
  gets `-<org slug>` (or `-<id>` if that is taken too). Google's issuer
  becomes the `google` kind; anything else `oidc`, labelled with its name.
- Client secrets were sealed for `(org_id, name)`. The migration lists
  them in `sso_reseal`; Core seals each again for its new name at its
  next start (`resealSSOSecrets`), one transaction per provider. One
  that doesn't open is logged and left for an operator to set again.
- Domains, just-in-time roles and `orgs.sso_only` are dropped: the
  console has no per-org requirement to carry them to. An org that had
  sso-only is back to passwords; an operator turns passwords off for the
  whole Dock if that's wanted.
- Org settings lose the Single sign-on page; `/org/sso-providers` and
  `/org/sso-only` are gone. `dock org sso` and `dock org sso-only` say
  that sign-in moved to `dock sso`. `dock admin sso-only` is replaced by
  `dock admin password-sign-in`.

## Consequences

- Operators register Dock once per provider, with one redirect URL;
  anyone who used an org provider before needs nothing new registered,
  since the URL is the same.
- With invite only off and Google or GitHub set up, anyone with such an
  account gets one on this Dock. It belongs to no org and sees nothing,
  but like any account it may create an org of its own and deploy to the
  servers: Dock has no limit on that yet. Turn invite only off only with
  providers that sign in your own people (a custom provider, or Google
  limited to a Workspace in its consent screen).
- Dock trusts every provider's verified email to find an existing
  account, including an operator's. An operator who adds a custom
  provider vouches for how it verifies emails.
- tempoBase is unchanged: it has its own sign-in.

## Amendment (2026-11-17): a step-up signs in at the provider again

Tried against a real Keycloak: "Confirm it's you" for a session that
came through a provider passed without the person typing anything. The
provider still had its own session (Keycloak's lasts hours), sent the
browser straight back, and Dock stamped the new session as confirmed
now. Signing out of Dock and in again did the same, so anyone at an
unlocked browser could reach a step-up's actions.

- A step-up starts with `confirm` (`POST /auth/sso/start`), and Dock
  asks the provider to sign the person in again: `prompt=login`, or
  `max_age=0` for Google, which refuses `prompt=login`.
- A provider's session counts as confirmed at the ID token's
  `auth_time`, when the person last signed in at the provider, not when
  Dock got the token; without `auth_time`, now, as before. A provider
  that ignores the request (or a sign-in on an old provider session)
  confirms nothing, and Dock asks again.
- GitHub's OAuth has neither: its step-ups still reuse GitHub's session.
