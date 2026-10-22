import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import {
  createContext,
  use,
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";
import { keys } from "../../data/queries";
import type { ServiceRef } from "../../data/types";
import type {
  DataChangeset,
  DataChangesetRequest,
  DataFilter,
  DataHistoryEntry,
  DataInfo,
  DataPage,
  DataPageRequest,
  DataQueryError,
  DataQueryFrame,
  DataQueryRequest,
  DataReferences,
  DataReferencesRequest,
  DataResultColumn,
  DataRow,
  DataSavedQuery,
  DataSchema,
  SaveDataQueryRequest,
} from "./types";

// Data access for the database panel. Components only use the hooks below;
// the hooks call a DataSource from context. Every method mirrors one
// endpoint under /api/v1/projects/{p}/environments/{e}/services/{s} (ADR
// 0016 section 8), so phase 3 wires the API by providing a DataSource built
// on openapi-fetch (`api.GET(...)`) where the Data route renders the panel.
// The dev demo provides the one in ./fixtures.ts.

/** The data endpoints of one Dock daemon. */
export type DataSource = {
  /** GET /data: engine, status and the caller's access. */
  info(ref: ServiceRef): Promise<DataInfo>;
  /** POST /data/setup (admin): create or rotate dock_reader and dock_writer. */
  setup(ref: ServiceRef): Promise<DataInfo>;
  /** GET /data/schema: the introspection result. */
  schema(ref: ServiceRef): Promise<DataSchema>;
  /**
   * GET /data/tables/{table}/rows?order=&after=&limit=&filter=<JSON
   * DataFilter[]>: one keyset page.
   */
  rows(
    ref: ServiceRef,
    req: DataPageRequest,
    signal: AbortSignal,
  ): Promise<DataPage>;
  /** GET /data/tables/{table}/references?key=<JSON>&limit=: incoming FK rows. */
  references(
    ref: ServiceRef,
    req: DataReferencesRequest,
    signal: AbortSignal,
  ): Promise<DataReferences[] | null>;
  /**
   * POST /data/query: an NDJSON stream of frames (header, rows..., end or
   * error). Aborting the signal cancels the query on the server.
   */
  query(
    ref: ServiceRef,
    req: DataQueryRequest,
    signal: AbortSignal,
  ): AsyncIterable<DataQueryFrame>;
  /**
   * GET /data/history?before=<audit_id>: the caller's past queries, newest
   * first. Not in the ADR's endpoint table yet; the Surface track names it.
   */
  history(ref: ServiceRef, before?: number): Promise<DataHistoryEntry[] | null>;
  /** GET /data/saved-queries */
  savedQueries(ref: ServiceRef): Promise<DataSavedQuery[] | null>;
  /** PUT /data/saved-queries (new) or /data/saved-queries/{id} (replace). */
  saveQuery(
    ref: ServiceRef,
    body: SaveDataQueryRequest,
    id?: number,
  ): Promise<DataSavedQuery>;
  /** DELETE /data/saved-queries/{id} */
  deleteSavedQuery(ref: ServiceRef, id: number): Promise<void>;
  /** POST /data/changesets: preview with dry_run, else stage a proposal. */
  changeset(ref: ServiceRef, req: DataChangesetRequest): Promise<DataChangeset>;
};

/** Provide with `<DataSourceContext value={source}>` around the panel. */
export const DataSourceContext = createContext<DataSource | null>(null);

function useSource() {
  const source = use(DataSourceContext);
  if (!source)
    throw new Error("The data panel needs a DataSourceContext provider");
  return source;
}

const dataKey = (ref: ServiceRef) => [...keys.service(ref), "data"];

/** GET /data. */
export function useDataInfo(ref: ServiceRef) {
  const source = useSource();
  return useQuery({
    queryKey: [...dataKey(ref), "info"],
    queryFn: () => source.info(ref),
  });
}

/** POST /data/setup; the result replaces the panel's info. */
export function useDataSetup(ref: ServiceRef) {
  const source = useSource();
  const client = useQueryClient();
  return useMutation({
    mutationFn: () => source.setup(ref),
    onSuccess: (info) => {
      client.setQueryData([...dataKey(ref), "info"], info);
      return client.invalidateQueries({
        queryKey: [...dataKey(ref), "schema"],
      });
    },
  });
}

/** GET /data/schema. The server caches the catalog for 60 s, so do we. */
export function useDataSchema(ref: ServiceRef, enabled = true) {
  const source = useSource();
  return useQuery({
    queryKey: [...dataKey(ref), "schema"],
    queryFn: () => source.schema(ref),
    staleTime: 60_000,
    enabled,
  });
}

/** What a table view shows: one table, its sort and filters. */
export type TableQuery = {
  table: string;
  order?: string;
  filters?: DataFilter[];
};

/**
 * GET /data/tables/{table}/rows, as keyset pages: fetchNextPage loads the
 * page after the last one's `next` cursor.
 */
export function useTableRows(ref: ServiceRef, q: TableQuery | null) {
  const source = useSource();
  return useInfiniteQuery({
    queryKey: [
      ...dataKey(ref),
      "rows",
      q?.table,
      q?.order ?? "",
      q?.filters ?? [],
    ],
    queryFn: ({ pageParam, signal }) => {
      if (!q) throw new Error("No table selected");
      return source.rows(
        ref,
        {
          table: q.table,
          order: q.order,
          filters: q.filters,
          after: pageParam || undefined,
        },
        signal,
      );
    },
    initialPageParam: "",
    getNextPageParam: (page) => page.next || undefined,
    enabled: !!q,
    // Rows aren't live: a page stays as read until the view reloads.
    staleTime: Number.POSITIVE_INFINITY,
  });
}

/** GET /data/tables/{table}/references for one row (null: none selected). */
export function useReferences(
  ref: ServiceRef,
  table: string,
  key: Record<string, string> | null,
) {
  const source = useSource();
  return useQuery({
    queryKey: [...dataKey(ref), "references", table, key],
    queryFn: ({ signal }) => {
      if (!key) throw new Error("No row selected");
      return source.references(ref, { table, key }, signal);
    },
    enabled: !!key,
  });
}

/** GET /data/history, paged back by audit ID. */
export function useQueryHistory(ref: ServiceRef) {
  const source = useSource();
  return useInfiniteQuery({
    queryKey: [...dataKey(ref), "history"],
    queryFn: ({ pageParam }) => source.history(ref, pageParam),
    initialPageParam: undefined as number | undefined,
    getNextPageParam: (page) => page?.at(-1)?.audit_id,
  });
}

/** GET /data/saved-queries */
export function useSavedQueries(ref: ServiceRef) {
  const source = useSource();
  return useQuery({
    queryKey: [...dataKey(ref), "saved"],
    queryFn: () => source.savedQueries(ref),
  });
}

/** PUT /data/saved-queries[/{id}] */
export function useSaveQuery(ref: ServiceRef) {
  const source = useSource();
  const client = useQueryClient();
  return useMutation({
    mutationFn: ({ id, ...body }: SaveDataQueryRequest & { id?: number }) =>
      source.saveQuery(ref, body, id),
    onSuccess: () =>
      client.invalidateQueries({ queryKey: [...dataKey(ref), "saved"] }),
  });
}

/** DELETE /data/saved-queries/{id} */
export function useDeleteSavedQuery(ref: ServiceRef) {
  const source = useSource();
  const client = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => source.deleteSavedQuery(ref, id),
    onSuccess: () =>
      client.invalidateQueries({ queryKey: [...dataKey(ref), "saved"] }),
  });
}

