# Agent eval

How well AI agents use Tempo Dock. Agents are a primary user (ADR 0017,
[docs/guide/agents.md](../../docs/guide/agents.md)), and unlike UI taste
their experience is measurable: give a real model a task and a scoped
token, then check what it left behind.

```sh
make agent-eval                                         # every case, MCP, Sonnet
make agent-eval AGENT_EVAL_FLAGS='-cases rollback|diagnose'
go run ./scripts/agenteval -mode cli                    # the CLI with --json instead
go run ./scripts/agenteval -model claude-opus-5-5 -runs 3
go run ./scripts/agenteval -dry                         # setup and checks, no model
go run ./scripts/agenteval -enforce                     # grants decide (ADR 0080), not scopes
go run ./scripts/agenteval -list
go run ./scripts/agenteval -tools                       # each MCP tool's size, largest first
```

Needs `claude` (Claude Code) logged in, and Docker for the data cases.
Not in CI: every case is a real model run.

## How a case runs

1. A fresh `dock serve` on the fake runtime, with its own data dir and
   loopback ports, so cases run in parallel (`-parallel`, default 4) and
   never meet another daemon. Data cases also get their own Postgres or
   Redis container, which the fake runtime dials (`DOCK_FAKE_DIAL`).
2. `Setup` seeds it as the admin through the CLI: usually project `shop`
   with `staging` and `production`, `web` and `api` deployed in each.
   `DOCK_FAKE_*` hooks add logs, spans, vulnerability findings and
   pending domains (dev/config.toml lists them).
3. The agent gets a scoped token (deploy on `shop/staging` unless the
   case says otherwise) and runs headless: `claude -p` in an empty
   directory with no settings, CLAUDE.md or other MCP servers, and only
   Dock's tools (MCP), or only Bash limited to `dock` (CLI).
4. `Admin`, when set, plays the person in the UI: it approves or rejects
   the agent's access requests.
5. `Check` reads the end state through the API as the admin, and the
   agent's final answer.

`-dry` runs setup and checks without an agent: every check must fail,
or it checks nothing.

## Output

Per case: pass or fail with the reason, tool calls, input tokens (all
turns, cached ones included: what the context cost) and output tokens.
Per run: the totals, and what the MCP tool list costs in context (the
input tokens of "Reply OK" with Dock's server minus without it).
Everything, transcripts and daemon logs included, lands in
`.data/agenteval/<time>/`. The tool list is measured as a deploy token
limited to one environment sees it, the token the guide recommends; its
title and annotations aren't sent to the model, so only names,
descriptions, schemas and the instructions count.

## Adding a case

Cases live in `cases_*.go`, one file per area. A good case is a task a
developer would really give an agent, phrased the way they would, with
a check of the outcome rather than of the tools used: an agent that gets
there another way still passes. Refusal cases check that nothing changed
and that the answer says why. Mine real failures: when an agent misuses
Dock, the misuse is a case.

## Other agents

`-mode manual` runs the cases for an agent outside the harness, such as
another vendor's: for each case it sets up the daemon and writes
`<out>/tasks/<case>.json` (the prompt, `DOCK_URL` and `DOCK_TOKEN` for
the CLI, the MCP endpoint's URL and authorization header, and where the
answer goes), then waits up to 90 minutes for
`<out>/tasks/<case>.answer.txt` and checks the result. The agent uses
the CLI or MCP (JSON-RPC over HTTP POST). Tool calls and tokens aren't
recorded in this mode.
