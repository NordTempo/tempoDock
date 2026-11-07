# 0060: Invite links work signed out, with a sign-up step

Status: accepted (asked by Saile, 2026-11-05; designed by the agent)

## Context

An invite ([0027](0027-organizations.md), phase 2) is a link with a code
that works once, for 7 days, for one email. Reading it needed a session,
so:

- someone opening the link signed out saw only the sign-in page, not
  which organization asked them in, as what, or by whom;
- someone invited without an account had no way in at all: the sign-in
  page said "ask the admin who invited you", and the admin had to ask an
  operator to create the account (`dock user create`), defeating the
  point of the link.

0027's phase 3 planned `signup = closed | invite | open`, with Dock
invite-only for now: no sign-up page, people join by an invite. This is
that "invite" level, made usable.

## Decision

| Topic | Decision |
|---|---|
| Reading an invite | `GET /api/v1/invites/{code}` is public. It answers an open invite with the organization (name and slug), the role, the invited email, who invited and until when; nothing else (no members, no projects, no other invites). A code that is unknown, used, revoked or expired is 404 with what to do |
| Creating the account | `POST /api/v1/invites/{code}/signup`, public, with a name and a password. The email is the invite's and can't be changed. In one transaction it creates the user, spends the invite and adds the membership with the invite's role, then signs in as a password login does (`signIn`): a session cookie, audited `user.login` |
| Password and name | The same rule as every other password a person sets (`checkNewPassword`: at least 12 characters, at most bcrypt's 72 bytes). A name is 1 to 64 characters, trimmed, no control characters, as `PATCH /me` takes it |
| An existing account | Refused (409): the email signs in and accepts the invite as before. A sign-up never takes over or changes an existing account |
| Single sign-on | While the platform's password sign-in is off ([0065](0065-sign-in-is-the-platforms.md)), no password accounts are made this way (403): the person continues with an identity provider as the invited email, and a new account's open invites are accepted then |
| Second factor | A new account has none. An organization that requires one ([0057](0057-two-factor-sign-in.md)) leaves the fresh session outside it, as for any member without a factor, and the account page asks them to add one |
| Rate limits | Both endpoints spend from the login limiter (`httpkit.LoginLimiter`): reading and failed lookups per client IP, a sign-up for a known code per (email, IP) with the email backstop, like a password attempt |
| Audit | `user.create` ("created user X from an invite to org") and `org.join` ("joined as R, creating their account from the invite"), as SSO's first sign-in records them |
| UI | The invite page reads the invite before anything else, signed in or not, and shows the organization, role, email, inviter and expiry. Signed in as the person invited: Join (`dock org join`). Signed in as someone else: says so and offers to switch account. Signed out: **Sign in to accept** (back to the page afterwards) and **Create an account**, a form in the page |

### Why a public endpoint is safe here

The invite code is a bearer secret: 256 random bits (`newSecret`), stored
only as its SHA-256, shown once to the admin who created it. Whoever holds
it is, by the admin's choice, the person the link was sent to. It is also
bound:

- **to one email.** The account it creates has the invite's email; the
  person can't pick another. Accepting it signed in still requires a
  session with that email. So a leaked link can create at most the one
  account the admin meant, with the role the admin chose, in the org the
  admin chose.
- **to one use and 7 days.** Joining spends it in the same transaction
  that adds the member (`joinTx`, `AcceptOrgInvite` only spends an
  unaccepted, unrevoked row), so two concurrent sign-ups can't both join.
  Revoking it before use ends it.
- **to nothing more than it says.** The public answer shows only what the
  holder needs to decide to accept. Guessing codes is hopeless at 256 bits
  and is rate limited anyway, so the endpoint can't be used to enumerate
  invites, emails or organizations.

What the holder of a leaked link gains is what the invited person would
have: one account and membership. The same is true of an emailed password
reset link elsewhere, and the mitigations are the same: short life, one
use, revocable, audited. Invite links should still go to the person
privately.

The email isn't verified by this: Dock sends no email (0027, phase 3).
Whoever holds the link owns the account for that address. An admin who
sends a link to the wrong place revokes it; the account it made can be
disabled or deleted by an operator like any other.

## Consequences

- Inviting someone new no longer needs an operator: the org's admin sends
  a link and the person is in, in one page.
- No platform switch turns invite sign-up off. Invites are created only by
  an org's admins, already the gate; `signup = closed` (0027, phase 3)
  would add one, and `SignUpWithInvite` is where it would be checked.
- `InviteInfo` lost its `status`: only open invites are shown, so it was
  always "open"; dead links are 404s with their reason.
- The sign-in page's note for invite links changes from "ask the admin who
  invited you" to pointing back at the invite to create the account.
