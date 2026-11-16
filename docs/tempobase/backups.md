# Backups

Everything tempoBase knows is in two files:

| File | Holds | Without it |
|---|---|---|
| `<data_dir>/tempobase.db` | People, connections, policy, tokens, saved queries, changesets, audit | Start over |
| `secrets_key_file` (default `<data_dir>/secrets.key`) | The key that encrypts saved connection URLs and the roles' passwords | Every connection must be re-added with its URL and set up again |

Back up both, and keep the key somewhere safe: anyone with the database
and the key can read your connection URLs. Keeping the key outside the
data directory (`secrets_key_file` on a mounted secret) means a stolen
volume backup is useless on its own. Your databases' own data is not in
tempoBase; back that up as you do today.

## Back up

`tempobase admin backup FILE` writes a consistent copy of the database
while the server runs:

```sh
sudo tempobase admin backup /var/backups/tempobase-$(date +%F).db
sudo cp /var/lib/tempobase/secrets.key /var/backups/   # once; it doesn't change
```

Don't copy `tempobase.db` itself while the server runs: it may be caught
mid-write. Run the backup from cron or a systemd timer; it takes a moment
and holds no lock that blocks people.

## Restore

1. Stop tempoBase.
2. Put the backup in place as `<data_dir>/tempobase.db`, remove any
   `tempobase.db-wal` and `tempobase.db-shm` next to it, and put the key
   file back.
3. Start tempoBase. A backup from an older version is migrated on start.

The same steps move tempoBase to another server. Sessions survive, so
people stay signed in.

## A lost key

Without the key file, a new one is created on the next start. People,
policy, tokens and history are intact, but the saved URLs and role
passwords can't be decrypted. For each connection, an admin pastes its URL
again (Change in the connection's settings) and runs setup, which rotates
the role passwords.
