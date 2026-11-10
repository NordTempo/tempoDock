# Agents and MCP

Agents (Claude Code, a CI job, any script) use a **token**, never a
person's password or the database URL. Every rule is enforced by tempoBase,
not by the agent behaving.

## Tokens

In Agents & MCP, anyone signed in creates tokens for themselves:

- **Name**, and an optional **expiry**.
- **Connections**: the ones it may use. None means every connection the
  policy allows.
- **Data access**: an optional override of the policy's `tokens` rule for
  this token, per production and non-production, and sensitive columns.

The secret (`tbt_...`) is shown once; only a hash is stored. Send it as
`Authorization: Bearer tbt_...`. Revoke it in the same place.

What a token can do:

- What the policy's `tokens` rule or its override allows, which is nothing
  by default, and never more than its creator's role allows (unless the
  creator is an admin).
- Never admin work: no setup, approvals, policy, people or connections.
- On production, `write` acts as `propose`: an agent's change to production
  data always waits for a person to approve the exact SQL.

## Connect an agent

The MCP endpoint is `POST /api/v1/mcp` (Streamable HTTP), bearer tokens
only. With the token in `TEMPOBASE_TOKEN`:

```sh
claude mcp add --transport http --scope project tempobase https://tempobase.example.com/api/v1/mcp \
  --header 'Authorization: Bearer ${TEMPOBASE_TOKEN}'
```

That writes `.mcp.json`, which other clients read too:

```json
{
  "mcpServers": {
    "tempobase": {
      "type": "http",
      "url": "https://tempobase.example.com/api/v1/mcp",
      "headers": { "Authorization": "Bearer ${TEMPOBASE_TOKEN}" }
    }
  }
}
```

Agents & MCP shows this snippet with your address filled in.

## Tools

| Tool | Does |
|---|---|
| `whoami` | The token's name, its person and its connection limit |
| `list_connections` | Connections the token may use: name, production, its level and sensitive column visibility |
| `data_status` | A connection's engine, version, setup and the token's access with the reasons |
| `data_schema` | Tables, columns, keys and relations |
| `data_rows` | A page of a table's rows, with filters and order |
| `data_query` | One read-only SQL statement |
| `data_insights` | How the database is doing: connections, slow queries, sessions and locks, table and index sizes, with hints |
| `propose_data_changes` | A changeset: committed with `write` on non-production, otherwise stored for a person to approve |

Each data tool takes `connection`, which may be left out when the token is
limited to one. The tools are Dock's, with the same names, limits and
rules. Errors come back as tool results with a code and what to do next:
`forbidden` means stop and ask a person.

Every page of this guide is a resource, `tempobase://docs/<page>`
(`tempobase://docs/agents` is this one), so an agent can read how
tempoBase works.
