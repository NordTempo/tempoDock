import createFetchClient from "openapi-fetch";
import { body, done, list } from "../../../src/api/client";
import type { DataEndpoints } from "../../../src/features/data/api";
import { frames } from "../../../src/features/data/ndjson";
import type {
  DataPage,
  DataReferences,
  DataRow,
} from "../../../src/features/data/types";
import { dataLimits } from "../../../src/features/data/types";
import type { Backend } from "./backend";
import type { components, paths } from "./schema";

// The Backend over tempoBase's HTTP API, with the client generated from
// web/tempobase/openapi.json. Paths carry the /api/v1 prefix; the session
// cookie rides along. Errors are the same problem documents as Dock's, so
// Dock's helpers (body, list, done, ApiError) read them.

const api = createFetchClient<paths>({
  baseUrl: window.location.origin,
  credentials: "include",
});

/** The server encodes each row as DataCell[]; the schema only says unknown. */
const page = (p: components["schemas"]["DataPage"]): DataPage => ({
  ...p,
  rows: p.rows as DataRow[] | null,
});

const C = "/api/v1/connections/{connection}";
const D = `${C}/data` as const;

/** One connection's data endpoints (ADR 0024 section 3). */
function dataEndpoints(connection: string): DataEndpoints {
  const path = { connection };
  const params = { params: { path } };
  return {
    info: () => body(api.GET(D, params)),
    setup: () => body(api.POST(`${D}/setup`, params)),
    schema: () => body(api.GET(`${D}/schema`, params)),
    insights: (order) =>
      body(api.GET(`${D}/insights`, { params: { path, query: { order } } })),
    cancelSession: (session, req) =>
      body(
        api.POST(`${D}/insights/sessions/{session}/cancel`, {
          params: { path: { ...path, session } },
          body: req,
        }),
      ),
    resetQueryStats: () =>
      done(api.POST(`${D}/insights/queries/reset`, params)),
    enableQueryStats: () =>
      body(api.POST(`${D}/insights/queries/enable`, params)),
    rows: async (req, signal) =>
      page(
        await body(
          api.GET(`${D}/tables/{table}/rows`, {
            params: {
              path: { ...path, table: req.table },
              query: {
                order: req.order || undefined,
                after: req.after || undefined,
                limit: req.limit,
                filter: req.filters?.length
                  ? JSON.stringify(req.filters)
                  : undefined,
                where: req.where || undefined,
                count: req.count || undefined,
                full: req.full || undefined,
                reveal: req.reveal || undefined,
              },
            },
            signal,
          }),
        ),
      ),
    references: async (req, signal) => {
      const refs = await body(
        api.GET(`${D}/tables/{table}/references`, {
          params: {
            path: { ...path, table: req.table },
            query: { key: JSON.stringify(req.key), limit: req.limit },
          },
          signal,
        }),
      );
      return (
        refs?.map((r): DataReferences => ({ ...r, page: page(r.page) })) ?? null
      );
    },
    query: async function* (req, signal) {
      const stream = await body(
        api.POST(`${D}/query`, {
          ...params,
          body: req,
          signal,
          parseAs: "stream",
        }),
      );
      if (!stream)
        throw new Error("The query response had no body. Run it again.");
      yield* frames(stream);
    },
    history: (before) =>
      body(
        api.GET(`${D}/history`, {
          params: { path, query: { before, limit: dataLimits.historyRows } },
        }),
      ),
    savedQueries: () => body(api.GET(`${D}/saved-queries`, params)),
    saveQuery: (req, id) =>
      body(
        id === undefined
          ? api.POST(`${D}/saved-queries`, { ...params, body: req })
          : api.PUT(`${D}/saved-queries/{id}`, {
              params: { path: { ...path, id } },
              body: req,
            }),
      ),
    deleteSavedQuery: (id) =>
      done(
        api.DELETE(`${D}/saved-queries/{id}`, {
          params: { path: { ...path, id } },
        }),
      ),
    changeset: (req) =>
      body(api.POST(`${D}/changesets`, { ...params, body: req })),
    changesets: () => body(api.GET(`${D}/changesets`, params)),
    commitChangeset: (id, req) =>
      body(
        api.POST(`${D}/changesets/{id}/commit`, {
          params: { path: { ...path, id } },
          body: req,
        }),
      ),
    rejectChangeset: (id, req) =>
      body(
        api.POST(`${D}/changesets/{id}/reject`, {
          params: { path: { ...path, id } },
          body: req,
        }),
      ),
    setSensitive: (columns) =>
      body(api.PUT(`${D}/sensitive-columns`, { ...params, body: { columns } })),
  };
}

