# Two-factor sign-in

A password alone shouldn't reach production. With two-factor sign-in, the
web UI and `dock login` also ask for a code from an authenticator app on
your phone. Organizations can require it of their members, and the
platform of its operators. API tokens are credentials of their own and are
not affected. The design is [ADR 0050](../decisions/0050-two-factor-sign-in.md).

## Setting up an authenticator app

Setup happens in the browser, on your account page (`/account`):

1. Add an authenticator app. Dock shows a QR code, and the key as text
   for typing in.
2. Scan it with any authenticator app (1Password, Bitwarden, Google
   Authenticator, Aegis, ...).
3. Type the 6-digit code the app shows. That confirms the app works and
   turns two-factor sign-in on.
4. Dock shows ten recovery codes, once. Store them somewhere safe, away
   from your phone.

Adding your first factor ends your other UI sessions, which never proved
it. One authenticator app per account.

```
$ dock me two-factor
KIND               NAME   ADDED   LAST USED
authenticator app  phone  3d ago  2h ago

10 unused recovery codes
```

`dock me two-factor` lists your factors; the CLI never sets them up.

## Recovery codes

Ten codes of 16 characters, for when your phone is lost or out of reach:

- Each works once, and only to finish a sign-in, never to confirm a
  sensitive action.
- Creating new codes on your account page replaces all the old ones.
- Run low? Create new ones while you can still sign in.

## Signing in

1. Email and password, as before.
2. A code from your authenticator app, or a recovery code.

The second step has five minutes. Five wrong codes end the attempt and you
start again with your password. Wrong codes count toward the same limit as
wrong passwords, per email and address, so someone who knows your password
can't lock you out by guessing codes from elsewhere. Every wrong code is in
the audit log.

## Confirming sensitive actions

A browser session may do sensitive things for 15 minutes after you sign in
or confirm. After that, the UI asks you to confirm it's you (with your
authenticator app, or your password if you have no second factor) and then
carries on. Sensitive means:

- Revealing secrets: variables, organization variables, staged changes,
  connection URLs, the backup password.
- Creating tokens and approving a `dock login`.
- Inviting people, changing roles, removing members.
- Approving an access request for a locked environment, and unlocking or
  opening one.
- Operators creating, resetting, disabling or deleting accounts.
- Changing the organization's or the platform's sign-in policy.
- Adding or removing factors and recovery codes.

Tokens never ask: a token is its own credential, and creating it was the
check.

## `dock login`

`dock login` opens the browser, where you sign in (with your code) and
approve the CLI. Approving creates a token, so a sign-in older than 15
minutes confirms first.

The token remembers whether the approving sign-in used a second factor,
and only then counts as two-factor. After adding your first factor, run
`dock login` again for a token that counts. `dock whoami` says which you
have:

```
$ dock whoami
...
two-factor    yes: approved from a two-factor sign-in
```

## Requiring two-factor sign-in

| Who | Set by | Command |
|---|---|---|
| Members of an organization | An admin with access to every project, in the organization's settings | `dock org two-factor on` |
| Operators of the platform | An operator, in the console | `dock security operators-two-factor on` |

Turning either on needs your own sign-in to have used a second factor, so
you can't lock yourself out.

While an organization requires it, a member whose sign-in (or `dock login`
token) used no second factor stays signed in but can't act in that
organization. Everything there answers `two_factor_required` (exit 3 in the
CLI) and says what to do; the account page still works, so they can add a
factor and sign in again.

While the platform requires it, such an operator keeps their roles in
organizations but can't run the platform (servers, backups, accounts,
settings) until they add one.

You can't remove your last factor while an organization of yours or the
platform requires one. `dock user list` shows who has two-factor sign-in on.

## Lost your phone and your recovery codes

Someone with shell access to the server resets it, as root or the service
user:

```sh
sudo dock admin reset-2fa ada@example.com
```

It removes the person's factors and recovery codes, ends their sessions and
`dock login` tokens, and records `user.2fa_reset` in the platform's audit
log. They sign in with their password and set up a new factor. It works
while the daemon runs.

There is deliberately no button for this in the UI: a stolen operator
session must not be able to strip someone's second factor. On a
single-user install, shell access to the server is the way back in.
