# Single sign-on

An organization can let its members sign in through the identity
provider it already runs: Google Workspace, Microsoft Entra ID, Okta,
Keycloak, Authentik, or anything else that speaks OpenID Connect. The
provider checks the password and MFA; Dock trusts what it says, for that
organization only. The design is [ADR 0058](../decisions/0058-sso-oidc.md).

## Connecting a provider

You need to be an admin of the organization with access to every
project.

In the browser it's **Organization settings → Single sign-on**; the
CLI is `dock org sso`.

1. At the provider, register Dock as a web application (an "OAuth
   client" or "OIDC app"):
   - redirect URL: `https://<your Dock address>/api/v1/auth/sso/callback`
     (the Single sign-on page and `dock org sso list` show it),
   - scopes: `openid email profile`,
   - grant type: authorization code (Dock uses PKCE).
2. Add it to Dock with the issuer URL, the client ID and the client
   secret, and the email domains it signs people in for:

```
$ printf %s "$CLIENT_SECRET" | dock org sso add google \
    --issuer https://accounts.google.com \
    --client-id 1234.apps.googleusercontent.com \
    --client-secret-stdin --domain example.com
✓ Added single sign-on provider google for example.com
  Redirect URL to register at the provider: https://dock.example.com/api/v1/auth/sso/callback
  New members: invite only
```

Dock reads the issuer's discovery document
(`<issuer>/.well-known/openid-configuration`) right away, so a wrong
URL fails here. The client secret is stored encrypted and never shown
again; replace it with `dock org sso set google --client-secret-stdin`.

### Issuer URLs

| Provider | Issuer |
|---|---|
| Google | `https://accounts.google.com` |
| Microsoft Entra ID | `https://login.microsoftonline.com/<tenant ID>/v2.0` |
| Okta | `https://<your org>.okta.com` (or an authorization server's URL) |
| Keycloak | `https://<host>/realms/<realm>` |
| Authentik | `https://<host>/application/o/<application slug>/` (with the trailing slash) |
| GitHub | Not OpenID Connect. Put Dex or Authentik in front of it and use their issuer |

The issuer must match what the provider puts in its tokens exactly,
trailing slash included.

## Who gets in

People sign in from the sign-in page with **Sign in with SSO** and
their email; Dock finds the provider by the email's domain and sends
them there (when several organizations sign that domain in, they pick
one). The provider must
say the email is verified, and it must be in one of the provider's
domains.

- **A member** of the organization signs in.
- **Someone with an invite** (`dock org invite`) joins with the invite's
  role on their first sign-in, with a new Dock account if they had none.
  The invite link's **Create an account** makes a password account
  instead, except in an organization that requires single sign-on
  ([ADR 0060](../decisions/0060-invite-links-signed-out.md)).
- **Anyone else** is refused and told to ask an admin for an invite,
  unless the provider has a just-in-time role:
  `dock org sso set google --jit-role read` (or `deploy`) lets anyone
  with an allowed, verified email join with that role. Admins are always
  made by an admin.

Accounts created this way have no password: their owners sign in through
the provider.

## What an SSO sign-in can do

- It acts in its organization only. For your other organizations, and
  for operating the platform, sign in with your password.
- It counts as two-factor where the organization requires it, so turn on
  MFA at the provider. If you also have an authenticator app in Dock,
  Dock asks for its code after the provider.
- Your second factors and recovery codes are managed from a password
  sign-in.
- `dock login` approved from an SSO sign-in gives a token bound the same
  way.

## Requiring single sign-on

```
$ dock org sso-only on
✓ Members act in Acme only after signing in with its single sign-on
```

Members who signed in another way stay signed in but can't act in the
organization until they sign in through its provider. API tokens are
not affected. Turning it on needs your own sign-in to have gone through
the organization's provider, so you know it works.

If the provider is down, an operator turns the requirement off on the
server:

```sh
sudo dock admin sso-only acme off
```

## Removing a provider

`dock org sso remove google` removes it and the links between people and
their identities there. People keep their accounts and memberships. The
last provider of an organization that requires single sign-on stays
until that's turned off.
