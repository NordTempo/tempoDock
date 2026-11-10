import { useState } from "react";
import { modKey, useKeydown } from "../../shell/keys";
import { clock, day, plural } from "../../ui/format";
import {
  type QueryOptions,
  type QueryRun,
  useCli,
  useDeleteSavedQuery,
  useQueryHistory,
  useQueryRunner,
  useSavedQueries,
  useSaveQuery,
} from "./api";
import { toCsv } from "./cells";
import { Grid } from "./Grid";
import { Act, Btn, ErrorText, Head, Kbd } from "./kit";
import { SqlEditor } from "./SqlEditor";
import type { DataHistoryEntry, DataTable } from "./types";
import { dataLimits } from "./types";

// The SQL tab, loaded on demand (React.lazy in ./DataPanel) so CodeMirror
// is only fetched when someone opens it.

const outcomeGlyph: Record<DataHistoryEntry["outcome"], [string, string]> = {
  ok: ["●", "text-(--g-ok)"],
  error: ["×", "text-(--g-fail)"],
  conflict: ["×", "text-(--g-fail)"],
  timeout: ["▲", "text-(--g-warn)"],
  canceled: ["–", "text-(--g-mut)"],
};

/** "line 2, column 7" for a 1-based character position in `sql`. */
function lineCol(sql: string, position: number) {
  const before = sql.slice(0, position - 1).split("\n");
  return `line ${before.length}, column ${(before.at(-1)?.length ?? 0) + 1}`;
}

const shellQuote = (s: string) =>
  `'${s.replace(/\s+/g, " ").trim().replaceAll("'", "'\\''")}'`;

/** Row caps and timeouts to pick from; the first is the default. */
const rowLimits = [dataLimits.queryRows, 1_000, 100_000];
const timeouts = [10, 30, 60];

