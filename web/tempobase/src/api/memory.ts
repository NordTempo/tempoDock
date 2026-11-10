import type { DataEndpoints } from "../../../src/features/data/api";
import { tableName } from "../../../src/features/data/cells";
import type { PolicyRule } from "../../../src/features/data/policy";
import { kinds, subjects } from "../../../src/features/data/policy";
import type {
  DataChangeset,
  DataColumnRef,
  DataEnvironmentKind,
  DataHistoryEntry,
  DataInfo,
  DataLevel,
  DataPermission,
  DataSavedQuery,
  DataSubject,
} from "../../../src/features/data/types";
import { dataLimits } from "../../../src/features/data/types";
import { builtIn } from "../data/policy";
import { ApiError, type Backend } from "./backend";
import {
  applyChanges,
  changeSql,
  type FakeTable,
  fakeInsights,
  findTable,
  makeTables,
  mask,
  runQuery,
  select,
} from "./fakeDatabase";
import type {
  AuditDetail,
  AuditEvent,
  Connection,
  Link,
  Me,
  Policy,
  PolicyScope,
  Role,
  TlsMode,
  Token,
  User,
} from "./types";

// An in-memory tempoBase server for working on the UI without one
// (`pnpm dev:tempobase:memory`); production builds leave it out. It
// follows ADR 0024 section 3's contract and the policy rules closely
// enough to click through every page. The state lives in localStorage so a reload keeps you signed in; clear
// `tempobase-memory` to start over. The first-admin setup code is "dev".

const storageKey = "tempobase-memory";
const setupCode = "dev";

type StoredUser = User & { password: string };
type StoredLink = Link & {
  code: string;
  user_id?: number;
  used: boolean;
  created_by: string;
};
type StoredToken = Token & { user_id: number };
type StoredConnection = Omit<Connection, "access" | "pending"> & {
  id: number;
  set_up_at?: string;
  set_up_by?: string;
};
type StoredRule = { connection_id: number | null } & PolicyRule;
type Saved = DataSavedQuery & { connection_id: number; user_id: number };
type Change = DataChangeset & { connection_id: number; user_id: number };

type State = {
  next: number;
  session: number | null;
  users: StoredUser[];
  links: StoredLink[];
  tokens: StoredToken[];
  connections: StoredConnection[];
  rules: StoredRule[];
  sensitive: (DataColumnRef & { connection_id: number })[];
  saved: Saved[];
  changesets: Change[];
  audit: AuditEvent[];
  details: AuditDetail[];
};

const empty: State = {
  next: 1,
  session: null,
  users: [],
  links: [],
  tokens: [],
  connections: [],
  rules: [],
  sensitive: [],
  saved: [],
  changesets: [],
  audit: [],
  details: [],
};

const subjectOf: Record<Role, DataSubject | null> = {
  viewer: "role:read",
  editor: "role:deploy",
  admin: null,
};

const rank: Record<DataLevel, number> = {
  none: 0,
  schema: 1,
  read: 2,
  propose: 3,
  write: 4,
};

const now = () => new Date().toISOString();
const later = (days: number) =>
  new Date(Date.now() + days * 86_400_000).toISOString();
const secret = (prefix: string) =>
  `${prefix}${Array.from(crypto.getRandomValues(new Uint8Array(24)), (b) =>
    b.toString(16).padStart(2, "0"),
  ).join("")}`;

const fail = (status: number, code: string, message: string): never => {
  throw new ApiError(status, code, message);
};

const sslModes: TlsMode[] = [
  "disable",
  "prefer",
  "require",
  "verify-ca",
  "verify-full",
];

