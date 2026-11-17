import {
  type QueryKey,
  useInfiniteQuery,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import {
  createContext,
  use,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { settling, useAction } from "../../data/action";
import type { PolicyRule } from "./policy";
import type { ChangeBody, CommitOptions, EditActions } from "./ReviewPanel";
import type {
  DataCancelRequest,
  DataCancelResult,
  DataChangeset,
  DataChangesetRequest,
  DataColumnRef,
  DataCommitRequest,
  DataFilter,
  DataHistoryEntry,
  DataInfo,
  DataInsights,
  DataKeyPage,
  DataKeyRequest,
  DataKeysRequest,
  DataKeyValue,
  DataPage,
  DataPageRequest,
  DataPermission,
  DataQueryError,
  DataQueryFrame,
  DataQueryRequest,
  DataQueryStatsResult,
  DataReferences,
  DataReferencesRequest,
  DataRejectRequest,
  DataResultColumn,
  DataRow,
  DataSavedQuery,
  DataSchema,
  DataStatementOrder,
  DataSubject,
  SaveDataQueryRequest,
} from "./types";
import { dataLimits } from "./types";

// Data access for the database panel. Components only use the hooks below;
// the hooks call the DataSource from context, which is bound to one
// database: a Dock service (./source.ts) or a tempoBase connection
// (web/tempobase/src/data/source.ts). Every method mirrors one endpoint
// under that database's /data (ADR 0016 section 8, ADR 0024 section 3);
// both hosts send the same types.

/** The data endpoints of one database. */
export type DataEndpoints = {
  /** GET /data: engine, status and the caller's access. */
  info(): Promise<DataInfo>;
  /** POST /data/setup (admin): create or rotate the panel's roles. */
  setup(): Promise<DataInfo>;
  /** GET /data/schema: the introspection result. */
  schema(): Promise<DataSchema>;
  /**
   * GET /data/tables/{table}/rows?order=&after=&limit=&filter=<JSON
   * DataFilter[]>: one keyset page.
   */
  rows(req: DataPageRequest, signal: AbortSignal): Promise<DataPage>;
  /** GET /data/tables/{table}/references?key=<JSON>&limit=: incoming FK rows. */
  references(
    req: DataReferencesRequest,
    signal: AbortSignal,
  ): Promise<DataReferences[] | null>;
  /**
   * POST /data/query: an NDJSON stream of frames (header, rows..., end or
   * error). Aborting the signal cancels the query on the server.
   */
  query(
    req: DataQueryRequest,
    signal: AbortSignal,
  ): AsyncIterable<DataQueryFrame>;
  /** GET /data/history?before=<audit_id>: the caller's past queries, newest first. */
  history(before?: number): Promise<DataHistoryEntry[] | null>;
  /** GET /data/saved-queries */
  savedQueries(): Promise<DataSavedQuery[] | null>;
  /** POST /data/saved-queries (new) or PUT /data/saved-queries/{id}. */
  saveQuery(body: SaveDataQueryRequest, id?: number): Promise<DataSavedQuery>;
  /** DELETE /data/saved-queries/{id} */
  deleteSavedQuery(id: number): Promise<void>;
  /** POST /data/changesets: preview with dry_run, else stage it as pending. */
  changeset(req: DataChangesetRequest): Promise<DataChangeset>;
  /** GET /data/changesets: the changesets the caller may see, newest first. */
  changesets(): Promise<DataChangeset[] | null>;
  /**
   * POST /data/changesets/{id}/commit: one's own with level write, anyone's
   * as an admin (approve). `confirm` is the source's `confirmName` on
   * production.
   */
  commitChangeset(id: number, req: DataCommitRequest): Promise<DataChangeset>;
  /** POST /data/changesets/{id}/reject (admin, or the author withdrawing). */
  rejectChangeset(id: number, req: DataRejectRequest): Promise<DataChangeset>;
  /** PUT /data/sensitive-columns (admin): replaces the list. */
  setSensitive(columns: DataColumnRef[]): Promise<DataColumnRef[] | null>;
  /**
   * Key-value databases (Redis): GET /data/keys, one page of keys; GET
   * /data/keys/value, one key with a page of its value; PUT
   * /data/sensitive-keys (admin). Only hosts with key-value databases
   * (Dock) have them.
   */
  keys?(req: DataKeysRequest, signal: AbortSignal): Promise<DataKeyPage>;
  keyValue?(req: DataKeyRequest, signal: AbortSignal): Promise<DataKeyValue>;
  setSensitiveKeys?(patterns: string[]): Promise<string[] | null>;
  /**
   * GET /data/insights?order=: every view the caller may see, read live
   * from the database's statistics.
   */
  insights(order: DataStatementOrder): Promise<DataInsights>;
  /** POST /data/insights/sessions/{session}/cancel (level write). */
  cancelSession(
    session: string,
    req: DataCancelRequest,
  ): Promise<DataCancelResult>;
  /** POST /data/insights/queries/reset (admin). */
  resetQueryStats(): Promise<void>;
  /** POST /data/insights/queries/enable: stages or creates it. */
  enableQueryStats(): Promise<DataQueryStatsResult>;
};

/** The panel's database roles, as messages name them. */
export type DataRoles = { reader: string; writer: string; masked: string };

/** What the panel says about its host where Dock and tempoBase differ. */
export type DataWords = {
  /** Who holds the roles and their passwords: "Dock". */
  product: string;
  /** What setup creates the roles with: "the owner credentials from ...". */
  owner: string;
  /** What a production commit confirms the name of: "Environment". */
  target: string;
  /** Status unreachable, when the server gives no reason. */
  unreachable: string;
  /** Status not_running. */
  notRunning: string;
  /**
   * The server doesn't load pg_stat_statements: what turning it on
   * involves. With `stagesQueryStats`, the host does it (Dock stages the
   * start command change); otherwise it says what to change.
   */
  queryStatsOff: string;
  stagesQueryStats: boolean;
};

/**
 * One database's endpoints and how to talk about it: its query key prefix,
 * CLI flags for command hints (absent: no hints), the role names, the name
 * a production commit confirms, and the effective policy where it lives.
 */
export type DataSource = DataEndpoints & {
  key: readonly unknown[];
  cli?: string;
  roles: DataRoles;
  confirmName: string;
  words: DataWords;
  /** The policy's subjects in the host's words ("read users", "viewers"). */
  subjectNames: Record<DataSubject, string>;
  /** The effective policy for this database (the sensitive columns note). */
  policy: {
    queryKey: readonly unknown[];
    queryFn: () => Promise<{ effective: readonly PolicyRule[] | null }>;
  };
};

/** Provide with `<DataSourceContext value={source}>` around the panel. */
export const DataSourceContext = createContext<DataSource | null>(null);

/** The bound source from context. */
export function useDataSource() {
  const source = use(DataSourceContext);
  if (!source)
    throw new Error("The data panel needs a DataSourceContext provider");
  return source;
}

/** The source's cache subtrees for these parts of the panel. */
const parts = (source: DataSource, ...names: string[]): QueryKey[] =>
  names.map((name) => [...source.key, name]);

/**
 * A CLI command hint with the database's flags appended, or undefined
 * where the host has no CLI.
 */
export function useCli() {
  const { cli } = useDataSource();
  return (command: string) =>
    cli === undefined ? undefined : `${command} ${cli}`;
}

/** GET /data. */
export function useDataInfo() {
  const source = useDataSource();
  return useQuery({
    queryKey: [...source.key, "info"],
    queryFn: () => source.info(),
  });
}

/** POST /data/setup; the result replaces the panel's info. */
export function useDataSetup() {
  const source = useDataSource();
  const client = useQueryClient();
  return useAction(
    async () => {
      const info = await source.setup();
      client.setQueryData([...source.key, "info"], info);
      return info;
    },
    () => [[...source.key, "schema"]],
  );
}

/** GET /data/schema. The server caches the catalog for 60 s, so do we. */
export function useDataSchema(enabled = true) {
  const source = useDataSource();
  return useQuery({
    queryKey: [...source.key, "schema"],
    queryFn: () => source.schema(),
    staleTime: 60_000,
    enabled,
  });
}

/**
 * GET /data/insights. Read when the Health tab opens and on refresh,
 * never in the background: the result is the database's state at
 * read_at. keepPreviousData lets the queries view show what changed
 * between two reads.
 */
export function useInsights(order: DataStatementOrder) {
  const source = useDataSource();
  return useQuery({
    queryKey: [...source.key, "insights", order],
    queryFn: () => source.insights(order),
    staleTime: Number.POSITIVE_INFINITY,
    gcTime: 0,
    refetchOnWindowFocus: false,
    placeholderData: (prev) => prev,
  });
}

/** The effective policy where the database lives. */
export function useDataPolicy() {
  const { policy } = useDataSource();
  return useQuery({ queryKey: [...policy.queryKey], queryFn: policy.queryFn });
}

/**
 * What a table view shows: one table, its sort and filters, and whether
 * its sensitive columns are revealed (never in the URL).
 */
export type TableQuery = {
  table: string;
  order?: string;
  filters?: DataFilter[];
  reveal?: boolean;
};

/**
 * GET /data/tables/{table}/rows, as keyset pages: fetchNextPage loads the
 * page after the last one's `next` cursor.
 */
export function useTableRows(q: TableQuery | null) {
  const source = useDataSource();
  return useInfiniteQuery({
    queryKey: [
      ...source.key,
      "rows",
      q?.table,
      q?.order ?? "",
      q?.filters ?? [],
      !!q?.reveal,
    ],
    queryFn: ({ pageParam, signal }) => {
      if (!q) throw new Error("No table selected");
      return source.rows(
        {
          table: q.table,
          order: q.order,
          filters: q.filters,
          after: pageParam || undefined,
          reveal: q.reveal,
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

/** The source's key-value methods; the panel only shows keys with them. */
function keySource(source: DataSource) {
  const { keys, keyValue, setSensitiveKeys } = source;
  if (!keys || !keyValue || !setSensitiveKeys)
    throw new Error("This host has no key-value databases.");
  return { keys, keyValue, setSensitiveKeys };
}

/**
 * GET /data/keys, as SCAN pages: fetchNextPage continues the scan. A page
 * may be short (or empty) and still have a next.
 */
export function useKeyPages(q: Omit<DataKeysRequest, "after">) {
  const source = useDataSource();
  return useInfiniteQuery({
    queryKey: [...source.key, "keys", q.db ?? 0, q.match ?? "", q.type ?? ""],
    queryFn: ({ pageParam, signal }) =>
      keySource(source).keys({ ...q, after: pageParam || undefined }, signal),
    initialPageParam: "",
    getNextPageParam: (page) => page.next || undefined,
    staleTime: Number.POSITIVE_INFINITY,
  });
}

/** GET /data/keys/value for one key (null: none selected), page by page. */
export function useKeyValue(q: Omit<DataKeyRequest, "after"> | null) {
  const source = useDataSource();
  return useInfiniteQuery({
    queryKey: [...source.key, "keys", "value", q],
    queryFn: ({ pageParam, signal }) => {
      if (!q) throw new Error("No key selected");
      return keySource(source).keyValue(
        { ...q, after: pageParam || undefined },
        signal,
      );
    },
    initialPageParam: "",
    getNextPageParam: (page) => page.next || undefined,
    enabled: !!q,
    staleTime: Number.POSITIVE_INFINITY,
  });
}

/** PUT /data/sensitive-keys (admin); the keyspace and key pages refetch. */
export function useSetSensitiveKeys() {
  const source = useDataSource();
  return useAction(
    (patterns: string[]) => keySource(source).setSensitiveKeys(patterns),
    () => parts(source, "schema", "keys"),
  );
}

/** GET /data/tables/{table}/references for one row (null: none selected). */
export function useReferences(
  table: string,
  key: Record<string, string> | null,
) {
  const source = useDataSource();
  return useQuery({
    queryKey: [...source.key, "references", table, key],
    queryFn: ({ signal }) => {
      if (!key) throw new Error("No row selected");
      return source.references({ table, key }, signal);
    },
    enabled: !!key,
  });
}

/** GET /data/history, paged back by audit ID. */
export function useQueryHistory() {
  const source = useDataSource();
  return useInfiniteQuery({
    queryKey: [...source.key, "history"],
    queryFn: ({ pageParam }) => source.history(pageParam),
    initialPageParam: undefined as number | undefined,
    // A short page is the last one.
    getNextPageParam: (page) =>
      page && page.length >= dataLimits.historyRows
        ? page.at(-1)?.audit_id
        : undefined,
  });
}

/** GET /data/saved-queries */
export function useSavedQueries() {
  const source = useDataSource();
  return useQuery({
    queryKey: [...source.key, "saved"],
    queryFn: () => source.savedQueries(),
  });
}

/** PUT /data/saved-queries[/{id}] */
export function useSaveQuery() {
  const source = useDataSource();
  return useAction(
    ({ id, ...body }: SaveDataQueryRequest & { id?: number }) =>
      source.saveQuery(body, id),
    () => parts(source, "saved"),
  );
}

/** DELETE /data/saved-queries/{id} */
export function useDeleteSavedQuery() {
  const source = useDataSource();
  return useAction(
    (id: number) => source.deleteSavedQuery(id),
    () => parts(source, "saved"),
  );
}

/** GET /data/changesets: pending proposals and recent decisions. */
export function useChangesets(enabled: boolean) {
  const source = useDataSource();
  return useQuery({
    queryKey: [...source.key, "changesets"],
    queryFn: () => source.changesets(),
    enabled,
  });
}

/** A commit that came back with conflicts, as an error naming them. */
function conflictError(c: DataChangeset) {
  const rows = (c.conflicts ?? []).map((x) => x.message).join("; ");
  return new Error(
    `Nothing was written: changeset ${c.id} conflicts with current data${rows ? `: ${rows}` : ""}. Reload the rows and edit again.`,
  );
}

/** Throws when a commit reports conflicts instead of failing the request. */
function committed(c: DataChangeset) {
  if (c.status === "conflict") throw conflictError(c);
  return c;
}

/** What a decided changeset changes: the list and the rows. */
const decided = (source: DataSource) =>
  parts(source, "changesets", "rows", "references");

/** POST /data/changesets/{id}/commit: approve (admin) or commit. */
export function useCommitChangeset() {
  const source = useDataSource();
  return useAction(
    async ({ id, confirm }: { id: number; confirm?: string }) =>
      committed(await source.commitChangeset(id, { confirm })),
    () => decided(source),
  );
}

/** POST /data/changesets/{id}/reject */
export function useRejectChangeset() {
  const source = useDataSource();
  return useAction(
    ({ id, reason }: { id: number; reason?: string }) =>
      source.rejectChangeset(id, { reason }),
    () => decided(source),
  );
}

/**
 * PUT /data/sensitive-columns (admin): replaces the list. The schema shows
 * the marks and pages mask by them, so both refetch.
 */
export function useSetSensitive() {
  const source = useDataSource();
  return useAction(
    (columns: DataColumnRef[]) => source.setSensitive(columns),
    () => parts(source, "schema", "rows", "references"),
  );
}

/**
 * Staged edits to the API: the SQL comes from a dry run; a commit stages
 * the changeset, then commits it with level write, or leaves it pending
 * for an admin with level propose or when asked to propose (`onProposed`
 * then gets it).
 */
export function useEditActions(
  access: DataPermission | undefined,
  onProposed: (changeset: DataChangeset) => void,
): EditActions | undefined {
  const source = useDataSource();
  const client = useQueryClient();
  const level = access?.level;
  const proposed = useRef(onProposed);
  proposed.current = onProposed;

  return useMemo(() => {
    if (level !== "propose" && level !== "write") return undefined;
    return {
      renderSql: async (body: ChangeBody) =>
        (await source.changeset({ ...body, dry_run: true })).sql,
      commit: async (
        body: ChangeBody,
        { confirm, message, propose }: CommitOptions,
      ) => {
        const staged = await settling(
          client,
          parts(source, "changesets"),
          async () => {
            const staged = await source.changeset({ ...body, message });
            if (level !== "propose" && !propose)
              committed(await source.commitChangeset(staged.id, { confirm }));
            return staged;
          },
        );
        if (level === "propose" || propose) proposed.current(staged);
      },
    };
  }, [source, client, level]);
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

/** A query's own row cap and statement timeout; 0 or absent: defaults. */
export type QueryOptions = Pick<
  DataQueryRequest,
  "limit" | "timeout_seconds" | "db"
>;

/**
 * POST /data/query, streamed: `run` starts a query (canceling one still
 * running), `cancel` stops it. Rows append per frame (up to 500 each). A
 * finished query refreshes the history, which the server records.
 */
export function useQueryRunner() {
  const source = useDataSource();
  const client = useQueryClient();
  const [result, setResult] = useState<QueryRun | null>(null);
  const controller = useRef<AbortController | null>(null);

  useEffect(() => () => controller.current?.abort(), []);

  const run = useCallback(
    async (sql: string, options: QueryOptions = {}) => {
      controller.current?.abort();
      const abort = new AbortController();
      controller.current = abort;
      let current: QueryRun = { sql, state: "running", columns: [], rows: [] };
      const update = (patch: Partial<QueryRun>) => {
        current = { ...current, ...patch };
        if (!abort.signal.aborted) setResult(current);
      };
      update({});
      // The server records the query in the history.
      await settling(client, parts(source, "history"), async () => {
        try {
          for await (const frame of source.query(
            { sql, ...options },
            abort.signal,
          )) {
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
      });
    },
    [source, client],
  );

  const cancel = useCallback(() => {
    controller.current?.abort();
    setResult((r) =>
      r?.state === "running" ? { ...r, state: "canceled" } : r,
    );
  }, []);

  return { result, run, cancel };
}
