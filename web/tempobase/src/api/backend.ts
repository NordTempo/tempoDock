import type { DataEndpoints } from "../../../src/features/data/api";
import type { PolicyRule } from "../../../src/features/data/policy";
import type {
  AuditDetail,
  AuditEvent,
  AuditQuery,
  Connection,
  ConnectionTest,
  CreateConnectionRequest,
  CreateTokenRequest,
  Link,
  LinkInfo,
  Me,
  Policy,
  Role,
  TestConnectionRequest,
  Token,
  UpdateConnectionRequest,
  UpdateTokenRequest,
  UpdateUserRequest,
  User,
} from "./types";

// tempoBase's HTTP API (ADR 0024 section 3) as one typed interface. The
// UI only talks to the server through it, so the in-memory backend
// (./memory.ts, for development until the server exists) and the HTTP
// one are interchangeable. Each method names its endpoint under /api/v1.

export type Backend = {
  /** GET /version */
  version(): Promise<{ version: string }>;
  /** GET /setup: no users yet, so the first admin must be created. */
  setupNeeded(): Promise<{ needed: boolean }>;
  /** POST /setup with the one-time code from the server's log. */
  setup(req: {
    code: string;
    email: string;
    name: string;
    password: string;
  }): Promise<Me>;
  /** POST /auth/login */
  login(req: { email: string; password: string }): Promise<Me>;
  /** POST /auth/logout */
  logout(): Promise<void>;
  /** GET /links/{code}: an invite or reset, before using it. */
  link(code: string): Promise<LinkInfo>;
  /** POST /links/{code}: set name (invites) and password, then signed in. */
  useLink(code: string, req: { name?: string; password: string }): Promise<Me>;
  /** GET /me; 401 when signed out. */
  me(): Promise<Me>;
  /** POST /me/password */
  changePassword(req: {
    current_password: string;
    new_password: string;
  }): Promise<void>;

  /** GET /users (admin) */
  users(): Promise<User[]>;
  /** PATCH /users/{id} (admin) */
  updateUser(id: number, req: UpdateUserRequest): Promise<User>;
  /** DELETE /users/{id} (admin; not the last admin) */
  deleteUser(id: number): Promise<void>;
  /** POST /users/{id}/reset-link (admin): the link with its url, once. */
  resetLink(id: number): Promise<Link>;
  /** GET /invites (admin): open invites, without url. */
  invites(): Promise<Link[]>;
  /** POST /invites (admin): the link with its url, once. */
  createInvite(req: { email: string; role: Role }): Promise<Link>;
  /** DELETE /invites/{id} (admin) */
  deleteInvite(id: number): Promise<void>;

  /** GET /tokens: one's own, or every token for admins. */
  tokens(): Promise<Token[]>;
  /** POST /tokens: the secret is in the response only. */
  createToken(
    req: CreateTokenRequest,
  ): Promise<{ token: Token; secret: string }>;
  /** PATCH /tokens/{id}: the token's own data access (creator or admin). */
  updateToken(id: number, req: UpdateTokenRequest): Promise<Token>;
  /** DELETE /tokens/{id}: revokes it (creator or admin). */
  deleteToken(id: number): Promise<void>;

  /** GET /connections: the ones the caller may see. */
  connections(): Promise<Connection[]>;
  /** GET /connections/{connection} */
  connection(name: string): Promise<Connection>;
  /** POST /connections (admin) */
  createConnection(req: CreateConnectionRequest): Promise<Connection>;
  /** POST /connections/test (admin): dials as the URL's user, saves nothing. */
  testConnection(req: TestConnectionRequest): Promise<ConnectionTest>;
  /** PATCH /connections/{connection} (admin) */
  updateConnection(
    name: string,
    req: UpdateConnectionRequest,
  ): Promise<Connection>;
  /** DELETE /connections/{connection} (admin) */
  deleteConnection(name: string): Promise<void>;

  /** GET /data-policy, or /connections/{connection}/data-policy. */
  policy(connection?: string): Promise<Policy>;
  /** PUT the rules at one scope (admin). */
  setPolicy(rules: PolicyRule[], connection?: string): Promise<Policy>;

  /** GET /audit (admin), newest first. */
  audit(q: AuditQuery): Promise<AuditEvent[]>;
  /** GET /audit/{id}/detail (admin) */
  auditDetail(id: number): Promise<AuditDetail>;

  /** The data endpoints under /connections/{connection}/data. */
  data(connection: string): DataEndpoints;
};

/** A failed call: the problem's status, code and message. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  constructor(status: number, code: string, message: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

export const isUnauthorized = (error: unknown) =>
  error instanceof ApiError && error.status === 401;

let current: Backend | undefined;

/**
 * Loads the backend before the app renders. Until tempoBase's server
 * and spec exist this is the in-memory one, in its own chunk.
 */
export async function loadBackend() {
  const { memoryBackend } = await import("./memory");
  current = memoryBackend();
}

/** The loaded backend. */
export function tb(): Backend {
  if (!current) throw new Error("tempoBase's backend isn't loaded yet");
  return current;
}