/** A postgres:// URL's parts, or a validation error naming what to fix. */
function parseUrl(raw: string) {
  let url: URL;
  try {
    url = new URL(raw.trim());
  } catch {
    return fail(
      422,
      "invalid",
      "Paste a URL like postgres://user:password@host:5432/database.",
    );
  }
  if (url.protocol !== "postgres:" && url.protocol !== "postgresql:")
    fail(
      422,
      "invalid",
      "The URL must start with postgres:// or postgresql://.",
    );
  if (!url.hostname) fail(422, "invalid", "The URL has no host.");
  const database = decodeURIComponent(url.pathname.replace(/^\//, ""));
  if (!database)
    fail(422, "invalid", "The URL has no database name after the host.");
  if (!url.username) fail(422, "invalid", "The URL has no user.");
  for (const key of url.searchParams.keys()) {
    if (key !== "sslmode")
      fail(
        422,
        "invalid",
        `Unsupported parameter ${key}: only sslmode (${sslModes.join(", ")}).`,
      );
  }
  const mode = url.searchParams.get("sslmode") ?? "prefer";
  const tls = sslModes.find((m) => m === mode);
  if (!tls)
    return fail(
      422,
      "invalid",
      `sslmode must be one of ${sslModes.join(", ")}.`,
    );
  const user = decodeURIComponent(url.username);
  const port = Number(url.port || 5432);
  return {
    host: url.hostname,
    port,
    database,
    user,
    tls,
    url: `${url.protocol}//${encodeURIComponent(user)}@${url.hostname}:${port}/${encodeURIComponent(database)}?sslmode=${tls}`,
  };
}

const nameRe = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;

/** The in-memory backend; one per page load. */
export function memoryBackend(): Backend {
  let state: State = load();
  // Fake databases by connection id; not persisted, rebuilt on reload.
  const databases = new Map<number, FakeTable[]>();
  const tablesOf = (c: StoredConnection) => {
    let t = databases.get(c.id);
    if (!t) {
      t = makeTables();
      databases.set(c.id, t);
    }
    return t;
  };

  if (!state.users.length) {
    console.info(
      `tempoBase (in-memory): no users yet: open ${location.origin}/setup#code=${setupCode} to create the first admin`,
    );
  }

  function load(): State {
    try {
      const raw = localStorage.getItem(storageKey);
      return raw ? { ...empty, ...(JSON.parse(raw) as Partial<State>) } : empty;
    } catch {
      return empty;
    }
  }
  /** Applies a change to the state and stores it. */
  function write<T>(change: (s: State) => T): T {
    const draft = structuredClone(state);
    const result = change(draft);
    state = draft;
    localStorage.setItem(storageKey, JSON.stringify(state));
    return result;
  }
  const id = (s: State) => s.next++;

  const me = (): StoredUser => {
    const u = state.users.find((x) => x.id === state.session && !x.disabled);
    if (!u) return fail(401, "unauthorized", "Sign in first.");
    return u;
  };
  const admin = () => {
    const u = me();
    if (u.role !== "admin") fail(403, "forbidden", "Only admins can do that.");
    return u;
  };
  const toMe = (u: StoredUser): Me => ({
    kind: "user",
    name: u.name,
    email: u.email,
    role: u.role,
    token_connections: null,
  });
  const toUser = ({ password: _, ...u }: StoredUser): User => u;
  const toLink = ({
    code: _c,
    used: _u,
    user_id: _i,
    ...l
  }: StoredLink): Link => l;

  function audit(
    s: State,
    action: string,
    target: string,
    summary: string,
    extra: Partial<AuditEvent> = {},
  ) {
    const u = s.users.find((x) => x.id === s.session);
    s.audit.unshift({
      id: id(s),
      created_at: now(),
      actor: u?.email ?? "system",
      actor_kind: u ? "user" : "system",
      action,
      target,
      summary,
      ...extra,
    });
    s.audit.length = Math.min(s.audit.length, 2000);
  }

  function signIn(s: State, u: StoredUser) {
    s.session = u.id;
    audit(s, "user.login", u.email, `${u.email} signed in`);
    return toMe(u);
  }

  // The policy: connection rule, default rule, built-in.
  const kindOf = (c: StoredConnection): DataEnvironmentKind =>
    c.production ? "production" : "non_production";
  function rule(
    subject: DataSubject,
    c: StoredConnection | null,
    kind: DataEnvironmentKind,
  ) {
    const here =
      c &&
      state.rules.find(
        (r) =>
          r.connection_id === c.id && r.subject === subject && r.kind === kind,
      );
    if (here) return { ...here, from: "connection" as PolicyScope };
    const def = state.rules.find(
      (r) =>
        r.connection_id === null && r.subject === subject && r.kind === kind,
    );
    if (def) return { ...def, from: "default" as PolicyScope };
    return {
      subject,
      kind,
      ...builtIn(subject),
      from: "built_in" as PolicyScope,
    };
  }

  /** The caller's permission on a connection, with the steps that decided it. */
  function access(u: StoredUser, c: StoredConnection): DataPermission {
    const production = c.production;
    if (u.role === "admin") {
      return {
        level: "write",
        sensitive: "visible",
        approve: true,
        manage: true,
        confirm_commit: production,
        why: [
          {
            source: "admin",
            detail: "admins can do everything",
            level: "write",
            sensitive: "visible",
          },
        ],
      };
    }
    const subject = subjectOf[u.role] ?? "role:read";
    const r = rule(subject, c, kindOf(c));
    const names = {
      "role:read": "viewers",
      "role:deploy": "editors",
      tokens: "tokens",
    };
    const where =
      r.from === "connection"
        ? `connection ${c.name}`
        : r.from === "default"
          ? "the default policy"
          : "the built-in defaults";
    return {
      level: r.level,
      sensitive: r.sensitive,
      approve: false,
      manage: false,
      confirm_commit: production && r.level === "write",
      why: [
        {
          source: "policy",
          subject,
          level: r.level,
          sensitive: r.sensitive,
          detail: `${where}: ${names[subject]} may ${r.level === "none" ? "do nothing" : r.level} on ${production ? "production" : "non-production"}`,
        },
      ],
    };
  }

  const pendingOf = (c: StoredConnection) =>
    state.changesets.filter(
      (x) => x.connection_id === c.id && x.status === "pending",
    ).length;

  function toConnection(u: StoredUser, c: StoredConnection): Connection {
    const { id: _, set_up_at: _a, set_up_by: _b, ...rest } = c;
    return {
      ...rest,
      access: access(u, c),
      pending: u.role === "admin" ? pendingOf(c) : 0,
    };
  }

  /** A connection the caller may see; invisible ones are 404 like missing ones. */
  function visible(name: string) {
    const u = me();
    const c = state.connections.find((x) => x.name === name);
    if (!c || access(u, c).level === "none")
      return fail(
        404,
        "not_found",
        `Connection ${name} not found. Check the name in the connection list.`,
      );
    return { u, c, perm: access(u, c) };
  }

  const need = (perm: DataPermission, level: DataLevel, what: string) => {
    if (rank[perm.level] < rank[level])
      fail(
        403,
        "forbidden",
        `Your data access is ${perm.level}; ${what} needs ${level}. An admin can change it in the policy.`,
      );
  };

  function policyOf(c: StoredConnection | null): Policy {
    const shown = c ? [kindOf(c)] : kinds;
    const effective = subjects.flatMap((subject) =>
      shown.map((kind) => {
        const r = rule(subject, c, kind);
        return {
          subject,
          kind,
          level: r.level,
          sensitive: r.sensitive,
          from: r.from,
        };
      }),
    );
    return {
      scope: c ? "connection" : "default",
      ...(c && { connection: c.name }),
      rules: state.rules
        .filter((r) => r.connection_id === (c?.id ?? null))
        .map(({ connection_id: _, ...r }) => r),
      effective,
    };
  }

  function data(name: string): DataEndpoints {
    const ready = () => {
      const v = visible(name);
      if (!v.c.set_up_at)
        fail(
          409,
          "setup_needed",
          "Not set up. An admin can set up data access in the connection's settings.",
        );
      return { ...v, tables: tablesOf(v.c) };
    };
    const sensitiveOf = (c: StoredConnection, t: FakeTable) =>
      new Set(
        state.sensitive
          .filter(
            (s) =>
              s.connection_id === c.id &&
              s.schema === t.meta.schema &&
              s.table === t.meta.name,
          )
          .map((s) => s.column),
      );
    const info = (): DataInfo => {
      const { c, perm } = visible(name);
      const unreachable = c.host.startsWith("unreachable");
      return {
        engine: "postgres",
        detected_by: "setting",
        used_by: [],
        server_version: "17.2",
        status: unreachable
          ? "unreachable"
          : c.set_up_at
            ? "ready"
            : "setup_needed",
        ...(unreachable && {
          reason: `dial tcp ${c.host}:${c.port}: no such host. Check the URL in the connection's settings.`,
        }),
        ...(c.set_up_at && { set_up_at: c.set_up_at, set_up_by: c.set_up_by }),
        access: perm,
      };
    };
    const changeset = (c: StoredConnection, cs: Change): DataChangeset => {
      const { connection_id: _, user_id: _u, ...rest } = cs;
      return { ...rest, confirm_commit: c.production };
    };
    return {
      info: async () => info(),
      setup: async () => {
        const { c, perm } = visible(name);
        if (!perm.manage)
          fail(403, "forbidden", "Only admins set up data access.");
        write((s) => {
          const x = s.connections.find((y) => y.id === c.id);
          if (x) {
            x.set_up_at = now();
            x.set_up_by = me().email;
          }
          audit(
            s,
            "data.setup",
            c.name,
            "created or rotated tempobase_reader, tempobase_writer, tempobase_reader_masked",
            { connection: c.name },
          );
        });
        return info();
      },
      insights: async () => {
        const { perm, tables } = ready();
        need(perm, "schema", "database insights");
        return fakeInsights(tables, rank[perm.level] >= rank.read);
      },
      schema: async () => {
        const { c, perm, tables } = ready();
        need(perm, "schema", "the schema");
        return {
          read_at: now(),
          tables: tables.map((t) => {
            const marks = sensitiveOf(c, t);
            return {
              ...t.meta,
              estimated_rows: t.meta.estimated_rows === -1 ? -1 : t.rows.length,
              columns: (t.meta.columns ?? []).map((col) => ({
                ...col,
                sensitive: marks.has(col.name),
              })),
            };
          }),
        };
      },
      rows: async (req) => {
        const { c, u, perm, tables } = ready();
        need(perm, "read", "browsing rows");
        const t =
          findTable(tables, req.table) ??
          fail(404, "not_found", `Table ${req.table} not found.`);
        if (req.reveal && perm.sensitive !== "visible")
          fail(403, "forbidden", "Your access masks sensitive columns.");
        let all: FakeTable["rows"];
        try {
          all = select(t, req.filters ?? [], req.order);
        } catch (e) {
          return fail(
            422,
            "invalid",
            e instanceof Error ? e.message : String(e),
          );
        }
        const start = Number(req.after?.replace(/^o/, "") || 0);
        const limit = req.limit ?? dataLimits.pageRows;
        const page = all.slice(start, start + limit);
        if (!start && (req.reveal || !req.filters?.length)) {
          write((s) =>
            audit(
              s,
              req.reveal ? "data.reveal" : "data.browse",
              `${c.name} ${tableName(t.meta)}`,
              `${u.email} ${req.reveal ? "revealed sensitive columns of" : "browsed"} ${tableName(t.meta)}`,
              { connection: c.name },
            ),
          );
        }
        return {
          columns: (t.meta.columns ?? []).map((col) => ({
            name: col.name,
            type_name: col.type_name,
          })),
          rows: mask(t, page, req.reveal ? new Set() : sensitiveOf(c, t)),
          estimated_rows: t.rows.length,
          ...(t.meta.exact_rows !== undefined && { exact_rows: all.length }),
          ...(start + limit < all.length && { next: `o${start + limit}` }),
        };
      },
      references: async (req) => {
        const { c, perm, tables } = ready();
        need(perm, "read", "references");
        const target = findTable(tables, req.table);
        if (!target) return [];
        return tables.flatMap((t) =>
          (t.meta.foreign_keys ?? [])
            .filter(
              (fk) =>
                `${fk.ref_schema}.${fk.ref_table}` === tableName(target.meta),
            )
            .map((fk) => {
              const filters = (fk.columns ?? []).map((col, i) => ({
                column: col,
                op: "eq" as const,
                value: req.key[fk.ref_columns?.[i] ?? ""] ?? "",
              }));
              const rows = select(t, filters);
              return {
                schema: t.meta.schema,
                table: t.meta.name,
                constraint: fk.name,
                columns: fk.columns,
                filters,
                page: {
                  columns: (t.meta.columns ?? []).map((col) => ({
                    name: col.name,
                    type_name: col.type_name,
                  })),
                  rows: mask(
                    t,
                    rows.slice(0, req.limit ?? 20),
                    sensitiveOf(c, t),
                  ),
                  estimated_rows: rows.length,
                },
              };
            }),
        );
      },
      query: async function* (req) {
        const { c, u, perm, tables } = ready();
        need(perm, "read", "SQL");
        let rows = 0;
        let outcome: DataHistoryEntry["outcome"] = "ok";
        let duration = 0;
        for (const frame of runQuery(
          tables,
          req.sql,
          (t) => (perm.sensitive === "masked" ? sensitiveOf(c, t) : new Set()),
          dataLimits.queryRows,
        )) {
          if (frame.type === "rows") rows += frame.rows?.length ?? 0;
          if (frame.type === "error") outcome = "error";
          if (frame.type === "end") duration = frame.duration_ms ?? 0;
          yield frame;
        }
        write((s) => {
          const detail: AuditDetail = {
            id: id(s),
            sql: req.sql,
            rows,
            duration_ms: duration,
            outcome,
          };
          s.details.push(detail);
          audit(
            s,
            "data.query",
            c.name,
            `${u.email} ran ${req.sql.replace(/'[^']*'/g, "?").slice(0, 80)}`,
            { connection: c.name, detail_id: detail.id },
          );
        });
      },
      history: async (before) => {
        const { c, u } = ready();
        return state.audit
          .filter(
            (e) =>
              e.action === "data.query" &&
              e.connection === c.name &&
              e.actor === u.email &&
              (!before || e.id < before),
          )
          .slice(0, dataLimits.historyRows)
          .flatMap((e) => {
            const d = state.details.find((x) => x.id === e.detail_id);
            return d
              ? [{ ...d, id: d.id, audit_id: e.id, created_at: e.created_at }]
              : [];
          });
      },
      savedQueries: async () => {
        const { c, u } = ready();
        return state.saved
          .filter((q) => q.connection_id === c.id && q.user_id === u.id)
          .map(({ connection_id: _, user_id: _u, ...q }) => q);
      },
      saveQuery: async (req, queryId) => {
        const { c, u } = ready();
        return write((s) => {
          const existing = s.saved.find(
            (q) => q.id === queryId && q.user_id === u.id,
          );
          if (existing) {
            Object.assign(existing, req, { updated_at: now() });
            const { connection_id: _, user_id: _u, ...q } = existing;
            return q;
          }
          if (
            s.saved.some(
              (q) =>
                q.connection_id === c.id &&
                q.user_id === u.id &&
                q.name === req.name,
            )
          )
            fail(
              409,
              "conflict",
              `You already have a query named ${req.name}.`,
            );
          const q = { id: id(s), ...req, created_at: now(), updated_at: now() };
          s.saved.push({ ...q, connection_id: c.id, user_id: u.id });
          return q;
        });
      },
      deleteSavedQuery: async (queryId) => {
        const { u } = ready();
        write((s) => {
          s.saved = s.saved.filter(
            (q) => !(q.id === queryId && q.user_id === u.id),
          );
        });
      },
      changeset: async (req) => {
        const { c, u, perm } = ready();
        need(perm, "propose", "changing data");
        const changes = req.changes ?? [];
        if (changes.length > dataLimits.changesetRows)
          fail(
            422,
            "invalid",
            `At most ${dataLimits.changesetRows} rows per changeset.`,
          );
        const base = {
          author: u.email,
          author_kind: "user" as const,
          changes,
          confirm_commit: c.production,
          created_at: now(),
          sql: changeSql(changes),
          message: req.message,
        };
        if (req.dry_run) return { ...base, id: 0, status: "preview" };
        return write((s) => {
          const cs: Change = {
            ...base,
            id: id(s),
            status: "pending",
            expires_at: later(7),
            connection_id: c.id,
            user_id: u.id,
          };
          s.changesets.unshift(cs);
          audit(
            s,
            "data.propose",
            c.name,
            `${u.email} proposed changeset ${cs.id}`,
            { connection: c.name },
          );
          return changeset(c, cs);
        });
      },
      changesets: async () => {
        const { c, u, perm } = ready();
        return state.changesets
          .filter(
            (x) =>
              x.connection_id === c.id && (perm.approve || x.user_id === u.id),
          )
          .map((x) => changeset(c, x));
      },
      commitChangeset: async (csId, req) => {
        const { c, u, perm, tables } = ready();
        const cs =
          state.changesets.find(
            (x) => x.id === csId && x.connection_id === c.id,
          ) ?? fail(404, "not_found", `Changeset ${csId} not found.`);
        const own = cs.user_id === u.id;
        if (!perm.approve && !(own && perm.level === "write"))
          fail(403, "forbidden", "Only admins approve other people's changes.");
        if (cs.status !== "pending")
          fail(409, "conflict", `Changeset ${csId} is ${cs.status}.`);
        if (c.production && req.confirm !== c.name)
          fail(
            422,
            "confirm",
            `Type the connection's name (${c.name}) to commit on production.`,
          );
        const result = applyChanges(tables, cs.changes ?? []);
        return write((s) => {
          const x = s.changesets.find((y) => y.id === csId);
          if (!x) return fail(404, "not_found", `Changeset ${csId} not found.`);
          x.decided_at = now();
          x.decided_by = u.email;
          if (result.conflict) {
            x.status = "conflict";
            x.conflicts = [
              {
                change: result.conflict.change,
                reason: "changed",
                message: result.conflict.message,
              },
            ];
          } else {
            x.status = "committed";
            x.rows_affected = result.affected;
            audit(
              s,
              own ? "data.commit" : "data.approve",
              c.name,
              `${u.email} committed changeset ${x.id}`,
              { connection: c.name },
            );
          }
          return changeset(c, x);
        });
      },
      rejectChangeset: async (csId, req) => {
        const { c, u, perm } = ready();
        return write((s) => {
          const x =
            s.changesets.find(
              (y) => y.id === csId && y.connection_id === c.id,
            ) ?? fail(404, "not_found", `Changeset ${csId} not found.`);
          if (!perm.approve && x.user_id !== u.id)
            fail(
              403,
              "forbidden",
              "Only admins reject other people's changes.",
            );
          x.status = "rejected";
          x.reason = req.reason;
          x.decided_at = now();
          x.decided_by = u.email;
          audit(
            s,
            "data.reject",
            c.name,
            `${u.email} rejected changeset ${x.id}`,
            { connection: c.name },
          );
          return changeset(c, x);
        });
      },
      setSensitive: async (columns) => {
        const { c, perm } = ready();
        if (!perm.manage)
          fail(403, "forbidden", "Only admins mark sensitive columns.");
        return write((s) => {
          s.sensitive = [
            ...s.sensitive.filter((x) => x.connection_id !== c.id),
            ...columns.map((col) => ({ ...col, connection_id: c.id })),
          ];
          return columns;
        });
      },
    };
  }

  const tokenOf = ({ user_id: _, ...t }: StoredToken): Token => t;
  const ownToken = (tokenId: number) => {
    const u = me();
    const t = state.tokens.find((x) => x.id === tokenId);
    if (!t || (t.user_id !== u.id && u.role !== "admin"))
      return fail(404, "not_found", `Token ${tokenId} not found.`);
    return t;
  };

  return {
    version: async () => ({ version: "dev (in-memory)" }),
    setupNeeded: async () => ({ needed: state.users.length === 0 }),
    setup: async (req) => {
      if (state.users.length)
        fail(
          409,
          "conflict",
          "tempoBase already has an admin. Sign in instead.",
        );
      if (req.code !== setupCode)
        fail(
          403,
          "forbidden",
          "That setup code is wrong or used. Open the link from the server's log again.",
        );
      if (req.password.length < 12)
        fail(422, "invalid", "Use at least 12 characters for the password.");
      return write((s) => {
        const u: StoredUser = {
          id: id(s),
          email: req.email,
          name: req.name,
          role: "admin",
          disabled: false,
          created_at: now(),
          password: req.password,
        };
        s.users.push(u);
        audit(
          s,
          "user.create",
          u.email,
          `${u.email} created as the first admin`,
        );
        return signIn(s, u);
      });
    },
    login: async (req) => {
      const u = state.users.find(
        (x) => x.email.toLowerCase() === req.email.toLowerCase(),
      );
      if (!u || u.disabled || !u.password || u.password !== req.password) {
        write((s) =>
          audit(
            s,
            "user.login_failed",
            req.email,
            `failed sign-in for ${req.email}`,
          ),
        );
        return fail(401, "unauthorized", "Wrong email or password.");
      }
      return write((s) => signIn(s, u));
    },
    logout: async () => {
      write((s) => {
        s.session = null;
      });
    },
    link: async (code) => {
      const l = state.links.find(
        (x) => x.code === code && !x.used && x.expires_at > now(),
      );
      if (!l)
        return fail(
          404,
          "not_found",
          "This link is used or expired. Ask an admin for a new one.",
        );
      return { kind: l.kind, email: l.email, expires_at: l.expires_at };
    },
    useLink: async (code, req) => {
      const l = state.links.find(
        (x) => x.code === code && !x.used && x.expires_at > now(),
      );
      if (!l)
        return fail(
          404,
          "not_found",
          "This link is used or expired. Ask an admin for a new one.",
        );
      if (req.password.length < 12)
        fail(422, "invalid", "Use at least 12 characters for the password.");
      return write((s) => {
        const link = s.links.find((x) => x.id === l.id);
        if (link) link.used = true;
        let u = s.users.find((x) =>
          l.kind === "reset" ? x.id === l.user_id : x.email === l.email,
        );
        if (!u) {
          u = {
            id: id(s),
            email: l.email,
            name: req.name?.trim() || l.email,
            role: l.role ?? "viewer",
            disabled: false,
            created_at: now(),
            password: req.password,
          };
          s.users.push(u);
        } else {
          u.password = req.password;
        }
        audit(s, "link.use", u.email, `${u.email} used a ${l.kind} link`);
        return signIn(s, u);
      });
    },
    me: async () => toMe(me()),
    changePassword: async (req) => {
      const u = me();
      if (u.password !== req.current_password)
        fail(403, "forbidden", "The current password is wrong.");
      if (req.new_password.length < 12)
        fail(
          422,
          "invalid",
          "Use at least 12 characters for the new password.",
        );
      write((s) => {
        const x = s.users.find((y) => y.id === u.id);
        if (x) x.password = req.new_password;
        audit(s, "user.update", u.email, `${u.email} changed their password`);
      });
    },

    users: async () => {
      admin();
      return state.users.map(toUser);
    },
    updateUser: async (userId, req) => {
      admin();
      return write((s) => {
        const u =
          s.users.find((x) => x.id === userId) ??
          fail(404, "not_found", `User ${userId} not found.`);
        const admins = s.users.filter((x) => x.role === "admin" && !x.disabled);
        if (
          u.role === "admin" &&
          admins.length === 1 &&
          ((req.role && req.role !== "admin") || req.disabled)
        )
          fail(
            409,
            "conflict",
            "That's the last admin. Make someone else an admin first.",
          );
        Object.assign(
          u,
          Object.fromEntries(
            Object.entries(req).filter(([, v]) => v !== undefined),
          ),
        );
        if (req.disabled)
          s.tokens = s.tokens.map((t) =>
            t.user_id === u.id && !t.revoked_at
              ? { ...t, revoked_at: now() }
              : t,
          );
        audit(s, "user.update", u.email, `${u.email} updated`);
        return toUser(u);
      });
    },
    deleteUser: async (userId) => {
      const self = admin();
      write((s) => {
        const u =
          s.users.find((x) => x.id === userId) ??
          fail(404, "not_found", `User ${userId} not found.`);
        if (
          u.role === "admin" &&
          s.users.filter((x) => x.role === "admin").length === 1
        )
          fail(
            409,
            "conflict",
            "That's the last admin. Make someone else an admin first.",
          );
        if (u.id === self.id)
          fail(409, "conflict", "You can't delete yourself.");
        s.users = s.users.filter((x) => x.id !== userId);
        s.tokens = s.tokens.filter((t) => t.user_id !== userId);
        audit(s, "user.delete", u.email, `${u.email} deleted`);
      });
    },
    resetLink: async (userId) => {
      const by = admin();
      return write((s) => {
        const u =
          s.users.find((x) => x.id === userId) ??
          fail(404, "not_found", `User ${userId} not found.`);
        const code = secret("");
        const l: StoredLink = {
          id: id(s),
          created_at: now(),
          kind: "reset",
          email: u.email,
          user_id: u.id,
          code,
          used: false,
          created_by: by.email,
          expires_at: later(1),
          url: `${location.origin}/link#${code}`,
        };
        s.links.push(l);
        audit(s, "link.create", u.email, `reset link for ${u.email}`);
        return toLink(l);
      });
    },
    invites: async () => {
      admin();
      return state.links
        .filter((l) => l.kind === "invite" && !l.used && l.expires_at > now())
        .map(({ url: _, ...l }) => toLink({ ...l }));
    },
    createInvite: async (req) => {
      const by = admin();
      if (
        state.users.some(
          (u) => u.email.toLowerCase() === req.email.toLowerCase(),
        )
      )
        fail(409, "conflict", `${req.email} already has an account.`);
      return write((s) => {
        const code = secret("");
        const l: StoredLink = {
          id: id(s),
          created_at: now(),
          kind: "invite",
          email: req.email,
          role: req.role,
          code,
          used: false,
          created_by: by.email,
          expires_at: later(7),
          url: `${location.origin}/link#${code}`,
        };
        s.links.push(l);
        audit(
          s,
          "link.create",
          req.email,
          `invited ${req.email} as ${req.role}`,
        );
        return toLink(l);
      });
    },
    deleteInvite: async (linkId) => {
      admin();
      write((s) => {
        s.links = s.links.filter((l) => l.id !== linkId);
      });
    },

    tokens: async () => {
      const u = me();
      return state.tokens
        .filter((t) => u.role === "admin" || t.user_id === u.id)
        .map(tokenOf);
    },
    createToken: async (req) => {
      const u = me();
      if (!req.name.trim()) fail(422, "invalid", "Give the token a name.");
      for (const name of req.connections ?? []) visible(name);
      return write((s) => {
        const value = secret("tbt_");
        const t: StoredToken = {
          id: id(s),
          name: req.name.trim(),
          prefix: value.slice(0, 10),
          user: u.email,
          user_id: u.id,
          connections: req.connections,
          data: req.data ?? null,
          created_at: now(),
          ...(req.expires_in_days && {
            expires_at: later(req.expires_in_days),
          }),
        };
        s.tokens.push(t);
        audit(s, "token.create", t.name, `token ${t.name} created`);
        return { token: tokenOf(t), secret: value };
      });
    },
    updateToken: async (tokenId, req) => {
      ownToken(tokenId);
      return write((s) => {
        const t =
          s.tokens.find((x) => x.id === tokenId) ??
          fail(404, "not_found", "Token not found.");
        t.data = req.data;
        audit(
          s,
          "data.policy",
          t.name,
          `data access of token ${t.name} changed`,
        );
        return tokenOf(t);
      });
    },
    deleteToken: async (tokenId) => {
      ownToken(tokenId);
      write((s) => {
        const t = s.tokens.find((x) => x.id === tokenId);
        if (t) t.revoked_at = now();
        audit(
          s,
          "token.revoke",
          t?.name ?? String(tokenId),
          `token ${t?.name} revoked`,
        );
      });
    },

    connections: async () => {
      const u = me();
      return state.connections
        .map((c) => toConnection(u, c))
        .filter((c) => c.access.level !== "none" || u.role === "admin");
    },
    connection: async (name) => {
      const { u, c } = visible(name);
      return toConnection(u, c);
    },
    createConnection: async (req) => {
      const u = admin();
      if (!nameRe.test(req.name))
        fail(
          422,
          "invalid",
          "Use 1 to 63 lower-case letters, digits and dashes for the name.",
        );
      if (state.connections.some((c) => c.name === req.name))
        fail(
          409,
          "conflict",
          `A connection named ${req.name} exists. Pick another name.`,
        );
      const parts = parseUrl(req.url);
      return write((s) => {
        const c: StoredConnection = {
          id: id(s),
          name: req.name,
          engine: "postgres",
          ...parts,
          production: req.production,
          custom_ca: !!req.ca_pem,
          created_at: now(),
          created_by: u.email,
        };
        s.connections.push(c);
        audit(
          s,
          "connection.create",
          c.name,
          `connection ${c.name} added (${parts.user}@${parts.host}/${parts.database})`,
          { connection: c.name },
        );
        return toConnection(u, c);
      });
    },
    testConnection: async (req) => {
      admin();
      const parts = parseUrl(req.url);
      if (parts.host.startsWith("unreachable"))
        return {
          ok: false,
          can_set_up: false,
          error: `dial tcp ${parts.host}:${parts.port}: no such host`,
        };
      return {
        ok: true,
        server_version: "17.2",
        can_set_up: parts.user !== "readonly",
      };
    },
    updateConnection: async (name, req) => {
      const u = admin();
      return write((s) => {
        const c =
          s.connections.find((x) => x.name === name) ??
          fail(404, "not_found", `Connection ${name} not found.`);
        if (req.name !== undefined && req.name !== c.name) {
          if (!nameRe.test(req.name))
            fail(
              422,
              "invalid",
              "Use 1 to 63 lower-case letters, digits and dashes for the name.",
            );
          if (s.connections.some((x) => x.name === req.name))
            fail(409, "conflict", `A connection named ${req.name} exists.`);
          c.name = req.name;
        }
        if (req.url) {
          const parts = parseUrl(req.url);
          const moved = parts.host !== c.host || parts.database !== c.database;
          Object.assign(c, parts);
          if (moved) {
            c.set_up_at = undefined;
            databases.delete(c.id);
          }
        }
        if (req.production !== undefined) c.production = req.production;
        audit(s, "connection.update", c.name, `connection ${c.name} changed`, {
          connection: c.name,
        });
        return toConnection(u, c);
      });
    },
    deleteConnection: async (name) => {
      admin();
      write((s) => {
        const c =
          s.connections.find((x) => x.name === name) ??
          fail(404, "not_found", `Connection ${name} not found.`);
        s.connections = s.connections.filter((x) => x.id !== c.id);
        s.rules = s.rules.filter((r) => r.connection_id !== c.id);
        s.sensitive = s.sensitive.filter((r) => r.connection_id !== c.id);
        s.saved = s.saved.filter((r) => r.connection_id !== c.id);
        s.changesets = s.changesets.filter((r) => r.connection_id !== c.id);
        databases.delete(c.id);
        audit(s, "connection.delete", c.name, `connection ${c.name} removed`, {
          connection: c.name,
        });
      });
    },

    policy: async (name) => {
      me();
      return policyOf(name ? visible(name).c : null);
    },
    setPolicy: async (rules, name) => {
      admin();
      const c = name ? visible(name).c : null;
      if (c && rules.some((r) => r.kind !== kindOf(c)))
        fail(
          422,
          "invalid",
          `Connection ${c.name} is ${kindOf(c).replace("_", "-")}: its rules can only name that kind.`,
        );
      write((s) => {
        s.rules = [
          ...s.rules.filter((r) => r.connection_id !== (c?.id ?? null)),
          ...rules.map(({ subject, kind, level, sensitive }) => ({
            connection_id: c?.id ?? null,
            subject,
            kind,
            level,
            sensitive,
          })),
        ];
        audit(
          s,
          "data.policy",
          c ? c.name : "default",
          `data policy of ${c ? `connection ${c.name}` : "the default"} changed`,
          c ? { connection: c.name } : {},
        );
      });
      return policyOf(c);
    },

    audit: async (q) => {
      admin();
      return state.audit
        .filter(
          (e) =>
            (!q.connection || e.connection === q.connection) &&
            (!q.action || e.action === q.action) &&
            (!q.before || e.id < q.before),
        )
        .slice(0, q.limit ?? 100);
    },
    auditDetail: async (detailId) => {
      admin();
      return (
        state.details.find((d) => d.id === detailId) ??
        fail(404, "not_found", "No detail for that event.")
      );
    },

    data,
  };
}
