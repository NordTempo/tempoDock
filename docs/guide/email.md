# Email

Dock sends its own mail through one SMTP server that operators set up for
the whole Dock: invites, password reset links, security notices, alert
email and access requests.
Organizations don't set a sender of their own. Dock never receives mail.
The design is [ADR 0079](../decisions/0079-dock-sends-its-own-email.md).

## Setting it up

You need to be an operator. In the browser it's **Console → Email**; the
CLI is `dock email`. Any SMTP service works (Postmark, Amazon SES, Resend,
Fastmail, your own Postfix). Use a From address on a domain whose SPF and
DKIM the service signs for, or mail lands in spam.

```
$ printf %s "$SMTP_PASSWORD" | dock email set --host smtp.postmarkapp.com --username "$SMTP_USER" --password-stdin --from dock@example.com
✓ Dock sends mail through smtp.postmarkapp.com:587
  Send a test with `dock email test`.
$ dock email test
✓ Sent a test email to you@example.com through smtp.postmarkapp.com:587
```

- Port 587 uses STARTTLS, required before the password is sent; port 465
  (`--security tls`) is TLS from the start. Certificates are verified.
- The server may be on your network or on the server itself
  (`--host 127.0.0.1 --port 25` for a local Postfix with no login).
- The password is stored encrypted and never shown again; `dock email
  set` without `--password-stdin` keeps it.
- Links in mail use Dock's public address (`dock address`). Without one,
  invites aren't emailed, since their links would only open on your
  network.

## What happens to a mail

Mail goes through an outbox: the request that causes it (an invite)
answers at once, and Dock sends it right after. A server that doesn't
answer is tried again after 30 seconds, 2, 10 and 30 minutes and 2 hours;
a recipient the server refuses fails at once. Fixing the settings retries
what is waiting straight away. `dock email` shows whether sending works
and what is waiting; `dock email log` and the Email page list the latest
mail and how it went.

Dock sends at most 60 mails a minute and 10 an hour to one address.

## Invites

With email set up, an invite is emailed to the person, and the Members
page says when it was sent, or why it failed. The link is still shown
once, under **Copy the link instead**, so you can send it yourself too.
Without email the page shows the link to copy, as before.

## Forgot password

With email set up and a public address, **Forgot password?** on the
sign-in page emails a link that sets a new password. It works once, for
an hour; the answer is the same whether or not the address has an
account. Setting the password signs the person out everywhere and keeps
their second factors. Without email, the page says to ask an operator
(`dock user reset-password`).

## Security notices

Dock emails an account's own address when its password changes or is
reset, a second factor is added or removed, a recovery code signs in or
new codes are made, and when an operator resets its two-factor sign-in.
These can't be turned off.

## Alert channels

Email alert channels send through this server: a channel is just its
recipients (`dock alerts channel add mail --email ops@example.com`).
Channels added before Dock had its own server moved onto it on upgrade; a
platform channel's server became Dock's when none was set.

## Access requests

When an agent's change in a locked environment becomes an access request,
the organization's admins get a mail with the Inbox link. Each turns it
off on their Account page or with `dock me email access-requests off`.
