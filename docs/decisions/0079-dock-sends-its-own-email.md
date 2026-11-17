# 0079: Dock sends its own email

Status: accepted (asked by Saile, 2026-11-13; designed by the agent, mocks
picked by Saile: console page A, invite email B, sent state A). Amends
[0060](0060-invite-links-signed-out.md) ("Dock sends no email").

## Context

Inviting someone printed a link the admin had to send by hand: "Dock
doesn't send email". The only mail Dock sent was alert email, and each
email alert channel carried an SMTP server of its own. Transactional mail
(invites now; password resets and security notices next) needs one
sender that operators set once. This is Dock's own mail, not mail for
apps: Dock receives nothing.

| Force | Means here |
|---|---|
| One sender | The platform's SMTP server is the only way Dock sends mail; nothing else dials SMTP |
| Never blocks | A request that causes mail answers at once; SMTP happens after |
| Idle means idle | No polling ([performance.md](../performance.md)); no goroutine or timer with nothing queued |
| Nothing gets worse | Without email, or when a send fails, the invite link is still there to copy |
| Bearer links | An invite link is a secret Dock otherwise keeps only as a hash |

## Decision

### The platform's SMTP server

| Topic | Decision |
|---|---|
| Who | Operators read it and send tests. Setting or removing it also needs a recent confirmation (`requireRecent`, [0057](0057-two-factor-sign-in.md)): it decides where every invite link goes. All three are audited in the platform log (`platform.email_set`, `platform.email_clear`, `platform.email_test`) |
| Where | Console, Email (`/console/email`); `dock email`, `dock email set\|test\|log\|off`; `GET\|PUT\|DELETE /platform/email`, `POST /platform/email/test`, `GET /platform/email/outbox` |
| Settings | Host, port (587, or 465 for TLS), security (`starttls`, or `tls` from the start), username, password, From address, From name (default Tempo Dock), Reply-To. One row in `email_settings`. The password is sealed, bound to its column (a one-row table, [0041](0041-sealed-values-bound-to-their-place.md)), write-only: the API says `password_set` |
| STARTTLS | Used when the server offers it, and required before a password is sent. Certificates are always verified; there is no switch to skip that |
| Where it may connect | The operator's host, like the platform's alert channels ([0062](0062-org-alerts-and-operators.md)): private addresses are fine (a LAN relay), and so is this server itself (a local Postfix or msmtp), unlike every other address Dock connects to. Link-local and cloud metadata addresses never. Checked when set for literal addresses and on every dial after DNS |
| Status | Every attempt records the server's last success or its error; the console's strip and `dock email` show it, with the outbox's counts |
| Orgs | Don't set a sender. The sender's reputation, SPF and DKIM are the platform domain's, and an org-chosen From would let one org send as anyone through the platform's relay. Revisit only if a hosted Dock sells custom senders |

### The outbox

