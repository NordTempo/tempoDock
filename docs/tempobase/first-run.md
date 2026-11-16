# First run

## The first admin

With no users yet, `tempobase serve` logs a one-time setup link:

```
no users yet: open https://tempobase.example.com/setup#code=... to create the first admin
```

Open it, enter your email, name and a password (at least 12 characters)
and you are signed in as admin. The code lives only in memory: it is gone
once used or when the server restarts, which prints a new one. It sits
after `#`, so it never reaches a proxy's logs.

Find the link with `journalctl -u tempobase` or on the
terminal. Without `base_url` the link can't know your public address;
open it through the address you actually use.

For automation, skip the link and create the admin on the server:

```sh
read -rs pw && printf '%s\n' "$pw" | tempobase admin create-user --email you@example.com --name You --role admin
```

The password comes from stdin, so it never shows up in the process list or
shell history.

## Signing in

Sign in at `/login` with email and password. Sessions are cookies that last
30 days. Changing your password (Account, under your name at the foot of the
sidebar) signs out your other sessions. Repeated failures are rate limited per address and per email,
and each one is in the audit log.

## Next

1. Add a database: [Connections](connections.md).
2. Invite people: [People and the policy](people.md).
3. Give an agent a token: [Agents and MCP](agents.md).
4. Back up the data directory and the key: [Backups](backups.md).

## A lost admin

Any admin can create a reset link for another user in Access.
If no admin can sign in, on the server:

```sh
tempobase admin reset-link --email you@example.com
```

It prints a link that sets a new password, valid for 24 hours and once.
To make a new admin instead: `tempobase admin create-user ... --role admin
--link`.