/** Saves text as a file through the browser. */
function download(name: string, text: string, type: string) {
  const url = URL.createObjectURL(new Blob([text], { type }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  URL.revokeObjectURL(url);
}

/**
 * Read-only SQL (G3): saved queries on the left, the editor (Mod-Enter
 * runs) under a header with Save and Run, then the streamed result in the
 * grid or the caller's history to load from.
 */
export default function SqlView({
  tables,
  initialSql = "",
}: {
  /** For completion. */
  tables: readonly DataTable[];
  /** Text to open the editor with (Health's "Explain in SQL"). */
  initialSql?: string;
}) {
  const [sql, setSql] = useState(initialSql);
  const [savedId, setSavedId] = useState<number | null>(null);
  const [naming, setNaming] = useState<string | null>(null);
  const [pane, setPane] = useState<"results" | "history">("results");
  const { result, run, cancel } = useQueryRunner();
  const [options, setOptions] = useState({
    limit: rowLimits[0] ?? dataLimits.queryRows,
    timeout: timeouts[0] ?? 10,
  });
  const queryOptions: QueryOptions = {
    limit: options.limit,
    timeout_seconds: options.timeout,
  };
  const flags = [
    options.limit !== 1_000 && `-n ${options.limit}`,
    options.timeout !== 10 && `--timeout ${options.timeout}s`,
  ]
    .filter(Boolean)
    .join(" ");
  const saved = useSavedQueries();
  const save = useSaveQuery();
  const remove = useDeleteSavedQuery();
  const cli = useCli();
  const running = result?.state === "running";
  // The result belongs to the text that ran; once that's edited it's dimmed.
  const stale = !!result && !running && result.sql !== sql;
  const current = saved.data?.find((q) => q.id === savedId);

  const start = () => {
    if (!sql.trim()) return;
    setPane("results");
    run(sql, queryOptions);
  };
  const load = (text: string, id: number | null = null) => {
    setSql(text);
    setSavedId(id);
    setPane("results");
  };

  useKeydown((e) => {
    if (e.defaultPrevented) return;
    if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
      e.preventDefault();
      start();
    } else if (e.key === "Escape" && running) cancel();
  });

  return (
    <div className="flex min-h-0 flex-1">
      <nav
        aria-label="Saved queries"
        className="flex w-[240px] shrink-0 flex-col gap-px overflow-y-auto border-r border-(--g-line) bg-(--g-side) p-2"
      >
        <div className="g-label flex items-center justify-between px-2.5 pt-1.5 pb-1">
          Saved queries
          <button
            type="button"
            className="g-lnk"
            disabled={!sql && savedId === null}
            onClick={() => load("")}
          >
            New
          </button>
        </div>
        {saved.data?.length === 0 && (
          <p className="px-2.5 text-xs text-(--g-mut)">Nothing saved yet.</p>
        )}
        {saved.data?.map((q) => (
          <div key={q.id} className="group relative">
            <button
              type="button"
              className="g-item pr-7"
              aria-current={q.id === savedId}
              title={q.sql}
              onClick={() => load(q.sql, q.id)}
            >
              <span className="truncate">{q.name}</span>
            </button>
            <button
              type="button"
              className="g-lnk absolute top-1.5 right-2 invisible group-hover:visible focus:visible"
              aria-label={`Delete ${q.name}`}
              onClick={() => {
                remove.mutate(q.id);
                if (q.id === savedId) setSavedId(null);
              }}
            >
              ×
            </button>
          </div>
        ))}
      </nav>

      <div className="flex min-w-0 flex-1 flex-col">
        <Head
          crumb="SQL"
          title={current?.name ?? "New query"}
          actions={
            <>
              <ErrorText error={save.error ?? remove.error} />
              {naming === null ? (
                <Btn
                  disabled={!sql.trim() || save.isPending}
                  onClick={() =>
                    current
                      ? save.mutate({ id: current.id, name: current.name, sql })
                      : setNaming("")
                  }
                >
                  Save
                </Btn>
              ) : (
                <form
                  className="flex items-center gap-1.5"
                  onSubmit={(e) => {
                    e.preventDefault();
                    if (!naming.trim()) return;
                    save.mutate(
                      { name: naming.trim(), sql },
                      {
                        onSuccess: (q) => {
                          setSavedId(q.id);
                          setNaming(null);
                        },
                      },
                    );
                  }}
                >
                  <input
                    // biome-ignore lint/a11y/noAutofocus: opened on request
                    autoFocus
                    className="g-field h-7 w-48"
                    placeholder="Name"
                    aria-label="Query name"
                    value={naming}
                    maxLength={100}
                    onChange={(e) => setNaming(e.target.value)}
                    onKeyDown={(e) => e.key === "Escape" && setNaming(null)}
                  />
                  <Btn type="submit">Save</Btn>
                </form>
              )}
              {running && (
                <Btn onClick={cancel}>
                  Cancel <Kbd>Esc</Kbd>
                </Btn>
              )}
              <Act
                primary
                cli={cli(
                  `dock db query ${shellQuote(sql || "SQL")}${flags ? ` ${flags}` : ""}`,
                )}
                disabled={!sql.trim() || running}
                onClick={start}
              >
                Run <Kbd>{`${modKey}↵`}</Kbd>
              </Act>
            </>
          }
        />
        <div className="h-[168px] shrink-0 border-b border-(--g-line)">
          <SqlEditor
            value={sql}
            onChange={setSql}
            onRun={start}
            tables={tables}
            errorAt={
              result?.state === "failed" ? result.error?.position : undefined
            }
          />
        </div>
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1 px-3.5 py-2">
          <div className="g-tabs">
            <button
              type="button"
              aria-pressed={pane === "results"}
              onClick={() => setPane("results")}
            >
              Results
            </button>
            <button
              type="button"
              aria-pressed={pane === "history"}
              onClick={() => setPane("history")}
            >
              History
            </button>
          </div>
          {pane === "results" && (
            <>
              <select
                aria-label="Row limit"
                className="g-field h-7"
                value={options.limit}
                onChange={(e) =>
                  setOptions({ ...options, limit: Number(e.target.value) })
                }
              >
                {rowLimits.map((n) => (
                  <option key={n} value={n}>
                    {n.toLocaleString("en")} rows
                  </option>
                ))}
              </select>
              <select
                aria-label="Statement timeout"
                className="g-field h-7"
                value={options.timeout}
                onChange={(e) =>
                  setOptions({ ...options, timeout: Number(e.target.value) })
                }
              >
                {timeouts.map((s) => (
                  <option key={s} value={s}>
                    {s} s timeout
                  </option>
                ))}
              </select>
              {result &&
                result.state !== "running" &&
                result.columns.length > 0 && (
                  <Act
                    small
                    cli={cli(
                      `dock db query ${shellQuote(result.sql)}${flags ? ` ${flags}` : ""} --csv > result.csv`,
                    )}
                    onClick={() =>
                      download(
                        "result.csv",
                        toCsv(result.columns, result.rows),
                        "text/csv",
                      )
                    }
                  >
                    CSV
                  </Act>
                )}
              <RunLine run={result} limit={options.limit} stale={stale} />
            </>
          )}
        </div>
        {pane === "history" ? (
          <History onLoad={load} />
        ) : (
          <>
            {result?.state === "failed" && <RunError run={result} />}
            <div
              className={`relative min-h-[200px] flex-1 border-t border-(--g-line) ${stale ? "opacity-50" : ""}`}
            >
              {result && result.columns.length > 0 && (
                <Grid columns={result.columns} rows={result.rows} />
              )}
              {/* Before the first run: what goes here, and how to get it. */}
              {!result && (
                <p className="p-4 text-(--g-mut)">
                  Rows appear here. Write a query above and press{" "}
                  <Kbd>{`${modKey}↵`}</Kbd> or Run.
                </p>
              )}
            </div>
          </>
        )}
      </div>
    </div>
  );
}

/** The caller's past queries, newest first; a click loads one. */
function History({ onLoad }: { onLoad: (sql: string) => void }) {
  const history = useQueryHistory();
  const entries = history.data?.pages.flatMap((page) => page ?? []) ?? [];
  return (
    <div className="min-h-0 flex-1 overflow-y-auto border-t border-(--g-line) px-2 pb-4">
      {history.error && (
        <p className="p-3">
          <ErrorText error={history.error} />
        </p>
      )}
      {history.data && entries.length === 0 && (
        <p className="p-3 text-(--g-mut)">No queries yet.</p>
      )}
      <ul>
        {entries.map((h) => {
          const [glyph, tone] = outcomeGlyph[h.outcome];
          return (
            <li key={h.audit_id}>
              <button
                type="button"
                className="g-item h-auto flex-col items-stretch gap-0 py-1.5"
                title={h.sql}
                onClick={() => onLoad(h.sql)}
              >
                <span className="truncate font-mono text-xs text-(--g-code)">
                  {h.sql}
                </span>
                <span className="text-xs text-(--g-mut)">
                  <span className={tone}>{glyph}</span> {plural(h.rows, "row")}{" "}
                  · {h.duration_ms} ms · {day(h.created_at)}{" "}
                  {clock(h.created_at)}
                </span>
              </button>
            </li>
          );
        })}
      </ul>
      {history.hasNextPage && (
        <button
          type="button"
          className="g-lnk px-2.5 py-1 text-xs"
          disabled={history.isFetchingNextPage}
          onClick={() => history.fetchNextPage()}
        >
          Older
        </button>
      )}
    </div>
  );
}

/**
 * The last run in one line, right of the result tabs; `stale` when the
 * editor no longer holds the text that ran.
 */
function RunLine({
  run,
  limit,
  stale,
}: {
  run: QueryRun | null;
  limit: number;
  stale: boolean;
}) {
  const line = "ml-auto text-xs text-(--g-mut)";
  if (!run) {
    return <span className={line}>One statement, read only</span>;
  }
  if (stale) {
    return (
      <span
        className={`${line} inline-flex items-center gap-1`}
        title={run.sql}
      >
        <span className="text-(--g-warn)">Edited since this ran</span> ·
        <Kbd>{`${modKey}↵`}</Kbd> runs it
      </span>
    );
  }
  const rows = plural(run.rows.length, "row");
  switch (run.state) {
    case "running":
      return <span className={line}>Running, {rows} so far</span>;
    case "canceled":
      return <span className={line}>Canceled after {rows}</span>;
    case "done":
      return (
        <span className={line} title={run.sql}>
          {run.end?.truncated && (
            <span className="text-(--g-warn)">
              Stopped at {limit.toLocaleString("en")} rows or the size cap
              {" · "}
            </span>
          )}
          {[run.end?.command, rows, `${run.end?.duration_ms ?? 0} ms`]
            .filter(Boolean)
            .join(" · ")}
        </span>
      );
    case "failed":
      return <span className={`${line} text-(--g-fail)`}>Failed</span>;
  }
}

/** Why the last run failed, and where. */
function RunError({ run }: { run: QueryRun }) {
  const e = run.error;
  return (
    <div className="border-t border-(--g-line) px-5 py-2.5 text-xs">
      <p className="text-(--g-fail)">
        <span className="font-semibold">The query failed: </span>
        {sentence(e?.message ?? "")}
        {e?.sqlstate && (
          <span className="font-mono text-(--g-mut)">
            {" "}
            SQLSTATE {e.sqlstate}
          </span>
        )}
        {e?.position ? (
          <span className="text-(--g-mut)">
            {" "}
            at {lineCol(run.sql, e.position)}
          </span>
        ) : null}
        {run.rows.length > 0 && (
          <span className="text-(--g-mut)">
            {" "}
            after {plural(run.rows.length, "row")}
          </span>
        )}
      </p>
      {e?.detail && <p className="text-(--g-mut)">{e.detail}</p>}
      {e?.hint && <p className="text-(--g-mut)">Hint: {e.hint}</p>}
    </div>
  );
}

/** A driver message as a sentence: a capital first, a full stop last. */
function sentence(text: string) {
  const t = text.trim();
  if (!t) return t;
  const s = t[0]?.toUpperCase() + t.slice(1);
  return /[.!?]$/.test(s) ? s : `${s}.`;
}
