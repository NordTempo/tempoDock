import { api, body, done } from "../../api/client";
import type { components } from "../../api/schema";
import { keys } from "../../data/queries";
import type { ServiceRef } from "../../data/types";
import type { DataSource } from "./api";
import { policyQuery, subjectNames } from "./dockPolicy";
import { frames } from "./ndjson";
import type { DataPage, DataReferences, DataRow } from "./types";
import { dataLimits } from "./types";

// The daemon's data endpoints (ADR 0016 section 8) as a DataSource bound
// to one service.

const P =
  "/api/v1/projects/{project}/environments/{environment}/services/{service}/data";

/** The server encodes each row as DataCell[]; the schema only says unknown. */
const page = (p: components["schemas"]["DataPage"]): DataPage => ({
  ...p,
  rows: p.rows as DataRow[] | null,
});

/**
 * One database service's panel, for `<DataSourceContext value={...}>`.
 * Make it once per service (useMemo): the panel's hooks key on it.
 */
export function serviceSource(ref: ServiceRef): DataSource {
  const path = {
    project: ref.project,
    environment: ref.environment,
    service: ref.service,
  };
  const params = { params: { path } };
  return {
    key: keys.data(ref),
    cli: `-s ${ref.service} -e ${ref.environment}`,
    roles: {
      reader: "dock_reader",
      writer: "dock_writer",
      masked: "dock_reader_masked",
    },
    confirmName: ref.environment,
    words: {
      product: "Dock",
      owner: "the owner credentials from the service's variables",
      target: "Environment",
      unreachable: "The container runs, but the database didn't answer.",
      notRunning:
        "The service has no running container. Deploy or restart it, then come back.",
      queryStatsOff:
        "Dock adds pg_stat_statements to the start command. The change waits to apply and goes live with the next deploy, which restarts the database for a few seconds.",
      stagesQueryStats: true,
    },
    subjectNames,
    policy: policyQuery({ project: ref.project, environment: ref.environment }),

    info: () => body(api.GET(P, params)),
    setup: () => body(api.POST(`${P}/setup` as const, params)),
    schema: () => body(api.GET(`${P}/schema` as const, params)),
    insights: (order) =>
      body(
        api.GET(`${P}/insights` as const, {
          params: { path, query: { order } },
        }),
      ),
    cancelSession: (session, req) =>
      body(
        api.POST(`${P}/insights/sessions/{session}/cancel` as const, {
          params: { path: { ...path, session } },
          body: req,
        }),
      ),
    resetQueryStats: () =>
      done(api.POST(`${P}/insights/queries/reset` as const, params)),
    enableQueryStats: () =>
      body(api.POST(`${P}/insights/queries/enable` as const, params)),
    rows: async (req, signal) =>
      page(
        await body(
          api.GET(`${P}/tables/{table}/rows` as const, {
            params: {
              path: { ...path, table: req.table },
              query: {
                order: req.order || undefined,
                after: req.after || undefined,
                limit: req.limit,
                filter: req.filters?.length
                  ? JSON.stringify(req.filters)
                  : undefined,
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
        api.GET(`${P}/tables/{table}/references` as const, {
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
        api.POST(`${P}/query` as const, {
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
        api.GET(`${P}/history` as const, {
          params: { path, query: { before, limit: dataLimits.historyRows } },
        }),
      ),
    savedQueries: () => body(api.GET(`${P}/saved-queries` as const, params)),
    saveQuery: (req, id) =>
      body(
        id === undefined
          ? api.POST(`${P}/saved-queries` as const, { ...params, body: req })
          : api.PUT(`${P}/saved-queries/{id}` as const, {
              params: { path: { ...path, id } },
              body: req,
            }),
      ),
    deleteSavedQuery: (id) =>
      done(
        api.DELETE(`${P}/saved-queries/{id}` as const, {
          params: { path: { ...path, id } },
        }),
      ),
    changeset: (req) =>
      body(api.POST(`${P}/changesets` as const, { ...params, body: req })),
    changesets: () => body(api.GET(`${P}/changesets` as const, params)),
    commitChangeset: (id, req) =>
      body(
        api.POST(`${P}/changesets/{id}/commit` as const, {
          params: { path: { ...path, id } },
          body: req,
        }),
      ),
    rejectChangeset: (id, req) =>
      body(
        api.POST(`${P}/changesets/{id}/reject` as const, {
          params: { path: { ...path, id } },
          body: req,
        }),
      ),
    setSensitive: (columns) =>
      body(
        api.PUT(`${P}/sensitive-columns` as const, {
          ...params,
          body: { columns },
        }),
      ),
  };
}
