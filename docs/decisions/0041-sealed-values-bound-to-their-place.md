# 0041: Sealed values are bound to where they are stored

Status: accepted

## Context

`internal/secrets` seals values at rest with AES-256-GCM: variable and
org variable values, release configs, connection tokens and keys, role
passwords. The key is a file next to the database, so a leaked database
alone reveals nothing. But GCM was used with no associated data, so a
sealed value opened wherever it was put. Someone who can write the
database (a restored, tampered backup; a bug that writes a row) could
move one org's GitHub token into another org's git connection, a
production variable into a development environment its developers can
reveal, or a production database URL into a non-production tempoBase
connection, and the daemon would decrypt and use or show it.

## Decision

| Topic | Decision |
|---|---|
| Format | The first byte of a sealed value is its format. 1 (unbound): `1 \|\| nonce \|\| ciphertext`, no associated data, as before. 2 (bound): the same with the AAD as GCM's associated data. Seal writes 2 only |
| AAD | Names the column and the row's key: `secrets.Bind("table.column", key...)`, each part length-prefixed so no two keys encode alike. The API is `Seal(plaintext, aad)` / `Open(sealed, aad)`, an AAD is only made with `Bind` or a `secrets.Column`, and the zero AAD seals and opens nothing |
| Keys | Natural keys, known before a row is inserted: a variable's environment, service (0: shared) and key; an org variable's org and key; a release's service and number; a git connection's, access key's and data panel roles' service (tempoBase: connection ID); a tunnel token's Cloudflare tunnel ID; the name of an alert channel, storage, Cloudflare or tempoBase connection; an analytics salt's day. One-row tables (backup settings, analytics settings, the GitHub App) bind the column only |
| Copies | A value that keeps its row keeps its bytes: a staged variable value becoming the live one shares the AAD (`variables.value`), and so does a release's record of the variable changes it applied (`releases.carried`), which restaging compares by bytes. A value that changes rows is opened and sealed again: an environment's clone, a rollback's or restart's release config, a renamed storage or tempoBase connection |
| Declared once | Each host lists its sealed columns (`internal/core/sealed.go`, `internal/tempobase/sealed.go`, `datapanel.AccessColumns`) as `secrets.Column` values and makes its AADs from them, so the reseal and the code agree. A new sealed column goes in that list |
| Migration | On start (core.New, tempobase.Open), when `install.secrets_format` is 1, every format-1 value of every listed column is resealed bound to its row, then the format is set to 2. Batches of 100 rows per write transaction; each update matches the row's key and its old bytes, so a concurrent change wins. A value that doesn't open is logged and left as it is |
| Idempotent | `Box.Reseal` derives the nonce from the value and the AAD (HMAC-SHA256 with a key derived from the secrets key), so resealing one value at one place always gives the same bytes: a run that was interrupted, or one in an admin command next to the daemon, writes what the other would, and a release's carried copy of a variable value matches the variable again |
| After | Once the format is 2 the box refuses format-1 values (`RefuseUnbound`), so an old value pasted into a row doesn't open there either |
| Cursors | The data panel's page cursors are sealed with the AAD `datapanel.cursor`; the database and caller stay in the plaintext they were bound by |

## Consequences

- A bound value moved to another row or column fails to open: the
  daemon reports it, and the remedy is to set the value again.
- Renaming a storage or tempoBase connection reseals its credentials.
- Rows are bound to keys, not to what other columns say: someone who can
  write the database can still flip a connection's production flag or
  rename rows. Binding protects where a value is, not the metadata
  around it.
- Until the first start of this version finishes, format-1 values open
  anywhere. The format marker lives in the database, so someone who can
  write it and kept an old format-1 value could reset the marker and have
  the next start bind the value where they put it.
- No downgrade: an older binary can't open format 2. The updater's
  rollback restores the database from its pre-update backup
  ([0028](0028-updates-from-the-console.md)), which still holds format 1.
- Page cursors handed out before the update stop working; the panel
  starts from the first page.
