# 0081: Named identity providers

Status: accepted (asked by Saile, 2026-11-14; designed by the agent).
Amends [0065](0065-sign-in-is-the-platforms.md).

## Context

ADR 0065 gave the console three kinds of provider: `google`, `github` and
`oidc`, any OpenID Connect provider by its issuer. Most providers people
use are OpenID Connect, but their issuers are easy to get wrong (Auth0's
ends in a slash, Keycloak's has `/realms/`, Microsoft's a tenant and
`/v2.0`), and the button showed a plain ring for all of them. Saile asked
for more providers in the console: pick one, fill in only what it needs.

## Decision

Six more kinds, each an OpenID Connect provider whose issuer Dock builds.
The issuer is stored as before; the fields are read back from it
(`SSOWhere` in the API), so nothing else is kept.

| Kind | Takes | Issuer |
|---|---|---|
| `microsoft` | `tenant`: its ID or a domain of it | `https://login.microsoftonline.com/<tenant ID>/v2.0` |
| `gitlab` | `domain`: a self-hosted GitLab's URL, or nothing for gitlab.com | `https://gitlab.com` or the URL |
| `okta` | `domain` | `https://<domain>` (the org authorization server) |
| `auth0` | `domain` | `https://<domain>/` |
| `keycloak` | `domain` (its base URL) and `realm` | `<base>/realms/<realm>` |
| `authentik` | `domain` (its base URL) and `realm` (the application's slug) | `<base>/application/o/<slug>/` |

A domain may be given with or without `https://`; http only on
localhost, as for issuers. Each kind has a default label (its name), a
monochrome mark on its button and setup steps in the console. `google`,
`github` and `oidc` are unchanged, and existing providers keep working.
`kind` lives in a CHECK constraint, so migration 0109 rebuilds the table,
keeping ids.

### Microsoft: one tenant, and only vouched-for emails

Entra ID needs two decisions the others don't.

**A tenant is required; `common`, `organizations` and `consumers` are
refused.** Those endpoints sign in people of any tenant (or any personal
Microsoft account): every token's `iss` names the person's own tenant, so
go-oidc's exact issuer check fails, and accepting them would mean
checking `iss` against the pattern `https://login.microsoftonline.com/{tid}/v2.0`
ourselves. That is doable, but it would make "anyone with a Microsoft
account" a provider, where the owner of any tenant decides what email its
users have. Dock's providers sign in an operator's own people, so one
tenant is enough, and go-oidc verifies it exactly. A tenant given by a
domain is resolved once, when the provider is saved: discovery under the
domain names the tenant ID, and Dock stores that issuer (the one tokens
carry). A token from another tenant fails the issuer check.

**The email must be vouched for by `xms_edov`.** Entra ID sends no
`email_verified`, and its `email` claim is whatever the directory says,
which a tenant's admins can set to anyone's address; matching it to an
existing Dock account would let them take that account over (the
"nOAuth" problem). Microsoft's optional claim `xms_edov` says the email's
domain is verified by the tenant that owns it. Dock signs a Microsoft
person in only when `xms_edov` (or `email_verified`) is true; otherwise
it says to add the optional claims `email` and `xms_edov` in the app
registration's Token configuration. The console's setup steps say so up
front.

The other kinds use the `email_verified` claim as `oidc` does.

### Not added

- **Slack** is OpenID Connect with a fixed issuer (`https://slack.com`),
  so it should work as `oidc`, untried. A named kind is a follow-up once
  someone has tried it against a real workspace.
- **Discord** has no OpenID Connect; like GitHub it would need its own
  OAuth and API code (`/users/@me`, `verified`). A follow-up if asked for.
- **Apple** needs a client secret that is a JWT signed with a private key
  and rotated every six months, and sends the email only on the first
  sign-in. Not cheap; a follow-up.
- **Microsoft multi-tenant** (`common`, `organizations`): see above. If
  wanted, it is an allow-list of tenant IDs checked against `tid`, on top
  of `xms_edov`.

## Consequences

- An operator picks a provider and gives a tenant, a domain or a realm;
  the button has the provider's mark. CLI: `dock sso add <kind>
  --tenant|--domain|--realm`, and `dock sso set` changes them.
- A Microsoft provider needs the optional claims set up; until then its
  sign-ins are refused with a message saying what to add.
- The kinds are listed in the CHECK constraint, `apitypes.SSOKindNames`
  and the UI's records; a new kind touches all three and a migration.
