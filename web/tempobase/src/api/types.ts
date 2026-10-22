import type { PolicyRule } from "../../../src/features/data/policy";
import type {
  AuditDetail,
  DataPermission,
  DataTokenAccess,
  UpdateTokenRequest,
} from "../../../src/features/data/types";

// tempoBase's own API types (ADR 0024 section 3, tbtypes), written from the
// ADR until web/tempobase/openapi.json exists; then they are derived from
// its generated schema. The data and policy types are Dock's apitypes,
// identical in both specs (the contract test checks it).

export type { AuditDetail, DataTokenAccess, UpdateTokenRequest };

export type Role = "viewer" | "editor" | "admin";

/** GET /me: the caller, a signed-in person or a token. */
export type Me = {
  kind: "user" | "token";
  name: string;
  email: string;
  role: Role;
  /** A token's connection limit; null: every connection the policy allows. */
  token_connections: string[] | null;
};

export type User = {
  id: number;
  email: string;
  name: string;
  role: Role;
  disabled: boolean;
  created_at: string;
};

export type UpdateUserRequest = {
  name?: string;
  role?: Role;
  disabled?: boolean;
};

/** An invite or a password reset; `url` only when just created. */
export type Link = {
  id: number;
  kind: "invite" | "reset";
  email: string;
  role?: Role;
  url?: string;
  expires_at: string;
};

/** GET /links/{code}: what a link is for, before using it. */
export type LinkInfo = Pick<Link, "kind" | "email" | "expires_at">;

export type Token = {
  id: number;
  name: string;
  /** The secret's first characters, for recognizing it. */
  prefix: string;
  /** The user who created it. */
  user: string;
  /** Connections it may use; null: every connection the policy allows. */
  connections: string[] | null;
  data: DataTokenAccess;
  created_at: string;
  expires_at?: string;
  revoked_at?: string;
};

export type CreateTokenRequest = {
  name: string;
  connections: string[] | null;
  data?: DataTokenAccess;
  expires_in_days?: number;
};

export type TlsMode =
  | "disable"
  | "prefer"
  | "require"
  | "verify-ca"
  | "verify-full";

/** A saved database. `url` never contains the password. */
export type Connection = {
  name: string;
  engine: "postgres";
  host: string;
  port: number;
  database: string;
  user: string;
  tls: TlsMode;
  production: boolean;
  url: string;
  created_at: string;
  created_by: string;
  /** The caller's access, from the policy. */
  access: DataPermission;
  /** Open changesets (admins only). */
  pending?: number;
};

export type CreateConnectionRequest = {
  name: string;
  url: string;
  production: boolean;
  ca_pem?: string;
};

export type UpdateConnectionRequest = Partial<CreateConnectionRequest>;

export type TestConnectionRequest = { url: string; ca_pem?: string };

/** POST /connections/test: dialed as the URL's user, nothing saved. */
export type ConnectionTest = {
  ok: boolean;
  server_version?: string;
  /** The user may create roles (CREATEROLE or superuser), so setup works. */
  can_set_up?: boolean;
  error?: string;
};

/** Where a tempoBase policy rule comes from. */
export type PolicyScope = "built_in" | "default" | "connection";

/** GET /data-policy, GET /connections/{c}/data-policy. */
export type Policy = {
  scope: "default" | "connection";
  connection?: string;
  rules: PolicyRule[] | null;
  effective: (PolicyRule & { from?: PolicyScope })[] | null;
};

export type AuditEvent = {
  id: number;
  actor: string;
  actor_kind: "user" | "token" | "system";
  action: string;
  connection?: string;
  target: string;
  summary: string;
  detail_id?: number;
  created_at: string;
};

export type AuditQuery = {
  connection?: string;
  action?: string;
  before?: number;
  limit?: number;
};
