# 0057: Two-factor sign-in, step-ups and break-glass

Status: accepted (asked by Saile, 2026-11-03; designed by the agent); amended by
[0065](0065-sign-in-is-the-platforms.md) (a provider's sign-in counts as two-factor; a step-up without a Dock
factor signs in through the provider again)

## Context

The UI controls production, and a password was the only thing between a
browser and it. Saile asked for passkeys and authenticator apps as second
factors, one-time recovery codes, a recent re-check before sensitive
actions, org and platform settings that require a second factor, and a
way back in for an admin who lost theirs.

| Force | Means here |
|---|---|
| Humans only | 2FA is about people in a browser and `dock login`. API and MCP tokens are credentials of their own and stay as they are |
| No lockout by guessers | The password limiter (#54) is per (email, IP) with an email backstop, so a guesser can't lock the owner out. Second factors must not undo that |
| Never locked out of your own server | A single-user install has nobody else to ask. The server itself is the last way in |
| Simple | No dependency for TOTP (RFC 6238 is 40 lines); one small, permissively licensed library for WebAuthn when passkeys land |
| Idle means idle | No timers: pending sign-ins and setups expire when read |

## Decision

Shipped in steps: authenticator apps, recovery codes, step-ups and the
policies first; passkeys next (same factor table, one more kind, below);
SSO in its own ADR.

### Factors and sign-in

| Topic | Decision |
|---|---|
| Authenticator app (TOTP) | RFC 6238: HMAC-SHA1, 6 digits, 30 s, one step of drift either side. A 160-bit key, sealed in `user_factors.secret` (bound to user and kind, ADR 0041). One app per user. Each time step works once per user (`last_step`), so a code can't be replayed, even within its window |
| Recovery codes | Ten codes of 16 characters (80 bits) when the first factor is added, shown once. Stored as SHA-256, each spent once. New codes replace all old ones. They finish a sign-in, never a step-up |
| Sign-in | Password first. A user with a factor gets a pending session (`sessions.pending`, five minutes, its own cookie `dock_login` scoped to `/api/v1/auth`) that authenticates nothing. `POST /auth/login/verify` with a code replaces it with a real session. Five wrong factors end the pending sign-in |
| Rate limits | Codes spend from the same `LoginLimiter` as passwords, per (email, IP), so a guesser on one address is stopped there. Every wrong factor is audited. On top, every wrong code from any sign-in or step-up counts per user (`users.factor_failures`), cleared by a right one: after 10 in a row each next try waits twice as long (from a second up to an hour), and at 100 codes are refused until `dock admin reset-2fa` or a password reset. That caps guessing with a stolen password at about 24 tries a day once the waits reach an hour, and a lock within days, from any number of addresses. Only someone who has the password reaches the code check, so this can't be used to lock out an owner whose password is safe; one whose password leaked is told to reset it |
| Step-up tries | A session that fails to confirm 5 times ends, so a stolen session cookie can't guess codes for long |
| Setup | One authenticator app setup, and one passkey prompt per purpose, in flight per user; starting again replaces it |
| What a session remembers | `sessions.factor`: the second factor its sign-in used (`passkey`, `totp`, `recovery`; later `sso`). Adding a factor in a session upgrades that session |
| First factor | Adding the first factor creates the recovery codes and ends the user's other sessions, which never proved it |

### Passkeys

WebAuthn credentials, as a second factor after the password, alone
(passwordless) and for step-ups.

| Topic | Decision |
|---|---|
| Relying party ID | The host the browser is on, taken from the request's `Origin`, accepted only for Dock's public address (the origin must equal `publicURL()` exactly: scheme, host, port) or `localhost` / `*.localhost` (development, SSH tunnels). Never an IP, even as the public address: a passkey is bound to its RP ID, and an IP or a host Dock doesn't own would bind it to something that can change hands. One origin per ceremony, so the answer's origin must be the one that asked |
| Ceremonies | Every challenge is a ceremony (`ceremonies.go`): in memory for five minutes, bound to the purpose (register, login, second, reauth), the user and session that asked (the pending session for a second factor), and taken out when answered, so an answer works once |
| Storage | `user_factors` kind `passkey`: `credential_id` (unique across users) and the go-webauthn `Credential` as JSON, sealed with `factorAAD(user, "passkey", credential ID)` |
| User verification | Preferred when the passkey is a second factor or a step-up (the password or session is the other factor). Required for passwordless sign-in, where the device's PIN or biometrics is the second factor; the user handle (the user ID as 8 bytes, nothing personal) must name the credential's own user |
| Sign count | Stored after every use. A count that goes backwards means a cloned authenticator: refused and audited (`user.passkey_cloned`). The new count is written only if the stored credential is still the one read (`UsePasskey` compares the old sealed value), so two sign-ins racing with the same passkey can't both pass. Authenticators that always report 0 (synced passkeys) are fine |
| Rate limits | Starting a passkey sign-in spends from the client IP's budget (`AllowIP`): it names no account. Answers spend from the same per (email, IP) budget as passwords and codes |
| Registration | Resident key and user verification preferred, "none" attestation (Dock doesn't vet authenticator makes), the user's passkeys excluded so a device isn't added twice. Adding and removing are audited (`user.2fa_add`, `user.2fa_remove`) and need a recent step-up |
| Library | [go-webauthn](https://github.com/go-webauthn/webauthn) (BSD-3; its dependencies are MIT, BSD and Apache-2.0). WebAuthn's attestation formats, COSE keys and CBOR are a lot of code to get exactly right, unlike TOTP; it is the maintained Go implementation, used by Authelia among others. Dock uses its validation and none of its HTTP or session handling |

### Step-ups for sensitive actions

A UI session may do sensitive things for **15 minutes** after signing in
or confirming (`sessions.verified_at`). After that the action answers
`403 reauth_required`; the UI asks the person to confirm and retries. A
user with a second factor confirms with it, a passkey or an
authenticator app code (not the password, not a recovery code); one
without confirms with their password.

Sensitive: revealing secrets (variables, org variables, staged changes,
connection URLs, the backup password), creating tokens, approving a
`dock login`, inviting people, changing roles or removing members,
operators creating, resetting, disabling or deleting accounts, changing
the org's or platform's sign-in policy, and adding or removing factors
and recovery codes.

**Approving an environment lock's access request (ADR 0044) is
sensitive too**: it lets an agent change a locked environment, as does
unlocking or opening one. Both call `Actor.requireRecent` in core
(`DecideRequest` when approving, `UnlockEnvironment`), so every path to
them is covered. Rejecting a request and locking need no step-up: they
only take power away.

15 minutes: long enough that a burst of admin work asks once, short
enough that an unlocked laptop left behind doesn't hand out secrets.

The data panel's queries (`DataQuery`, writes included) don't ask for a
step-up: who may read or write which database is the data policy's job
(ADR 0016), with production writes confirmed by name. Revealing a
connection URL does ask.

Tokens skip step-ups: a token is its own credential, and its creation
was the step-up.

### `dock login` with two factors

`dock login` opens the browser page where a signed-in user approves the
CLI's code (`internal/core/cli_login.go`). With 2FA:

1. The browser signs in as above: password, then the second factor, or
   a passkey alone.
2. Approving the CLI creates a token, so it is a step-up: a session older
   than 15 minutes confirms first.
3. The token remembers whether the approving session used a second factor
   (`tokens.two_factor`), and counts as two-factor wherever that's
   required. A token from a session without one doesn't; its owner runs
   `dock login` again after adding a factor.

### Policies

| Policy | Decision |
|---|---|
| Org: require two-factor for members | `orgs.require_two_factor`, set by an unlimited admin (`PUT /org/two-factor`, org settings). A member whose session or `dock login` token used no second factor stays signed in but acts in no org there, and every org operation says why (`two_factor_required`); their account page still works so they can add one. Turning it on needs the admin's own session to have used one |
| Platform: require two-factor for operators | `platform_settings.operators_two_factor`, held in memory. An operator's session without a second factor keeps their org roles but loses operating the platform until they add one. Turning it on needs the caller's own session to have used one |
| Removing your last factor | Refused while an org of yours or the platform requires one |

### Break-glass

On the server, as root or the service user:

```sh
sudo dock admin reset-2fa <email>
```

It opens the database directly (like `dock admin bootstrap`), removes
the user's factors and recovery codes, ends their sessions and `dock
login` tokens, and audits `user.2fa_reset`. They sign in with their
password and set up a new factor. Shell access to the server is the
strongest proof Dock can ask for, and it needs nobody else. An operator
can't reset another user's 2FA from the UI: a stolen operator session
must not be able to strip someone's second factor.

## Consequences

- A password alone no longer reaches production for users who add a
  factor, and orgs can require it.
- Passkeys work only on Dock's public address and localhost. An install
  reached by IP alone offers authenticator apps; one whose public address
  changes needs its passkeys added again (the old ones stay listed until
  removed).
- Sessions from before this change have no `verified_at`: their first
  sensitive action asks to confirm.
- The UI handles one more error (`reauth_required`) in one place, the API
  client, and retries the call.
- Lost factors plus lost recovery codes need shell access to the server.
  That is the intended price.
- tempoBase has its own login and is unchanged.
