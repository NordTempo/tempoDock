# Single sign-on

People can sign in to Dock with Google, GitHub, Microsoft Entra ID,
GitLab, Okta, Auth0, Keycloak, Authentik, or any other identity provider
that speaks OpenID Connect. The sign-in page shows a **Continue with** button for each,
next to the email and password form. Providers are the platform's:
operators set them up once for the whole Dock, and organizations still
decide who is a member. The design is [ADR 0065](../decisions/0065-sign-in-is-the-platforms.md),
amending [ADR 0058](../decisions/0058-sso-oidc.md); the named providers
are [ADR 0081](../decisions/0081-named-identity-providers.md).

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
| Microsoft Entra ID | App registrations, New registration (single tenant), Web redirect URI; Certificates & secrets; Token configuration, the optional ID token claims `email` and `xms_edov` | Tenant (its ID or a domain of it), client ID and secret |
| GitLab | User settings (or a group's, or Admin area), Applications, scopes `openid email profile` | Client ID and secret; a self-hosted GitLab's URL |
| Okta | Applications, Create App Integration, OIDC, Web Application, Sign-in redirect URI | Okta domain, client ID and secret |
| Auth0 | Applications, Create Application, Regular Web Application, Allowed Callback URLs | Auth0 domain, client ID and secret |
| Keycloak | The realm's Clients, Create client, OpenID Connect, Client authentication on, Valid redirect URI | Keycloak URL, realm, client ID and secret |
| Authentik | Applications, Create with provider, OAuth2/OpenID, Confidential, Redirect URI | authentik URL, the application's slug (as `--realm`), client ID and secret |
| Any other OpenID Connect provider | A web application ("OAuth client", "OIDC app") with the scopes `openid email profile` and the authorization code grant (Dock uses PKCE) | Issuer URL, client ID and secret, and a label for its button |

```
$ printf %s "$CLIENT_SECRET" | dock sso add google --client-id 1234.apps.googleusercontent.com --client-secret-stdin
✓ Added identity provider google: the sign-in page shows Continue with Google
  Redirect URL to register at the provider: https://dock.example.com/api/v1/auth/sso/callback

$ printf %s "$CLIENT_SECRET" | dock sso add github --client-id Ov23li... --client-secret-stdin

$ printf %s "$CLIENT_SECRET" | dock sso add microsoft --tenant contoso.onmicrosoft.com \
    --client-id 0f1e2d3c-... --client-secret-stdin

$ printf %s "$CLIENT_SECRET" | dock sso add staff --kind keycloak \
    --domain id.example.com --realm staff --label "Example ID" \
    --client-id dock --client-secret-stdin

$ printf %s "$CLIENT_SECRET" | dock sso add corp --kind oidc \
    --issuer https://id.example.com/oauth2/default --label "Example ID" \
    --client-id dock --client-secret-stdin
```

Named after a kind (`google`, `microsoft`, `okta`, ...), a provider is
that kind; any other name needs `--kind`. Dock builds the issuer from the
tenant, domain or realm and reads its discovery document
(`<issuer>/.well-known/openid-configuration`) right away, so a wrong
domain fails here. The client secret is stored encrypted and never shown
again; replace it with `dock sso set <name> --client-secret-stdin`.

### Microsoft Entra ID

- Dock signs in one tenant's people. `common`, `organizations` and
  `consumers` are refused: they would let any tenant's owner decide which
  email their users have. A tenant given by its domain is stored by its
  ID.
- Entra ID doesn't verify the `email` claim. Dock signs someone in only
  when the optional claim `xms_edov` says the email's domain is verified,
  so add `email` and `xms_edov` to the ID token in Token configuration.
  Without them, sign-ins are refused with a message saying so.

### Issuers of other providers

With `--kind oidc`, the issuer must match what the provider puts in its
tokens exactly, trailing slash included.

## Who gets in

The provider must say the email is verified.

- **Someone with a Dock account** signs in: the first time Dock finds
  the account by its email and links it to the provider's identity;
  after that the identity finds it, whatever email the provider sends.
  An account made from an invite link never proved its email (whoever
  held the link chose its password), so that first sign-in takes it over
  instead: its sessions and tokens end, its password stops working and
  its second factors are removed.
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
  provider again (or use your Dock second factor). Dock asks the provider
  to sign you in again even if you are still signed in there, and counts
  a sign-in as confirmed when you last signed in at the provider, so
  signing in to Dock on a provider session hours old asks you to
  confirm before sensitive actions. GitHub can't be asked: its step-ups
  reuse your GitHub session.

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

## Signing out

Signing out of Dock ends the Dock session, and for a provider that
supports it (OpenID Connect RP-Initiated Logout: its discovery document
has an `end_session_endpoint`) the provider's session too: the browser
goes to the provider, which comes back to Dock's sign-in page. The next
**Continue with** then asks for the password again. Dock sends the ID
token the session signed in with (`id_token_hint`, kept encrypted with
the session), its client ID, and the sign-out URL,
`https://<your Dock address>/login` (the Sign-in page and `dock sso list`
show it). Register it at the provider, or the provider refuses to send
the browser back:

| Provider | Register the sign-out URL as |
|---|---|
| Keycloak | the client's Valid post logout redirect URIs |
| Okta | the app's Sign-out redirect URIs |
| Microsoft Entra ID | another Web redirect URI |
| Auth0 | Allowed Logout URLs (and turn on RP-Initiated Logout End Session Endpoint Discovery in the tenant's Advanced settings) |
| Authentik | the provider's Post logout redirect URIs |
| Any other OpenID Connect provider | its post-logout redirect URI, if it has an end-session endpoint |

Google, GitHub and GitLab have no such endpoint: signing out ends only
the Dock session, and the provider's session stays (the next **Continue
with** signs straight in). So do sessions whose provider doesn't answer
within three seconds.

Not built yet:

- **Group or role mapping.** A provider says who someone is; membership
  and roles come from invites and admins (ADR 0065 dropped mapping on
  purpose).
- **Back-channel logout.** Ending someone's session at the provider, or
  disabling them there, doesn't end their Dock sessions: those last up
  to 30 days. Disable the account in Dock (Console, Users) to cut them
  off at once.
- **Old sessions after a step-up.** Confirming through a provider signs
  in again with a new session; the one before stays until it expires.

## Removing a provider

`dock sso remove google` removes it, the links between people and their
identities there, and the sessions signed in through it. People keep
their accounts and memberships. The last provider stays while password
sign-in is off.
