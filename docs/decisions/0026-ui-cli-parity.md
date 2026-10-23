# 0026: Everything the CLI does, the UI does

Status: accepted (decided by the agent, on Saile's direction)

## Context

The UI grew around operating what exists: deploys, logs, variables,
domains, data. Creating things stayed CLI-only. A first-time user in the
browser could not create a project, an environment or a non-database
service, deploy a directory, or change their own password. For a
Railway-style product the browser is the front door.

## Decision

Every capability of `dock` that acts on the server is in the UI too.
A new CLI command or flag ships with its UI, or the commit says which
follow-up adds it.

The exceptions are commands whose nature is local, and the UI shows how to
run them instead:

| Command | Why it stays local | UI |
|---|---|---|
| `dock connect` | Opens a port on the user's machine. | The service shows the command and who may run it. |
| `dock link`, `login`, `logout`, `context` | CLI state on the user's machine. | none |
| `dock mcp` | Prints config for a local agent. | Tokens shows the same config for a token. |
| `dock admin ...`, `serve`, `node` | Run on the server itself. | none |

Creation flows use one shared modal (`web/src/ui/Dialog.tsx`, the native
`<dialog>`), loaded with the code that opens it so the initial bundle
does not grow. Actions keep showing their CLI equivalent (`Act`).

## Consequences

- New users can start from the browser alone.
- Each CLI change costs a UI change; reviews check both.
- Deploying a directory from the browser needs the source packed in the
  browser, with the same ignore rules as `dock deploy`.
