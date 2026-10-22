import { ApiError } from "../api/client";
import type { components } from "../api/schema";

// TODO(m3): typed placeholders for the server and promote API the Core and
// Surface tracks are adding (ADR 0014 section 7, amendment). Once it is in
// web/openapi.json: run `pnpm gen:api`, derive these types from the schema
// in ./types.ts, move the calls below to openapi-fetch in ./mutations.ts,
// handle `server_status` in ./live.ts like the other bus events, and delete
// this file.

type Schemas = components["schemas"];

/** A server's link to the control plane, as its node reports it. */
type ServerLink = {
  status: "online" | "offline" | "incompatible" | "joining";
  /** When the status last changed. */
  status_since?: string;
  docker_version?: string;
  ingress?: "direct" | "cloudflare_tunnel";
  /** What DNS should point at (direct ingress only). */
  public_ips?: string[] | null;
  /** The CNAME target for custom domains (tunnel ingress only). */
  tunnel_hostname?: string;
  /** Generated hostnames are <service>-<env>.<base_domain>. */
  base_domain?: string;
  /** Pinned node key; empty for the control plane and after a revoke. */
  key_fingerprint?: string;
};

export type Server = Omit<Schemas["Server"], "status"> & ServerLink;

export type ServerPatch = Schemas["UpdateServerRequest"] & {
  builder?: boolean;
  base_domain?: string;
};

export type NewServer = {
  name: string;
  accepts_production: boolean;
  accepts_non_production: boolean;
  builder: boolean;
};

/** The join token is returned once, here, and never again. */
export type AddedServer = {
  server: Server;
  join_token: string;
  install_command: string;
};

export type PromoteRequest = { from: string; to: string; services?: string[] };

/** What a promote started, and what it left alone and why. */
export type Promotion = {
  promoted: { service: string; release: Schemas["Release"] }[] | null;
  skipped: { service: string; reason: string }[] | null;
};

/** The bus event for link status changes (ADR 0014: server.status). */
export type ServerStatusEvent = {
  type: "server_status";
  time: string;
  server?: Server;
};

async function call<T>(method: string, path: string, payload?: unknown) {
  const response = await fetch(path, {
    method,
    credentials: "include",
    headers:
      payload === undefined
        ? undefined
        : { "content-type": "application/json" },
    body: payload === undefined ? undefined : JSON.stringify(payload),
  });
  const data: unknown =
    response.status === 204 ? null : await response.json().catch(() => null);
  if (!response.ok) {
    const detail =
      typeof data === "object" && data && "detail" in data
        ? String(data.detail)
        : response.statusText;
    const code =
      typeof data === "object" && data && "code" in data
        ? String(data.code)
        : "";
    throw new ApiError(response.status, code, detail);
  }
  // Placeholder only: the generated client types these responses.
  return data as T;
}

const server = (name: string) => `/api/v1/servers/${encodeURIComponent(name)}`;

export const placeholderApi = {
  addServer: (input: NewServer) =>
    call<AddedServer>("POST", "/api/v1/servers", input),
  removeServer: (name: string) => call<null>("DELETE", server(name)),
  revokeServer: (name: string) =>
    call<Server>("POST", `${server(name)}/revoke`),
  updateServer: (name: string, patch: ServerPatch) =>
    call<Server>("PATCH", server(name), patch),
  promote: (project: string, input: PromoteRequest) =>
    call<Promotion>(
      "POST",
      `/api/v1/projects/${encodeURIComponent(project)}/promote`,
      input,
    ),
};