/** POST /data/changesets with dry_run: the SQL a commit would run. */
export function usePreviewChangeset(ref: ServiceRef) {
  const source = useSource();
  return useMutation({
    mutationFn: (changes: DataChangesetRequest["changes"]) =>
      source.changeset(ref, { changes, dry_run: true }),
  });
}

/** A SQL query's result as its frames arrive. */
export type QueryRun = {
  sql: string;
  /** running until the end or error frame (or a failed request). */
  state: "running" | "done" | "failed" | "canceled";
  columns: DataResultColumn[];
  rows: DataRow[];
  /** The end frame. */
  end?: DataQueryFrame;
  /** The error frame, or the request's own error as a message. */
  error?: DataQueryError;
};

/**
 * POST /data/query, streamed: `run` starts a query (canceling one still
 * running), `cancel` stops it. Rows append per frame (up to 500 each). A
 * finished query refreshes the history, which the server records.
 */
export function useQueryRunner(ref: ServiceRef) {
  const source = useSource();
  const client = useQueryClient();
  const [result, setResult] = useState<QueryRun | null>(null);
  const controller = useRef<AbortController | null>(null);

  useEffect(() => () => controller.current?.abort(), []);

  const run = useCallback(
    async (sql: string) => {
      controller.current?.abort();
      const abort = new AbortController();
      controller.current = abort;
      let current: QueryRun = { sql, state: "running", columns: [], rows: [] };
      const update = (patch: Partial<QueryRun>) => {
        current = { ...current, ...patch };
        if (!abort.signal.aborted) setResult(current);
      };
      update({});
      try {
        for await (const frame of source.query(ref, { sql }, abort.signal)) {
          switch (frame.type) {
            case "header":
              update({ columns: frame.columns ?? [] });
              break;
            case "rows":
              update({ rows: current.rows.concat(frame.rows ?? []) });
              break;
            case "end":
              update({ state: "done", end: frame });
              break;
            case "error":
              update({ state: "failed", error: frame.error });
              break;
          }
        }
      } catch (e) {
        if (abort.signal.aborted) return;
        update({
          state: "failed",
          error: { message: e instanceof Error ? e.message : String(e) },
        });
      }
      if (!abort.signal.aborted) {
        client.invalidateQueries({ queryKey: [...dataKey(ref), "history"] });
      }
    },
    [source, client, ref],
  );

  const cancel = useCallback(() => {
    controller.current?.abort();
    setResult((r) =>
      r?.state === "running" ? { ...r, state: "canceled" } : r,
    );
  }, []);

  return { result, run, cancel };
}
