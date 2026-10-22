import { useState } from "react";
import { modKey, useKeydown } from "../../shell/keys";
import { clock, day, plural } from "../../ui/format";
import { Act, Button, ErrorText } from "../../ui/kit";
import {
  type QueryRun,
  useCli,
  useDataSource,
  useDeleteSavedQuery,
  useQueryHistory,
  useQueryRunner,
  useSavedQueries,
  useSaveQuery,
} from "./api";
import { Grid } from "./Grid";
import { SqlEditor } from "./SqlEditor";
import type { DataHistoryEntry, DataTable } from "./types";
import { dataLimits } from "./types";

// The SQL tab, loaded on demand (React.lazy in ./DataPanel) so CodeMirror
// is only fetched when someone opens it.

const outcomeGlyph: Record<DataHistoryEntry["outcome"], [string, string]> = {
  ok: ["✓", "text-ok"],
  error: ["✕", "text-fail"],
  conflict: ["✕", "text-fail"],
  timeout: ["▲", "text-warn"],
  canceled: ["–", "text-mut"],
};

/** "line 2, column 7" for a 1-based character position in `sql`. */
function lineCol(sql: string, position: number) {
  const before = sql.slice(0, position - 1).split("\n");
  return `line ${before.length}, column ${(before.at(-1)?.length ?? 0) + 1}`;
}

const shellQuote = (s: string) =>
  `'${s.replace(/\s+/g, " ").trim().replaceAll("'", "'\\''")}'`;

/**
 * Read-only SQL: the editor (Mod-Enter runs), the streamed result in the
 * grid, and the caller's saved queries and history to load from.
 */
export default function SqlView({
  tables,
}: {
  /** For completion. */
  tables: readonly DataTable[];
}) {
  const [sql, setSql] = useState("");
  const [savedId, setSavedId] = useState<number | null>(null);
  const [naming, setNaming] = useState<string | null>(null);
  const { result, run, cancel } = useQueryRunner();
  const saved = useSavedQueries();
  const save = useSaveQuery();
  const remove = useDeleteSavedQuery();
  const history = useQueryHistory();
  const cli = useCli();
  const running = result?.state === "running";
  const current = saved.data?.find((q) => q.id === savedId);

  const start = () => sql.trim() && run(sql);
  const load = (text: string, id: number | null = null) => {
    setSql(text);
    setSavedId(id);
  };

  useKeydown((e) => {
    if (e.defaultPrevented) return;
    if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
      e.preventDefault();
      start();
    } else if (e.key === "Escape" && running) cancel();
  });

  return (
    <div className="flex min-h-0 flex-1 gap-4">
      <aside className="flex w-[240px] shrink-0 flex-col gap-4 overflow-y-auto text-xs">
        <section>
          <h3 className="mb-1 text-sm font-semibold">Saved</h3>
          {saved.data?.length === 0 && (
            <p className="text-mut">Nothing saved yet.</p>
          )}
          <ul>
            {saved.data?.map((q) => (
              <li key={q.id} className="group flex items-baseline gap-2">
                <button
                  type="button"
                  className={`min-w-0 flex-1 truncate py-0.5 text-left hover:text-white ${
                    q.id === savedId ? "text-white" : "text-[#d4d4d4]"
                  }`}
                  title={q.sql}
                  onClick={() => load(q.sql, q.id)}
                >
                  {q.name}
                </button>
                <button
                  type="button"
                  className="lnk invisible group-hover:visible"
                  aria-label={`Delete ${q.name}`}
                  onClick={() => remove.mutate(q.id)}
                >
                  ×
                </button>
              </li>
            ))}
          </ul>
        </section>
        <section>
          <h3 className="mb-1 text-sm font-semibold">History</h3>
          {history.data?.pages[0]?.length === 0 && (
            <p className="text-mut">No queries yet.</p>
          )}
          <ul className="flex flex-col gap-1.5">
            {history.data?.pages.flatMap((page) =>
              (page ?? []).map((h) => {
                const [glyph, tone] = outcomeGlyph[h.outcome];
                return (
                  <li key={h.audit_id}>
                    <button
                      type="button"
                      className="w-full text-left hover:text-white"
                      title={h.sql}
                      onClick={() => load(h.sql)}
                    >
                      <span className="block truncate font-mono text-[#d4d4d4]">
                        {h.sql}
                      </span>
                      <span className="text-mut">
                        <span className={tone}>{glyph}</span>{" "}
                        {plural(h.rows, "row")} · {h.duration_ms} ms ·{" "}
                        {day(h.created_at)} {clock(h.created_at)}
                      </span>
                    </button>
                  </li>
                );
              }),
            )}
          </ul>
          {history.hasNextPage && (
            <button
              type="button"
              className="lnk mt-1"
              disabled={history.isFetchingNextPage}
              onClick={() => history.fetchNextPage()}
            >
              older
            </button>
          )}
        </section>
      </aside>

      <div className="flex min-w-0 flex-1 flex-col gap-2">
        <div className="h-[168px] shrink-0 border-y border-line">
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
        <div className="flex flex-wrap items-start gap-2.5">
          <Act
            primary
            cli={cli(`dock db query ${shellQuote(sql || "SQL")}`)}
            disabled={!sql.trim() || running}
            onClick={start}
          >
            Run <kbd>{modKey}↵</kbd>
          </Act>
          {running && (
            <Button onClick={cancel}>
              Cancel <kbd>Esc</kbd>
            </Button>
          )}
          {naming === null ? (
            <Button
              disabled={!sql.trim() || save.isPending}
              onClick={() =>
                current
                  ? save.mutate({ id: current.id, name: current.name, sql })
                  : setNaming("")
              }
            >
              {current ? `Save "${current.name}"` : "Save"}
            </Button>
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
                className="field w-48 py-1"
                placeholder="Name"
                aria-label="Query name"
                value={naming}
                maxLength={100}
                onChange={(e) => setNaming(e.target.value)}
                onKeyDown={(e) => e.key === "Escape" && setNaming(null)}
              />
              <Button type="submit" small>
                Save
              </Button>
            </form>
          )}
          <span className="self-center">
            <ErrorText error={save.error ?? remove.error} />
          </span>
        </div>
        <RunStatus run={result} />
        <div className="relative min-h-[200px] flex-1 border-t border-line">
          {result && result.columns.length > 0 && (
            <Grid columns={result.columns} rows={result.rows} />
          )}
        </div>
      </div>
    </div>
  );
}

