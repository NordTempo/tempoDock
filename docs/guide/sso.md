# Single sign-on

People can sign in to Dock with Google, GitHub, or any identity provider
that speaks OpenID Connect (Microsoft Entra ID, Okta, Keycloak,
Authentik). The sign-in page shows a **Continue with** button for each,
next to the email and password form. Providers are the platform's:
operators set them up once for the whole Dock, and organizations still
decide who is a member. The design is [ADR 0065](../decisions/0065-sign-in-is-the-platforms.md),
amending [ADR 0058](../decisions/0058-sso-oidc.md).

## Adding a provider

You need to be an operator. In the browser it's **Console → Sign-in**;
the CLI is `dock sso`.

Every provider sends people back to the same redirect URL,
`https://<your Dock address>/api/v1/auth/sso/callback` (the Sign-in
page and `dock sso list` show it). Register it at the provider:

| Provider | Where | What Dock needs |
|---|---|---|
| Google | Google Cloud, APIs & Services, Credentials, Create OAuth client ID, Web application, Authorized redirect URI | Client ID and secret |
| GitHub | Settings, Developer settings, OAuth Apps, New OAuth App, Authorization callback URL | Client ID and secret. Dock reads the account's primary verified email |
| Any OpenID Connect provider | A web application ("OAuth client", "OIDC app") with the scopes `openid email profile` and the authorization code grant (Dock uses PKCE) | Issuer URL, client ID and secret, and a label for its button |

```
$ printf %s "$CLIENT_SECRET" | dock sso add google --client-id 1234.apps.googleusercontent.com --client-secret-stdin
✓ Added identity provider google: the sign-in page shows Continue with Google
  Redirect URL to register at the provider: https://dock.example.com/api/v1/auth/sso/callback

$ printf %s "$CLIENT_SECRET" | dock sso add github --client-id Ov23li... --client-secret-stdin

$ printf %s "$CLIENT_SECRET" | dock sso add corp --kind oidc \
    --issuer https://id.example.com/realms/staff --label "Example ID" \
    --client-id dock --client-secret-stdin
```

Named `google` or `github`, a provider is that kind; any other name needs
`--kind`. Dock reads an OpenID Connect issuer's discovery document
(`<issuer>/.well-known/openid-configuration`) right away, so a wrong URL
fails here. The client secret is stored encrypted and never shown again;
replace it with `dock sso set <name> --client-secret-stdin`.

### Issuer URLs

| Provider | Issuer |
|---|---|
| Microsoft Entra ID | `https://login.microsoftonline.com/<tenant ID>/v2.0` |
| Okta | `https://<your org>.okta.com` (or an authorization server's URL) |
| Keycloak | `https://<host>/realms/<realm>` |
| Authentik | `https://<host>/application/o/<application slug>/` (with the trailing slash) |

The issuer must match what the provider puts in its tokens exactly,
trailing slash included.

## Who gets in

The provider must say the email is verified.

- **Someone with a Dock account** signs in: the first time Dock finds
  the account by its email and links it to the provider's identity;
  after that the identity finds it, whatever email the provider sends.
- **Someone with an open invite** gets an account on their first sign-in,
  and their invites are accepted.
- **Anyone else**, while sign-up is **invite only** (the default), gets
  no account: Dock says they signed in with the provider but this Dock is
  invite only, and to ask an admin for an invite.

With invite only off (`dock sso invite-only off`), anyone a provider
signs in gets an account, in no organization until an admin invites or
adds them. Like any account, it may create an organization of its own,
so turn invite only off only with providers that sign in your own people.

Accounts created this way have no password: their owners sign in through
the provider. An operator can give them one from the console's Users.

## What a provider's sign-in can do

- Everything a password sign-in can: every organization the person
  belongs to, the console for operators, their own second factors.
- It counts as two-factor where an organization or the platform
  requires it, so turn on MFA at the provider. If you also have an
  authenticator app or passkey in Dock, Dock asks for it after the
  provider.
- When Dock asks you to confirm it's you, you sign in through the same
  provider again (or use your Dock second factor).

## Turning passwords off

```
$ dock sso password-sign-in off
  password sign-in  off
  invite only       on
```

The sign-in page then shows only the providers and passkeys, and invite
links make no password accounts. Sessions already signed in stay until
they end. Turning it off needs a provider and your own sign-in to have
gone through one, so you know it works.

If every provider breaks, turn passwords back on from the server:

```sh
sudo dock admin password-sign-in on
sudo dock admin password-sign-in on --password-for ops@example.com   # also a new password, shown once
```

## Removing a provider

`dock sso remove google` removes it, the links between people and their
identities there, and the sessions signed in through it. People keep
their accounts and memberships. The last provider stays while password
sign-in is off.