| Topic | Decision |
|---|---|
| Table | `email_outbox`: kind, org (NULL: the platform's), ref (`invite:<id>`), recipient, subject, a sealed body, status (`queued`, `sent`, `failed`), attempts, next try, error, times |
| Bodies | The rendered mail is sealed, bound to the row's random nonce, and dropped once the mail is sent or given up on. What stays is who, what, when and the outcome |
| Queuing | A mail is inserted in the transaction of what caused it (an invite and its mail commit together); the sender is kicked after the commit |
| Sender | One goroutine, counted in `c.wg`, that runs only while mail is due: a pass takes up to 20 due mails over one connection; with only retries left it waits on one timer for the earliest; with nothing queued it exits. `Start` kicks it once, so queued mail survives a restart |
| Retries | After 30 s, 2 min, 10 min, 30 min and 2 h, then given up on (about 2 h 45 min). A 5xx answer (550 no such user) fails at once. Changing the settings makes waiting mail due at once |
| Bounds | 1,000 queued across the platform and 100 per org; 10 mails an hour to one address; 60 sends a minute. Past the first two, the caller says why and shows the link |
| Live | Each change publishes an `email` event: to the org's admins for an org's mail, to operators for all. The invite section, invite rows and the console patch from it |
| Retention | Done mail goes after 30 days, in the retention pass |

### Mail

- `internal/mail`: the SMTP client (alert channels' former `sendMail`,
  now for batches), MIME (`multipart/alternative`, text first, quoted
  printable) and embedded templates, parsed on first use.
- Headers come only from parsed addresses (written by `net/mail`, which
  encodes names), Dock's own values and a one-line, encoded subject, so
  nothing a user writes adds a header. `Auto-Submitted: auto-generated`
  keeps auto-replies away. HTML is `html/template`, escaped.
- Links use Dock's public address only, never a request's host. Without
  one, mail with a link isn't sent and the caller says so.
- No tracking: no pixels, remote images, fonts or rewritten links.
- The invite is true black like Dock (mock B): who invited, the role,
  for whom, until when, one button and the link written out.

### Which mail

| Mail | To | Turn off? | When |
|---|---|---|---|
| Invite | The address invited, from `CreateInvite` and the console's invite; a new link sends again | No | Now |
| Test | The operator, or `--to` | n/a | Now |
| Password reset link | The account's email, from "Forgot password?" while password sign-in is on | No | Now |
| Security notices | The account's email: its password changed or reset, a factor added or removed, a recovery code used, new recovery codes, two-factor reset | No | Now |
| Alert email channels | Their recipients, through the platform's server: an email channel keeps only its recipients | Per channel | Now |
| Access requests | The org's admins (not temporary ones), each unless they turned it off | Yes, per user (`dock me email`, Account) | Now |
| Sign-in from a new device, email change, digests, status page subscribers | | | Later |

### Password reset by email

| Topic | Decision |
|---|---|
| Offered | While passwords sign in, email is set up and Dock has a public address (`GET /auth/methods` says `password_reset`); otherwise "Forgot password?" says who resets passwords |
| Asking | `POST /auth/password-reset {email}`, public, rate limited like logins. The answer is the same whether or not the address has an account; a disabled account gets nothing. Audited `user.password_reset_sent` |
| The link | `<public address>/reset-password/dpr_...`, 256 random bits kept as SHA-256 in `password_resets`; it works once, for an hour, and a newer link, a password change or an operator's reset replaces it. Links go a day after they expire |
| Using it | `POST /auth/password-reset/{code} {password}` sets the password (the usual rule), spends the link, ends every session and `dock login` token, keeps second factors, sends a notice. It doesn't sign in: the person signs in with the new password and their second factor. Amended 2026-11-17: on an account whose email the link proves for the first time (made from an invite), what was set up before goes too: second factors, recovery codes and tokens, as a single sign-on's takeover removes them ([0065](0065-sign-in-is-the-platforms.md)) |
| Limits | Reset links count against the per-address limit (10 an hour); security notices and alerts don't |

### Security notices and access requests

- **Notices** go to the account's own address and can't be turned off.
  When someone else acted (an operator's reset), the notice names them.
  They say what to do if it wasn't the person: reset the password, check
  the factors, tell an operator.
- **Access requests**: a new request (not a newer call replacing a
  pending one) is mailed to its org's admins with `users.email_requests`
  on, with the Inbox link. Without a public address none is sent: the
  Inbox and the org's alert channels carry it.
- Mail queued by a command that runs core on its own (`dock admin
  reset-2fa`) waits for the daemon's next pass: only a started daemon
  sends, so the two never race.

### Alert email channels

- A channel holds only its recipients (`to`); each notification is a mail
  per recipient in the outbox, its outcome the channel's status. A test
  sends at once.
- On start, a channel with an SMTP server of its own moves: a platform
  channel's server becomes the platform's when it has none (the operator
  set it up for this), and every channel keeps only its recipients.
  Audited `alert.channel_email` in its owner's log. An org that used its
  own relay keeps its recipients, not its sender.
- `dock alerts channel add --email TO,...`; the old `--smtp-*` and
  `--from` flags say where SMTP went.

**No second factor recovery by email.** Email would become a way past the
second factor; recovery codes and `dock admin reset-2fa` stay the ways
back.

## Consequences

- An invite reaches the person without the admin copying anything; the
  link is still shown once, behind "Copy the link instead" once sent.
- Invites list how their mail went (`Invite.mail`, `dock org invites`).
- Every mail Dock sends goes through one server and one outbox.
- Without email set up, reset links, notices, request mail and email
  channels don't go; channels say why in their status.
- A Dock without a public address sends no invite mail.