const byId = (id: number) => ({ params: { path: { id } } });
const byName = (connection: string) => ({ params: { path: { connection } } });

/** The Backend talking to the tempobase server this page came from. */
export function httpBackend(): Backend {
  return {
    version: () => body(api.GET("/api/v1/version")),
    setupNeeded: () => body(api.GET("/api/v1/setup")),
    setup: (req) => body(api.POST("/api/v1/setup", { body: req })),
    login: (req) => body(api.POST("/api/v1/auth/login", { body: req })),
    logout: () => done(api.POST("/api/v1/auth/logout")),
    link: (code) =>
      body(api.GET("/api/v1/links/{code}", { params: { path: { code } } })),
    useLink: (code, req) =>
      body(
        api.POST("/api/v1/links/{code}", {
          params: { path: { code } },
          body: req,
        }),
      ),
    me: () => body(api.GET("/api/v1/me")),
    changePassword: (req) =>
      done(api.POST("/api/v1/me/password", { body: req })),

    users: () => list(api.GET("/api/v1/users")),
    updateUser: (id, req) =>
      body(api.PATCH("/api/v1/users/{id}", { ...byId(id), body: req })),
    deleteUser: (id) => done(api.DELETE("/api/v1/users/{id}", byId(id))),
    resetLink: (id) =>
      body(api.POST("/api/v1/users/{id}/reset-link", byId(id))),
    invites: () => list(api.GET("/api/v1/invites")),
    createInvite: (req) => body(api.POST("/api/v1/invites", { body: req })),
    deleteInvite: (id) => done(api.DELETE("/api/v1/invites/{id}", byId(id))),

    tokens: () => list(api.GET("/api/v1/tokens")),
    createToken: (req) => body(api.POST("/api/v1/tokens", { body: req })),
    updateToken: (id, req) =>
      body(api.PATCH("/api/v1/tokens/{id}", { ...byId(id), body: req })),
    deleteToken: (id) => done(api.DELETE("/api/v1/tokens/{id}", byId(id))),

    connections: () => list(api.GET("/api/v1/connections")),
    connection: (name) => body(api.GET(C, byName(name))),
    createConnection: (req) =>
      body(api.POST("/api/v1/connections", { body: req })),
    testConnection: (req) =>
      body(api.POST("/api/v1/connections/test", { body: req })),
    updateConnection: (name, req) =>
      body(api.PATCH(C, { ...byName(name), body: req })),
    deleteConnection: (name) => done(api.DELETE(C, byName(name))),

    policy: (connection) =>
      connection === undefined
        ? body(api.GET("/api/v1/data-policy"))
        : body(api.GET(`${C}/data-policy`, byName(connection))),
    setPolicy: (rules, connection) =>
      connection === undefined
        ? body(api.PUT("/api/v1/data-policy", { body: { rules } }))
        : body(
            api.PUT(`${C}/data-policy`, {
              ...byName(connection),
              body: { rules },
            }),
          ),

    audit: (query) => list(api.GET("/api/v1/audit", { params: { query } })),
    auditDetail: (id) => body(api.GET("/api/v1/audit/{id}/detail", byId(id))),

    data: dataEndpoints,
  };
}
