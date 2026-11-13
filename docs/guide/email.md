# Email

Dock sends its own mail through one SMTP server that operators set up for
the whole Dock: invites now, password resets and security notices next.
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
