import { api, body, done } from "../../api/client";
import type { components } from "../../api/schema";
import type { ServiceRef } from "../../data/types";
import type { DataSource } from "./api";
import type {
  DataPage,
  DataQueryFrame,
  DataReferences,
  DataRow,
} from "./types";
import { dataLimits } from "./types";

// The daemon's data endpoints (ADR 0016 section 8) as a DataSource.

const P =
  "/api/v1/projects/{project}/environments/{environment}/services/{service}/data";

const path = (ref: ServiceRef) => ({
  project: ref.project,
  environment: ref.environment,
  service: ref.service,
});

/** The server encodes each row as DataCell[]; the schema only says unknown. */
const page = (p: components["schemas"]["DataPage"]): DataPage => ({
  ...p,
  rows: p.rows as DataRow[] | null,
});

/**
 * Splits an NDJSON body into frames as it arrives. The caller's abort
 * signal closes the request; the daemon then cancels the query.
 */
async function* frames(
  stream: ReadableStream<Uint8Array<ArrayBuffer>>,
): AsyncGenerator<DataQueryFrame> {
  const reader = stream.pipeThrough(new TextDecoderStream()).getReader();
  let buffered = "";
  try {
    for (;;) {
      const { value, done: finished } = await reader.read();
      if (finished) break;
      buffered += value;
      let newline = buffered.indexOf("\n");
      while (newline >= 0) {
        const line = buffered.slice(0, newline).trim();
        buffered = buffered.slice(newline + 1);
        if (line) yield JSON.parse(line) as DataQueryFrame;
        newline = buffered.indexOf("\n");
      }
    }
    if (buffered.trim()) yield JSON.parse(buffered) as DataQueryFrame;
  } finally {
    reader.releaseLock();
  }
}

/** The API's data endpoints, for `<DataSourceContext value={apiSource}>`. */
export const apiSource: DataSource = {
  info: (ref) => body(api.GET(P, { params: { path: path(ref) } })),
  setup: (ref) =>
    body(api.POST(`${P}/setup` as const, { params: { path: path(ref) } })),
  schema: (ref) =>
    body(api.GET(`${P}/schema` as const, { params: { path: path(ref) } })),
  rows: async (ref, req, signal) =>
    page(
      await body(
        api.GET(`${P}/tables/{table}/rows` as const, {
          params: {
            path: { ...path(ref), table: req.table },
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
  references: async (ref, req, signal) => {
    const refs = await body(
      api.GET(`${P}/tables/{table}/references` as const, {
        params: {
          path: { ...path(ref), table: req.table },
          query: { key: JSON.stringify(req.key), limit: req.limit },
        },
        signal,
      }),
    );
    return (
      refs?.map((r): DataReferences => ({ ...r, page: page(r.page) })) ?? null
    );
  },
  query: async function* (ref, req, signal) {
    const stream = await body(
      api.POST(`${P}/query` as const, {
        params: { path: path(ref) },
        body: req,
        signal,
        parseAs: "stream",
      }),
    );
    if (!stream)
      throw new Error("The query response had no body. Run it again.");
    yield* frames(stream);
  },
  history: (ref, before) =>
    body(
      api.GET(`${P}/history` as const, {
        params: {
          path: path(ref),
          query: { before, limit: dataLimits.historyRows },
        },
      }),
    ),
  savedQueries: (ref) =>
    body(
      api.GET(`${P}/saved-queries` as const, { params: { path: path(ref) } }),
    ),
  saveQuery: (ref, req, id) =>
    body(
      id === undefined
        ? api.POST(`${P}/saved-queries` as const, {
            params: { path: path(ref) },
            body: req,
          })
        : api.PUT(`${P}/saved-queries/{id}` as const, {
            params: { path: { ...path(ref), id } },
            body: req,
          }),
    ),
  deleteSavedQuery: (ref, id) =>
    done(
      api.DELETE(`${P}/saved-queries/{id}` as const, {
        params: { path: { ...path(ref), id } },
      }),
    ),
  changeset: (ref, req) =>
    body(
      api.POST(`${P}/changesets` as const, {
        params: { path: path(ref) },
        body: req,
      }),
    ),
  changesets: (ref) =>
    body(api.GET(`${P}/changesets` as const, { params: { path: path(ref) } })),
  commitChangeset: (ref, id, req) =>
    body(
      api.POST(`${P}/changesets/{id}/commit` as const, {
        params: { path: { ...path(ref), id } },
        body: req,
      }),
    ),
  rejectChangeset: (ref, id, req) =>
    body(
      api.POST(`${P}/changesets/{id}/reject` as const, {
        params: { path: { ...path(ref), id } },
        body: req,
      }),
    ),
};