/** One line on the last run: running, its end frame, or its error. */
function RunStatus({ run }: { run: QueryRun | null }) {
  const { cli } = useDataSource();
  if (!run) {
    return (
      <p className="text-mut">
        Read-only. One statement, {dataLimits.queryRows.toLocaleString("en")}{" "}
        rows at most, 10 s timeout.
      </p>
    );
  }
  const rows = plural(run.rows.length, "row");
  switch (run.state) {
    case "running":
      return (
        <p className="font-mono text-xs">
          <span className="text-prog">
            <span className="pi" aria-hidden />
          </span>{" "}
          running, {rows} so far
        </p>
      );
    case "canceled":
      return (
        <p className="font-mono text-xs text-mut">– canceled after {rows}</p>
      );
    case "done":
      return (
        <p className="font-mono text-xs">
          <span className="text-ok">✓</span> {run.end?.command} · {rows} ·{" "}
          {run.end?.duration_ms} ms
          {run.end?.truncated && (
            <span className="text-warn">
              {" "}
              ▲ stopped at a row or size cap
              {cli !== undefined &&
                "; `dock db query --limit` returns up to 100 000"}
            </span>
          )}
        </p>
      );
    case "failed": {
      const e = run.error;
      return (
        <div className="font-mono text-xs">
          <p className="text-fail">
            ✕ {e?.message}
            {e?.sqlstate && <span className="text-mut"> {e.sqlstate}</span>}
            {e?.position ? (
              <span className="text-mut">
                {" "}
                at {lineCol(run.sql, e.position)}
              </span>
            ) : null}
            {run.rows.length > 0 && (
              <span className="text-mut"> after {rows}</span>
            )}
          </p>
          {e?.detail && <p className="text-mut">{e.detail}</p>}
          {e?.hint && <p className="text-mut">Hint: {e.hint}</p>}
        </div>
      );
    }
  }
}
