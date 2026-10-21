# 0005: OpenAPI API with generated clients

Status: accepted

## Context

The CLI and UI must have full parity, so all logic lives behind one API. The UI
is TypeScript and the server is Go, so we need end-to-end types across that
boundary. Agents should also be able to call the API directly.

Options:

- **Huma (OpenAPI 3.1 from Go types):** plain JSON over HTTP, spec generated
  from handler structs, easy to `curl`, spec doubles as API docs.
- **ConnectRPC (protobuf):** stricter schema and built-in streaming, but adds a
  schema language and a codegen step on both sides.

## Decision

- Huma for the HTTP API, versioned under `/api/v1`.
- The UI uses a client generated with `openapi-typescript` + `openapi-fetch`.
  Generation runs in CI and fails the build if the committed client is stale.
- The CLI uses the Go types directly.
- Live data (deploy progress, logs, status) over Server-Sent Events.

## Consequences

- One source of truth for the API contract: the Go handler types.
- Agents and scripts get documented JSON endpoints for free.
- SSE is one-directional, which covers every live use case we have.
